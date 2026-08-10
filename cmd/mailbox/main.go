package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/Nader-jo/Litebox/internal/app"
	"github.com/Nader-jo/Litebox/internal/auth"
	"github.com/Nader-jo/Litebox/internal/config"
	"github.com/Nader-jo/Litebox/internal/ops"
	"github.com/Nader-jo/Litebox/internal/repository"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "litebox:", err)
		os.Exit(1)
	}
}

func run() error {
	command := "serve"
	arguments := os.Args[1:]
	if len(arguments) > 0 && !strings.HasPrefix(arguments[0], "-") {
		command, arguments = arguments[0], arguments[1:]
	}
	if command == "version" {
		fmt.Printf("Litebox %s (%s, %s)\n", version, commit, date)
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if command == "restore" {
		flags := flag.NewFlagSet("restore", flag.ContinueOnError)
		input := flags.String("input", "", "backup directory")
		if err := flags.Parse(arguments); err != nil {
			return err
		}
		if *input == "" {
			return fmt.Errorf("restore requires --input")
		}
		return ops.Restore(context.Background(), cfg, *input)
	}
	if command == "healthcheck" {
		client := &http.Client{Timeout: 4 * time.Second}
		_, port, splitErr := net.SplitHostPort(cfg.ListenAddr)
		if splitErr != nil || port == "" {
			return fmt.Errorf("APP_LISTEN_ADDR must include a port")
		}
		response, err := client.Get("http://127.0.0.1:" + port + "/health/ready")
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("readiness returned HTTP %d", response.StatusCode)
		}
		return nil
	}
	logger := newLogger(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	application, err := app.New(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer application.Close()
	switch command {
	case "serve":
		return application.Serve(ctx)
	case "migrate":
		fmt.Println("Database is up to date.")
		return nil
	case "doctor":
		flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
		deep := flags.Bool("deep", false, "re-hash every stored attachment")
		if err := flags.Parse(arguments); err != nil {
			return err
		}
		if err := ops.Doctor(ctx, cfg, application.Repository, application.Store, *deep); err != nil {
			return err
		}
		fmt.Println("Doctor found no integrity problems.")
		return nil
	case "backup":
		flags := flag.NewFlagSet("backup", flag.ContinueOnError)
		output := flags.String("output", "", "new backup directory")
		if err := flags.Parse(arguments); err != nil {
			return err
		}
		if *output == "" {
			return fmt.Errorf("backup requires --output")
		}
		manifest, err := ops.Backup(ctx, cfg, application.Repository, application.Store, *output)
		if err != nil {
			return err
		}
		fmt.Printf("Backup complete: %d blobs, database %d bytes.\n", len(manifest.Blobs), manifest.Database.Size)
		return nil
	case "create-admin":
		return createAdmin(ctx, application.Repository, arguments)
	case "reset-password":
		return resetPassword(ctx, application.Repository, arguments)
	case "reindex":
		if err := application.Repository.Reindex(ctx); err != nil {
			return err
		}
		fmt.Println("Search index rebuilt.")
		return nil
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func createAdmin(ctx context.Context, repo *repository.Repository, arguments []string) error {
	flags := flag.NewFlagSet("create-admin", flag.ContinueOnError)
	email := flags.String("email", "", "administrator email")
	name := flags.String("name", "", "display name")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *email == "" || *name == "" {
		return fmt.Errorf("create-admin requires --email and --name")
	}
	password, err := readPassword("Password: ")
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	_, err = repo.CreateFirstUser(ctx, *email, *name, hash)
	return err
}

func resetPassword(ctx context.Context, repo *repository.Repository, arguments []string) error {
	flags := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	email := flags.String("email", "", "administrator email")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *email == "" {
		return fmt.Errorf("reset-password requires --email")
	}
	password, err := readPassword("New password: ")
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	return repo.UpdatePassword(ctx, *email, hash)
}

func readPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		value, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return string(value), err
	}
	value, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(value), err
}

func newLogger(cfg config.Config) *slog.Logger {
	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	options := &slog.HandlerOptions{Level: level}
	if cfg.Environment == "production" {
		return slog.New(slog.NewJSONHandler(os.Stdout, options))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, options))
}
