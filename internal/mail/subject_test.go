package mail

import "testing"

func TestNormalizeSubject(t *testing.T) {
	tests := map[string]string{
		"Re: RE: Invoice":        "invoice",
		" Fwd:   Hello   world ": "hello world",
		"AW: Antwort":            "antwort",
		"":                       "",
	}
	for input, expected := range tests {
		if actual := NormalizeSubject(input); actual != expected {
			t.Errorf("NormalizeSubject(%q) = %q, want %q", input, actual, expected)
		}
	}
	if actual := ReplySubject("RE: RE: Hello"); actual != "Re: Hello" {
		t.Fatalf("unexpected reply subject %q", actual)
	}
	if actual := References("<a> <b> <a>", "<b>"); actual != "<a> <b>" {
		t.Fatalf("unexpected references %q", actual)
	}
}
