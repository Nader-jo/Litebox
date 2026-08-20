// Package httpserver exposes Litebox's secure server-rendered HTTP surface.
package httpserver

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nader-jo/Litebox/internal/auth"
	"github.com/Nader-jo/Litebox/internal/blobstore"
	"github.com/Nader-jo/Litebox/internal/config"
	"github.com/Nader-jo/Litebox/internal/ids"
	"github.com/Nader-jo/Litebox/internal/model"
	"github.com/Nader-jo/Litebox/internal/provider"
	"github.com/Nader-jo/Litebox/internal/repository"
	"github.com/Nader-jo/Litebox/internal/service"
	"github.com/Nader-jo/Litebox/internal/settings"
	webassets "github.com/Nader-jo/Litebox/web"
)

type authKey struct{}
type requestIDKey struct{}

type authState struct {
	Session   model.Session
	CSRFToken string
	Mailbox   model.Mailbox
}

// Server owns HTTP routing and middleware.
type Server struct {
	config        atomic.Pointer[config.Config]
	repository    *repository.Repository
	store         blobstore.Store
	provider      provider.Client
	mailbox       *service.Mailbox
	logger        *slog.Logger
	http          *http.Server
	trusted       []*net.IPNet
	loginAccount  *windowLimiter
	loginIP       *windowLimiter
	resetAccount  *windowLimiter
	resetIP       *windowLimiter
	send          *windowLimiter
	passwordSlots chan struct{}
	dummyPassword string
	settings      *settings.Store
	settingsMu    sync.Mutex
	readinessMu   sync.Mutex
	readinessAt   time.Time
	readinessErr  error
	applyConfig   func(config.Config)
	accountEmails chan accountEmail
	emailStart    sync.Once
	emailCancel   context.CancelFunc
	emailWait     sync.WaitGroup
	emailClose    sync.Once
}

type accountEmail struct {
	recipient      string
	subject        string
	body           string
	resetUserID    string
	resetTokenHash []byte
	resetTTL       time.Duration
}

// New builds the complete HTTP server without starting a listener.
func New(cfg config.Config, repo *repository.Repository, store blobstore.Store, providerClient provider.Client, mailbox *service.Mailbox, logger *slog.Logger) (*Server, error) {
	dummyPassword, err := auth.HashPassword("litebox-dummy-password")
	if err != nil {
		return nil, fmt.Errorf("initialize password verification: %w", err)
	}
	server := &Server{repository: repo, store: store, provider: providerClient, mailbox: mailbox,
		logger: logger, loginAccount: newWindowLimiter(5, 10*time.Minute), loginIP: newWindowLimiter(25, 10*time.Minute),
		resetAccount: newWindowLimiter(3, 10*time.Minute), resetIP: newWindowLimiter(20, 10*time.Minute), send: newWindowLimiter(30, time.Hour),
		passwordSlots: make(chan struct{}, 4), dummyPassword: dummyPassword, accountEmails: make(chan accountEmail, 256)}
	server.ApplyConfig(cfg)
	for _, value := range cfg.TrustedProxyCIDRs {
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			return nil, fmt.Errorf("parse trusted proxy CIDR %q: %w", value, err)
		}
		server.trusted = append(server.trusted, network)
	}
	mux := http.NewServeMux()
	server.routes(mux)
	handler := server.requestID(server.recoverer(server.securityHeaders(mux)))
	server.http = &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // Attachment downloads are streamed and may be intentionally slow.
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	return server, nil
}

// HTTP returns the configured standard-library server.
func (s *Server) HTTP() *http.Server { return s.http }

// StartBackground begins bounded, process-local account-email delivery. It is
// separate from New so a failed application assembly cannot leak a goroutine.
func (s *Server) StartBackground() {
	s.emailStart.Do(func() {
		emailContext, cancelEmails := context.WithCancel(context.Background())
		s.emailCancel = cancelEmails
		s.emailWait.Add(1)
		go s.accountEmailWorker(emailContext)
	})
}

// Close stops process-local account email delivery before the repository is
// closed. Password-reset requests can be repeated if shutdown interrupts one.
func (s *Server) Close() {
	s.emailClose.Do(func() {
		if s.emailCancel != nil {
			s.emailCancel()
		}
		s.emailWait.Wait()
	})
}

