package httpserver

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/Nader-jo/Litebox/internal/auth"
	"github.com/Nader-jo/Litebox/internal/config"
	"github.com/Nader-jo/Litebox/internal/ids"
	mailx "github.com/Nader-jo/Litebox/internal/mail"
	"github.com/Nader-jo/Litebox/internal/model"
	"github.com/Nader-jo/Litebox/internal/provider"
	"github.com/Nader-jo/Litebox/internal/repository"
	"github.com/Nader-jo/Litebox/internal/search"
	"github.com/Nader-jo/Litebox/internal/service"
	"github.com/Nader-jo/Litebox/internal/settings"
	"github.com/Nader-jo/Litebox/internal/ui"
)

func (s *Server) live(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"status":"ok"}`)
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.readiness(ctx); err != nil {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"status":"ready"}`)
}

func (s *Server) readiness(ctx context.Context) error {
	s.readinessMu.Lock()
	defer s.readinessMu.Unlock()
	if !s.readinessAt.IsZero() && time.Since(s.readinessAt) < 5*time.Second {
		return s.readinessErr
	}
	err := s.repository.Ping(ctx)
	if err == nil {
		err = s.store.Health(ctx)
	}
	// Do not turn one canceled probe into a cached outage for other callers.
	if ctx.Err() == nil {
		s.readinessAt, s.readinessErr = time.Now(), err
	}
	return err
}

func (s *Server) root(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/inbox", http.StatusSeeOther)
}

func (s *Server) setupPage(w http.ResponseWriter, r *http.Request) {
	hasUsers, err := s.repository.HasUsers(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if hasUsers {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	token := r.URL.Query().Get("token")
	if s.currentConfig().Environment == "production" && s.settings != nil {
		valid, err := s.settings.VerifySetupToken(r.Context(), token)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if !valid {
			s.renderError(w, r, http.StatusForbidden, "Use the one-time setup link printed in the container logs.")
			return
		}
	}
	s.render(w, r, http.StatusOK, ui.SetupPage(s.setupData(token, "")))
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.renderError(w, r, http.StatusRequestEntityTooLarge, "The setup form is too large.")
			return
		}
		s.renderError(w, r, http.StatusBadRequest, "Invalid setup form.")
		return
	}
	hasUsers, err := s.repository.HasUsers(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if hasUsers {
		s.renderError(w, r, http.StatusNotFound, "Setup is already complete.")
		return
	}
	token := r.FormValue("token")
	if s.currentConfig().Environment == "production" && s.settings != nil {
		valid, err := s.settings.VerifySetupToken(r.Context(), token)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if !valid {
			s.renderError(w, r, http.StatusForbidden, "The setup link is invalid or expired.")
			return
		}
	}
	emailAddress := strings.TrimSpace(r.FormValue("email"))
	if parsed, err := mail.ParseAddress(emailAddress); err != nil || !strings.EqualFold(parsed.Address, emailAddress) {
		s.render(w, r, http.StatusUnprocessableEntity, ui.SetupPage(s.setupData(token, "Enter a valid administrator email.")))
		return
	} else {
		emailAddress = strings.ToLower(parsed.Address)
	}
	primaryAddress := strings.TrimSpace(r.FormValue("primary_address"))
	if primaryAddress == "" {
		primaryAddress = s.currentConfig().PrimaryAddress
	}
	if parsed, err := mail.ParseAddress(primaryAddress); err != nil || !strings.EqualFold(parsed.Address, primaryAddress) {
		s.render(w, r, http.StatusUnprocessableEntity, ui.SetupPage(s.setupData(token, "Enter a valid mailbox address.")))
		return
	} else {
		primaryAddress = strings.ToLower(parsed.Address)
	}
	mailboxName := strings.TrimSpace(r.FormValue("mailbox_display_name"))
	if mailboxName == "" {
		mailboxName = s.currentConfig().DisplayName
	}
	baseURL := strings.TrimSpace(r.FormValue("base_url"))
	if baseURL == "" {
		baseURL = s.currentConfig().BaseURL.String()
	}
	parsedURL, err := config.ParseBaseURL(baseURL, s.currentConfig().Environment == "production")
	if err != nil {
		s.render(w, r, http.StatusUnprocessableEntity, ui.SetupPage(s.setupData(token, "Enter a valid public URL (HTTPS is required in production).")))
		return
	}
	apiKey, webhookSecret, domainID := strings.TrimSpace(r.FormValue("resend_api_key")), strings.TrimSpace(r.FormValue("resend_webhook_secret")), strings.TrimSpace(r.FormValue("resend_domain_id"))
	if s.currentConfig().Environment == "production" && (apiKey == "" || webhookSecret == "") {
		s.render(w, r, http.StatusUnprocessableEntity, ui.SetupPage(s.setupData(token, "Resend API key and webhook secret are required in production.")))
		return
	}
	password := r.FormValue("password")
	if password != r.FormValue("password_confirmation") {
		s.render(w, r, http.StatusUnprocessableEntity, ui.SetupPage(s.setupData(token, "The passwords do not match.")))
		return
	}
	hash, err := s.hashPassword(r.Context(), password)
	if err != nil {
		s.render(w, r, http.StatusUnprocessableEntity, ui.SetupPage(s.setupData(token, err.Error())))
		return
	}
	current := s.currentConfig()
	current.BaseURL = parsedURL
	current.PrimaryAddress = primaryAddress
	current.DisplayName = mailboxName
	current.ResendAPIKey, current.ResendWebhookSecret, current.ResendDomainID = apiKey, webhookSecret, domainID
	current.AllowUnconfigured = false
	allowedRecipients := make(map[string]struct{}, len(current.AllowedRecipients)+1)
	for address := range current.AllowedRecipients {
		allowedRecipients[address] = struct{}{}
	}
	allowedRecipients[strings.ToLower(primaryAddress)] = struct{}{}
	current.AllowedRecipients = allowedRecipients
	if err := current.Validate(); err != nil {
		s.render(w, r, http.StatusUnprocessableEntity, ui.SetupPage(s.setupData(token, "The installation settings are invalid: "+err.Error())))
		return
	}
	if s.settings == nil {
		s.internalError(w, r, errors.New("installation settings are unavailable"))
		return
	}
	value := settings.Values{Configured: true, BaseURL: parsedURL.String(), SessionTTLHours: int(current.SessionTTL / time.Hour), LogLevel: current.LogLevel,
		MaxWebhookBodyBytes: current.MaxWebhookBodyBytes, MaxMessageTextBytes: current.MaxMessageTextBytes, MaxUploadRequestBytes: current.MaxUploadRequestBytes,
		MaxOutboundAttachmentBytes: current.MaxOutboundAttachmentBytes, MaxAttachmentCount: current.MaxAttachmentCount,
		ResendAPIKey: apiKey, ResendWebhookSecret: webhookSecret, ResendDomainID: domainID}
	err = s.settings.CompleteSetup(r.Context(), token, current.Environment == "production", value, func(tx *sql.Tx) error {
		_, err := s.repository.CompleteInitialSetupTx(r.Context(), tx, primaryAddress, mailboxName, emailAddress, r.FormValue("display_name"), hash)
		return err
	})
	if errors.Is(err, settings.ErrSetupUnavailable) {
		s.renderError(w, r, http.StatusForbidden, "The setup link is invalid, expired, or already used.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if s.applyConfig != nil {
		s.applyConfig(current)
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) setupData(token, message string) ui.PageData {
	cfg := s.currentConfig()
	return ui.PageData{Title: "First-run setup", PrimaryAddress: cfg.PrimaryAddress, SetupToken: token,
		SetupBaseURL: cfg.BaseURL.String(), SetupMailboxName: cfg.DisplayName,
		SetupDomainID: cfg.ResendDomainID, Error: message}
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	hasUsers, err := s.repository.HasUsers(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !hasUsers {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, ui.LoginPage(ui.PageData{PrimaryAddress: s.currentConfig().PrimaryAddress, Notice: noticeMessage(r.URL.Query().Get("notice"))}))
}

func (s *Server) loginPost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "Invalid sign-in form.")
		return
	}
	emailAddress := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	ip := s.clientIP(r)
	accountKey := emailAddress
	if !s.takeLoginAttempt(ip, accountKey) {
		s.render(w, r, http.StatusTooManyRequests, ui.LoginPage(ui.PageData{PrimaryAddress: s.currentConfig().PrimaryAddress, Error: "Too many sign-in attempts. Try again later."}))
		return
	}
	user, err := s.repository.FindUserByEmail(r.Context(), emailAddress)
	passwordHash := s.dummyPassword
	if err == nil {
		passwordHash = user.PasswordHash
	} else if !errors.Is(err, repository.ErrNotFound) {
		s.internalError(w, r, err)
		return
	}
	select {
	case s.passwordSlots <- struct{}{}:
	case <-r.Context().Done():
		return
	}
	passwordValid := auth.VerifyPassword(r.FormValue("password"), passwordHash)
	<-s.passwordSlots
	if err != nil || !passwordValid {
		s.render(w, r, http.StatusUnauthorized, ui.LoginPage(ui.PageData{PrimaryAddress: s.currentConfig().PrimaryAddress, Error: "Invalid email or password."}))
		return
	}
	sessionToken, err := auth.NewToken()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	csrfToken, err := auth.NewToken()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	sessionID, err := s.repository.CreateSession(r.Context(), user.ID, auth.TokenHash(sessionToken), auth.TokenHash(csrfToken),
		time.Now().Add(s.currentConfig().SessionTTL), ipHash(s.clientIP(r)), truncate(r.UserAgent(), 512))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	_ = s.repository.TouchSession(r.Context(), sessionID, user.ID, true)
	s.loginAccount.reset(accountKey)
	s.setSessionCookies(w, sessionToken, csrfToken)
	http.Redirect(w, r, "/inbox", http.StatusSeeOther)
}

func (s *Server) invitationPage(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	invitation, err := s.repository.InvitationByToken(r.Context(), auth.TokenHash(token))
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "This invitation is invalid or has expired.")
		return
	}
	s.render(w, r, http.StatusOK, ui.InvitationPage(ui.PageData{Title: "Accept invitation", Token: token, Invitation: invitation}))
}

