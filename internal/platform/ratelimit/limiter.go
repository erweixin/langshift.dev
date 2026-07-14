// Package ratelimit provides fail-closed, privacy-preserving distributed rate
// limits for public and authenticated product boundaries.
package ratelimit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	valkey "github.com/valkey-io/valkey-go"
)

var (
	ErrConfiguration = errors.New("rate limit configuration is invalid")
	ErrUnavailable   = errors.New("rate limit dependency is unavailable")
	ErrInvalidResult = errors.New("rate limit result is invalid")
)

type Limit struct {
	Capacity int64
	Window   time.Duration
}

type Request struct {
	Action        string
	SubjectDigest string
	Cost          int64
	Limit         Limit
}

type Decision struct {
	Allowed    bool
	Remaining  int64
	RetryAfter time.Duration
	ResetAfter time.Duration
}

type Limiter struct {
	Client    valkey.Client
	Namespace string
}

// GCRA uses Valkey's server clock so horizontally scaled API instances do not
// depend on synchronized application clocks. The script is deliberately not
// marked retryable: an ambiguous write may conservatively consume capacity,
// but must never be counted twice by an automatic transport retry.
var gcraScript = valkey.NewLuaScriptNoSha(`
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000000 + tonumber(clock[2])
local interval = tonumber(ARGV[1])
local capacity = tonumber(ARGV[2])
local cost = tonumber(ARGV[3])
local tat = tonumber(redis.call('GET', KEYS[1])) or now
if tat < now then tat = now end
local next_tat = tat + interval * cost
local burst = interval * capacity
local allow_at = next_tat - burst
if allow_at > now then
  local retry_ms = math.ceil((allow_at - now) / 1000)
  local reset_ms = math.ceil(math.max(0, tat - now) / 1000)
  return {0, 0, retry_ms, reset_ms}
end
local ttl_ms = math.max(1, math.ceil((next_tat - now) / 1000))
redis.call('SET', KEYS[1], tostring(next_tat), 'PX', ttl_ms)
local used = math.ceil((next_tat - now) / interval)
local remaining = math.max(0, capacity - used)
return {1, remaining, 0, ttl_ms}
`)

func (limiter Limiter) Allow(ctx context.Context, request Request) (Decision, error) {
	if limiter.Client == nil || !validToken(limiter.Namespace, 64) || !validToken(request.Action, 96) || !validDigest(request.SubjectDigest) || request.Cost < 1 || request.Limit.Capacity < 1 || request.Limit.Capacity > 100000 || request.Cost > request.Limit.Capacity || request.Limit.Window < time.Millisecond || request.Limit.Window > 24*time.Hour {
		return Decision{}, ErrConfiguration
	}
	intervalMicros := request.Limit.Window.Microseconds() / request.Limit.Capacity
	if intervalMicros < 1 {
		return Decision{}, ErrConfiguration
	}
	key := limiter.Namespace + ":rl:{" + request.SubjectDigest + "}:" + request.Action
	result, err := gcraScript.Exec(ctx, limiter.Client, []string{key}, []string{strconv.FormatInt(intervalMicros, 10), strconv.FormatInt(request.Limit.Capacity, 10), strconv.FormatInt(request.Cost, 10)}).ToArray()
	if err != nil {
		return Decision{}, ErrUnavailable
	}
	if len(result) != 4 {
		return Decision{}, ErrInvalidResult
	}
	values := make([]int64, len(result))
	for index := range result {
		values[index], err = result[index].AsInt64()
		if err != nil || values[index] < 0 {
			return Decision{}, ErrInvalidResult
		}
	}
	if values[0] != 0 && values[0] != 1 || values[1] > request.Limit.Capacity {
		return Decision{}, ErrInvalidResult
	}
	return Decision{Allowed: values[0] == 1, Remaining: values[1], RetryAfter: time.Duration(values[2]) * time.Millisecond, ResetAfter: time.Duration(values[3]) * time.Millisecond}, nil
}

// SubjectDigest prevents email, IP address, session, and user identifiers from
// appearing in cache keys or operational snapshots. Callers must use a
// purpose-specific pepper kept outside the database and Valkey.
func SubjectDigest(pepper []byte, purpose string, parts ...[]byte) (string, error) {
	if len(pepper) < 32 || !validToken(purpose, 96) || len(parts) == 0 {
		return "", ErrConfiguration
	}
	mac := hmac.New(sha256.New, pepper)
	_, _ = mac.Write([]byte(purpose))
	for _, part := range parts {
		if len(part) == 0 || len(part) > 4096 {
			return "", ErrConfiguration
		}
		_, _ = mac.Write([]byte{0})
		_, _ = mac.Write(part)
	}
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validToken(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' && character != '_' && character != '.' {
			return false
		}
	}
	return true
}
