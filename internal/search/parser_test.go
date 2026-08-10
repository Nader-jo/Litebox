package search

import "testing"

func TestParseCombinedQuery(t *testing.T) {
	query, err := Parse(`"renewal notice" from:alice@example.com to:hello@example.com subject:invoice has:attachment is:unread is:starred after:2026-01-01 before:2026-08-01 literal:colon`)
	if err != nil {
		t.Fatal(err)
	}
	if query.From != "alice@example.com" || query.To != "hello@example.com" || query.Subject != "invoice" || !query.HasAttachment || !query.Unread || !query.Starred {
		t.Fatalf("unexpected filters: %#v", query)
	}
	if len(query.Terms) != 2 || query.FTS() == "" {
		t.Fatalf("unexpected terms: %#v", query.Terms)
	}
}

func TestParseRejectsMalformedInput(t *testing.T) {
	for _, value := range []string{`"unfinished`, "after:yesterday", "has:calendar", "is:important"} {
		if _, err := Parse(value); err == nil {
			t.Errorf("expected %q to fail", value)
		}
	}
}
