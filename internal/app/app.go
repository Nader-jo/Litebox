// Package app assembles and runs the Litebox modular monolith.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
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
	// Config is the effective startup snapshot. Reloadable settings are held by
	// the HTTP and mailbox components as immutable atomic snapshots.
	Config     config.Config
	Repository *repository.Repository
	Store      *blobstore.FileStore
	Server     *httpserver.Server
	Jobs       *jobs.Runner
	database   interface{ Close() error }
	logger     *slog.Logger
}

type options struct {
	applyLogLevel func(string)
}

// Option customizes process-level integrations without coupling app to the
// executable's logger construction.
type Option func(*options)

// WithLogLevelUpdater applies persisted and live log-level changes to a
// dynamic logger such as slog.LevelVar.
func WithLogLevelUpdater(update func(string)) Option {
	return func(value *options) { value.applyLogLevel = update }
}

// New initializes storage, schema, provider adapters, workers, and HTTP routes.
func New(ctx context.Context, cfg config.Config, logger *slog.Logger, appOptions ...Option) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	var opts options
	for _, apply := range appOptions {
		apply(&opts)
	}
	if cfg.MasterKeyPath == "" {
		cfg.MasterKeyPath = filepath.Join(cfg.DataDir, ".litebox", "master.key")
	}
	databaseExisted := false
	if _, err := os.Stat(cfg.DBPath); err == nil {
		databaseExisted = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect database path: %w", err)
	}
	var (
		masterKey []byte
		err       error
	)
	if databaseExisted {
		masterKey, err = secrets.Load(cfg.MasterKeyPath)
	} else {
		masterKey, err = secrets.LoadOrCreate(cfg.MasterKeyPath)
	}
	if err != nil {
		return nil, err
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
	if opts.applyLogLevel != nil {
		opts.applyLogLevel(cfg.LogLevel)
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
		mailbox.ApplyConfig(next)
		providerClient.UpdateCredentials(next.ResendAPIKey, next.ResendWebhookSecret)
		server.ApplyConfig(next)
		if opts.applyLogLevel != nil {
			opts.applyLogLevel(next.LogLevel)
		}
	})
	if cfg.Environment == "production" && !persisted.Configured {
		token, tokenErr := settingsStore.RotateSetupToken(ctx, 30*24*time.Hour)
		if tokenErr != nil {
			return fail(fmt.Errorf("rotate setup token: %w", tokenErr))
		}
		logger.Warn("initial setup required", "setup_url", cfg.BaseURL.String()+"/setup?token="+token)
	}
	if err := enqueueMaintenance(ctx, repo, time.Now().UTC()); err != nil {
		logger.Warn("could not schedule maintenance", "error", err)
	}
	server.StartBackground()
	return application, nil
}

// Serve runs HTTP and background workers until cancellation or server failure.
func (a *App) Serve(ctx context.Context) error {
	workerContext, cancelWorkers := context.WithCancel(ctx)
	a.Jobs.Start(workerContext)
	schedulerDone := make(chan struct{})
	defer func() {
		cancelWorkers()
		a.Jobs.Wait()
		<-schedulerDone
	}()
	go func() {
		defer close(schedulerDone)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-workerContext.Done():
				return
			case now := <-ticker.C:
				if err := enqueueMaintenance(workerContext, a.Repository, now.UTC()); err != nil && workerContext.Err() == nil {
					a.logger.Warn("could not schedule maintenance", "error", err)
				}
			}
		}
	}()
	serverErrors := make(chan error, 1)
	go func() {
		a.logger.Info("Litebox listening", "address", a.Config.ListenAddr, "environment", a.Config.Environment)
		serverErrors <- a.Server.HTTP().ListenAndServe()
	}()
	var result error
	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := a.Server.HTTP().Shutdown(shutdownContext); err != nil {
			result = fmt.Errorf("shutdown HTTP server: %w", err)
			if closeErr := a.Server.HTTP().Close(); closeErr != nil {
				result = errors.Join(result, fmt.Errorf("force close HTTP server: %w", closeErr))
			}
		}
		if err := <-serverErrors; err != nil && !errors.Is(err, http.ErrServerClosed) {
			result = errors.Join(result, err)
		}
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			result = err
		}
	}
	return result
}

// Close releases process-local delivery and then the database after server and
// durable workers have stopped.
func (a *App) Close() error {
	a.Server.Close()
	return a.database.Close()
}

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

func enqueueMaintenance(ctx context.Context, repo *repository.Repository, now time.Time) error {
	_, _, cleanupErr := repo.EnqueueJob(ctx, "cleanup_sessions", "cleanup-sessions:"+now.UTC().Format("2006-01-02"), "{}", 200, 3, now)
	return errors.Join(cleanupErr, enqueueDueDigests(ctx, repo, now.UTC()))
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