func (s *Server) accountEmailWorker(ctx context.Context) {
	defer s.emailWait.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case message := <-s.accountEmails:
			sendContext, cancel := context.WithTimeout(ctx, time.Minute)
			var err error
			if message.resetUserID != "" {
				err = s.repository.CreatePasswordResetForUser(sendContext, message.resetUserID, message.resetTokenHash, time.Now().UTC().Add(message.resetTTL))
			}
			if err == nil {
				err = s.sendAccountEmail(sendContext, message.recipient, message.subject, message.body)
			}
			cancel()
			if err != nil && ctx.Err() == nil {
				s.logger.Error("account email delivery failed", "error", err)
			}
		}
	}
}

func (s *Server) enqueueAccountEmail(message accountEmail) bool {
	select {
	case s.accountEmails <- message:
		return true
	default:
		return false
	}
}

// Configure attaches persisted settings and the callback used after the setup
// wizard commits a new runtime snapshot.
func (s *Server) Configure(store *settings.Store, apply func(config.Config)) {
	s.settings, s.applyConfig = store, apply
}

// ApplyConfig replaces reloadable runtime values. Listener and trust topology
// remain fixed for the process lifetime.
func (s *Server) ApplyConfig(cfg config.Config) {
	snapshot := cfg
	s.config.Store(&snapshot)
}

func (s *Server) currentConfig() config.Config { return *s.config.Load() }