func (s *Server) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "Invalid invitation form.")
		return
	}
	token := strings.TrimSpace(r.FormValue("token"))
	invitation, err := s.repository.InvitationByToken(r.Context(), auth.TokenHash(token))
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "This invitation is invalid or has expired.")
		return
	}
	password := r.FormValue("password")
	if password != r.FormValue("password_confirmation") {
		s.render(w, r, http.StatusUnprocessableEntity, ui.InvitationPage(ui.PageData{Title: "Accept invitation", Token: token, Invitation: invitation, Error: "The passwords do not match."}))
		return
	}
	hash, err := s.hashPassword(r.Context(), password)
	if err != nil {
		s.render(w, r, http.StatusUnprocessableEntity, ui.InvitationPage(ui.PageData{Title: "Accept invitation", Token: token, Invitation: invitation, Error: err.Error()}))
		return
	}
	if _, err := s.repository.AcceptInvitation(r.Context(), auth.TokenHash(token), hash); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			s.renderError(w, r, http.StatusNotFound, "This invitation is invalid or has expired.")
			return
		}
		s.internalError(w, r, err)
		return
	}
	http.Redirect(w, r, "/login?notice=invitation-accepted", http.StatusSeeOther)
}

func (s *Server) passwordResetPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, ui.PasswordResetPage(ui.PageData{Title: "Reset password", Notice: noticeMessage(r.URL.Query().Get("notice"))}))
}

func (s *Server) requestPasswordReset(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "Invalid password reset form.")
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	if !s.takeResetAttempt(s.clientIP(r), email) {
		s.render(w, r, http.StatusTooManyRequests, ui.PasswordResetPage(ui.PageData{Title: "Reset password", Error: "Too many reset requests. Try again later."}))
		return
	}
	token, tokenErr := auth.NewToken()
	user, userErr := s.repository.FindUserByEmail(r.Context(), email)
	if tokenErr != nil {
		s.logger.Error("generate password reset token", "request_id", requestID(r), "error", tokenErr)
	} else if userErr == nil {
		resetURL := strings.TrimRight(s.currentConfig().BaseURL.String(), "/") + "/password-reset/confirm?token=" + url.QueryEscape(token)
		queued := s.enqueueAccountEmail(accountEmail{
			recipient:   user.Email,
			subject:     "Reset your Litebox password",
			body:        fmt.Sprintf("A password reset was requested for your Litebox account.\n\nOpen this link within 30 minutes:\n%s\n\nIf you did not request this, you can ignore this message.", resetURL),
			resetUserID: user.ID, resetTokenHash: auth.TokenHash(token), resetTTL: 30 * time.Minute,
		})
		if !queued {
			s.logger.Error("password reset delivery queue is full", "request_id", requestID(r))
		}
	} else if !errors.Is(userErr, repository.ErrNotFound) {
		s.logger.Error("look up password reset account", "request_id", requestID(r), "error", userErr)
	}
	// Keep the public response uniform even though known accounts require a
	// little more local work. Provider I/O is handled by the bounded worker.
	if remaining := 100*time.Millisecond - time.Since(started); remaining > 0 {
		timer := time.NewTimer(remaining)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.Context().Done():
			return
		}
	}
	http.Redirect(w, r, "/password-reset?notice=reset-requested", http.StatusSeeOther)
}

func (s *Server) passwordResetConfirmPage(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if _, err := s.repository.UserForPasswordReset(r.Context(), auth.TokenHash(token)); err != nil {
		s.renderError(w, r, http.StatusNotFound, "This password reset link is invalid or has expired.")
		return
	}
	s.render(w, r, http.StatusOK, ui.PasswordResetConfirmPage(ui.PageData{Title: "Choose a new password", Token: token}))
}

