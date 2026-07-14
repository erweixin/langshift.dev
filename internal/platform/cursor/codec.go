// Package cursor signs opaque keyset pagination cursors. A cursor is bound to
// its operation and principal scope so it cannot be replayed across resources.
package cursor

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const Version = 1

var ErrInvalid = errors.New("pagination cursor is invalid")

type Codec struct{ Key []byte }

type envelope struct {
	Version int             `json:"v"`
	Scope   string          `json:"scope"`
	Data    json.RawMessage `json:"data"`
}

func (codec Codec) Encode(scope string, value any) (string, error) {
	if len(codec.Key) < 32 || scope == "" || value == nil {
		return "", ErrInvalid
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", ErrInvalid
	}
	payload, err := json.Marshal(envelope{Version: Version, Scope: scope, Data: data})
	if err != nil || len(payload) > 2048 {
		return "", ErrInvalid
	}
	mac := codec.digest(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac), nil
}

func (codec Codec) Decode(token, scope string, target any) error {
	if len(codec.Key) < 32 || token == "" || len(token) > 4096 || scope == "" || target == nil {
		return ErrInvalid
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return ErrInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(payload) == 0 || len(payload) > 2048 {
		return ErrInvalid
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, codec.digest(payload)) {
		return ErrInvalid
	}
	var value envelope
	if strictDecode(payload, &value) != nil || value.Version != Version || value.Scope != scope || len(value.Data) == 0 {
		return ErrInvalid
	}
	if strictDecode(value.Data, target) != nil {
		return ErrInvalid
	}
	return nil
}

func (codec Codec) digest(payload []byte) []byte {
	mac := hmac.New(sha256.New, codec.Key)
	_, _ = mac.Write([]byte("lites-pagination-cursor-v1"))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

func strictDecode(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	return nil
}