func (s *Server) routes(mux *http.ServeMux) {
	staticFS, err := fs.Sub(webassets.Static, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	mux.HandleFunc("GET /health/live", s.live)
	mux.HandleFunc("GET /health/ready", s.ready)
	mux.HandleFunc("GET /setup", s.setupPage)
	mux.HandleFunc("POST /setup", s.requireSameOrigin(s.setup))
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.requireSameOrigin(s.loginPost))
	mux.HandleFunc("GET /invite", s.invitationPage)
	mux.HandleFunc("POST /invite", s.requireSameOrigin(s.acceptInvitation))
	mux.HandleFunc("GET /password-reset", s.passwordResetPage)
	mux.HandleFunc("POST /password-reset", s.requireSameOrigin(s.requestPasswordReset))
	mux.HandleFunc("GET /password-reset/confirm", s.passwordResetConfirmPage)
	mux.HandleFunc("POST /password-reset/confirm", s.requireSameOrigin(s.consumePasswordReset))
	mux.HandleFunc("POST /webhooks/resend", s.webhook)

	authenticated := func(handler http.HandlerFunc) http.Handler { return s.authenticate(s.csrf(handler)) }
	writable := func(handler http.HandlerFunc) http.Handler {
		return authenticated(func(w http.ResponseWriter, r *http.Request) {
			if authFrom(r).Mailbox.Role == "viewer" {
				s.renderError(w, r, http.StatusForbidden, "This mailbox membership is read-only.")
				return
			}
			handler(w, r)
		})
	}
	manageable := func(handler http.HandlerFunc) http.Handler {
		return authenticated(func(w http.ResponseWriter, r *http.Request) {
			role := authFrom(r).Mailbox.Role
			if role != "owner" && role != "admin" {
				s.renderError(w, r, http.StatusForbidden, "Mailbox administrator access is required.")
				return
			}
			handler(w, r)
		})
	}
	mux.Handle("GET /", s.authenticate(http.HandlerFunc(s.root)))
	mux.Handle("POST /logout", authenticated(s.logout))
	mux.Handle("POST /mailboxes/select", authenticated(s.selectMailbox))
	for _, folder := range []string{"inbox", "sent", "archive", "starred", "trash"} {
		folder := folder
		mux.Handle("GET /"+folder, s.authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.folder(w, r, folder) })))
	}
	mux.Handle("GET /threads/{threadID}", s.authenticate(http.HandlerFunc(s.thread)))
	for _, action := range []string{"archive", "unarchive", "star", "unstar", "trash", "restore"} {
		action := action
		mux.Handle("POST /threads/{threadID}/"+action, writable(func(w http.ResponseWriter, r *http.Request) { s.threadAction(w, r, action) }))
	}
	mux.Handle("POST /threads/{threadID}/read", writable(func(w http.ResponseWriter, r *http.Request) { s.threadRead(w, r, true) }))
	mux.Handle("POST /threads/{threadID}/unread", writable(func(w http.ResponseWriter, r *http.Request) { s.threadRead(w, r, false) }))
	mux.Handle("POST /threads/{threadID}/delete", writable(s.threadDelete))
	mux.Handle("DELETE /threads/{threadID}", writable(s.threadDelete))
	mux.Handle("GET /threads/{threadID}/reply", s.authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.reply(w, r, false) })))
	mux.Handle("GET /threads/{threadID}/reply-all", s.authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.reply(w, r, true) })))
	mux.Handle("POST /threads/{threadID}/reply", authenticated(func(w http.ResponseWriter, r *http.Request) { s.reply(w, r, false) }))
	mux.Handle("POST /threads/{threadID}/reply-all", authenticated(func(w http.ResponseWriter, r *http.Request) { s.reply(w, r, true) }))
	mux.Handle("GET /compose", s.authenticate(http.HandlerFunc(s.compose)))
	mux.Handle("POST /drafts", writable(s.createDraft))
	mux.Handle("GET /drafts", s.authenticate(http.HandlerFunc(s.drafts)))
	mux.Handle("GET /drafts/{draftID}", s.authenticate(http.HandlerFunc(s.draft)))
	mux.Handle("POST /drafts/{draftID}", writable(s.saveDraft))
	mux.Handle("PATCH /drafts/{draftID}", writable(s.saveDraft))
	mux.Handle("POST /drafts/{draftID}/send", writable(s.sendDraft))
	mux.Handle("POST /drafts/{draftID}/attachments", writable(s.uploadAttachment))
	mux.Handle("POST /drafts/{draftID}/attachments/{attachmentID}/delete", writable(s.deleteDraftAttachment))
	mux.Handle("DELETE /drafts/{draftID}/attachments/{attachmentID}", writable(s.deleteDraftAttachment))
	mux.Handle("POST /drafts/{draftID}/delete", writable(s.deleteDraft))
	mux.Handle("DELETE /drafts/{draftID}", writable(s.deleteDraft))
	mux.Handle("GET /attachments/{attachmentID}", s.authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.attachment(w, r, false) })))
	mux.Handle("GET /attachments/{attachmentID}/inline", s.authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.attachment(w, r, true) })))
	mux.Handle("GET /search", s.authenticate(http.HandlerFunc(s.search)))
	mux.Handle("GET /admin/system", s.authenticate(http.HandlerFunc(s.admin)))
	for _, path := range []string{"/admin/jobs", "/admin/webhooks", "/admin/storage"} {
		mux.Handle("GET "+path, s.authenticate(http.HandlerFunc(s.admin)))
	}
	mux.Handle("POST /admin/jobs/{jobID}/retry", manageable(s.retryJob))
	mux.Handle("GET /settings", s.authenticate(http.HandlerFunc(s.admin)))
	mux.Handle("GET /settings/mailboxes", s.authenticate(http.HandlerFunc(s.admin)))
	mux.Handle("GET /settings/people", s.authenticate(http.HandlerFunc(s.admin)))
	mux.Handle("GET /settings/sessions", s.authenticate(http.HandlerFunc(s.admin)))
	mux.Handle("GET /settings/digest", s.authenticate(http.HandlerFunc(s.admin)))
	mux.Handle("POST /settings/system", manageable(s.updateSystemSettings))
	mux.Handle("POST /settings/profile", manageable(s.updateSettings))
	mux.Handle("POST /settings/digest", authenticated(s.updateDigest))
	mux.Handle("POST /settings/digest/test", authenticated(s.sendDigestTest))
	mux.Handle("POST /mailboxes", manageable(s.createMailbox))
	mux.Handle("POST /mailboxes/{mailboxID}/aliases", manageable(s.addAlias))
	mux.Handle("POST /mailboxes/{mailboxID}/aliases/{addressID}/color", manageable(s.updateAliasColor))
	mux.Handle("POST /mailboxes/{mailboxID}/aliases/{addressID}/delete", manageable(s.deleteAlias))
	mux.Handle("POST /mailboxes/{mailboxID}/members", manageable(s.addMember))
	mux.Handle("POST /mailboxes/{mailboxID}/members/{userID}/delete", manageable(s.removeMember))
	mux.Handle("POST /sessions/{sessionID}/revoke", authenticated(s.revokeSession))
}

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := ids.New()
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, requestID)))
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("panic in HTTP request", "request_id", requestID(r), "error", recovered, "stack", string(debug.Stack()))
				s.renderError(w, r, http.StatusInternalServerError, "An unexpected error occurred.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// requireSameOrigin prevents login CSRF and cross-site submission of other
// unauthenticated account forms. Non-browser clients without fetch metadata or
// an Origin/Referer header remain supported.
func (s *Server) requireSameOrigin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") || !s.sameOriginHeader(r) {
			s.renderError(w, r, http.StatusForbidden, "Cross-site form submissions are not allowed.")
			return
		}
		next(w, r)
	}
}

