package mail

import (
	"strings"
	"testing"
)

func TestSanitizeHTML(t *testing.T) {
	input := `<div onclick="steal()"><script>alert(1)</script><iframe src="https://bad"></iframe><img src="https://tracker/pixel"><img src="cid:logo"><a href="javascript:bad()">bad</a><p>Safe text</p></div>`
	output, blocked := SanitizeHTML(input, func(cid string) string {
		if cid == "logo" {
			return "/attachments/local/inline"
		}
		return ""
	})
	for _, forbidden := range []string{"script", "iframe", "onclick", "javascript:", "tracker"} {
		if strings.Contains(strings.ToLower(output), forbidden) {
			t.Fatalf("sanitized output contains %q: %s", forbidden, output)
		}
	}
	if !blocked || !strings.Contains(output, "/attachments/local/inline") || !strings.Contains(output, "Safe text") {
		t.Fatalf("sanitizer lost expected content: %s", output)
	}
}

func TestTextFromHTML(t *testing.T) {
	if actual := TextFromHTML("<p>Hello <strong>world</strong></p>"); actual != "Hello world" {
		t.Fatalf("unexpected text %q", actual)
	}
}
