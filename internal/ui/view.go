// Package ui contains the server-rendered Litebox interface.
package ui

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Nader-jo/Litebox/internal/mail"
	"github.com/Nader-jo/Litebox/internal/model"
)

// PageData is the bounded view model shared by full and progressively enhanced pages.
type PageData struct {
	Title          string
	PrimaryAddress string
	User           model.User
	CSRFToken      string
	CurrentFolder  string
	Threads        []model.ThreadSummary
	Thread         *model.Thread
	Draft          *model.Draft
	Drafts         []model.Draft
	Jobs           []model.Job
	Webhooks       []model.WebhookEvent
	Stats          model.SystemStats
	SearchQuery    string
	StorageHealth  string
	MailboxName    string
	Error          string
	Notice         string
	HasUser        bool
	CurrentCursor  string
	NextPageURL    string
	FirstPageURL   string
	BackURL        string
}

func threadURL(id string, data PageData) string {
	query := url.Values{"folder": {data.CurrentFolder}}
	if data.CurrentCursor != "" {
		query.Set("cursor", data.CurrentCursor)
	}
	if data.SearchQuery != "" {
		query.Set("q", data.SearchQuery)
	}
	return "/threads/" + id + "?" + query.Encode()
}

func backURL(data PageData) string {
	if data.BackURL != "" {
		return data.BackURL
	}
	return "/" + data.CurrentFolder
}

func displaySubject(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(No subject)"
	}
	return value
}

func relativeTime(value time.Time) string {
	now := time.Now()
	if value.IsZero() {
		return "—"
	}
	if now.Sub(value) < 24*time.Hour && now.Day() == value.Local().Day() {
		return value.Local().Format("15:04")
	}
	if now.Year() == value.Local().Year() {
		return value.Local().Format("Jan 2")
	}
	return value.Local().Format("Jan 2, 2006")
}

func fullTime(value time.Time) string { return value.Local().Format("Mon, Jan 2, 2006 at 15:04") }

func formatAddresses(value []model.Address) string { return mail.FormatAddresses(value) }

func firstRecipient(value []model.Address) string {
	if len(value) == 0 {
		return "No recipient"
	}
	if value[0].Name != "" {
		return value[0].Name
	}
	return value[0].Address
}

func statusLabel(value string) string {
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + strings.ReplaceAll(value[1:], "_", " ")
}

func byteSize(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	divisor, exponent := int64(unit), 0
	for amount := value / unit; amount >= unit; amount /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(divisor), "KMGTPE"[exponent])
}

func folderTitle(value string) string {
	switch value {
	case "":
		return "Inbox"
	case "archive":
		return "Archive"
	case "starred":
		return "Starred"
	case "trash":
		return "Trash"
	case "sent":
		return "Sent"
	case "drafts":
		return "Drafts"
	default:
		return strings.ToUpper(value[:1]) + value[1:]
	}
}

func isTerminalStatus(value string) bool {
	switch value {
	case "delivered", "bounced", "failed", "suppressed", "complained":
		return true
	default:
		return false
	}
}

func systemHealth(data PageData) string {
	if data.StorageHealth != "Healthy" || data.Stats.DeadJobs > 0 || data.Stats.FailedAttachments > 0 || data.Stats.MissingRawMessages > 0 {
		return "Needs attention"
	}
	return "Healthy"
}

func draftAction(id string) string {
	if id == "" {
		return "/drafts"
	}
	return "/drafts/" + id
}