func (s *Server) consumePasswordReset(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "Invalid password reset form.")
		return
	}
	token := strings.TrimSpace(r.FormValue("token"))
	if _, err := s.repository.UserForPasswordReset(r.Context(), auth.TokenHash(token)); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			s.renderError(w, r, http.StatusNotFound, "This password reset link is invalid or has expired.")
			return
		}
		s.internalError(w, r, err)
		return
	}
	password := r.FormValue("password")
	if password != r.FormValue("password_confirmation") {
		s.render(w, r, http.StatusUnprocessableEntity, ui.PasswordResetConfirmPage(ui.PageData{Title: "Choose a new password", Token: token, Error: "The passwords do not match."}))
		return
	}
	hash, err := s.hashPassword(r.Context(), password)
	if err != nil {
		s.render(w, r, http.StatusUnprocessableEntity, ui.PasswordResetConfirmPage(ui.PageData{Title: "Choose a new password", Token: token, Error: err.Error()}))
		return
	}
	if err := s.repository.ConsumePasswordReset(r.Context(), auth.TokenHash(token), hash); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			s.renderError(w, r, http.StatusNotFound, "This password reset link is invalid or has expired.")
			return
		}
		s.internalError(w, r, err)
		return
	}
	http.Redirect(w, r, "/login?notice=password-reset-complete", http.StatusSeeOther)
}

func (s *Server) sendAccountEmail(ctx context.Context, recipient, subject, body string) error {
	primary, err := s.repository.PrimaryMailbox(ctx)
	if err != nil {
		return err
	}
	_, err = s.provider.Send(ctx, provider.SendRequest{From: model.Address{Name: primary.DisplayName, Address: primary.Address},
		To: []model.Address{{Address: recipient}}, Subject: subject, Text: body, IdempotencyKey: "litebox-account/" + ids.New()})
	return err
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(s.currentConfig().CookieName); err == nil {
		_ = s.repository.DeleteSession(r.Context(), auth.TokenHash(cookie.Value))
	}
	s.clearCookies(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) selectMailbox(w http.ResponseWriter, r *http.Request) {
	requested := strings.TrimSpace(r.FormValue("mailbox_id"))
	mailbox, err := s.repository.MailboxForUser(r.Context(), authFrom(r).Session.User.ID, requested)
	if err != nil || mailbox.ID != requested {
		s.renderError(w, r, http.StatusForbidden, "You do not have access to that mailbox.")
		return
	}
	s.setMailboxCookie(w, mailbox.ID)
	destination := r.FormValue("return_to")
	if destination == "" || !strings.HasPrefix(destination, "/") || strings.HasPrefix(destination, "//") {
		destination = "/inbox"
	}
	destination = addMailboxQuery(destination, requested)
	http.Redirect(w, r, destination, http.StatusSeeOther)
}

func (s *Server) folder(w http.ResponseWriter, r *http.Request, folder string) {
	before, beforeID, err := parseThreadCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "This mailbox page link is invalid.")
		return
	}
	threads, hasMore, err := s.repository.ListThreadsPage(r.Context(), authFrom(r).Mailbox.ID, folder, 50, before, beforeID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	data := s.pageData(r, folderTitle(folder))
	data.CurrentFolder, data.Threads = folder, threads
	setPagination(&data, r, threads, hasMore)
	s.render(w, r, http.StatusOK, ui.MailboxPage(data))
}

func (s *Server) thread(w http.ResponseWriter, r *http.Request) {
	mailboxID := authFrom(r).Mailbox.ID
	thread, err := s.repository.ThreadByID(r.Context(), mailboxID, r.PathValue("threadID"))
	if err != nil {
		s.repositoryError(w, r, err)
		return
	}
	if authFrom(r).Mailbox.Role != "viewer" {
		_ = s.repository.MarkThreadRead(r.Context(), mailboxID, thread.ID, true)
		thread, _ = s.repository.ThreadByID(r.Context(), mailboxID, thread.ID)
	}
	folder := r.URL.Query().Get("folder")
	if folder != "search" && !validFolder(folder) {
		switch {
		case thread.IsTrashed:
			folder = "trash"
		case thread.IsArchived:
			folder = "archive"
		default:
			folder = "inbox"
		}
	}
	before, beforeID, err := parseThreadCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "This mailbox page link is invalid.")
		return
	}
	var threads []model.ThreadSummary
	var hasMore bool
	searchQuery := strings.TrimSpace(r.URL.Query().Get("q"))
	if folder == "search" {
		parsed, parseErr := search.Parse(searchQuery)
		if parseErr != nil {
			s.renderError(w, r, http.StatusBadRequest, "This search link is invalid.")
			return
		}
		threads, hasMore, err = s.repository.SearchThreadsPage(r.Context(), mailboxID, parsed, 50, before, beforeID)
	} else {
		threads, hasMore, err = s.repository.ListThreadsPage(r.Context(), mailboxID, folder, 50, before, beforeID)
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	data := s.pageData(r, thread.Subject)
	data.CurrentFolder, data.Threads, data.Thread = folder, threads, &thread
	data.SearchQuery, data.CurrentCursor = searchQuery, r.URL.Query().Get("cursor")
	if data.CurrentCursor != "" {
		data.FirstPageURL = contextListURL(folder, searchQuery, "", mailboxID)
	}
	if hasMore && len(threads) > 0 {
		data.NextPageURL = contextListURL(folder, searchQuery, encodeThreadCursor(threads[len(threads)-1]), mailboxID)
	}
	data.BackURL = contextListURL(folder, searchQuery, data.CurrentCursor, mailboxID)
	s.render(w, r, http.StatusOK, ui.MailboxPage(data))
}

func (s *Server) threadAction(w http.ResponseWriter, r *http.Request, action string) {
	if err := s.repository.ThreadAction(r.Context(), authFrom(r).Mailbox.ID, r.PathValue("threadID"), action); err != nil {
		s.repositoryError(w, r, err)
		return
	}
	destination := "/inbox"
	if action == "trash" {
		destination = "/trash"
	}
	http.Redirect(w, r, requestMailboxURL(r, destination, authFrom(r).Mailbox.ID), http.StatusSeeOther)
}

func (s *Server) threadRead(w http.ResponseWriter, r *http.Request, read bool) {
	if err := s.repository.MarkThreadRead(r.Context(), authFrom(r).Mailbox.ID, r.PathValue("threadID"), read); err != nil {
		s.repositoryError(w, r, err)
		return
	}
	http.Redirect(w, r, requestMailboxURL(r, "/inbox", authFrom(r).Mailbox.ID), http.StatusSeeOther)
}

func (s *Server) threadDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.repository.DeleteThread(r.Context(), authFrom(r).Mailbox.ID, r.PathValue("threadID")); err != nil {
		s.repositoryError(w, r, err)
		return
	}
	http.Redirect(w, r, requestMailboxURL(r, "/trash", authFrom(r).Mailbox.ID), http.StatusSeeOther)
}

func (s *Server) compose(w http.ResponseWriter, r *http.Request) {
	if authFrom(r).Mailbox.Role == "viewer" {
		s.renderError(w, r, http.StatusForbidden, "This mailbox membership is read-only.")
		return
	}
	draft := model.Draft{}
	data := s.pageData(r, "Compose")
	data.CurrentFolder, data.Draft = "drafts", &draft
	s.render(w, r, http.StatusOK, ui.MailboxPage(data))
}