func (s *Server) sameOriginHeader(r *http.Request) bool {
	source := strings.TrimSpace(r.Header.Get("Origin"))
	if source == "" {
		source = strings.TrimSpace(r.Header.Get("Referer"))
	}
	if source == "" {
		return true
	}
	parsed, err := url.Parse(source)
	base := s.currentConfig().BaseURL
	return err == nil && base != nil && parsed.Scheme == base.Scheme && strings.EqualFold(parsed.Host, base.Host)
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := s.currentConfig()
		cookie, err := r.Cookie(cfg.CookieName)
		if err != nil || cookie.Value == "" {
			s.redirectLogin(w, r)
			return
		}
		session, err := s.repository.FindSession(r.Context(), auth.TokenHash(cookie.Value))
		if errors.Is(err, repository.ErrNotFound) {
			s.clearCookies(w)
			s.redirectLogin(w, r)
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if time.Since(session.LastSeenAt) >= 5*time.Minute {
			_ = s.repository.TouchSession(r.Context(), session.ID, session.User.ID, false)
			session.LastSeenAt = time.Now().UTC()
		}
		csrfCookie, _ := r.Cookie(cfg.CookieName + "_csrf")
		csrfToken := ""
		if csrfCookie != nil && auth.VerifyToken(csrfCookie.Value, session.CSRFHash) {
			csrfToken = csrfCookie.Value
		}
		preferredMailbox := strings.TrimSpace(r.URL.Query().Get("mailbox"))
		if mailboxCookie, cookieErr := r.Cookie(cfg.CookieName + "_mailbox"); cookieErr == nil {
			if preferredMailbox == "" {
				preferredMailbox = mailboxCookie.Value
			}
		}
		mailbox, err := s.repository.MailboxForUser(r.Context(), session.User.ID, preferredMailbox)
		if errors.Is(err, repository.ErrNotFound) {
			s.renderError(w, r, http.StatusForbidden, "Your account does not have access to a mailbox.")
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if r.URL.Query().Get("mailbox") == "" && preferredMailbox != mailbox.ID {
			s.setMailboxCookie(w, mailbox.ID)
		}
		state := authState{Session: session, CSRFToken: csrfToken, Mailbox: mailbox}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authKey{}, state)))
	})
}

func (s *Server) csrf(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next(w, r)
			return
		}
		state := authFrom(r)
		cfg := s.currentConfig()
		candidate := r.Header.Get("X-CSRF-Token")
		if candidate == "" {
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
				r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxUploadRequestBytes)
				if err := r.ParseMultipartForm(4 << 20); err != nil {
					s.renderError(w, r, http.StatusRequestEntityTooLarge, "The upload is too large.")
					return
				}
				if r.MultipartForm != nil {
					defer func() { _ = r.MultipartForm.RemoveAll() }()
				}
			} else {
				r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxUploadRequestBytes)
				if err := r.ParseForm(); err != nil {
					s.renderError(w, r, http.StatusRequestEntityTooLarge, "The form is too large.")
					return
				}
			}
			candidate = r.FormValue("csrf_token")
		}
		if candidate == "" || subtle.ConstantTimeCompare(auth.TokenHash(candidate), state.Session.CSRFHash) != 1 {
			s.renderError(w, r, http.StatusForbidden, "This form expired. Refresh the page and try again.")
			return
		}
		next(w, r)
	}
}

func (s *Server) clientIP(r *http.Request) string {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	remote := net.ParseIP(host)
	if remote == nil || !s.trustedProxy(remote) {
		return host
	}
	forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	current := remote
	for index := len(forwarded) - 1; index >= 0; index-- {
		candidate := net.ParseIP(strings.TrimSpace(forwarded[index]))
		if candidate == nil {
			return host
		}
		current = candidate
		if !s.trustedProxy(candidate) {
			return candidate.String()
		}
	}
	return current.String()
}

