// lites-local-mailbox is a fixture-only STARTTLS mailbox and reminder sink for
// the hermetic macOS product stack. It must never be deployed as mail service.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	configuration, err := loadConfig()
	if err == nil {
		err = run(ctx, configuration, logger)
	}
	if err != nil {
		logger.Error("local mailbox stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, configuration config, logger *slog.Logger) error {
	certificate, err := tls.LoadX509KeyPair(configuration.certificateFile, configuration.keyFile)
	if err != nil {
		return errors.New("load local mailbox certificate")
	}
	reminderToken, err := readToken(configuration.reminderTokenFile)
	if err != nil {
		return err
	}
	smtpListener, err := net.Listen("tcp", configuration.smtpAddress)
	if err != nil {
		return err
	}
	defer smtpListener.Close()
	box := &mailbox{}
	api := &http.Server{Addr: configuration.apiAddress, Handler: box.handler(reminderToken), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	internal := &http.Server{Addr: configuration.internalAddress, Handler: box.handler(reminderToken), TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}}, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	errorsChannel := make(chan error, 3)
	go func() { errorsChannel <- serveSMTP(smtpListener, certificate, box) }()
	go func() { errorsChannel <- api.ListenAndServe() }()
	go func() {
		listener, listenErr := net.Listen("tcp", internal.Addr)
		if listenErr != nil {
			errorsChannel <- listenErr
			return
		}
		errorsChannel <- internal.ServeTLS(listener, "", "")
	}()
	logger.Info("fixture-only local mailbox ready", "smtp", configuration.smtpAddress, "api", configuration.apiAddress, "internal", configuration.internalAddress)
	select {
	case <-ctx.Done():
		err = nil
	case err = <-errorsChannel:
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			err = nil
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = smtpListener.Close()
	_ = api.Shutdown(shutdown)
	_ = internal.Shutdown(shutdown)
	return err
}

func readToken(path string) (string, error) {
	contents, err := os.ReadFile(path)
	value := strings.TrimSpace(string(contents))
	if err != nil || len(value) < 32 || len(value) > 8192 || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("local reminder token is invalid")
	}
	return value, nil
}
