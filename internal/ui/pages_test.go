package ui

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Nader-jo/Litebox/internal/model"
	"github.com/a-h/templ"
)

func TestEmptyMailboxHidesComposeForViewers(t *testing.T) {
	t.Parallel()

	viewer := renderComponent(t, EmptyMailbox("inbox", false))
	if strings.Contains(viewer, "Write a message") {
		t.Fatalf("read-only empty state rendered a compose action: %s", viewer)
	}

	member := renderComponent(t, EmptyMailbox("inbox", true))
	if !strings.Contains(member, "Write a message") {
		t.Fatalf("writable empty state omitted its compose action: %s", member)
	}
}

func TestComposerExposesPendingActionFeedback(t *testing.T) {
	t.Parallel()

	markup := renderComponent(t, Composer(PageData{
		CSRFToken: "csrf",
		Addresses: []model.MailboxAddress{{
			Address:         "team@example.com",
			OutboundEnabled: true,
		}},
	}, model.Draft{}))

	for _, expected := range []string{
		`data-compose-form`,
		`data-pending-label="Saving…"`,
		`data-pending-label="Sending…"`,
		`<span class="button-label">Send message</span>`,
	} {
		if !strings.Contains(markup, expected) {
			t.Fatalf("composer omitted %q: %s", expected, markup)
		}
	}
}

func TestShortcutDialogDocumentsPrimaryNavigation(t *testing.T) {
	t.Parallel()

	markup := renderComponent(t, ShortcutDialog())
	for _, expected := range []string{"Keyboard shortcuts", "Compose", "Search", "Inbox", "Reply"} {
		if !strings.Contains(markup, expected) {
			t.Fatalf("shortcut dialog omitted %q: %s", expected, markup)
		}
	}
}

func renderComponent(t *testing.T, component templ.Component) string {
	t.Helper()

	var rendered bytes.Buffer
	if err := component.Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render component: %v", err)
	}
	return rendered.String()
}
