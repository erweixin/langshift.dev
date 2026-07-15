//go:build integration

package agentworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/behavior"
	"github.com/langshift/lites/internal/llmgateway/provider"
	"github.com/langshift/lites/internal/payload"
)

func TestContextSourceStoreLoadsOnlyFencedImmutableTenantSources(t *testing.T) {
	ctx := context.Background()
	admin := contextPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	agent := contextPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer agent.Close()
	const (
		userID        = "a7000000-0000-4000-8000-000000000001"
		tenantID      = "a7000000-0000-4000-8000-000000000002"
		runID         = "a7000000-0000-4000-8000-000000000003"
		conversation  = "a7000000-0000-4000-8000-000000000004"
		commandID     = "a7000000-0000-4000-8000-000000000005"
		outboxID      = "a7000000-0000-4000-8000-000000000006"
		jobID         = "a7000000-0000-4000-8000-000000000007"
		attemptID     = "a7000000-0000-4000-8000-000000000008"
		epoch         = "a7000000-0000-4000-8000-000000000009"
		behaviorID    = "a7000000-0000-4000-8000-000000000010"
		behaviorEvent = "a7000000-0000-4000-8000-000000000011"
		messageID     = "a7000000-0000-4000-8000-000000000012"
		messageEvent  = "a7000000-0000-4000-8000-000000000013"
	)
	now := time.Now().UTC().Truncate(time.Microsecond)
	binding := func(id string, fill string) behavior.Binding {
		return behavior.Binding{ID: id, Version: "v1", Hash: strings.Repeat(fill, 64)}
	}
	behaviorManifest := behavior.Manifest{SchemaVersion: 1, Profile: behavior.Coach, Model: binding("model", "1"), Prompt: binding("prompt", "2"), ProfileDefinition: binding("profile", "3"), GuardrailPolicy: binding("guardrail", "4"), RouterPolicy: binding("router", "5"), SourceCommit: "abcdef", CreatedAt: now}
	behaviorHash, err := behaviorManifest.Hash()
	if err != nil {
		t.Fatal(err)
	}
	snapshotID := "behavior-" + behaviorHash
	behaviorJSON, _ := json.Marshal(behaviorManifest)
	payloads := &memoryPayloads{}
	document := MessageDocument{SchemaVersion: 1, Role: "user", Content: []provider.ContentBlock{{Type: "text", Text: "Explain my next step"}}}
	messageJSON, _ := json.Marshal(document)
	messageManifest, err := payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: messageID, Class: "run-message", ContentType: "application/json"}, messageJSON)
	if err != nil {
		t.Fatal(err)
	}
	setup := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'context-owner@example.invalid','en','active')`, []any{userID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Context Tenant','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO agent.events(id,tenant_id,user_id,seq,event_type,event_schema_version,aggregate_kind,aggregate_id,aggregate_version,store_epoch,occurred_at,committed_at,actor,correlation_id,payload_ref,payload_hash) VALUES($1,$2,$3,1,'BehaviorSnapshotCreated',1,'behavior_snapshot',$4,1,$5,$6,$6,'{"kind":"service"}',$4,'encrypted://context/behavior','behavior-event')`, []any{behaviorEvent, tenantID, userID, behaviorID, epoch, now}},
		{`INSERT INTO agent.behavior_snapshots(id,tenant_id,snapshot_id,profile_name,manifest,manifest_hash,source_commit,created_by,created_event_id,created_at) VALUES($1,$2,$3,'coach',$4,$5,'abcdef',$6,$7,$8)`, []any{behaviorID, tenantID, snapshotID, behaviorJSON, behaviorHash, userID, behaviorEvent, now}},
		{`INSERT INTO agent.outbox(id,tenant_id,command_id,command_type,aggregate_kind,aggregate_id,store_epoch,payload_ref,payload_hash,status,available_at,published_at) VALUES($1,$2,$3,'StartAgentRun','run',$4,$5,'encrypted://context/start','start-hash','published',$6,$6)`, []any{outboxID, tenantID, commandID, runID, epoch, now}},
		{`INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,due_at,enqueued_at) VALUES($1,$2,$3,'interactive','llm',50,1,5,'running',$4,$5,$4)`, []any{jobID, tenantID, commandID, now, now.Add(time.Hour)}},
		{`INSERT INTO agent.job_attempts(id,tenant_id,job_id,command_id,fence,lease_token_hash,lease_expires_at,worker_id,status,started_at) VALUES($1,$2,$3,$4,1,$5,$6,'context-worker','running',$7)`, []any{attemptID, tenantID, jobID, commandID, bytes.Repeat([]byte{0xa7}, 32), now.Add(time.Hour), now}},
		{`INSERT INTO agent.runs(id,tenant_id,user_id,conversation_id,status,run_version,active_command_id,active_attempt_id,current_fence,lease_token_hash,lease_expires_at,due_at,profile_snapshot_id,budget_snapshot,root_run_id,depth,inherited_budget_microunits,created_at,updated_at) VALUES($1,$2,$3,$4,'executing',3,$5,$6,1,$7,$8,$8,$9,'{"max_cost_microunits":1000}',$1,0,0,$10,$10)`, []any{runID, tenantID, userID, conversation, commandID, attemptID, bytes.Repeat([]byte{0xa7}, 32), now.Add(time.Hour), snapshotID, now}},
		{`INSERT INTO agent.events(id,tenant_id,user_id,seq,event_type,event_schema_version,aggregate_kind,aggregate_id,aggregate_version,store_epoch,occurred_at,committed_at,actor,correlation_id,payload_ref,payload_hash) VALUES($1,$2,$3,2,'MessageFinalized',1,'run_message',$4,1,$5,$6,$6,'{"kind":"user"}',$4,$7,$8)`, []any{messageEvent, tenantID, userID, messageID, epoch, now, messageManifest.Ref, messageManifest.Hash}},
		{`INSERT INTO agent.run_messages(id,tenant_id,user_id,run_id,role,message_index,payload_ref,payload_hash,content_hash,content_type,source_kind,trust_label,finalized_event_id,finalized_at) VALUES($1,$2,$3,$4,'user',0,$5,$6,$7,'application/json','conversation_user','user_asserted',$8,$9)`, []any{messageID, tenantID, userID, runID, messageManifest.Ref, messageManifest.Hash, sha256Bytes(messageJSON), messageEvent, now}},
	}
	for _, statement := range setup {
		if _, err = admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	claim := validClaim()
	claim.RunID, claim.TenantID, claim.UserID, claim.StoreEpoch, claim.CommandID, claim.JobID, claim.AttemptID, claim.RunVersion, claim.Fence = runID, tenantID, userID, epoch, commandID, jobID, attemptID, 3, 1
	store := ContextSourceStore{Pool: agent, Payloads: payloads}
	sources, err := store.Load(ctx, Execution{Claim: claim, Payload: CommandPayload{RunID: runID, CorrelationID: "context-correlation"}, TurnIndex: 1})
	if err != nil || sources.RunID != runID || sources.BehaviorSnapshotID != snapshotID || len(sources.Messages) != 1 || sources.Messages[0].Document.Content[0].Text != "Explain my next step" {
		t.Fatalf("sources=%#v err=%v", sources, err)
	}
	stale := claim
	stale.CommandID = "a7000000-0000-4000-8000-000000000099"
	if _, err = store.Load(ctx, Execution{Claim: stale}); !errors.Is(err, ErrContextFence) {
		t.Fatalf("stale command error=%v", err)
	}
	payloads.mu.Lock()
	payloads.objects[messageManifest.Ref][0] ^= 0xff
	payloads.mu.Unlock()
	if _, err = store.Load(ctx, Execution{Claim: claim}); !errors.Is(err, ErrContextIntegrity) {
		t.Fatalf("tampered payload error=%v", err)
	}
}

func contextPool(t *testing.T, ctx context.Context, variable string) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv(variable)
	if url == "" {
		t.Skip(variable + " is not configured")
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}
