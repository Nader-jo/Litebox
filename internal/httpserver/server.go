// Package httpserver exposes Litebox's secure server-rendered HTTP surface.
package httpserver

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/Nader-jo/Litebox/internal/auth"
	"github.com/Nader-jo/Litebox/internal/blobstore"
	"github.com/Nader-jo/Litebox/internal/config"
	"github.com/Nader-jo/Litebox/internal/ids"
	"github.com/Nader-jo/Litebox/internal/model"
	"github.com/Nader-jo/Litebox/internal/provider"
	"github.com/Nader-jo/Litebox/internal/repository"
	"github.com/Nader-jo/Litebox/internal/service"
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
	config     config.Config
	repository *repository.Repository
	store      blobstore.Store
	provider   provider.Client
	mailbox    *service.Mailbox
	logger     *slog.Logger
	http       *http.Server
	trusted    []*net.IPNet
	login      *loginLimiter
	send       *windowLimiter
}

// New builds the complete HTTP server without starting a listener.
func New(cfg config.Config, repo *repository.Repository, store blobstore.Store, providerClient provider.Client, mailbox *service.Mailbox, logger *slog.Logger) (*Server, error) {
	server := &Server{config: cfg, repository: repo, store: store, provider: providerClient, mailbox: mailbox,
		logger: logger, login: &loginLimiter{attempts: make(map[string][]time.Time)},
		send: newWindowLimiter(30, time.Hour)}
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

func (s *Server) routes(mux *http.ServeMux) {
	staticFS, err := fs.Sub(webassets.Static, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	mux.HandleFunc("GET /health/live", s.live)
	mux.HandleFunc("GET /health/ready", s.ready)
	mux.HandleFunc("GET /setup", s.setupPage)
	mux.HandleFunc("POST /setup", s.setup)
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.loginPost)
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
	mux.Handle("POST /settings/profile", manageable(s.updateSettings))
	mux.Handle("POST /mailboxes", manageable(s.createMailbox))
	mux.Handle("POST /mailboxes/{mailboxID}/aliases", manageable(s.addAlias))
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

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(s.config.CookieName)
		if err != nil || cookie.Value == "" {
			s.redirectLogin(w, r)
			return
		}
		session, err := s.repository.FindSession(r.Context(), auth.TokenHash(cookie.Value))
		if err != nil {
			s.clearCookies(w)
			s.redirectLogin(w, r)
			return
		}
		if time.Since(session.LastSeenAt) >= 5*time.Minute {
			_ = s.repository.TouchSession(r.Context(), session.ID, session.User.ID, false)
			session.LastSeenAt = time.Now().UTC()
		}
		csrfCookie, _ := r.Cookie(s.config.CookieName + "_csrf")
		csrfToken := ""
		if csrfCookie != nil && auth.VerifyToken(csrfCookie.Value, session.CSRFHash) {
			csrfToken = csrfCookie.Value
		}
		preferredMailbox := ""
		if mailboxCookie, cookieErr := r.Cookie(s.config.CookieName + "_mailbox"); cookieErr == nil {
			preferredMailbox = mailboxCookie.Value
		}
		mailbox, err := s.repository.MailboxForUser(r.Context(), session.User.ID, preferredMailbox)
		if err != nil {
			s.renderError(w, r, http.StatusForbidden, "Your account does not have access to a mailbox.")
			return
		}
		if preferredMailbox != mailbox.ID {
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
		candidate := r.Header.Get("X-CSRF-Token")
		if candidate == "" {
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
				r.Body = http.MaxBytesReader(w, r.Body, s.config.MaxUploadRequestBytes)
				if err := r.ParseMultipartForm(s.config.MaxUploadRequestBytes); err != nil {
					s.renderError(w, r, http.StatusRequestEntityTooLarge, "The upload is too large.")
					return
				} else {
					r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
					_ = r.ParseForm()
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
	trusted := false
	for _, network := range s.trusted {
		if network.Contains(remote) {
			trusted = true
			break
		}
	}
	if trusted {
		if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); net.ParseIP(forwarded) != nil {
			return forwarded
		}
	}
	return host
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
	maxAge := int(s.config.SessionTTL.Seconds())
	http.SetCookie(w, &http.Cookie{Name: s.config.CookieName, Value: sessionToken, Path: "/", MaxAge: maxAge,
		Expires: time.Now().Add(s.config.SessionTTL), HttpOnly: true, Secure: s.config.SecureCookies(), SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: s.config.CookieName + "_csrf", Value: csrfToken, Path: "/", MaxAge: maxAge,
		Expires: time.Now().Add(s.config.SessionTTL), HttpOnly: false, Secure: s.config.SecureCookies(), SameSite: http.SameSiteStrictMode})
}

func (s *Server) clearCookies(w http.ResponseWriter) {
	for _, name := range []string{s.config.CookieName, s.config.CookieName + "_csrf", s.config.CookieName + "_mailbox"} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: name == s.config.CookieName,
			Secure: s.config.SecureCookies(), SameSite: http.SameSiteLaxMode})
	}
}

func (s *Server) setMailboxCookie(w http.ResponseWriter, mailboxID string) {
	http.SetCookie(w, &http.Cookie{Name: s.config.CookieName + "_mailbox", Value: mailboxID, Path: "/",
		MaxAge: int(s.config.SessionTTL.Seconds()), Expires: time.Now().Add(s.config.SessionTTL), HttpOnly: true,
		Secure: s.config.SecureCookies(), SameSite: http.SameSiteLaxMode})
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

type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
}

func (l *loginLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := time.Now().Add(-10 * time.Minute)
	values := l.attempts[key][:0]
	for _, value := range l.attempts[key] {
		if value.After(cutoff) {
			values = append(values, value)
		}
	}
	l.attempts[key] = values
	return len(values) < 5
}

func (l *loginLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attempts[key] = append(l.attempts[key], time.Now())
}

func (l *loginLimiter) success(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}

type windowLimiter struct {
	mu     sync.Mutex
	events map[string][]time.Time
	limit  int
	window time.Duration
	now    func() time.Time
}

func newWindowLimiter(limit int, window time.Duration) *windowLimiter {
	return &windowLimiter{events: make(map[string][]time.Time), limit: limit, window: window, now: time.Now}
}

func (l *windowLimiter) take(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := l.now().Add(-l.window)
	values := l.events[key][:0]
	for _, value := range l.events[key] {
		if value.After(cutoff) {
			values = append(values, value)
		}
	}
	if len(values) >= l.limit {
		l.events[key] = values
		return false
	}
	l.events[key] = append(values, l.now())
	return true
}