func (s *Server) trustedProxy(address net.IP) bool {
	for _, network := range s.trusted {
		if network.Contains(address) {
			return true
		}
	}
	return false
}

func (s *Server) redirectLogin(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/login")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) setSessionCookies(w http.ResponseWriter, sessionToken, csrfToken string) {
	cfg := s.currentConfig()
	maxAge := int(cfg.SessionTTL.Seconds())
	http.SetCookie(w, &http.Cookie{Name: cfg.CookieName, Value: sessionToken, Path: "/", MaxAge: maxAge,
		Expires: time.Now().Add(cfg.SessionTTL), HttpOnly: true, Secure: cfg.SecureCookies(), SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: cfg.CookieName + "_csrf", Value: csrfToken, Path: "/", MaxAge: maxAge,
		Expires: time.Now().Add(cfg.SessionTTL), HttpOnly: false, Secure: cfg.SecureCookies(), SameSite: http.SameSiteStrictMode})
}

func (s *Server) clearCookies(w http.ResponseWriter) {
	cfg := s.currentConfig()
	for _, name := range []string{cfg.CookieName, cfg.CookieName + "_csrf", cfg.CookieName + "_mailbox"} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: name == cfg.CookieName,
			Secure: cfg.SecureCookies(), SameSite: http.SameSiteLaxMode})
	}
}

func (s *Server) setMailboxCookie(w http.ResponseWriter, mailboxID string) {
	cfg := s.currentConfig()
	http.SetCookie(w, &http.Cookie{Name: cfg.CookieName + "_mailbox", Value: mailboxID, Path: "/",
		MaxAge: int(cfg.SessionTTL.Seconds()), Expires: time.Now().Add(cfg.SessionTTL), HttpOnly: true,
		Secure: cfg.SecureCookies(), SameSite: http.SameSiteLaxMode})
}

func authFrom(r *http.Request) authState {
	state, _ := r.Context().Value(authKey{}).(authState)
	return state
}

func requestID(r *http.Request) string {
	value, _ := r.Context().Value(requestIDKey{}).(string)
	return value
}

func ipHash(value string) []byte {
	hash := sha256.Sum256([]byte(value))
	return hash[:]
}

type windowLimiter struct {
	mu        sync.Mutex
	events    map[string][]time.Time
	limit     int
	window    time.Duration
	maxKeys   int
	lastSweep time.Time
	now       func() time.Time
}

func newWindowLimiter(limit int, window time.Duration) *windowLimiter {
	return &windowLimiter{events: make(map[string][]time.Time), limit: limit, window: window, maxKeys: 10_000, now: time.Now}
}

func (l *windowLimiter) take(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	key = limiterStorageKey(key)
	now := l.now()
	cutoff := now.Add(-l.window)
	if l.lastSweep.IsZero() || now.Sub(l.lastSweep) >= l.window/4 {
		for candidate, events := range l.events {
			events = currentEvents(events, cutoff)
			if len(events) == 0 {
				delete(l.events, candidate)
			} else {
				l.events[candidate] = events
			}
		}
		l.lastSweep = now
	}
	if _, exists := l.events[key]; !exists && len(l.events) >= l.maxKeys {
		return false
	}
	values := currentEvents(l.events[key], cutoff)
	if len(values) >= l.limit {
		l.events[key] = values
		return false
	}
	l.events[key] = append(values, now)
	return true
}

func (l *windowLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.events, limiterStorageKey(key))
}

func limiterStorageKey(key string) string {
	digest := sha256.Sum256([]byte(key))
	return string(digest[:])
}

func (s *Server) takeLoginAttempt(ip, account string) bool {
	if !s.loginIP.take(ip) {
		return false
	}
	return s.loginAccount.take(account)
}

func (s *Server) takeResetAttempt(ip, account string) bool {
	if !s.resetIP.take(ip) {
		return false
	}
	return s.resetAccount.take(account)
}

func (s *Server) hashPassword(ctx context.Context, password string) (string, error) {
	select {
	case s.passwordSlots <- struct{}{}:
		defer func() { <-s.passwordSlots }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return auth.HashPassword(password)
}

func currentEvents(events []time.Time, cutoff time.Time) []time.Time {
	values := events[:0]
	for _, value := range events {
		if value.After(cutoff) {
			values = append(values, value)
		}
	}
	return values
}