func (s *Server) reply(w http.ResponseWriter, r *http.Request, all bool) {
	if authFrom(r).Mailbox.Role == "viewer" {
		s.renderError(w, r, http.StatusForbidden, "This mailbox membership is read-only.")
		return
	}
	mailboxID := authFrom(r).Mailbox.ID
	thread, err := s.repository.ThreadByID(r.Context(), mailboxID, r.PathValue("threadID"))
	if err != nil {
		s.repositoryError(w, r, err)
		return
	}
	if len(thread.Messages) == 0 {
		s.internalError(w, r, errors.New("thread contains no messages"))
		return
	}
	original := thread.Messages[len(thread.Messages)-1]
	replyTarget := original.From
	if values := original.Recipients["reply_to"]; len(values) > 0 {
		replyTarget = values[0]
	}
	to := []model.Address{replyTarget}
	if all {
		localAddresses, addressErr := s.repository.MailboxAddressSet(r.Context(), mailboxID)
		if addressErr != nil {
			s.internalError(w, r, addressErr)
			return
		}
		to = mailx.ReplyAll(replyTarget, original.From, original.Recipients["to"], original.Recipients["cc"], localAddresses)
	}
	draft := model.Draft{ThreadID: thread.ID, ReplyToMessageID: original.ID, To: to, Subject: mailx.ReplySubject(original.Subject)}
	data := s.pageData(r, "Reply")
	data.CurrentFolder, data.Draft = "drafts", &draft
	s.render(w, r, http.StatusOK, ui.MailboxPage(data))
}

func (s *Server) createDraft(w http.ResponseWriter, r *http.Request) {
	draft, ok := s.parseDraftForm(w, r, "")
	if !ok {
		return
	}
	mailboxID := authFrom(r).Mailbox.ID
	created, err := s.repository.CreateDraft(r.Context(), mailboxID, draft)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if r.FormValue("intent") == "send" {
		if len(created.To) == 0 {
			data := s.pageData(r, "Compose")
			data.Draft, data.Error = &created, "Add at least one recipient."
			s.render(w, r, http.StatusUnprocessableEntity, ui.MailboxPage(data))
			return
		}
		if !s.allowSend(w, r) {
			return
		}
		sender, senderErr := s.sender(r)
		if senderErr != nil {
			data := s.pageData(r, "Compose")
			data.Draft, data.Error = &created, "Choose an available From address."
			s.render(w, r, http.StatusUnprocessableEntity, ui.MailboxPage(data))
			return
		}
		_, threadID, err := s.repository.QueueDraft(r.Context(), created.ID, mailboxID, sender)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		http.Redirect(w, r, requestMailboxURL(r, "/threads/"+threadID+"?folder=sent&notice=message-queued", mailboxID), http.StatusSeeOther)
		return
	}
	if r.FormValue("intent") == "continue" {
		http.Redirect(w, r, requestMailboxURL(r, "/drafts/"+created.ID, mailboxID), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, requestMailboxURL(r, "/drafts?notice=draft-saved", mailboxID), http.StatusSeeOther)
}

func (s *Server) drafts(w http.ResponseWriter, r *http.Request) {
	drafts, err := s.repository.ListDrafts(r.Context(), authFrom(r).Mailbox.ID, 100)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	data := s.pageData(r, "Drafts")
	data.CurrentFolder, data.Drafts = "drafts", drafts
	s.render(w, r, http.StatusOK, ui.MailboxPage(data))
}

func (s *Server) draft(w http.ResponseWriter, r *http.Request) {
	draft, err := s.repository.DraftByID(r.Context(), authFrom(r).Mailbox.ID, r.PathValue("draftID"))
	if err != nil {
		s.repositoryError(w, r, err)
		return
	}
	data := s.pageData(r, "Draft")
	data.CurrentFolder, data.Draft = "drafts", &draft
	s.render(w, r, http.StatusOK, ui.MailboxPage(data))
}

func (s *Server) saveDraft(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("draftID")
	draft, ok := s.parseDraftForm(w, r, id)
	if !ok {
		return
	}
	mailboxID := authFrom(r).Mailbox.ID
	if err := s.repository.SaveDraft(r.Context(), mailboxID, draft); err != nil {
		s.repositoryError(w, r, err)
		return
	}
	if r.FormValue("intent") == "send" {
		if len(draft.To) == 0 {
			data := s.pageData(r, "Draft")
			data.Draft, data.Error = &draft, "Add at least one recipient."
			s.render(w, r, http.StatusUnprocessableEntity, ui.MailboxPage(data))
			return
		}
		if !s.allowSend(w, r) {
			return
		}
		sender, senderErr := s.sender(r)
		if senderErr != nil {
			data := s.pageData(r, "Draft")
			data.Draft, data.Error = &draft, "Choose an available From address."
			s.render(w, r, http.StatusUnprocessableEntity, ui.MailboxPage(data))
			return
		}
		_, threadID, err := s.repository.QueueDraft(r.Context(), id, mailboxID, sender)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		http.Redirect(w, r, requestMailboxURL(r, "/threads/"+threadID+"?folder=sent&notice=message-queued", mailboxID), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, requestMailboxURL(r, "/drafts?notice=draft-saved", mailboxID), http.StatusSeeOther)
}

func (s *Server) sendDraft(w http.ResponseWriter, r *http.Request) {
	mailboxID := authFrom(r).Mailbox.ID
	draft, err := s.repository.DraftByID(r.Context(), mailboxID, r.PathValue("draftID"))
	if err != nil {
		s.repositoryError(w, r, err)
		return
	}
	if len(draft.To)+len(draft.Cc)+len(draft.Bcc) == 0 {
		data := s.pageData(r, "Draft")
		data.CurrentFolder, data.Draft, data.Error = "drafts", &draft, "Add at least one recipient."
		s.render(w, r, http.StatusUnprocessableEntity, ui.MailboxPage(data))
		return
	}
	if !s.allowSend(w, r) {
		return
	}
	sender, senderErr := s.sender(r)
	if senderErr != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, "Choose an available From address.")
		return
	}
	_, threadID, err := s.repository.QueueDraft(r.Context(), draft.ID, mailboxID, sender)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	http.Redirect(w, r, requestMailboxURL(r, "/threads/"+threadID+"?folder=sent&notice=message-queued", mailboxID), http.StatusSeeOther)
}

func (s *Server) allowSend(w http.ResponseWriter, r *http.Request) bool {
	if s.send.take(authFrom(r).Session.User.ID) {
		return true
	}
	w.Header().Set("Retry-After", "3600")
	s.renderError(w, r, http.StatusTooManyRequests, "The hourly send limit has been reached. Try again later.")
	return false
}

func (s *Server) parseDraftForm(w http.ResponseWriter, r *http.Request, id string) (model.Draft, bool) {
	to, err := mailx.ParseAddresses(r.FormValue("to"))
	if err != nil {
		s.renderDraftError(w, r, id, "The To field contains an invalid address.")
		return model.Draft{}, false
	}
	cc, err := mailx.ParseAddresses(r.FormValue("cc"))
	if err != nil {
		s.renderDraftError(w, r, id, "The Cc field contains an invalid address.")
		return model.Draft{}, false
	}
	bcc, err := mailx.ParseAddresses(r.FormValue("bcc"))
	if err != nil {
		s.renderDraftError(w, r, id, "The Bcc field contains an invalid address.")
		return model.Draft{}, false
	}
	if len(to)+len(cc)+len(bcc) > 20 {
		s.renderDraftError(w, r, id, "A message can have at most 20 recipients.")
		return model.Draft{}, false
	}
	subject := cleanFormHeader(r.FormValue("subject"))
	body := r.FormValue("body")
	if int64(len(body)) > s.currentConfig().MaxMessageTextBytes {
		s.renderDraftError(w, r, id, "The message body is too large.")
		return model.Draft{}, false
	}
	return model.Draft{ID: id, ThreadID: r.FormValue("thread_id"), ReplyToMessageID: r.FormValue("reply_to_message_id"),
		To: to, Cc: cc, Bcc: bcc, Subject: subject, TextBody: body}, true
}

