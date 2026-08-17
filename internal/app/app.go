// Package app assembles and runs the Litebox modular monolith.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/Nader-jo/Litebox/internal/blobstore"
	"github.com/Nader-jo/Litebox/internal/config"
	"github.com/Nader-jo/Litebox/internal/db"
	"github.com/Nader-jo/Litebox/internal/httpserver"
	"github.com/Nader-jo/Litebox/internal/jobs"
	"github.com/Nader-jo/Litebox/internal/model"
	"github.com/Nader-jo/Litebox/internal/provider"
	"github.com/Nader-jo/Litebox/internal/repository"
	"github.com/Nader-jo/Litebox/internal/secrets"
	"github.com/Nader-jo/Litebox/internal/service"
	"github.com/Nader-jo/Litebox/internal/settings"
)

// App contains the process-level application graph.
type App struct {
	Config     config.Config
	Repository *repository.Repository
	Store      *blobstore.FileStore
	Server     *httpserver.Server
	Jobs       *jobs.Runner
	database   interface{ Close() error }
	logger     *slog.Logger
}

// New initializes storage, schema, provider adapters, workers, and HTTP routes.
func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*App, error) {
	if cfg.MasterKeyPath == "" {
		cfg.MasterKeyPath = filepath.Join(cfg.DataDir, ".litebox", "master.key")
	}
	database, err := db.Open(ctx, cfg.DBPath)
	if err != nil {
		return nil, err
	}
	fail := func(cause error) (*App, error) {
		database.Close()
		return nil, cause
	}
	if err := db.Migrate(ctx, database); err != nil {
		return fail(err)
	}
	repo := repository.New(database)
	masterKey, err := secrets.LoadOrCreate(cfg.MasterKeyPath)
	if err != nil {
		return fail(err)
	}
	settingsStore := settings.New(database, masterKey)
	persisted, err := settingsStore.Load(ctx)
	if err != nil {
		return fail(fmt.Errorf("load installation settings: %w", err))
	}
	legacy := settings.Values{Configured: cfg.ResendAPIKey != "" && cfg.ResendWebhookSecret != "",
		BaseURL: cfg.BaseURL.String(), SessionTTLHours: int(cfg.SessionTTL / time.Hour), LogLevel: cfg.LogLevel,
		MaxWebhookBodyBytes: cfg.MaxWebhookBodyBytes, MaxMessageTextBytes: cfg.MaxMessageTextBytes,
		MaxUploadRequestBytes: cfg.MaxUploadRequestBytes, MaxOutboundAttachmentBytes: cfg.MaxOutboundAttachmentBytes,
		MaxAttachmentCount: cfg.MaxAttachmentCount, ResendAPIKey: cfg.ResendAPIKey,
		ResendWebhookSecret: cfg.ResendWebhookSecret, ResendDomainID: cfg.ResendDomainID}
	if !persisted.Configured && legacy.Configured {
		if err := settingsStore.ImportLegacy(ctx, legacy); err != nil {
			return fail(fmt.Errorf("import legacy settings: %w", err))
		}
		persisted, err = settingsStore.Load(ctx)
		if err != nil {
			return fail(fmt.Errorf("reload installation settings: %w", err))
		}
	}
	// A freshly migrated row contains safe localhost defaults. Until setup (or
	// the one-time legacy import) is complete, use the operator's bootstrap URL
	// and limits so the setup link reflects the real public endpoint.
	if !persisted.Configured {
		persisted = legacy
	}
	cfg, err = cfg.ApplySettings(persisted)
	if err != nil {
		return fail(err)
	}
	_, primaryErr := repo.PrimaryMailboxID(ctx)
	createdPrimary := errors.Is(primaryErr, repository.ErrNotFound)
	if primaryErr != nil && !createdPrimary {
		return fail(primaryErr)
	}
	if createdPrimary {
		mailboxID, ensureErr := repo.EnsureMailbox(ctx, cfg.PrimaryAddress, cfg.DisplayName)
		if ensureErr != nil {
			return fail(fmt.Errorf("ensure primary mailbox: %w", ensureErr))
		}
		for address := range cfg.AllowedRecipients {
			if err := repo.EnsureMailboxAddress(ctx, mailboxID, address, cfg.DisplayName); err != nil {
				return fail(fmt.Errorf("ensure configured mailbox address %q: %w", address, err))
			}
		}
	} else {
		primary, loadErr := repo.PrimaryMailbox(ctx)
		if loadErr != nil {
			return fail(loadErr)
		}
		cfg.PrimaryAddress, cfg.DisplayName = primary.Address, primary.DisplayName
	}
	store, err := blobstore.NewFileStore(cfg.StorageRoot, cfg.StorageTempRoot)
	if err != nil {
		return fail(err)
	}
	providerClient := provider.NewResend(cfg.ResendAPIKey, cfg.ResendWebhookSecret)
	mailbox := service.NewMailbox(cfg, repo, store, providerClient)
	runner := jobs.New(repo, mailbox.JobHandlers(), cfg.WorkerCount, cfg.JobPollInterval, cfg.JobLease, logger)
	server, err := httpserver.New(cfg, repo, store, providerClient, mailbox, logger)
	if err != nil {
		return fail(err)
	}
	application := &App{Config: cfg, Repository: repo, Store: store, Server: server, Jobs: runner, database: database, logger: logger}
	server.Configure(settingsStore, func(next config.Config) {
		application.Config = next
		mailbox.ApplyConfig(next)
		providerClient.UpdateCredentials(next.ResendAPIKey, next.ResendWebhookSecret)
		server.ApplyConfig(next)
	})
	if cfg.Environment == "production" && !persisted.Configured {
		if token, generated, tokenErr := settingsStore.EnsureSetupToken(ctx, 30*24*time.Hour); tokenErr == nil && generated {
			logger.Warn("initial setup required", "setup_url", cfg.BaseURL.String()+"/setup?token="+token)
		}
	}
	_, _, _ = repo.EnqueueJob(ctx, "cleanup_sessions", "cleanup-sessions:"+time.Now().UTC().Format("2006-01-02"), "{}", 200, 3, time.Now())
	_ = enqueueDueDigests(ctx, repo, time.Now().UTC())
	return application, nil
}

