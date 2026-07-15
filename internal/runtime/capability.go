package runtime

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

const CapabilityVersion = "lites-runtime-capability.v1"

const maximumCapabilityTTL = 5 * time.Minute

var (
	ErrMalformedCapability = errors.New("runtime capability is malformed")
	ErrInvalidCapability   = errors.New("runtime capability is invalid")
	ErrExpiredCapability   = errors.New("runtime capability is expired")
)

type CapabilityClaims struct {
	Purpose               string        `json:"purpose"`
	Issuer                string        `json:"iss"`
	Audience              string        `json:"aud"`
	TenantID              string        `json:"tenant_id"`
	UserID                string        `json:"user_id"`
	RunID                 string        `json:"run_id"`
	ToolCallID            string        `json:"tool_call_id"`
	CommandID             string        `json:"command_id"`
	AttemptID             string        `json:"attempt_id"`
	Fence                 uint64        `json:"fence"`
	PolicySnapshotID      string        `json:"policy_snapshot_id"`
	PolicyHash            string        `json:"policy_hash"`
	WorkspaceID           string        `json:"workspace_id,omitempty"`
	BaseWorkspaceRevision string        `json:"base_workspace_revision,omitempty"`
	WorkspaceMode         WorkspaceMode `json:"workspace_mode"`
	NetworkPolicyHash     string        `json:"network_policy_hash"`
	SecretScopeHash       string        `json:"secret_scope_hash"`
	LeaseTokenHash        string        `json:"lease_token_hash"`
	RequestHash           string        `json:"request_hash"`
	ApprovalID            string        `json:"approval_id,omitempty"`
	ApprovalVersion       uint64        `json:"approval_version,omitempty"`
	IssuedAt              int64         `json:"iat"`
	ExpiresAt             int64         `json:"exp"`
	Nonce                 string        `json:"nonce"`
}

type capabilityHeader struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
	Type      string `json:"typ"`
}

type CapabilityKeyWindow struct {
	NotBefore time.Time
	NotAfter  time.Time
}

type CapabilityVerifier struct {
	Issuer, Audience string
	Keys             map[string]ed25519.PublicKey
	KeyWindows       map[string]CapabilityKeyWindow
	MaximumTTL       time.Duration
	ClockSkew        time.Duration
}

func SignCapability(claims CapabilityClaims, keyID string, key ed25519.PrivateKey, maximumTTL time.Duration) (string, error) {
	if keyID == "" || len(key) != ed25519.PrivateKeySize || !validCapabilityClaims(claims, maximumTTL) {
		return "", ErrInvalidCapability
	}
	headerJSON, err := json.Marshal(capabilityHeader{Algorithm: "EdDSA", KeyID: keyID, Type: CapabilityVersion})
	if err != nil {
		return "", ErrInvalidCapability
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", ErrInvalidCapability
	}
	unsigned := capabilityEncode(headerJSON) + "." + capabilityEncode(claimsJSON)
	signature := ed25519.Sign(key, []byte(unsigned))
	return unsigned + "." + capabilityEncode(signature), nil
}

func (verifier CapabilityVerifier) Verify(token string, now time.Time) (CapabilityClaims, error) {
	if verifier.Issuer == "" || verifier.Audience == "" || verifier.MaximumTTL <= 0 || verifier.MaximumTTL > maximumCapabilityTTL || verifier.ClockSkew < 0 || verifier.ClockSkew > 30*time.Second {
		return CapabilityClaims{}, ErrInvalidCapability
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return CapabilityClaims{}, ErrMalformedCapability
	}
	var header capabilityHeader
	if err := capabilityDecode(parts[0], &header); err != nil || header.Algorithm != "EdDSA" || header.Type != CapabilityVersion || header.KeyID == "" {
		return CapabilityClaims{}, ErrMalformedCapability
	}
	key, ok := verifier.Keys[header.KeyID]
	if !ok || len(key) != ed25519.PublicKeySize {
		return CapabilityClaims{}, ErrInvalidCapability
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), signature) {
		return CapabilityClaims{}, ErrInvalidCapability
	}
	var claims CapabilityClaims
	if err = capabilityDecode(parts[1], &claims); err != nil || !validCapabilityClaims(claims, verifier.MaximumTTL) || claims.Issuer != verifier.Issuer || claims.Audience != verifier.Audience {
		return CapabilityClaims{}, ErrInvalidCapability
	}
	if verifier.KeyWindows != nil {
		window, found := verifier.KeyWindows[header.KeyID]
		issuedAt := time.Unix(claims.IssuedAt, 0).UTC()
		if !found || window.NotBefore.IsZero() || window.NotAfter.IsZero() || !window.NotAfter.After(window.NotBefore) || issuedAt.Before(window.NotBefore) || !issuedAt.Before(window.NotAfter) {
			return CapabilityClaims{}, ErrInvalidCapability
		}
	}
	nowUnix := now.UTC().Unix()
	skew := int64(verifier.ClockSkew / time.Second)
	if claims.IssuedAt > nowUnix+skew {
		return CapabilityClaims{}, ErrInvalidCapability
	}
	if claims.ExpiresAt <= nowUnix-skew {
		return CapabilityClaims{}, ErrExpiredCapability
	}
	return claims, nil
}

func validCapabilityClaims(claims CapabilityClaims, maximumTTL time.Duration) bool {
	if claims.Purpose != "runtime_session" || claims.Issuer == "" || claims.Audience == "" || claims.TenantID == "" || claims.UserID == "" || claims.RunID == "" || claims.ToolCallID == "" || claims.CommandID == "" || claims.AttemptID == "" || claims.Fence == 0 || !snapshotIDPattern.MatchString(claims.PolicySnapshotID) || !validHexHash(claims.PolicyHash) || !validDigest(claims.NetworkPolicyHash) || !validDigest(claims.SecretScopeHash) || !validLeaseHash(claims.LeaseTokenHash) || !validHexHash(claims.RequestHash) || claims.Nonce == "" || claims.IssuedAt <= 0 || claims.ExpiresAt <= claims.IssuedAt || maximumTTL <= 0 || maximumTTL > maximumCapabilityTTL || time.Duration(claims.ExpiresAt-claims.IssuedAt)*time.Second > maximumTTL {
		return false
	}
	if claims.WorkspaceMode != WorkspaceNone && claims.WorkspaceMode != WorkspaceReadOnly && claims.WorkspaceMode != WorkspaceReadWrite {
		return false
	}
	if claims.WorkspaceMode == WorkspaceNone && (claims.WorkspaceID != "" || claims.BaseWorkspaceRevision != "") || claims.WorkspaceMode != WorkspaceNone && (claims.WorkspaceID == "" || claims.BaseWorkspaceRevision == "") {
		return false
	}
	if (claims.ApprovalID == "") != (claims.ApprovalVersion == 0) {
		return false
	}
	nonce, err := base64.RawURLEncoding.DecodeString(claims.Nonce)
	return err == nil && len(nonce) >= 16 && len(nonce) <= 32
}

func validHexHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validLeaseHash(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func capabilityEncode(value []byte) string { return base64.RawURLEncoding.EncodeToString(value) }

func capabilityDecode(value string, target any) error {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return ErrMalformedCapability
	}
	return nil
}
