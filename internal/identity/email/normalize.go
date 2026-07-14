// Package email owns canonical email normalization and validation. The exact
// normalized value is the database uniqueness and rate-limit identity.
package email

import (
	"errors"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

var ErrInvalid = errors.New("email address is invalid")

func Normalize(value string) (string, error) {
	value = norm.NFC.String(strings.TrimSpace(value))
	if value == "" || len(value) > 254 || !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
		return "", ErrInvalid
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Name != "" || parsed.Address != value {
		return "", ErrInvalid
	}
	at := strings.LastIndexByte(value, '@')
	if at < 1 || at == len(value)-1 || strings.Contains(value[:at], "@") {
		return "", ErrInvalid
	}
	// Lites deliberately treats the complete mailbox case-insensitively. This
	// avoids duplicate accounts whose providers already fold local-part case.
	return strings.ToLower(value), nil
}
