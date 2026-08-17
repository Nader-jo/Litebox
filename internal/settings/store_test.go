package settings

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nader-jo/Litebox/internal/db"
)

func TestEncryptedSettingsAndSingleUseSetupToken(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := db.Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	store := New(database, []byte("01234567890123456789012345678901"))
	if err := store.Save(ctx, Values{Configured: true, BaseURL: "https://mail.example.com", SessionTTLHours: 24, LogLevel: "info", ResendAPIKey: "re_test", ResendWebhookSecret: "whsec_test"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(ctx)
	if err != nil || loaded.ResendAPIKey != "re_test" || loaded.ResendWebhookSecret != "whsec_test" {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	if err := store.Save(ctx, Values{BaseURL: "https://mail.example.com", SessionTTLHours: 24, LogLevel: "info"}); err != nil {
		t.Fatal(err)
	}
	token, generated, err := store.EnsureSetupToken(ctx, time.Hour)
	if err != nil || !generated || token == "" {
		t.Fatalf("token=%q generated=%v err=%v", token, generated, err)
	}
	valid, err := store.VerifySetupToken(ctx, token)
	if err != nil || !valid {
		t.Fatalf("token should verify: valid=%v err=%v", valid, err)
	}
	if err := store.ConsumeSetupToken(ctx); err != nil {
		t.Fatal(err)
	}
	valid, err = store.VerifySetupToken(ctx, token)
	if err != nil || valid {
		t.Fatalf("consumed token should be invalid: valid=%v err=%v", valid, err)
	}
}
