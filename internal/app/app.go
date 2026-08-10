// Package app assembles and runs the Litebox modular monolith.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Nader-jo/Litebox/internal/blobstore"
	"github.com/Nader-jo/Litebox/internal/config"
	"github.com/Nader-jo/Litebox/internal/db"
	"github.com/Nader-jo/Litebox/internal/httpserver"
	"github.com/Nader-jo/Litebox/internal/jobs"
	"github.com/Nader-jo/Litebox/internal/provider"
	"github.com/Nader-jo/Litebox/internal/repository"
	"github.com/Nader-jo/Litebox/internal/service"
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
	mailboxID, err := repo.EnsureMailbox(ctx, cfg.PrimaryAddress, cfg.DisplayName)
	if err != nil {
		return fail(fmt.Errorf("ensure primary mailbox: %w", err))
	}
	for address := range cfg.AllowedRecipients {
		if err := repo.EnsureMailboxAddress(ctx, mailboxID, address, cfg.DisplayName); err != nil {
			return fail(fmt.Errorf("ensure configured mailbox address %q: %w", address, err))
		}
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
	_, _, _ = repo.EnqueueJob(ctx, "cleanup_sessions", "cleanup-sessions:"+time.Now().UTC().Format("2006-01-02"), "{}", 200, 3, time.Now())
	return &App{Config: cfg, Repository: repo, Store: store, Server: server, Jobs: runner, database: database, logger: logger}, nil
}

// Serve runs HTTP and background workers until cancellation or server failure.
func (a *App) Serve(ctx context.Context) error {
	workerContext, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()
	a.Jobs.Start(workerContext)
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
