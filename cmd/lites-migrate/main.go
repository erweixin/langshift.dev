package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5"
	"github.com/langshift/lites/internal/migrations"
)

func main() {
	direction := flag.String("direction", "up", "up, down, or status")
	steps := flag.Int("steps", 0, "number of migration steps; zero means all pending for up")
	root := flag.String("root", ".", "trusted migration artifact root")
	manifestPath := flag.String("manifest", "deploy/migrations/manifest.json", "manifest path relative to root")
	flag.Parse()
	if flag.NArg() != 0 {
		fail(errors.New("positional arguments are not accepted"))
	}
	databaseURL, err := databaseCredential()
	if err != nil {
		fail(err)
	}
	manifest, err := migrations.LoadManifest(*root, *manifestPath)
	if err != nil {
		fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	connection, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		fail(errors.New("database connection failed"))
	}
	defer func() { _ = connection.Close(context.Background()) }()
	result, err := (migrations.Runner{Connection: connection, Manifest: manifest}).Run(ctx, *direction, *steps)
	if err != nil {
		fail(err)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if err = encoder.Encode(result); err != nil {
		fail(err)
	}
}

func databaseCredential() (string, error) {
	path := os.Getenv("DATABASE_URL_FILE")
	direct := os.Getenv("DATABASE_URL")
	allowInsecure, err := strconv.ParseBool(envString("ALLOW_INSECURE_DEVELOPMENT", "false"))
	if err != nil || (path != "" && direct != "") || (!allowInsecure && path == "") {
		return "", errors.New("database credential configuration is invalid")
	}
	if path == "" {
		if direct == "" {
			return "", errors.New("database credential is missing")
		}
		return direct, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("database credential file is unavailable")
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, 8193))
	value := strings.TrimSpace(string(contents))
	if err != nil || len(contents) > 8192 || value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("database credential file is invalid")
	}
	return value, nil
}

func envString(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func fail(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
