package httpserver

import (
	"context"
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
	"github.com/Nader-jo/Litebox/internal/ids"
	mailx "github.com/Nader-jo/Litebox/internal/mail"
	"github.com/Nader-jo/Litebox/internal/model"
	"github.com/Nader-jo/Litebox/internal/provider"
	"github.com/Nader-jo/Litebox/internal/repository"
	"github.com/Nader-jo/Litebox/internal/search"
	"github.com/Nader-jo/Litebox/internal/service"
	"github.com/Nader-jo/Litebox/internal/ui"
)

func (s *Server) live(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"status":"ok"}`)
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.repository.Ping(ctx); err != nil {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	if err := s.store.Health(ctx); err != nil {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"status":"ready"}`)
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
	s.render(w, r, http.StatusOK, ui.SetupPage(ui.PageData{Title: "First-run setup", PrimaryAddress: s.config.PrimaryAddress}))
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	hasUsers, err := s.repository.HasUsers(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if hasUsers {
		s.renderError(w, r, http.StatusNotFound, "Setup is already complete.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "Invalid setup form.")
		return
	}
	emailAddress := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	if _, err := mail.ParseAddress(emailAddress); err != nil {
		s.render(w, r, http.StatusUnprocessableEntity, ui.SetupPage(ui.PageData{PrimaryAddress: s.config.PrimaryAddress, Error: "Enter a valid administrator email."}))
		return
	}
	password := r.FormValue("password")
	if password != r.FormValue("password_confirmation") {
		s.render(w, r, http.StatusUnprocessableEntity, ui.SetupPage(ui.PageData{PrimaryAddress: s.config.PrimaryAddress, Error: "The passwords do not match."}))
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		s.render(w, r, http.StatusUnprocessableEntity, ui.SetupPage(ui.PageData{PrimaryAddress: s.config.PrimaryAddress, Error: err.Error()}))
		return
	}
	if _, err := s.repository.CreateFirstUser(r.Context(), emailAddress, r.FormValue("display_name"), hash); err != nil {
		s.internalError(w, r, err)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
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
	s.render(w, r, http.StatusOK, ui.LoginPage(ui.PageData{PrimaryAddress: s.config.PrimaryAddress}))
}

func (s *Server) loginPost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	_ = r.ParseForm()
	emailAddress := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	key := s.clientIP(r) + "\x00" + emailAddress
	if !s.login.allow(key) {
		time.Sleep(500 * time.Millisecond)
		s.render(w, r, http.StatusTooManyRequests, ui.LoginPage(ui.PageData{PrimaryAddress: s.config.PrimaryAddress, Error: "Too many sign-in attempts. Try again later."}))
		return
	}
	user, err := s.repository.FindUserByEmail(r.Context(), emailAddress)
	if err != nil || !auth.VerifyPassword(r.FormValue("password"), user.PasswordHash) {
		s.login.fail(key)
		time.Sleep(250 * time.Millisecond)
		s.render(w, r, http.StatusUnauthorized, ui.LoginPage(ui.PageData{PrimaryAddress: s.config.PrimaryAddress, Error: "Invalid email or password."}))
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
		time.Now().Add(s.config.SessionTTL), ipHash(s.clientIP(r)), truncate(r.UserAgent(), 512))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	_ = s.repository.TouchSession(r.Context(), sessionID, user.ID, true)
	s.login.success(key)
	s.setSessionCookies(w, sessionToken, csrfToken)
	http.Redirect(w, r, "/inbox", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(s.config.CookieName); err == nil {
		_ = s.repository.DeleteSession(r.Context(), auth.TokenHash(cookie.Value))
	}
	s.clearCookies(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) folder(w http.ResponseWriter, r *http.Request, folder string) {
	before, beforeID, err := parseThreadCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "This mailbox page link is invalid.")
		return
	}
	threads, hasMore, err := s.repository.ListThreadsPage(r.Context(), folder, 50, before, beforeID)
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
	thread, err := s.repository.ThreadByID(r.Context(), r.PathValue("threadID"))
	if err != nil {
		s.repositoryError(w, r, err)
		return
	}
	_ = s.repository.MarkThreadRead(r.Context(), thread.ID, true)
	thread, _ = s.repository.ThreadByID(r.Context(), thread.ID)
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
		threads, hasMore, err = s.repository.SearchThreadsPage(r.Context(), parsed, 50, before, beforeID)
	} else {
		threads, hasMore, err = s.repository.ListThreadsPage(r.Context(), folder, 50, before, beforeID)
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	data := s.pageData(r, thread.Subject)
	data.CurrentFolder, data.Threads, data.Thread = folder, threads, &thread
	data.SearchQuery, data.CurrentCursor = searchQuery, r.URL.Query().Get("cursor")
	if data.CurrentCursor != "" {
		data.FirstPageURL = contextListURL(folder, searchQuery, "")
	}
	if hasMore && len(threads) > 0 {
		data.NextPageURL = contextListURL(folder, searchQuery, encodeThreadCursor(threads[len(threads)-1]))
	}
	data.BackURL = contextListURL(folder, searchQuery, data.CurrentCursor)
	s.render(w, r, http.StatusOK, ui.MailboxPage(data))
}

func (s *Server) threadAction(w http.ResponseWriter, r *http.Request, action string) {
	if err := s.repository.ThreadAction(r.Context(), r.PathValue("threadID"), action); err != nil {
		s.repositoryError(w, r, err)
		return
	}
	destination := "/inbox"
	if action == "trash" {
		destination = "/trash"
	}
	http.Redirect(w, r, destination, http.StatusSeeOther)
}

func (s *Server) threadRead(w http.ResponseWriter, r *http.Request, read bool) {
	if err := s.repository.MarkThreadRead(r.Context(), r.PathValue("threadID"), read); err != nil {
		s.repositoryError(w, r, err)
		return
	}
	http.Redirect(w, r, "/inbox", http.StatusSeeOther)
}

func (s *Server) threadDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.repository.DeleteThread(r.Context(), r.PathValue("threadID")); err != nil {
		s.repositoryError(w, r, err)
		return
	}
	http.Redirect(w, r, "/trash", http.StatusSeeOther)
}

