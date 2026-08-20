package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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
	"github.com/Nader-jo/Litebox/internal/db"
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
	command, arguments, help, err := parseInvocation(os.Args[1:])
	if err != nil {
		return err
	}
	if help {
		return printUsage(os.Stdout, command)
	}
	if command == "version" {
		fmt.Printf("Litebox %s (%s, %s)\n", version, commit, date)
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if command == "restore" {
		flags := flag.NewFlagSet("restore", flag.ContinueOnError)
		input := flags.String("input", "", "backup directory")
		if err := flags.Parse(arguments); err != nil {
			return err
		}
		if *input == "" {
			return fmt.Errorf("restore requires --input")
		}
		return ops.Restore(ctx, cfg, *input)
	}
	if command == "healthcheck" {
		if len(arguments) != 0 {
			return fmt.Errorf("healthcheck does not accept arguments")
		}
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
	if command == "migrate" {
		if len(arguments) != 0 {
			return fmt.Errorf("migrate does not accept arguments")
		}
		database, err := db.Open(ctx, cfg.DBPath)
		if err != nil {
			return err
		}
		defer database.Close()
		if err := db.Migrate(ctx, database); err != nil {
			return err
		}
		fmt.Println("Database is up to date.")
		return nil
	}
	logger, updateLogLevel := newLogger(cfg)
	application, err := app.New(ctx, cfg, logger, app.WithLogLevelUpdater(updateLogLevel))
	if err != nil {
		return err
	}
	defer application.Close()
	switch command {
	case "serve":
		if len(arguments) != 0 {
			return fmt.Errorf("serve does not accept arguments")
		}
		return application.Serve(ctx)
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
	}
	return fmt.Errorf("unknown command %q", command)
}

var commands = map[string]struct{}{
	"serve": {}, "migrate": {}, "healthcheck": {}, "doctor": {}, "backup": {}, "restore": {},
	"create-admin": {}, "reset-password": {}, "reindex": {}, "version": {},
}

func parseInvocation(arguments []string) (command string, rest []string, help bool, err error) {
	if len(arguments) == 0 {
		return "serve", nil, false, nil
	}
	first := arguments[0]
	if first == "-h" || first == "--help" {
		if len(arguments) != 1 {
			return "", nil, false, fmt.Errorf("%s does not accept arguments", first)
		}
		return "", nil, true, nil
	}
	if first == "help" {
		if len(arguments) > 2 {
			return "", nil, false, fmt.Errorf("help accepts at most one command")
		}
		if len(arguments) == 1 {
			return "", nil, true, nil
		}
		if _, ok := commands[arguments[1]]; !ok {
			return "", nil, false, fmt.Errorf("unknown command %q", arguments[1])
		}
		return arguments[1], nil, true, nil
	}
	if first == "-v" || first == "--version" {
		if len(arguments) != 1 {
			return "", nil, false, fmt.Errorf("%s does not accept arguments", first)
		}
		return "version", nil, false, nil
	}
	command, rest = "serve", arguments
	if !strings.HasPrefix(first, "-") {
		command, rest = first, arguments[1:]
	}
	if _, ok := commands[command]; !ok {
		return "", nil, false, fmt.Errorf("unknown command %q", command)
	}
	for _, argument := range rest {
		if argument == "-h" || argument == "--help" {
			return command, nil, true, nil
		}
	}
	switch command {
	case "serve", "migrate", "healthcheck", "reindex", "version":
		if len(rest) != 0 {
			return "", nil, false, fmt.Errorf("%s does not accept arguments", command)
		}
	}
	if err := validateCommandArguments(command, rest); err != nil {
		return "", nil, false, err
	}
	return command, rest, false, nil
}

func validateCommandArguments(command string, arguments []string) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var required []*string
	switch command {
	case "doctor":
		flags.Bool("deep", false, "re-hash every stored attachment")
	case "backup":
		required = append(required, flags.String("output", "", "new backup directory"))
	case "restore":
		required = append(required, flags.String("input", "", "backup directory"))
	case "create-admin":
		required = append(required,
			flags.String("email", "", "administrator email"),
			flags.String("name", "", "display name"),
		)
	case "reset-password":
		required = append(required, flags.String("email", "", "administrator email"))
	default:
		return nil
	}
	if err := flags.Parse(arguments); err != nil {
		return fmt.Errorf("%s: %w", command, err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("%s does not accept positional arguments", command)
	}
	for _, value := range required {
		if strings.TrimSpace(*value) == "" {
			return fmt.Errorf("%s is missing a required option", command)
		}
	}
	return nil
}

func printUsage(output io.Writer, command string) error {
	var usage strings.Builder
	if command != "" {
		usage.WriteString("Usage: litebox ")
		usage.WriteString(command)
		usage.WriteString("\n\n")
	}
	usage.WriteString("Litebox self-hosted mailbox\n")
	usage.WriteString("\nUsage: litebox [command] [options]\n")
	usage.WriteString("\nCommands:\n")
	usage.WriteString("  serve                         Start the HTTP server and workers (default)\n")
	usage.WriteString("  migrate                       Apply database migrations only\n")
	usage.WriteString("  healthcheck                   Check the local readiness endpoint\n")
	usage.WriteString("  doctor [--deep]               Verify database and private storage\n")
	usage.WriteString("  backup --output <directory>   Create a new verified backup\n")
	usage.WriteString("  restore --input <directory>   Restore into an empty data location\n")
	usage.WriteString("  create-admin --email --name   Create the first administrator\n")
	usage.WriteString("  reset-password --email        Reset an administrator password\n")
	usage.WriteString("  reindex                       Rebuild full-text search\n")
	usage.WriteString("  version                       Print build metadata\n")
	usage.WriteString("\nRun 'litebox help <command>' for command syntax.\n")
	_, err := io.WriteString(output, usage.String())
	return err
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
	if errors.Is(err, io.EOF) && value != "" {
		err = nil
	}
	return strings.TrimSpace(value), err
}

func newLogger(cfg config.Config) (*slog.Logger, func(string)) {
	level := &slog.LevelVar{}
	update := func(value string) {
		next := slog.LevelInfo
		switch value {
		case "debug":
			next = slog.LevelDebug
		case "warn":
			next = slog.LevelWarn
		case "error":
			next = slog.LevelError
		}
		level.Set(next)
	}
	update(cfg.LogLevel)
	options := &slog.HandlerOptions{Level: level}
	if cfg.Environment == "production" {
		return slog.New(slog.NewJSONHandler(os.Stdout, options)), update
	}
	return slog.New(slog.NewTextHandler(os.Stdout, options)), update
}
