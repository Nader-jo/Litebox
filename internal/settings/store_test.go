package settings

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
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
	token, err := store.RotateSetupToken(ctx, time.Hour)
	if err != nil || token == "" {
		t.Fatalf("token=%q err=%v", token, err)
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
	first, err := store.RotateSetupToken(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.RotateSetupToken(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("rotation returned the same token")
	}
	if valid, err := store.VerifySetupToken(ctx, first); err != nil || valid {
		t.Fatalf("previous token remained valid: valid=%v err=%v", valid, err)
	}
	if valid, err := store.VerifySetupToken(ctx, second); err != nil || !valid {
		t.Fatalf("new token was not valid: valid=%v err=%v", valid, err)
	}
}

func TestCompleteSetupAtomicallyClaimsTokenAndRollsBackFailures(t *testing.T) {
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
	token, err := store.RotateSetupToken(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	value := Values{Configured: true, BaseURL: "https://mail.example.com", SessionTTLHours: 24, LogLevel: "info"}
	start := make(chan struct{})
	errorsFound := make(chan error, 2)
	var mutations atomic.Int32
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errorsFound <- store.CompleteSetup(ctx, token, true, value, func(*sql.Tx) error {
				mutations.Add(1)
				return nil
			})
		}()
	}
	close(start)
	wait.Wait()
	close(errorsFound)
	succeeded, unavailable := 0, 0
	for err := range errorsFound {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrSetupUnavailable):
			unavailable++
		default:
			t.Fatalf("unexpected setup error: %v", err)
		}
	}
	if succeeded != 1 || unavailable != 1 || mutations.Load() != 1 {
		t.Fatalf("succeeded=%d unavailable=%d mutations=%d", succeeded, unavailable, mutations.Load())
	}

	rollbackDB, err := db.Open(ctx, filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackDB.Close()
	if err := db.Migrate(ctx, rollbackDB); err != nil {
		t.Fatal(err)
	}
	rollbackStore := New(rollbackDB, []byte("01234567890123456789012345678901"))
	rollbackToken, err := rollbackStore.RotateSetupToken(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("injected setup failure")
	err = rollbackStore.CompleteSetup(ctx, rollbackToken, true, value, func(*sql.Tx) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("setup error = %v, want injected failure", err)
	}
	if valid, verifyErr := rollbackStore.VerifySetupToken(ctx, rollbackToken); verifyErr != nil || !valid {
		t.Fatalf("rolled-back token valid=%v err=%v", valid, verifyErr)
	}
	loaded, err := rollbackStore.Load(ctx)
	if err != nil || loaded.Configured {
		t.Fatalf("rolled-back settings=%+v err=%v", loaded, err)
	}
}