func (s *Server) renderDraftError(w http.ResponseWriter, r *http.Request, id, message string) {
	draft := model.Draft{ID: id, To: []model.Address{}, Subject: r.FormValue("subject"), TextBody: r.FormValue("body")}
	data := s.pageData(r, "Draft")
	data.CurrentFolder, data.Draft, data.Error = "drafts", &draft, message
	s.render(w, r, http.StatusUnprocessableEntity, ui.MailboxPage(data))
}

func (s *Server) uploadAttachment(w http.ResponseWriter, r *http.Request) {
	draftID := r.PathValue("draftID")
	draft, err := s.repository.DraftByID(r.Context(), authFrom(r).Mailbox.ID, draftID)
	if err != nil {
		s.repositoryError(w, r, err)
		return
	}
	if len(draft.Attachments) >= s.currentConfig().MaxAttachmentCount {
		s.renderError(w, r, http.StatusUnprocessableEntity, "This draft has too many attachments.")
		return
	}
	file, header, err := r.FormFile("attachment")
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "Choose a file to attach.")
		return
	}
	defer file.Close()
	var total int64
	for _, attachment := range draft.Attachments {
		total += attachment.SizeBytes
	}
	if header.Size < 0 || total+header.Size > s.currentConfig().MaxOutboundAttachmentBytes {
		s.renderError(w, r, http.StatusRequestEntityTooLarge, "Attachments exceed the configured 25 MiB budget.")
		return
	}
	attachmentID := ids.New()
	key := "drafts/" + draftID + "/" + attachmentID
	contentType := defaultUploadContentType(header.Header.Get("Content-Type"))
	info, err := s.store.Put(r.Context(), key, file, header.Size, contentType)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	attachment := model.Attachment{ID: attachmentID, DraftID: draftID, Filename: service.SafeFilename(header.Filename),
		SafeFilename: service.SafeFilename(header.Filename), ContentType: contentType, StorageBackend: "filesystem",
		StorageKey: key, SizeBytes: info.Size, SHA256: info.SHA256, StorageStatus: "ready", CreatedAt: time.Now().UTC()}
	cfg := s.currentConfig()
	if err := s.repository.AddDraftAttachmentWithinLimits(r.Context(), attachment, cfg.MaxAttachmentCount, cfg.MaxOutboundAttachmentBytes); err != nil {
		_ = s.store.Delete(r.Context(), key)
		if errors.Is(err, repository.ErrAttachmentLimit) {
			s.renderError(w, r, http.StatusRequestEntityTooLarge, "Attachments exceed the configured budget.")
			return
		}
		s.internalError(w, r, err)
		return
	}
	http.Redirect(w, r, requestMailboxURL(r, "/drafts/"+draftID+"?notice=attachment-added", authFrom(r).Mailbox.ID), http.StatusSeeOther)
}

func (s *Server) deleteDraftAttachment(w http.ResponseWriter, r *http.Request) {
	key, err := s.repository.DeleteDraftAttachment(r.Context(), authFrom(r).Mailbox.ID, r.PathValue("draftID"), r.PathValue("attachmentID"))
	if err != nil {
		s.repositoryError(w, r, err)
		return
	}
	_ = s.store.Delete(r.Context(), key)
	http.Redirect(w, r, requestMailboxURL(r, "/drafts/"+r.PathValue("draftID")+"?notice=attachment-removed", authFrom(r).Mailbox.ID), http.StatusSeeOther)
}

func (s *Server) deleteDraft(w http.ResponseWriter, r *http.Request) {
	keys, err := s.repository.DeleteDraft(r.Context(), authFrom(r).Mailbox.ID, r.PathValue("draftID"))
	if err != nil {
		s.repositoryError(w, r, err)
		return
	}
	for _, key := range keys {
		_ = s.store.Delete(r.Context(), key)
	}
	http.Redirect(w, r, requestMailboxURL(r, "/drafts?notice=draft-discarded", authFrom(r).Mailbox.ID), http.StatusSeeOther)
}

func (s *Server) attachment(w http.ResponseWriter, r *http.Request, inline bool) {
	attachment, err := s.repository.AttachmentByID(r.Context(), authFrom(r).Mailbox.ID, r.PathValue("attachmentID"))
	if err != nil || attachment.StorageStatus != "ready" {
		s.renderError(w, r, http.StatusNotFound, "This attachment is not available.")
		return
	}
	reader, info, err := s.store.Get(r.Context(), attachment.StorageKey)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "This attachment is missing from private storage.")
		return
	}
	defer reader.Close()
	disposition := "attachment"
	if inline && inlineAllowed(attachment.ContentType) {
		disposition = "inline"
	}
	w.Header().Set("Content-Type", attachment.ContentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": attachment.SafeFilename}))
	w.Header().Set("Content-Length", fmt.Sprint(info.Size))
	w.Header().Set("Cache-Control", "private, no-store")
	if attachment.SHA256 != "" {
		w.Header().Set("ETag", `"sha256-`+attachment.SHA256+`"`)
	}
	_, _ = io.Copy(w, reader)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("q"))
	query, err := search.Parse(raw)
	if err != nil {
		data := s.pageData(r, "Search")
		data.CurrentFolder, data.SearchQuery, data.Error = "search", raw, err.Error()
		s.render(w, r, http.StatusUnprocessableEntity, ui.MailboxPage(data))
		return
	}
	before, beforeID, err := parseThreadCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "This search page link is invalid.")
		return
	}
	threads, hasMore, err := s.repository.SearchThreadsPage(r.Context(), authFrom(r).Mailbox.ID, query, 50, before, beforeID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	data := s.pageData(r, "Search")
	data.CurrentFolder, data.SearchQuery, data.Threads = "search", raw, threads
	setPagination(&data, r, threads, hasMore)
	s.render(w, r, http.StatusOK, ui.MailboxPage(data))
}

func setPagination(data *ui.PageData, r *http.Request, threads []model.ThreadSummary, hasMore bool) {
	data.CurrentCursor = r.URL.Query().Get("cursor")
	if data.CurrentCursor != "" {
		query := r.URL.Query()
		query.Del("cursor")
		data.FirstPageURL = r.URL.Path
		if encoded := query.Encode(); encoded != "" {
			data.FirstPageURL += "?" + encoded
		}
	}
	if hasMore && len(threads) > 0 {
		query := r.URL.Query()
		query.Set("cursor", encodeThreadCursor(threads[len(threads)-1]))
		data.NextPageURL = r.URL.Path + "?" + query.Encode()
	}
}