// Serve runs HTTP and background workers until cancellation or server failure.
func (a *App) Serve(ctx context.Context) error {
	workerContext, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()
	a.Jobs.Start(workerContext)
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-workerContext.Done():
				return
			case now := <-ticker.C:
				_ = enqueueDueDigests(workerContext, a.Repository, now.UTC())
			}
		}
	}()
	serverErrors := make(chan error, 1)
	go func() {
		a.logger.Info("Litebox listening", "address", a.Config.ListenAddr, "environment", a.Config.Environment)
		serverErrors <- a.Server.HTTP().ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := a.Server.HTTP().Shutdown(shutdownContext); err != nil {
			return fmt.Errorf("shutdown HTTP server: %w", err)
		}
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	cancelWorkers()
	a.Jobs.Wait()
	return nil
}

// Close releases the database after server and workers have stopped.
func (a *App) Close() error { return a.database.Close() }

func enqueueDueDigests(ctx context.Context, repo *repository.Repository, now time.Time) error {
	subs, err := repo.DueDigestSubscriptions(ctx)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		since, until, due := digestWindow(sub, now)
		if !due {
			continue
		}
		payload, _ := json.Marshal(map[string]any{"subscription_id": sub.ID, "since": since.UnixMilli(), "until": until.UnixMilli()})
		if _, _, err := repo.EnqueueJob(ctx, "send_digest", "digest:"+sub.ID+":"+fmt.Sprint(until.Unix()), string(payload), 40, 6, now); err != nil {
			return err
		}
	}
	return nil
}

func digestWindow(sub model.DigestSubscription, now time.Time) (time.Time, time.Time, bool) {
	location, err := time.LoadLocation(sub.Timezone)
	if err != nil {
		location = time.UTC
	}
	local := now.In(location)
	if sub.Frequency == "weekly" && local.Weekday() != time.Monday {
		return time.Time{}, time.Time{}, false
	}
	scheduled := time.Date(local.Year(), local.Month(), local.Day(), sub.SendHour, 0, 0, 0, location)
	if local.Before(scheduled) {
		return time.Time{}, time.Time{}, false
	}
	if sub.LastSentAt != nil && !sub.LastSentAt.Before(scheduled.UTC()) {
		return time.Time{}, time.Time{}, false
	}
	days := 1
	if sub.Frequency == "weekly" {
		days = 7
	}
	return scheduled.AddDate(0, 0, -days).UTC(), scheduled.UTC(), true
}
