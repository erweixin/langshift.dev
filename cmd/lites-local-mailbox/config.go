package main

import (
	"errors"
	"os"
)

type config struct {
	smtpAddress       string
	apiAddress        string
	internalAddress   string
	certificateFile   string
	keyFile           string
	reminderTokenFile string
}

func loadConfig() (config, error) {
	value := config{
		smtpAddress:       environment("SMTP_ADDRESS", ":1025"),
		apiAddress:        environment("API_ADDRESS", ":8025"),
		internalAddress:   environment("INTERNAL_ADDRESS", ":8443"),
		certificateFile:   os.Getenv("SERVER_TLS_CERT_FILE"),
		keyFile:           os.Getenv("SERVER_TLS_KEY_FILE"),
		reminderTokenFile: os.Getenv("REMINDER_BEARER_TOKEN_FILE"),
	}
	if os.Getenv("LITES_ENVIRONMENT") != "engineering-test" || os.Getenv("LITES_LOCAL_COMPOSE") != "true" {
		return config{}, errors.New("local mailbox is restricted to the engineering-test Compose stack")
	}
	if value.smtpAddress != ":1025" || value.apiAddress != ":8025" || value.internalAddress != ":8443" || value.certificateFile == "" || value.keyFile == "" || value.certificateFile == value.keyFile || value.reminderTokenFile == "" {
		return config{}, errors.New("local mailbox configuration is invalid")
	}
	return value, nil
}

func environment(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