func encodeThreadCursor(thread model.ThreadSummary) string {
	value := strconv.FormatInt(thread.LatestMessageAt.UnixMilli(), 10) + "." + thread.ID
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func parseThreadCursor(value string) (time.Time, string, error) {
	if value == "" {
		return time.Time{}, "", nil
	}
	if len(value) > 512 {
		return time.Time{}, "", errors.New("cursor is too long")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return time.Time{}, "", err
	}
	timestamp, identifier, found := strings.Cut(string(decoded), ".")
	if !found || identifier == "" || len(identifier) > 128 {
		return time.Time{}, "", errors.New("invalid cursor")
	}
	milliseconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || milliseconds <= 0 {
		return time.Time{}, "", errors.New("invalid cursor timestamp")
	}
	return time.UnixMilli(milliseconds).UTC(), identifier, nil
}

func contextListURL(folder, rawSearch, cursor, mailboxID string) string {
	path := "/" + folder
	query := url.Values{}
	if folder == "search" {
		path = "/search"
		query.Set("q", rawSearch)
	}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	if mailboxID != "" {
		query.Set("mailbox", mailboxID)
	}
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return path
}

func addMailboxQuery(path, mailboxID string) string {
	if mailboxID == "" {
		return path
	}
	parsed, err := url.Parse(path)
	if err != nil {
		return path
	}
	query := parsed.Query()
	query.Set("mailbox", mailboxID)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func requestMailboxURL(r *http.Request, path, mailboxID string) string {
	if r.URL.Query().Get("mailbox") == "" {
		return path
	}
	return addMailboxQuery(path, mailboxID)
}

func (s *Server) admin(w http.ResponseWriter, r *http.Request) {
	state := authFrom(r)
	data := s.pageData(r, "Settings")
	data.CurrentFolder = "admin"
	switch r.URL.Path {
	case "/settings", "/settings/mailboxes":
		data.Title, data.SettingsSection = "Mailboxes", "mailboxes"
	case "/settings/people":
		if !data.CanManage {
			s.renderError(w, r, http.StatusForbidden, "Mailbox administrator access is required.")
			return
		}
		members, err := s.repository.ListMailboxMembers(r.Context(), state.Mailbox.ID)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		data.Title, data.SettingsSection, data.Members = "People", "people", members
	case "/settings/sessions":
		sessions, err := s.repository.ListSessions(r.Context(), state.Session.User.ID)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		data.Title, data.SettingsSection, data.Sessions, data.CurrentSession = "Sessions", "sessions", sessions, state.Session.ID
	case "/settings/digest":
		if digest, err := s.repository.GetDigestSubscription(r.Context(), state.Session.User.ID); err == nil {
			data.Digest = digest
		} else if !errors.Is(err, repository.ErrNotFound) {
			s.internalError(w, r, err)
			return
		}
		data.DigestMailboxes = data.Mailboxes
		preview := data.Digest
		if preview.ID == "" {
			preview.UserID, preview.Frequency, preview.MailboxScope, preview.Timezone = state.Session.User.ID, "daily", "all", "UTC"
		}
		previewSince, previewUntil := digestPreviewWindow(preview, time.Now().UTC())
		if counts, countErr := s.repository.DigestCounts(r.Context(), preview, previewSince, previewUntil); countErr == nil {
			data.DigestPreview = counts
		}
		data.Title, data.SettingsSection = "Email summary", "digest"
	default:
		if !data.CanOperateSystem {
			s.renderError(w, r, http.StatusForbidden, "Primary mailbox administrator access is required for system operations.")
			return
		}
		stats, err := s.repository.SystemStats(r.Context(), s.currentConfig().DBPath)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		jobList, err := s.repository.ListJobs(r.Context(), 50)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		webhooks, err := s.repository.ListWebhooks(r.Context(), 50)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		health := "Healthy"
		if err := s.store.Health(r.Context()); err != nil {
			health = "Unavailable"
		}
		data.Title, data.SettingsSection = "System", "system"
		cfg := s.currentConfig()
		data.RuntimeBaseURL, data.RuntimeSessionTTL, data.RuntimeLogLevel = cfg.BaseURL.String(), int(cfg.SessionTTL/time.Hour), cfg.LogLevel
		data.Stats, data.Jobs, data.Webhooks, data.StorageHealth = stats, jobList, webhooks, health
	}
	s.render(w, r, http.StatusOK, ui.MailboxPage(data))
}

func (s *Server) updateDigest(w http.ResponseWriter, r *http.Request) {
	state := authFrom(r)
	sendHour, err := strconv.Atoi(strings.TrimSpace(r.FormValue("send_hour")))
	if err != nil {
		sendHour = 8
	}
	mailboxScope := r.FormValue("mailbox_scope")
	if mailboxScope == "" {
		mailboxScope = "all"
	}
	var selected []string
	for _, id := range r.Form["mailbox_ids"] {
		if id != "" {
			selected = append(selected, id)
		}
	}
	sub := model.DigestSubscription{UserID: state.Session.User.ID, RecipientEmail: strings.TrimSpace(r.FormValue("recipient_email")),
		Frequency: r.FormValue("frequency"), Timezone: strings.TrimSpace(r.FormValue("timezone")), SendHour: sendHour,
		MailboxScope: mailboxScope, MailboxIDs: selected, Enabled: r.FormValue("enabled") == "on"}
	if sub.Timezone == "" {
		sub.Timezone = "UTC"
	}
	if err := s.repository.SaveDigestSubscription(r.Context(), sub); err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	http.Redirect(w, r, requestMailboxURL(r, "/settings/digest", state.Mailbox.ID), http.StatusSeeOther)
}

func (s *Server) sendDigestTest(w http.ResponseWriter, r *http.Request) {
	state := authFrom(r)
	sub, err := s.repository.GetDigestSubscription(r.Context(), state.Session.User.ID)
	if err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, "Save the summary schedule before sending a test.")
		return
	}
	since, until := digestPreviewWindow(sub, time.Now().UTC())
	if err := s.mailbox.SendDigestPreview(r.Context(), sub, since, until); err != nil {
		s.logger.Error("digest test failed", "request_id", requestID(r), "error", err)
		http.Redirect(w, r, requestMailboxURL(r, "/settings/digest?notice=digest-test-failed", state.Mailbox.ID), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, requestMailboxURL(r, "/settings/digest?notice=digest-test-sent", state.Mailbox.ID), http.StatusSeeOther)
}

func digestPreviewWindow(sub model.DigestSubscription, now time.Time) (time.Time, time.Time) {
	location, err := time.LoadLocation(sub.Timezone)
	if err != nil {
		location = time.UTC
	}
	local := now.In(location)
	end := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	days := 1
	if sub.Frequency == "weekly" {
		days = 7
	}
	return end.AddDate(0, 0, -days).UTC(), end.UTC()
}

func (s *Server) updateSystemSettings(w http.ResponseWriter, r *http.Request) {
	state := authFrom(r)
	if !state.Mailbox.IsPrimary || (state.Mailbox.Role != "owner" && state.Mailbox.Role != "admin") || s.settings == nil {
		s.renderError(w, r, http.StatusForbidden, "Primary mailbox administrator access is required for system settings.")
		return
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	current := s.currentConfig()
	baseURL, err := config.ParseBaseURL(r.FormValue("base_url"), current.Environment == "production")
	if err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, "Enter a valid public URL (HTTPS is required in production).")
		return
	}
	ttl, err := strconv.Atoi(r.FormValue("session_ttl_hours"))
	if err != nil || ttl < 1 || ttl > 8760 {
		s.renderError(w, r, http.StatusUnprocessableEntity, "Session lifetime must be between 1 and 8760 hours.")
		return
	}
	logLevel := strings.ToLower(strings.TrimSpace(r.FormValue("log_level")))
	if logLevel != "debug" && logLevel != "info" && logLevel != "warn" && logLevel != "error" {
		s.renderError(w, r, http.StatusUnprocessableEntity, "Choose a valid log level.")
		return
	}
	apiKey, secret, domain := strings.TrimSpace(r.FormValue("resend_api_key")), strings.TrimSpace(r.FormValue("resend_webhook_secret")), strings.TrimSpace(r.FormValue("resend_domain_id"))
	if apiKey == "" {
		apiKey = current.ResendAPIKey
	}
	if secret == "" {
		secret = current.ResendWebhookSecret
	}
	if domain == "" {
		domain = current.ResendDomainID
	}
	next := current
	next.BaseURL, next.SessionTTL, next.LogLevel = baseURL, time.Duration(ttl)*time.Hour, logLevel
	next.ResendAPIKey, next.ResendWebhookSecret, next.ResendDomainID = apiKey, secret, domain
	next.AllowUnconfigured = false
	if err := next.Validate(); err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, "The system settings are invalid: "+err.Error())
		return
	}
	value := settings.Values{Configured: true, BaseURL: baseURL.String(), SessionTTLHours: ttl, LogLevel: logLevel,
		MaxWebhookBodyBytes: current.MaxWebhookBodyBytes, MaxMessageTextBytes: current.MaxMessageTextBytes, MaxUploadRequestBytes: current.MaxUploadRequestBytes,
		MaxOutboundAttachmentBytes: current.MaxOutboundAttachmentBytes, MaxAttachmentCount: current.MaxAttachmentCount,
		ResendAPIKey: apiKey, ResendWebhookSecret: secret, ResendDomainID: domain}
	if err := s.settings.Save(r.Context(), value); err != nil {
		s.internalError(w, r, err)
		return
	}
	if s.applyConfig != nil {
		s.applyConfig(next)
	}
	http.Redirect(w, r, requestMailboxURL(r, "/admin/system", state.Mailbox.ID), http.StatusSeeOther)
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	if err := s.repository.UpdateMailboxDisplayName(r.Context(), authFrom(r).Mailbox.ID, r.FormValue("display_name")); err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	http.Redirect(w, r, requestMailboxURL(r, "/settings/mailboxes", authFrom(r).Mailbox.ID), http.StatusSeeOther)
}

