package erasure

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	valkey "github.com/valkey-io/valkey-go"
)

var ErrCacheUnavailable = errors.New("subject cache purge is unavailable")

var purgeTaggedCache = valkey.NewLuaScriptNoSha(`
local members = redis.call('SMEMBERS', KEYS[1])
for _, key in ipairs(members) do redis.call('UNLINK', key) end
redis.call('DEL', KEYS[1])
return members
`)

var putTaggedCache = valkey.NewLuaScriptNoSha(`
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
redis.call('SADD', KEYS[2], KEYS[1])
return 1
`)

// TaggedSubjectCache makes every durable subject cache entry discoverable from
// one privacy-preserving tag set. Writers register a key after populating it;
// erasure removes the complete set and the tag atomically.
type TaggedSubjectCache struct {
	Client     valkey.Client
	Namespace  string
	ReceiptKey []byte
}

// Put makes a content-bearing cache value and its erasure tag visible in one
// atomic operation. The generated key is confined to the subject's cluster
// hash slot, so it cannot be accidentally shared with another subject.
func (cache TaggedSubjectCache) Put(ctx context.Context, tenantID, userID, suffix string, value []byte, ttl time.Duration) (string, error) {
	key, err := cache.Key(tenantID, userID, suffix)
	if err != nil || len(value) == 0 || len(value) > 8<<20 || ttl < time.Second || ttl > 24*time.Hour {
		return "", ErrInvalid
	}
	if err = putTaggedCache.Exec(ctx, cache.Client, []string{key, cache.tag(tenantID, userID)}, []string{string(value), strconv.FormatInt(ttl.Milliseconds(), 10)}).Error(); err != nil {
		return "", ErrCacheUnavailable
	}
	return key, nil
}

func (cache TaggedSubjectCache) Key(tenantID, userID, suffix string) (string, error) {
	if !cache.valid(tenantID, userID) || suffix == "" || len(suffix) > 512 || strings.ContainsAny(suffix, "{}:\\\x00\r\n") || strings.Contains(suffix, "..") {
		return "", ErrInvalid
	}
	return cache.subjectKeyPrefix(tenantID, userID) + suffix, nil
}

func (cache TaggedSubjectCache) Register(ctx context.Context, tenantID, userID string, keys ...string) error {
	if !cache.valid(tenantID, userID) || len(keys) == 0 || len(keys) > 1000 {
		return ErrInvalid
	}
	tag := cache.tag(tenantID, userID)
	prefix := cache.subjectKeyPrefix(tenantID, userID)
	commands := make(valkey.Commands, 0, len(keys))
	for _, key := range keys {
		if key == "" || len(key) > 1024 || strings.ContainsAny(key, "\x00\r\n") || key == tag || !strings.HasPrefix(key, prefix) {
			return ErrInvalid
		}
		commands = append(commands, cache.Client.B().Sadd().Key(tag).Member(key).Build())
	}
	for _, result := range cache.Client.DoMulti(ctx, commands...) {
		if result.Error() != nil {
			return ErrCacheUnavailable
		}
	}
	return nil
}

func (cache TaggedSubjectCache) PurgeSubject(ctx context.Context, tenantID, userID, recoveryEpoch string) (int, string, error) {
	if !cache.valid(tenantID, userID) || recoveryEpoch == "" {
		return 0, "", ErrInvalid
	}
	values, err := purgeTaggedCache.Exec(ctx, cache.Client, []string{cache.tag(tenantID, userID)}, nil).ToArray()
	if err != nil {
		return 0, "", ErrCacheUnavailable
	}
	keys := make([]string, 0, len(values))
	for _, value := range values {
		key, valueErr := value.ToString()
		if valueErr != nil {
			return 0, "", ErrCacheUnavailable
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	mac := hmac.New(sha256.New, cache.ReceiptKey)
	_, _ = mac.Write([]byte("lites-subject-cache-purge-v1\x00" + tenantID + "\x00" + userID + "\x00" + recoveryEpoch + "\x00"))
	for _, key := range keys {
		_, _ = mac.Write([]byte(key + "\x00"))
	}
	return len(keys), hex.EncodeToString(mac.Sum(nil)), nil
}

func (cache TaggedSubjectCache) valid(tenantID, userID string) bool {
	return cache.Client != nil && len(cache.ReceiptKey) >= 32 && cache.Namespace != "" && len(cache.Namespace) <= 64 && !strings.ContainsAny(cache.Namespace, ":{}\x00\r\n") && tenantID != "" && userID != ""
}

func (cache TaggedSubjectCache) tag(tenantID, userID string) string {
	return cache.Namespace + ":subject-cache:{" + cache.subjectTag(tenantID, userID) + "}"
}

func (cache TaggedSubjectCache) subjectKeyPrefix(tenantID, userID string) string {
	return cache.Namespace + ":subject-data:{" + cache.subjectTag(tenantID, userID) + "}:"
}

func (cache TaggedSubjectCache) subjectTag(tenantID, userID string) string {
	mac := hmac.New(sha256.New, cache.ReceiptKey)
	_, _ = mac.Write([]byte("lites-subject-cache-tag-v1\x00" + tenantID + "\x00" + userID))
	return hex.EncodeToString(mac.Sum(nil))
}
