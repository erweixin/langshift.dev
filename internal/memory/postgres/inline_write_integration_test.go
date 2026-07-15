//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/security/opaque"
)

func TestMemoryWriteCommitsAsInlinePlatformTool(t *testing.T) {
	ctx := context.Background()
	admin := memoryPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := memoryPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()
	const tenantID = "6d000000-0000-4000-8000-000000000001"
	const userID = "6d000000-0000-4000-8000-000000000002"
	const runID = "6d000000-0000-4000-8000-000000000003"
	const conversationID = "6d000000-0000-4000-8000-000000000004"
	const correlationID = "6d000000-0000-4000-8000-000000000005"
	const storeEpoch = "6d000000-0000-4000-8000-000000000006"
	const memoryID = "6d000000-0000-4000-8000-000000000007"
	now := time.Date(2026, time.July, 15, 8, 0, 0, 0, time.UTC)
	if _, err := admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'inline-memory@example.invalid','en','active')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Inline Memory','active','US',$2)`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO product.memory_policies(tenant_id,user_id,version,enabled,retention_days,allowed_kinds,updated_at) VALUES($1,$2,3,true,365,'["preference"]',$3)`, tenantID, userID, now); err != nil {
		t.Fatal(err)
	}
	appender := eventpostgres.Appender{Now: func() time.Time { return now }}
	memoryHandler := InlineWriteHandler{Appender: appender, IDKey: bytes.Repeat([]byte{0x62}, 32)}
	store := executionpostgres.RunStore{
		Pool: pool, Appender: appender, IDKey: bytes.Repeat([]byte{0x61}, 32), StoreEpoch: storeEpoch,
		Now: func() time.Time { return now }, Epochs: memoryEpochStub{epoch: storeEpoch},
		Tokens:   opaque.Manager{Purpose: "inline-memory-run", Pepper: bytes.Repeat([]byte{0x63}, 32)},
		LeaseTTL: 2 * time.Minute, Random: bytes.NewReader(bytes.Repeat([]byte{0x64}, 256)),
		InlineTools: map[string]executionpostgres.InlinePlatformToolHandler{"memory_write": memoryHandler},
	}
	accepted, err := store.Accept(ctx, executionpostgres.AcceptRunCommand{RunID: runID, TenantID: tenantID, UserID: userID, ConversationID: conversationID, CorrelationID: correlationID, DueAt: now.Add(time.Hour), ProfileSnapshotID: "coach@sha256:inline-memory", BudgetSnapshot: json.RawMessage(`{"max_steps":16}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://memory/accepted", Hash: "accepted"}, QueuedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://memory/queued", Hash: "queued"}, StartCommand: executionpostgres.PayloadPointer{Ref: "encrypted://memory/start", Hash: "start"}, QueueClass: "interactive", ResourceClass: "llm", Priority: 50, CostUnits: 4, MaxAttempts: 5})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimStart(ctx, executionpostgres.ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: accepted.StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: runID, PayloadRef: "encrypted://memory/start", PayloadHash: "start"}, ConsumerName: "agent-run-worker", WorkerID: "inline-memory-worker", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: executionpostgres.PayloadPointer{Ref: "encrypted://memory/run-started", Hash: "run-started"}, AttemptStartedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://memory/attempt-started", Hash: "attempt-started"}, AttemptExpiredEvent: executionpostgres.PayloadPointer{Ref: "encrypted://memory/attempt-expired", Hash: "attempt-expired"}})
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := now.Add(365 * 24 * time.Hour)
	write := MemoryWrite{
		MemoryID: memoryID, ScopeKind: "user", ScopeID: userID, MemoryKind: "preference",
		ContentRef: "vault://payload/memory/1", ContentHMAC: [32]byte{1, 2, 3}, ContentType: "text",
		SensitivityLabels: []string{"private", "user_preference"}, SourceKind: "user_stated",
		Sources:        []SourceReference{{Kind: "user_statement", Ref: "event://conversation/message/42", Version: 1}},
		DataSubjectIDs: []string{userID}, DerivationKind: "direct", Confidence: 1,
		EncryptionSubjectID: userID, KeyRef: "vault://transit/memory/user", EmbeddingModelID: "embedding-v3",
		EmbeddingModelVersion: "2026-07-01", VectorDimensions: 1536, Tags: []string{"communication-style"},
		PolicyVersion: 3, IndexGeneration: 1, GuardrailSnapshotID: "guardrail@sha256:memory-safe",
		ExpiresAt:     &expiresAt,
		UpsertedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://memory/upserted", Hash: "memory-upserted-v2"},
	}
	result, err := store.RequestTools(ctx, executionpostgres.RequestToolsCommand{
		Claim: claim, ExpectedRunVersion: claim.RunVersion, StepID: "remember-user-preference", JoinPolicy: "all", QuorumCount: 1,
		PlanResultHash: "inline-memory-plan", Actor: json.RawMessage(`{"kind":"service","id":"agent-run-worker"}`), CorrelationID: correlationID,
		AttemptCompletedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://memory/attempt-completed", Hash: "attempt-completed"},
		GroupJoinedEvent:      executionpostgres.PayloadPointer{Ref: "encrypted://memory/group-joined", Hash: "group-joined"},
		RunResumeQueuedEvent:  executionpostgres.PayloadPointer{Ref: "encrypted://memory/resume-queued", Hash: "resume-queued"},
		ResumeCommand:         executionpostgres.PayloadPointer{Ref: "encrypted://memory/resume", Hash: "resume"}, ResumeQueueClass: "interactive", ResumeResourceClass: "llm", ResumePriority: 50, ResumeCostUnits: 2, ResumeMaxAttempts: 5,
		ToolRequests: []executionpostgres.ToolRequest{{ToolName: "memory_write", DescriptorSnapshotID: "memory_write@sha256:v2", NormalizedInputRef: "encrypted://memory/write-input", RequestHash: "memory-write-request", EffectClass: "idempotent_write", EffectKey: "memory:" + memoryID + ":v1", EffectScope: "tenant:" + tenantID + ":user:" + userID, ProviderID: "platform-memory", Required: true, ExecutionMode: "inline_platform", InlineInput: write, RequestedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://memory/tool-requested", Hash: "tool-requested"}, SucceededEvent: executionpostgres.PayloadPointer{Ref: "encrypted://memory/tool-succeeded", Hash: "tool-succeeded"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Resumed || result.RunVersion != 5 || result.ResumeCommandID == "" || len(result.ToolCalls) != 1 || result.ToolCalls[0].CommandID != "" || result.ToolCalls[0].JobID != "" {
		t.Fatalf("unexpected inline result: %#v", result)
	}
	var runStatus, toolStatus, mode, memoryStatus, projectionStatus string
	var runVersion, toolVersion, memoryVersion, currentRevision, revisionCount, sourceCount, subjectCount, projectionCount, executeCommands int
	var toolEventID, memoryCausationID string
	err = admin.QueryRow(ctx, `
		SELECT r.status,r.run_version,t.status,t.tool_call_version,t.execution_mode,t.result_event_id::text,
		       d.status,d.version,d.current_revision,p.status,
		       (SELECT count(*) FROM agent.memory_document_revisions WHERE tenant_id=$1 AND memory_id=$3),
		       (SELECT count(*) FROM agent.memory_revision_sources WHERE tenant_id=$1 AND memory_id=$3),
		       (SELECT count(*) FROM agent.memory_revision_subjects WHERE tenant_id=$1 AND memory_id=$3),
		       (SELECT count(*) FROM agent.memory_index_projections WHERE tenant_id=$1 AND memory_id=$3),
		       (SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND command_type='ExecuteToolCall'),
		       me.causation_id::text
		FROM agent.runs r
		JOIN agent.tool_calls t ON t.tenant_id=r.tenant_id AND t.run_id=r.id
		JOIN agent.memory_documents d ON d.tenant_id=r.tenant_id AND d.id=$3
		JOIN agent.memory_index_projections p ON p.tenant_id=d.tenant_id AND p.memory_id=d.id
		JOIN agent.events me ON me.tenant_id=d.tenant_id AND me.id=d.upserted_event_id
		WHERE r.tenant_id=$1 AND r.id=$2`, tenantID, runID, memoryID).Scan(&runStatus, &runVersion, &toolStatus, &toolVersion, &mode, &toolEventID, &memoryStatus, &memoryVersion, &currentRevision, &projectionStatus, &revisionCount, &sourceCount, &subjectCount, &projectionCount, &executeCommands, &memoryCausationID)
	if err != nil {
		t.Fatal(err)
	}
	if runStatus != "queued" || runVersion != 5 || toolStatus != "succeeded" || toolVersion != 2 || mode != "inline_platform" || memoryStatus != "active" || memoryVersion != 1 || currentRevision != 1 || projectionStatus != "pending" || revisionCount != 1 || sourceCount != 1 || subjectCount != 1 || projectionCount != 1 || executeCommands != 0 || memoryCausationID != toolEventID {
		t.Fatalf("run=%s/v%d tool=%s/v%d/%s memory=%s/v%d/r%d projection=%s counts=%d/%d/%d/%d execute=%d causation=%s tool_event=%s", runStatus, runVersion, toolStatus, toolVersion, mode, memoryStatus, memoryVersion, currentRevision, projectionStatus, revisionCount, sourceCount, subjectCount, projectionCount, executeCommands, memoryCausationID, toolEventID)
	}
}

type memoryEpochStub struct{ epoch string }

func (stub memoryEpochStub) CurrentStoreEpoch(context.Context) (string, error) {
	return stub.epoch, nil
}

func memoryPool(t *testing.T, ctx context.Context, name string) *pgxpool.Pool {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required", name)
	}
	pool, err := pgxpool.New(ctx, value)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}
