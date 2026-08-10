package mail

import (
	"testing"

	"github.com/Nader-jo/Litebox/internal/model"
)

func TestParseAddresses(t *testing.T) {
	addresses, err := ParseAddresses(`"Doe, Jane" <Jane@Example.com>, jane@example.com, José <jose@example.com>`)
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) != 2 {
		t.Fatalf("expected deduplicated addresses, got %d", len(addresses))
	}
	if addresses[0].Name != "Doe, Jane" || addresses[0].Address != "jane@example.com" {
		t.Fatalf("unexpected first address: %#v", addresses[0])
	}
	if _, err := ParseAddresses("jane@"); err == nil {
		t.Fatal("expected invalid address error")
	}
}

func TestReplyAllExcludesOwnAndDuplicates(t *testing.T) {
	own := map[string]struct{}{"hello@example.com": {}}
	result := ReplyAll(model.Address{}, model.Address{Address: "alice@example.com"},
		[]model.Address{{Address: "hello@example.com"}, {Address: "bob@example.com"}, {Address: "alice@example.com"}},
		[]model.Address{{Address: "bob@example.com"}}, own)
	if len(result) != 2 || result[0].Address != "alice@example.com" || result[1].Address != "bob@example.com" {
		t.Fatalf("unexpected reply-all result: %#v", result)
	}
}

func TestAllowedRecipient(t *testing.T) {
	if !IsAllowedRecipient([]model.Address{{Address: "HELLO@example.com"}}, map[string]struct{}{"hello@example.com": {}}) {
		t.Fatal("expected case-insensitive recipient match")
	}
}
