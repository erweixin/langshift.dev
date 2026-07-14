//go:build integration

package mail

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/security/opaque"
)

func mustURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

type integrationEpoch struct{ value string }

func (authority integrationEpoch) CurrentStoreEpoch(context.Context) (string, error) {
	return authority.value, nil
}

type integrationKeys struct{ key payload.Key }

func (provider integrationKeys) Current(context.Context, string) (payload.Key, error) {
	return provider.key, nil
}
func (provider integrationKeys) ByID(context.Context, string, string) (payload.Key, error) {
	return provider.key, nil
}

type integrationBlobs struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (store *integrationBlobs) Put(_ context.Context, key string, value []byte) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if current, exists := store.values[key]; exists && !bytes.Equal(current, value) {
		return "", errors.New("immutable blob conflict")
	}
	store.values[key] = append([]byte(nil), value...)
	return key, nil
}
func (store *integrationBlobs) Get(_ context.Context, key string) ([]byte, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, exists := store.values[key]
	if !exists {
		return nil, errors.New("blob missing")
	}
	return append([]byte(nil), value...), nil
}

type recordingSender struct {
	mu       sync.Mutex
	messages []Message
	ids      []string
}

func (sender *recordingSender) Send(_ context.Context, id string, message Message) error {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	sender.ids = append(sender.ids, id)
	sender.messages = append(sender.messages, message)
	return nil
}

func TestDispatcherDecryptsSendsAndCompletesInboxExactlyOnce(t *testing.T) {
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, os.Getenv("LITES_TEST_ADMIN_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	pool, err := pgxpool.New(ctx, os.Getenv("LITES_TEST_IDENTITY_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	const tenantID = "8a000000-0000-4000-8000-000000000001"
	const epochID = "8b000000-0000-4000-8000-000000000001"
	if _, err = admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region) VALUES($1,'enterprise','Mail Test','active','US')`, tenantID); err != nil {
		t.Fatal(err)
	}
	blobs := &integrationBlobs{values: map[string][]byte{}}
	store := payload.EnvelopeStore{Keys: integrationKeys{key: payload.Key{ID: "mail-key-v1", Material: bytes.Repeat([]byte{0x91}, 32)}}, Blobs: blobs, Random: bytes.NewReader(bytes.Repeat([]byte{0x92}, 128))}
	commandID := "8c000000-0000-4000-8000-000000000001"
	descriptor := payload.Descriptor{TenantID: tenantID, ObjectID: commandID, Class: "mail-command", ContentType: "application/json"}
	manifest, err := store.Put(ctx, descriptor, []byte(`{"template":"verify-email-v1","recipient":"person@example.com","token":"abcdefghijklmnopqrstuvwxyz0123456789","expires_at":"2030-07-15T12:00:00Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	command := eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: epochID, CommandID: commandID, CommandType: "identity.email.verify", AggregateKind: "email_verification", AggregateID: "8d000000-0000-4000-8000-000000000001", PayloadRef: manifest.Ref, PayloadHash: manifest.Hash}
	sender := &recordingSender{}
	dispatcher := Dispatcher{Payloads: store, Inbox: eventpostgres.InboxStore{Pool: pool, Epochs: integrationEpoch{epochID}, Tokens: opaque.Manager{Purpose: "mail-inbox", Pepper: bytes.Repeat([]byte{0x93}, 32), Random: bytes.NewReader(bytes.Repeat([]byte{0x94}, 512))}, LeaseTTL: time.Minute, Random: bytes.NewReader(bytes.Repeat([]byte{0x95}, 512))}, Sender: sender, AppURL: mustURL(t, "https://app.lites.dev"), StoreEpoch: epochID, ConsumerName: "mail-test-worker"}
	first, err := dispatcher.Dispatch(ctx, command)
	if err != nil || !first.Claimed || !first.Completed || first.Replayed {
		t.Fatalf("first=%#v error=%v", first, err)
	}
	replay, err := dispatcher.Dispatch(ctx, command)
	if err != nil || !replay.Completed || !replay.Replayed || replay.Claimed {
		t.Fatalf("replay=%#v error=%v", replay, err)
	}
	if len(sender.ids) != 1 || sender.ids[0] != commandID || len(sender.messages) != 1 || sender.messages[0].To != "person@example.com" {
		t.Fatalf("ids=%#v messages=%#v", sender.ids, sender.messages)
	}
	var status string
	if err = admin.QueryRow(ctx, `SELECT status FROM agent.inbox WHERE tenant_id=$1 AND consumer_name='mail-test-worker' AND command_id=$2`, tenantID, commandID).Scan(&status); err != nil || status != "completed" {
		t.Fatalf("status=%q error=%v", status, err)
	}
}