func (s *Server) compose(w http.ResponseWriter, r *http.Request) {
	draft := model.Draft{}
	data := s.pageData(r, "Compose")
	data.CurrentFolder, data.Draft = "drafts", &draft
	s.render(w, r, http.StatusOK, ui.MailboxPage(data))
}

func (s *Server) reply(w http.ResponseWriter, r *http.Request, all bool) {
	thread, err := s.repository.ThreadByID(r.Context(), r.PathValue("threadID"))
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
		to = mailx.ReplyAll(replyTarget, original.From, original.Recipients["to"], original.Recipients["cc"], s.config.AllowedRecipients)
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
	created, err := s.repository.CreateDraft(r.Context(), s.mailboxID, draft)
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
		sender := s.sender(r)
		_, threadID, err := s.repository.QueueDraft(r.Context(), created.ID, s.mailboxID, sender)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		http.Redirect(w, r, "/threads/"+threadID+"?folder=sent", http.StatusSeeOther)
		return
	}
	if r.FormValue("intent") == "continue" {
		http.Redirect(w, r, "/drafts/"+created.ID, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/drafts", http.StatusSeeOther)
}

func (s *Server) drafts(w http.ResponseWriter, r *http.Request) {
	drafts, err := s.repository.ListDrafts(r.Context(), 100)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	data := s.pageData(r, "Drafts")
	data.CurrentFolder, data.Drafts = "drafts", drafts
	s.render(w, r, http.StatusOK, ui.MailboxPage(data))
}

func (s *Server) draft(w http.ResponseWriter, r *http.Request) {
	draft, err := s.repository.DraftByID(r.Context(), r.PathValue("draftID"))
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
	if err := s.repository.SaveDraft(r.Context(), draft); err != nil {
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
		sender := s.sender(r)
		_, threadID, err := s.repository.QueueDraft(r.Context(), id, s.mailboxID, sender)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		http.Redirect(w, r, "/threads/"+threadID+"?folder=sent", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/drafts", http.StatusSeeOther)
}

func (s *Server) sendDraft(w http.ResponseWriter, r *http.Request) {
	draft, err := s.repository.DraftByID(r.Context(), r.PathValue("draftID"))
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
	_, threadID, err := s.repository.QueueDraft(r.Context(), draft.ID, s.mailboxID, s.sender(r))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	http.Redirect(w, r, "/threads/"+threadID+"?folder=sent", http.StatusSeeOther)
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
	if int64(len(body)) > s.config.MaxMessageTextBytes {
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
	draft, err := s.repository.DraftByID(r.Context(), draftID)
	if err != nil {
		s.repositoryError(w, r, err)
		return
	}
	if len(draft.Attachments) >= s.config.MaxAttachmentCount {
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
	if header.Size < 0 || total+header.Size > s.config.MaxOutboundAttachmentBytes {
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
	if err := s.repository.AddDraftAttachment(r.Context(), attachment); err != nil {
		_ = s.store.Delete(r.Context(), key)
		s.internalError(w, r, err)
		return
	}
	http.Redirect(w, r, "/drafts/"+draftID, http.StatusSeeOther)
}

func (s *Server) deleteDraftAttachment(w http.ResponseWriter, r *http.Request) {
	key, err := s.repository.DeleteDraftAttachment(r.Context(), r.PathValue("draftID"), r.PathValue("attachmentID"))
	if err != nil {
		s.repositoryError(w, r, err)
		return
	}
	_ = s.store.Delete(r.Context(), key)
	http.Redirect(w, r, "/drafts/"+r.PathValue("draftID"), http.StatusSeeOther)
}

func (s *Server) deleteDraft(w http.ResponseWriter, r *http.Request) {
	keys, err := s.repository.DeleteDraft(r.Context(), r.PathValue("draftID"))
	if err != nil {
		s.repositoryError(w, r, err)
		return
	}
	for _, key := range keys {
		_ = s.store.Delete(r.Context(), key)
	}
	http.Redirect(w, r, "/drafts", http.StatusSeeOther)
}

func (s *Server) attachment(w http.ResponseWriter, r *http.Request, inline bool) {
	attachment, err := s.repository.AttachmentByID(r.Context(), r.PathValue("attachmentID"))
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
	w.Header().Set("Cache-Control", "private, max-age=3600")
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
	threads, hasMore, err := s.repository.SearchThreadsPage(r.Context(), query, 50, before, beforeID)
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

func contextListURL(folder, rawSearch, cursor string) string {
	path := "/" + folder
	query := url.Values{}
	if folder == "search" {
		path = "/search"
		query.Set("q", rawSearch)
	}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return path
}

func (s *Server) admin(w http.ResponseWriter, r *http.Request) {
	stats, err := s.repository.SystemStats(r.Context(), s.config.DBPath)
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
	data := s.pageData(r, "System")
	mailbox, _ := s.repository.PrimaryMailbox(r.Context())
	data.CurrentFolder, data.Stats, data.Jobs, data.Webhooks, data.StorageHealth, data.MailboxName = "admin", stats, jobList, webhooks, health, mailbox.DisplayName
	s.render(w, r, http.StatusOK, ui.MailboxPage(data))
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	if err := s.repository.UpdateMailboxDisplayName(r.Context(), r.FormValue("display_name")); err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	http.Redirect(w, r, "/admin/system", http.StatusSeeOther)
}

func (s *Server) retryJob(w http.ResponseWriter, r *http.Request) {
	if err := s.repository.RetryJob(r.Context(), r.PathValue("jobID")); err != nil {
		s.repositoryError(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/system", http.StatusSeeOther)
}

func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.config.MaxWebhookBodyBytes)
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
	return ui.PageData{Title: title, PrimaryAddress: s.config.PrimaryAddress, User: state.Session.User, CSRFToken: state.CSRFToken}
}

func (s *Server) sender(r *http.Request) model.Address {
	mailbox, err := s.repository.PrimaryMailbox(r.Context())
	if err != nil {
		return model.Address{Name: s.config.DisplayName, Address: s.config.PrimaryAddress}
	}
	return model.Address{Name: mailbox.DisplayName, Address: mailbox.Address}
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, component templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
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
