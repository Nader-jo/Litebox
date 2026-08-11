package ui

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestTablerIconCatalogRendersEveryUsedIcon(t *testing.T) {
	t.Parallel()

	names := []string{
		"archive", "arrow-left", "arrow-up-right", "check", "close", "draft",
		"health", "inbox", "keyboard", "logout", "mail", "menu", "paperclip", "plus", "reply",
		"search", "send", "sessions", "settings", "star", "trash", "users",
	}
	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var rendered bytes.Buffer
			if err := Icon(name).Render(context.Background(), &rendered); err != nil {
				t.Fatalf("render Icon(%q): %v", name, err)
			}
			markup := rendered.String()
			if !strings.Contains(markup, "<path") {
				t.Fatalf("Icon(%q) rendered without an SVG path: %s", name, markup)
			}
			if !strings.Contains(markup, `stroke-width="2"`) {
				t.Fatalf("Icon(%q) does not use the Tabler outline stroke: %s", name, markup)
			}
		})
	}
}
