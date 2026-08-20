package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/Nader-jo/Litebox/internal/config"
)

func TestParseInvocationHandlesHelpVersionAndUnknownCommandsWithoutStartup(t *testing.T) {
	tests := []struct {
		args        []string
		wantCommand string
		wantHelp    bool
		wantError   bool
	}{
		{args: nil, wantCommand: "serve"},
		{args: []string{"--help"}, wantHelp: true},
		{args: []string{"help", "backup"}, wantCommand: "backup", wantHelp: true},
		{args: []string{"backup", "--help"}, wantCommand: "backup", wantHelp: true},
		{args: []string{"--version"}, wantCommand: "version"},
		{args: []string{"unknown"}, wantError: true},
	}
	for _, test := range tests {
		command, _, help, err := parseInvocation(test.args)
		if command != test.wantCommand || help != test.wantHelp || (err != nil) != test.wantError {
			t.Fatalf("args=%v command=%q help=%v err=%v", test.args, command, help, err)
		}
	}
	var output bytes.Buffer
	if err := printUsage(&output, "backup"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "backup --output") {
		t.Fatalf("backup help omitted syntax: %s", output.String())
	}
}

func TestParseInvocationValidatesFlagCommandsBeforeStartup(t *testing.T) {
	tests := [][]string{
		{"doctor", "--unknown"},
		{"doctor", "unexpected"},
		{"backup"},
		{"backup", "--output", "backup", "unexpected"},
		{"create-admin", "--email", "owner@example.com"},
	}
	for _, arguments := range tests {
		if _, _, _, err := parseInvocation(arguments); err == nil {
			t.Fatalf("parseInvocation(%q) succeeded, want an error", arguments)
		}
	}
	if command, rest, help, err := parseInvocation([]string{"backup", "--output", "backup"}); err != nil || command != "backup" || help || len(rest) != 2 {
		t.Fatalf("valid backup invocation = (%q, %q, %v, %v)", command, rest, help, err)
	}
}

func TestLoggerLevelCanBeReloaded(t *testing.T) {
	logger, update := newLogger(config.Config{Environment: "development", LogLevel: "error", BaseURL: &url.URL{Scheme: "http", Host: "localhost"}})
	if logger.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("info logging should initially be disabled")
	}
	update("debug")
	if !logger.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("debug logging should be enabled after reload")
	}
}
