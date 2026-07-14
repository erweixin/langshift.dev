// Package trustedcontext signs the short-lived identity context issued by the
// gateway to internal services. Browser input is never accepted as this type.
package trustedcontext

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const Version = "LITES-TC1"

var (
	ErrMalformed        = errors.New("trusted context is malformed")
	ErrInvalidSignature = errors.New("trusted context signature is invalid")
	ErrInvalidClaims    = errors.New("trusted context claims are invalid")
	ErrExpired          = errors.New("trusted context is expired")
	ErrUnknownKey       = errors.New("trusted context signing key is unknown")
)

type Claims struct {
	Issuer       string   `json:"iss"`
	Audience     string   `json:"aud"`
	SubjectID    string   `json:"sub"`
	TenantID     string   `json:"tenant_id"`
	MembershipID string   `json:"membership_id"`
	SessionID    string   `json:"session_id"`
	Roles        []string `json:"roles"`
	IssuedAt     int64    `json:"iat"`
	ExpiresAt    int64    `json:"exp"`
	Nonce        string   `json:"nonce"`
}

type header struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
	Type      string `json:"typ"`
}

type Verifier struct {
	Issuer     string
	Audience   string
	Keys       map[string]ed25519.PublicKey
	MaximumTTL time.Duration
	ClockSkew  time.Duration
}

func Sign(claims Claims, keyID string, key ed25519.PrivateKey, maximumTTL time.Duration) (string, error) {
	if len(key) != ed25519.PrivateKeySize || keyID == "" || !validClaims(claims, maximumTTL) {
		return "", ErrInvalidClaims
	}
	headerJSON, err := json.Marshal(header{Algorithm: "EdDSA", KeyID: keyID, Type: Version})
	if err != nil {
		return "", fmt.Errorf("encode trusted context header: %w", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("encode trusted context claims: %w", err)
	}
	unsigned := encode(headerJSON) + "." + encode(claimsJSON)
	signature := ed25519.Sign(key, []byte(unsigned))
	return unsigned + "." + encode(signature), nil
}

func (v Verifier) Verify(token string, now time.Time) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return Claims{}, ErrMalformed
	}
	var h header
	if err := decodeStrict(parts[0], &h); err != nil || h.Algorithm != "EdDSA" || h.Type != Version || h.KeyID == "" {
		return Claims{}, ErrMalformed
	}
	key, ok := v.Keys[h.KeyID]
	if !ok {
		return Claims{}, ErrUnknownKey
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != ed25519.SignatureSize {
		return Claims{}, ErrMalformed
	}
	if !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), signature) {
		return Claims{}, ErrInvalidSignature
	}
	var claims Claims
	if err := decodeStrict(parts[1], &claims); err != nil || !validClaims(claims, v.MaximumTTL) {
		return Claims{}, ErrInvalidClaims
	}
	if claims.Issuer != v.Issuer || claims.Audience != v.Audience {
		return Claims{}, ErrInvalidClaims
	}
	nowUnix := now.Unix()
	skew := int64(v.ClockSkew / time.Second)
	if claims.IssuedAt > nowUnix+skew {
		return Claims{}, ErrInvalidClaims
	}
	if claims.ExpiresAt <= nowUnix-skew {
		return Claims{}, ErrExpired
	}
	return claims, nil
}

func validClaims(claims Claims, maximumTTL time.Duration) bool {
	if claims.Issuer == "" || claims.Audience == "" || claims.SubjectID == "" || claims.TenantID == "" || claims.MembershipID == "" || claims.SessionID == "" || claims.Nonce == "" || len(claims.Roles) == 0 {
		return false
	}
	if claims.IssuedAt <= 0 || claims.ExpiresAt <= claims.IssuedAt || maximumTTL <= 0 || time.Duration(claims.ExpiresAt-claims.IssuedAt)*time.Second > maximumTTL {
		return false
	}
	seen := make(map[string]struct{}, len(claims.Roles))
	for _, role := range claims.Roles {
		if role == "" {
			return false
		}
		if _, exists := seen[role]; exists {
			return false
		}
		seen[role] = struct{}{}
	}
	return true
}

func decodeStrict(value string, target any) error {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrMalformed
	}
	return nil
}

func encode(value []byte) string { return base64.RawURLEncoding.EncodeToString(value) }