func (s *Server) createMailbox(w http.ResponseWriter, r *http.Request) {
	state := authFrom(r)
	mailbox, err := s.repository.CreateMailbox(r.Context(), state.Session.User.ID, r.FormValue("address"), r.FormValue("display_name"))
	if err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, "The mailbox could not be created. Check that its address is valid and unused.")
		return
	}
	s.setMailboxCookie(w, mailbox.ID)
	http.Redirect(w, r, addMailboxQuery("/settings/mailboxes", mailbox.ID), http.StatusSeeOther)
}

func (s *Server) addAlias(w http.ResponseWriter, r *http.Request) {
	state := authFrom(r)
	if r.PathValue("mailboxID") != state.Mailbox.ID {
		s.renderError(w, r, http.StatusForbidden, "The selected mailbox changed. Refresh and try again.")
		return
	}
	if err := s.repository.AddMailboxAddress(r.Context(), state.Mailbox.ID, r.FormValue("address"), r.FormValue("display_name")); err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, "The alias must be a valid, unused email address.")
		return
	}
	http.Redirect(w, r, requestMailboxURL(r, "/settings/mailboxes", state.Mailbox.ID), http.StatusSeeOther)
}

func (s *Server) deleteAlias(w http.ResponseWriter, r *http.Request) {
	state := authFrom(r)
	if r.PathValue("mailboxID") != state.Mailbox.ID {
		s.renderError(w, r, http.StatusForbidden, "The selected mailbox changed. Refresh and try again.")
		return
	}
	if err := s.repository.DeleteMailboxAddress(r.Context(), state.Mailbox.ID, r.PathValue("addressID")); err != nil {
		s.repositoryError(w, r, err)
		return
	}
	http.Redirect(w, r, requestMailboxURL(r, "/settings/mailboxes", state.Mailbox.ID), http.StatusSeeOther)
}

func (s *Server) updateAliasColor(w http.ResponseWriter, r *http.Request) {
	state := authFrom(r)
	if r.PathValue("mailboxID") != state.Mailbox.ID {
		s.renderError(w, r, http.StatusForbidden, "The selected mailbox changed. Refresh and try again.")
		return
	}
	if err := s.repository.UpdateAddressColor(r.Context(), state.Mailbox.ID, r.PathValue("addressID"), r.FormValue("color")); err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	http.Redirect(w, r, requestMailboxURL(r, "/settings/mailboxes", state.Mailbox.ID), http.StatusSeeOther)
}

