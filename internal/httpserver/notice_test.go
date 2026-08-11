package httpserver

import "testing"

func TestNoticeMessageUsesServerControlledCopy(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"message-queued":            "Message queued for delivery",
		"draft-saved":               "Draft saved",
		"attachment-added":          "Attachment added",
		"attachment-removed":        "Attachment removed",
		"draft-discarded":           "Draft discarded",
		"<script>alert(1)</script>": "",
		"":                          "",
	}
	for code, want := range tests {
		if got := noticeMessage(code); got != want {
			t.Errorf("noticeMessage(%q) = %q, want %q", code, got, want)
		}
	}
}
