// Package password implements the versioned Argon2id credential contract.
// Passwords are never truncated: policy is evaluated in Unicode code points
// and the complete UTF-8 value is passed to the KDF.
package password

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	Algorithm        = "argon2id"
	CurrentVersion   = 1
	MinimumRunes     = 15
	MaximumRunes     = 128
	minimumPepperLen = 32
)

var (
	ErrInvalidConfiguration = errors.New("password hasher configuration is invalid")
	ErrInvalidPassword      = errors.New("password does not satisfy policy")
	ErrInvalidParameters    = errors.New("stored password parameters are invalid")
)

// Parameters are persisted as JSON next to the derived key. They are bounded
// during verification to prevent a corrupted row from causing resource abuse.
type Parameters struct {
	Version   uint32 `json:"version"`
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memory_kib"`
	Threads   uint8  `json:"threads"`
	KeyLength uint32 `json:"key_length"`
	Salt      string `json:"salt"`
}

type Hasher struct {
	Parameters Parameters
	Pepper     []byte
	Random     io.Reader
}

func ProductionParameters() Parameters {
	return Parameters{Version: CurrentVersion, Time: 3, MemoryKiB: 64 * 1024, Threads: 4, KeyLength: 32}
}

func ValidateForRegistration(value string) error {
	if !utf8.ValidString(value) {
		return ErrInvalidPassword
	}
	length := utf8.RuneCountInString(value)
	if length < MinimumRunes || length > MaximumRunes {
		return ErrInvalidPassword
	}
	return nil
}

func ValidateForAuthentication(value string) error {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > MaximumRunes {
		return ErrInvalidPassword
	}
	return nil
}

func (hasher Hasher) Hash(value string) ([]byte, Parameters, error) {
	if err := ValidateForRegistration(value); err != nil {
		return nil, Parameters{}, err
	}
	parameters := hasher.Parameters
	if err := validateConfiguration(parameters, hasher.Pepper); err != nil {
		return nil, Parameters{}, err
	}
	randomSource := hasher.Random
	if randomSource == nil {
		randomSource = rand.Reader
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(randomSource, salt); err != nil {
		return nil, Parameters{}, fmt.Errorf("read password salt: %w", err)
	}
	parameters.Salt = base64.RawURLEncoding.EncodeToString(salt)
	key := derive(value, hasher.Pepper, salt, parameters)
	return key, parameters, nil
}

func (hasher Hasher) Verify(value string, expected []byte, parameters Parameters) (bool, error) {
	if err := ValidateForAuthentication(value); err != nil {
		return false, err
	}
	if err := validateConfiguration(hasher.Parameters, hasher.Pepper); err != nil {
		return false, err
	}
	if err := validateStored(parameters); err != nil {
		return false, err
	}
	salt, err := base64.RawURLEncoding.DecodeString(parameters.Salt)
	if err != nil || len(salt) != 16 || len(expected) != int(parameters.KeyLength) {
		return false, ErrInvalidParameters
	}
	actual := derive(value, hasher.Pepper, salt, parameters)
	defer zero(actual)
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

func (hasher Hasher) NeedsRehash(parameters Parameters) bool {
	desired := hasher.Parameters
	return parameters.Version != desired.Version || parameters.Time != desired.Time || parameters.MemoryKiB != desired.MemoryKiB || parameters.Threads != desired.Threads || parameters.KeyLength != desired.KeyLength
}

func derive(value string, pepper, salt []byte, parameters Parameters) []byte {
	secret := []byte(value)
	if len(pepper) > 0 {
		mac := hmac.New(sha256.New, pepper)
		_, _ = mac.Write(secret)
		secret = mac.Sum(nil)
		defer zero(secret)
	}
	return argon2.IDKey(secret, salt, parameters.Time, parameters.MemoryKiB, parameters.Threads, parameters.KeyLength)
}

func validateConfiguration(parameters Parameters, pepper []byte) error {
	if len(pepper) != 0 && len(pepper) < minimumPepperLen {
		return ErrInvalidConfiguration
	}
	if parameters.Version != CurrentVersion || parameters.Time < 1 || parameters.Time > 10 || parameters.MemoryKiB < 8*1024 || parameters.MemoryKiB > 1024*1024 || parameters.Threads < 1 || parameters.Threads > 16 || parameters.KeyLength < 16 || parameters.KeyLength > 64 {
		return ErrInvalidConfiguration
	}
	return nil
}

func validateStored(parameters Parameters) error {
	if parameters.Version != CurrentVersion || parameters.Time < 1 || parameters.Time > 10 || parameters.MemoryKiB < 8*1024 || parameters.MemoryKiB > 1024*1024 || parameters.Threads < 1 || parameters.Threads > 16 || parameters.KeyLength < 16 || parameters.KeyLength > 64 || parameters.Salt == "" {
		return ErrInvalidParameters
	}
	return nil
}

func zero(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