func (s *Server) addMember(w http.ResponseWriter, r *http.Request) {
	state := authFrom(r)
	if r.PathValue("mailboxID") != state.Mailbox.ID {
		s.renderError(w, r, http.StatusForbidden, "The selected mailbox changed. Refresh and try again.")
		return
	}
	role := strings.TrimSpace(r.FormValue("role"))
	if role == "owner" && state.Mailbox.Role != "owner" {
		s.renderError(w, r, http.StatusForbidden, "Only an owner can grant ownership.")
		return
	}
	password := r.FormValue("password")
	if password == "" {
		token, err := auth.NewToken()
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		invitation, err := s.repository.CreateInvitation(r.Context(), state.Session.User.ID, state.Mailbox.ID, r.FormValue("email"), r.FormValue("display_name"), role, auth.TokenHash(token), time.Now().UTC().Add(72*time.Hour))
		if err != nil {
			s.renderError(w, r, http.StatusUnprocessableEntity, err.Error())
			return
		}
		inviteURL := strings.TrimRight(s.currentConfig().BaseURL.String(), "/") + "/invite?token=" + url.QueryEscape(token)
		body := fmt.Sprintf("You have been invited to %s on Litebox.\n\nAccept the invitation within 72 hours:\n%s\n\nYour mailbox role: %s", invitation.MailboxName, inviteURL, role)
		if err := s.sendAccountEmail(r.Context(), invitation.Email, "You have been invited to Litebox", body); err != nil {
			s.logger.Error("invitation email failed", "request_id", requestID(r), "error", err)
			s.renderError(w, r, http.StatusBadGateway, "The invitation was created but could not be delivered. Check provider settings and try again.")
			return
		}
		http.Redirect(w, r, requestMailboxURL(r, "/settings/people?notice=invitation-sent", state.Mailbox.ID), http.StatusSeeOther)
		return
	} else {
		hash, err := s.hashPassword(r.Context(), password)
		if err != nil {
			s.renderError(w, r, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if _, err := s.repository.CreateUserWithMembership(r.Context(), state.Session.User.ID, state.Mailbox.ID,
			r.FormValue("email"), r.FormValue("display_name"), hash, role); err != nil {
			s.renderError(w, r, http.StatusUnprocessableEntity, "The administrator could not be created. The email may already be in use.")
			return
		}
	}
	http.Redirect(w, r, requestMailboxURL(r, "/settings/people?notice=person-added", state.Mailbox.ID), http.StatusSeeOther)
}

func (s *Server) removeMember(w http.ResponseWriter, r *http.Request) {
	state := authFrom(r)
	if r.PathValue("mailboxID") != state.Mailbox.ID {
		s.renderError(w, r, http.StatusForbidden, "The selected mailbox changed. Refresh and try again.")
		return
	}
	targetRole, err := s.repository.MailboxRoleForUser(r.Context(), state.Mailbox.ID, r.PathValue("userID"))
	if err != nil {
		s.repositoryError(w, r, err)
		return
	}
	if targetRole == "owner" && state.Mailbox.Role != "owner" {
		s.renderError(w, r, http.StatusForbidden, "Only an owner can revoke another owner.")
		return
	}
	if err := s.repository.RemoveMailboxAccess(r.Context(), state.Session.User.ID, state.Mailbox.ID, r.PathValue("userID")); err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if r.PathValue("userID") == state.Session.User.ID {
		s.setMailboxCookie(w, "")
		http.Redirect(w, r, requestMailboxURL(r, "/inbox", state.Mailbox.ID), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, requestMailboxURL(r, "/settings/people", state.Mailbox.ID), http.StatusSeeOther)
}

func (s *Server) revokeSession(w http.ResponseWriter, r *http.Request) {
	state := authFrom(r)
	if err := s.repository.DeleteUserSession(r.Context(), state.Session.User.ID, r.PathValue("sessionID")); err != nil {
		s.repositoryError(w, r, err)
		return
	}
	if r.PathValue("sessionID") == state.Session.ID {
		s.clearCookies(w)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, requestMailboxURL(r, "/settings/sessions", state.Mailbox.ID), http.StatusSeeOther)
}

func (s *Server) retryJob(w http.ResponseWriter, r *http.Request) {
	if !authFrom(r).Mailbox.IsPrimary {
		s.renderError(w, r, http.StatusForbidden, "Primary mailbox administrator access is required for system operations.")
		return
	}
	if err := s.repository.RetryJob(r.Context(), r.PathValue("jobID")); err != nil {
		s.repositoryError(w, r, err)
		return
	}
	http.Redirect(w, r, requestMailboxURL(r, "/admin/system", authFrom(r).Mailbox.ID), http.StatusSeeOther)
}

func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.currentConfig().MaxWebhookBodyBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	headers := provider.WebhookHeaders{ID: r.Header.Get("svix-id"), Timestamp: r.Header.Get("svix-timestamp"), Signature: r.Header.Get("svix-signature")}
	if err := s.provider.VerifyWebhook(raw, headers); err != nil {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	var payload struct {
		Type      string    `json:"type"`
		CreatedAt time.Time `json:"created_at"`
		Data      struct {
			EmailID string `json:"email_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.Type == "" || payload.Data.EmailID == "" {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	eventID := ids.New()
	event := model.WebhookEvent{ID: eventID, SvixID: headers.ID, EventType: payload.Type, ResendEmailID: payload.Data.EmailID,
		RawPayload: string(raw), ReceivedAt: time.Now().UTC()}
	jobKind, dedupeKey := "process_provider_event", "provider-event:"+headers.ID
	jobPayload := map[string]string{"webhook_event_id": eventID}
	if payload.Type == "email.received" {
		jobKind, dedupeKey = "ingest_inbound", "ingest:"+payload.Data.EmailID
		jobPayload["resend_email_id"] = payload.Data.EmailID
	} else if !isDeliveryEvent(payload.Type) {
		w.WriteHeader(http.StatusOK)
		return
	}
	if _, err := s.repository.PersistWebhook(r.Context(), event, payload.CreatedAt, jobKind, dedupeKey, jobPayload); err != nil {
		s.logger.Error("failed to persist verified webhook", "request_id", requestID(r), "svix_id", headers.ID, "error", err)
		http.Error(w, "temporary failure", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) pageData(r *http.Request, title string) ui.PageData {
	state := authFrom(r)
	mailboxes, _ := s.repository.ListMailboxesForUser(r.Context(), state.Session.User.ID)
	canManage := state.Mailbox.Role == "owner" || state.Mailbox.Role == "admin"
	return ui.PageData{Title: title, PrimaryAddress: state.Mailbox.Address, MailboxName: state.Mailbox.DisplayName,
		Mailbox: state.Mailbox, Mailboxes: mailboxes, Addresses: state.Mailbox.Addresses,
		User: state.Session.User, CSRFToken: state.CSRFToken, CanManage: canManage, CanWrite: state.Mailbox.Role != "viewer",
		CanOperateSystem: canManage && state.Mailbox.IsPrimary, Notice: noticeMessage(r.URL.Query().Get("notice"))}
}

func noticeMessage(code string) string {
	switch code {
	case "message-queued":
		return "Message queued for delivery"
	case "draft-saved":
		return "Draft saved"
	case "attachment-added":
		return "Attachment added"
	case "attachment-removed":
		return "Attachment removed"
	case "draft-discarded":
		return "Draft discarded"
	case "digest-test-sent":
		return "Summary test sent"
	case "digest-test-failed":
		return "Summary test could not be delivered"
	case "password-reset-complete":
		return "Password updated. Sign in again."
	case "invitation-accepted":
		return "Invitation accepted. You can sign in now."
	case "reset-requested":
		return "If that email belongs to a Litebox account, a reset link is on its way."
	case "invitation-sent":
		return "Invitation sent"
	case "person-added":
		return "Person added"
	default:
		return ""
	}
}

func (s *Server) sender(r *http.Request) (model.Address, error) {
	return s.repository.SenderForMailbox(r.Context(), authFrom(r).Mailbox.ID, r.FormValue("from_address"))
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, component templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	if err := component.Render(r.Context(), w); err != nil {
		s.logger.Error("failed to render page", "request_id", requestID(r), "error", err)
	}
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, message string) {
	s.render(w, r, status, ui.ErrorPage(status, message))
}

func (s *Server) repositoryError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, repository.ErrNotFound) {
		s.renderError(w, r, http.StatusNotFound, "The requested item does not exist.")
		return
	}
	s.internalError(w, r, err)
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.logger.Error("HTTP request failed", slog.String("request_id", requestID(r)), slog.String("error", err.Error()))
	s.renderError(w, r, http.StatusInternalServerError, "The request could not be completed.")
}

func truncate(value string, maximum int) string {
	if len(value) > maximum {
		return value[:maximum]
	}
	return value
}

func cleanFormHeader(value string) string {
	return strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(value))
}

func validFolder(value string) bool {
	switch value {
	case "inbox", "sent", "archive", "starred", "trash":
		return true
	default:
		return false
	}
}

func folderTitle(value string) string {
	if value == "" {
		return "Inbox"
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func inlineAllowed(value string) bool {
	switch value {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func defaultUploadContentType(value string) string {
	if mediaType, _, err := mime.ParseMediaType(value); err == nil && mediaType != "" {
		return mediaType
	}
	return "application/octet-stream"
}

func isDeliveryEvent(value string) bool {
	switch value {
	case "email.sent", "email.delivered", "email.delivery_delayed", "email.bounced", "email.failed", "email.suppressed", "email.complained":
		return true
	default:
		return false
	}
}
