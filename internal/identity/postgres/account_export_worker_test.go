package postgres

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/langshift/lites/internal/payload"
)

type exportTextPayloadStore struct {
	descriptor payload.Descriptor
	manifest   payload.Manifest
	body       []byte
}

func (store exportTextPayloadStore) Put(context.Context, payload.Descriptor, []byte) (payload.Manifest, error) {
	return payload.Manifest{}, errors.New("unexpected Put")
}

func (store exportTextPayloadStore) Get(_ context.Context, descriptor payload.Descriptor, manifest payload.Manifest) ([]byte, error) {
	if descriptor != store.descriptor || manifest != store.manifest {
		return nil, errors.New("unexpected payload descriptor or manifest")
	}
	return append([]byte(nil), store.body...), nil
}

func TestDecodeAccountExportWorkCanonicalizesAndRejectsUntrustedShape(t *testing.T) {
	work, err := DecodeAccountExportWork([]byte(`{"request_id":"10000000-0000-4000-8000-000000000001","user_id":"10000000-0000-4000-8000-000000000002","scope":["missions","account"],"format":"zip"}`))
	if err != nil || len(work.Scope) != 2 || work.Scope[0] != "account" || work.Scope[1] != "missions" {
		t.Fatalf("work=%#v err=%v", work, err)
	}
	invalid := []string{
		`{"request_id":"x","user_id":"u","scope":["account","account"],"format":"json"}`,
		`{"request_id":"x","user_id":"10000000-0000-4000-8000-000000000002","scope":["account"],"format":"json"}`,
		`{"request_id":"10000000-0000-4000-8000-000000000001","user_id":"u","scope":["account"],"format":"json"}`,
		`{"request_id":"x","user_id":"u","scope":["billing"],"format":"json"}`,
		`{"request_id":"x","user_id":"u","scope":["account"],"format":"tar"}`,
		`{"request_id":"x","user_id":"u","scope":["account"],"format":"json","secret":"no"}`,
	}
	for _, body := range invalid {
		if _, err = DecodeAccountExportWork([]byte(body)); err == nil {
			t.Fatalf("accepted invalid body %s", body)
		}
	}
}

func TestEncodeAccountExportProducesDeterministicPortableJSONAndZIP(t *testing.T) {
	document := accountExportDocument{SchemaVersion: 1, ExportID: "export", TenantID: "tenant", UserID: "user", GeneratedAt: time.Date(2026, 7, 18, 1, 2, 3, 0, time.UTC), Scope: []string{"account"}, Data: map[string]any{"account": map[string]any{"profile": map[string]any{"locale": "en"}}}}
	plain, err := encodeAccountExport(document, "json")
	if err != nil || !json.Valid(plain) || !bytes.HasSuffix(plain, []byte("\n")) {
		t.Fatalf("plain=%q err=%v", plain, err)
	}
	first, err := encodeAccountExport(document, "zip")
	if err != nil {
		t.Fatal(err)
	}
	second, err := encodeAccountExport(document, "zip")
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("zip is not deterministic: err=%v", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(first), int64(len(first)))
	if err != nil || len(reader.File) != 1 || reader.File[0].Name != "lites-account-export.json" {
		t.Fatalf("archive=%#v err=%v", reader.File, err)
	}
	entry, err := reader.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(entry)
	_ = entry.Close()
	if err != nil || !bytes.Equal(decoded, plain) {
		t.Fatalf("decoded=%q err=%v", decoded, err)
	}
}

func TestEncodeAccountExportEnforcesArchiveBudget(t *testing.T) {
	document := accountExportDocument{SchemaVersion: 1, ExportID: "export", TenantID: "tenant", UserID: "user", GeneratedAt: time.Now().UTC(), Scope: []string{"account"}, Data: map[string]any{"account": strings.Repeat("x", maxAccountExportBytes)}}
	if _, err := encodeAccountExport(document, "json"); !errorsIs(err, ErrAccountExportTooLarge) {
		t.Fatalf("err=%v", err)
	}
	if _, err := encodeAccountExport(document, "zip"); !errorsIs(err, ErrAccountExportTooLarge) {
		t.Fatalf("compressed archive bypassed plaintext budget: err=%v", err)
	}
}

func TestReadExportClaimTextHydratesPayloadWithoutLeakingReference(t *testing.T) {
	manifest := payload.Manifest{Ref: "encrypted://claim", Hash: strings.Repeat("a", 64)}
	descriptor := payload.Descriptor{TenantID: "tenant", ObjectID: "claim-revision", Class: "capability-claim-statement", ContentType: "application/json"}
	store := AccountExportStore{Payloads: exportTextPayloadStore{descriptor: descriptor, manifest: manifest, body: []byte(`{"schema_version":1,"text":"I can lead a product discovery interview."}`)}}
	reference, err := json.Marshal(map[string]any{"object_id": descriptor.ObjectID, "manifest": manifest})
	if err != nil {
		t.Fatal(err)
	}
	text, err := store.readExportClaimText(context.Background(), descriptor.TenantID, descriptor.Class, string(reference))
	if err != nil || text != "I can lead a product discovery interview." {
		t.Fatalf("text=%q err=%v", text, err)
	}
	if _, err = store.readExportClaimText(context.Background(), descriptor.TenantID, descriptor.Class, `{"object_id":"claim-revision","manifest":{"ref":"encrypted://claim","hash":"`+strings.Repeat("a", 64)+`"},"unexpected":true}`); err == nil {
		t.Fatal("accepted an untrusted claim reference shape")
	}
}

func errorsIs(err, target error) bool {
	return err != nil && strings.Contains(err.Error(), target.Error())
}
