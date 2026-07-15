package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
)

func TestPortfolioManifestIsCanonicalAndRejectsSubstitutionShapes(t *testing.T) {
	command := validPortfolioCommand(time.Now().UTC())
	command.Artifacts = []PortfolioArtifactBinding{
		{RevisionID: "revision-b", ArtifactID: "artifact-b", Revision: 2, ContentHash: "hash-b"},
		{RevisionID: "revision-a", ArtifactID: "artifact-a", Revision: 1, ContentHash: "hash-a"},
	}
	command.Evidence = []EvidenceBinding{
		{EvidenceID: "evidence-b", Version: 2, ContentHash: "evidence-hash-b"},
		{EvidenceID: "evidence-a", Version: 1, ContentHash: "evidence-hash-a"},
	}
	manifest, manifestHash, artifacts, evidence, err := canonicalPortfolioManifest(command)
	if err != nil || manifestHash == "" {
		t.Fatalf("manifest hash=%q err=%v", manifestHash, err)
	}
	if artifacts[0].ArtifactID != "artifact-a" || evidence[0].EvidenceID != "evidence-a" {
		t.Fatalf("manifest bindings are not canonical: %#v %#v", artifacts, evidence)
	}
	if command.Artifacts[0].ArtifactID != "artifact-b" || command.Evidence[0].EvidenceID != "evidence-b" {
		t.Fatal("canonicalization mutated caller input")
	}
	second := command
	second.Artifacts = []PortfolioArtifactBinding{command.Artifacts[1], command.Artifacts[0]}
	second.Evidence = []EvidenceBinding{command.Evidence[1], command.Evidence[0]}
	secondManifest, secondHash, _, _, err := canonicalPortfolioManifest(second)
	if err != nil || secondHash != manifestHash || !bytes.Equal(secondManifest, manifest) {
		t.Fatalf("permutation changed manifest: %s/%s err=%v", manifestHash, secondHash, err)
	}
	duplicateArtifact := command
	duplicateArtifact.Artifacts = append(duplicateArtifact.Artifacts, PortfolioArtifactBinding{RevisionID: "revision-a-older", ArtifactID: "artifact-a", Revision: 3, ContentHash: "other"})
	if _, _, _, _, err = canonicalPortfolioManifest(duplicateArtifact); !errors.Is(err, ErrPortfolioBinding) {
		t.Fatalf("duplicate artifact error=%v", err)
	}
	duplicateEvidence := command
	duplicateEvidence.Evidence = append(duplicateEvidence.Evidence, EvidenceBinding{EvidenceID: "evidence-a", Version: 9, ContentHash: "other"})
	if _, _, _, _, err = canonicalPortfolioManifest(duplicateEvidence); !errors.Is(err, ErrPortfolioBinding) {
		t.Fatalf("duplicate evidence error=%v", err)
	}
}

func TestPortfolioExportInputFailsClosedBeforeDatabaseAccess(t *testing.T) {
	now := time.Now().UTC()
	valid := validPortfolioCommand(now)
	if !validPortfolioExport(valid) {
		t.Fatal("valid portfolio export rejected")
	}
	for name, mutate := range map[string]func(*RequestPortfolioExportCommand){
		"unknown format":         func(command *RequestPortfolioExportCommand) { command.Format = "docx" },
		"missing workspace hash": func(command *RequestPortfolioExportCommand) { command.Workspace.ManifestHash = "" },
		"unbound artifact":       func(command *RequestPortfolioExportCommand) { command.Artifacts[0].RevisionID = "" },
		"unbound evidence":       func(command *RequestPortfolioExportCommand) { command.Evidence[0].Version = 0 },
		"foreground queue":       func(command *RequestPortfolioExportCommand) { command.Run.QueueClass = "interactive" },
		"wrong agent profile":    func(command *RequestPortfolioExportCommand) { command.Run.BehaviorProfile = "coach" },
		"cross-tenant run":       func(command *RequestPortfolioExportCommand) { command.Run.TenantID = "other" },
		"scalar actor":           func(command *RequestPortfolioExportCommand) { command.Actor = json.RawMessage(`1`) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.Artifacts = append([]PortfolioArtifactBinding(nil), valid.Artifacts...)
			candidate.Evidence = append([]EvidenceBinding(nil), valid.Evidence...)
			mutate(&candidate)
			if validPortfolioExport(candidate) {
				t.Fatal("invalid portfolio export accepted")
			}
		})
	}
	if _, err := (PortfolioExportStore{}).Request(t.Context(), valid); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("invalid store error=%v", err)
	}
	store := PortfolioExportStore{IDKey: bytes.Repeat([]byte{0x77}, 32)}
	first, err := store.portfolioEventIDs(valid.ExportID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.portfolioEventIDs(valid.ExportID)
	if err != nil || first != second || first.event == first.outbox || first.event == first.publish || first.outbox == first.publish {
		t.Fatalf("unstable or colliding event IDs: %#v %#v err=%v", first, second, err)
	}
}

func validPortfolioCommand(now time.Time) RequestPortfolioExportCommand {
	return RequestPortfolioExportCommand{
		ExportID: "export", RequestID: "request", TenantID: "tenant", UserID: "user", ProjectID: "project", ProjectVersion: 4, CorrelationID: "correlation",
		Workspace: WorkspaceExportBinding{BindingID: "binding", Version: 3, Revision: "git:head", ManifestHash: "workspace-hash"},
		Format:    "pdf", Artifacts: []PortfolioArtifactBinding{{RevisionID: "revision", ArtifactID: "artifact", Revision: 2, ContentHash: "artifact-hash", ObjectVersion: "object-v2", WorkspaceRevision: "git:head", MediaType: "text/markdown", ByteSize: 2048, ScanResultHash: "scan-hash", EvidenceManifestHash: "evidence-manifest-hash"}}, Evidence: []EvidenceBinding{{EvidenceID: "evidence", Version: 2, ContentHash: "evidence-hash"}},
		Actor: json.RawMessage(`{"kind":"user"}`), RequestedEvent: PayloadPointer{Ref: "encrypted://portfolio/requested", Hash: "portfolio-requested"},
		Run: executionpostgres.AcceptRunCommand{RunID: "run", TenantID: "tenant", UserID: "user", ConversationID: "conversation", CorrelationID: "correlation", DueAt: now.Add(time.Hour), BehaviorProfile: "artifact_builder", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_steps":16}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://run/accepted", Hash: "run-accepted"}, QueuedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://run/queued", Hash: "run-queued"}, StartCommand: executionpostgres.PayloadPointer{Ref: "encrypted://run/start", Hash: "run-start"}, QueueClass: "background", ResourceClass: "llm", Priority: 50, CostUnits: 8, MaxAttempts: 5},
	}
}

func TestPortfolioLifecycleInputAndResultContractsFailClosed(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	claim := executionpostgres.RunClaim{RunID: "run", TenantID: "tenant", UserID: "user", StoreEpoch: "epoch", RunVersion: 3, CommandID: "command", ConsumerName: "portfolio-builder", RequestHash: "request-hash", JobID: "job", InboxID: "inbox", AttemptID: "attempt", Fence: 1, LeaseToken: "lease-token", LeaseExpiresAt: now.Add(time.Minute)}
	start := StartPortfolioBuildCommand{ExportID: "export", TenantID: "tenant", UserID: "user", ExpectedExportVersion: 1, Claim: claim, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: "correlation", BuildStartedEvent: PayloadPointer{Ref: "encrypted://portfolio/started", Hash: "portfolio-started"}}
	if !validStartPortfolioBuild(start) {
		t.Fatal("valid portfolio build start rejected")
	}
	invalidStart := start
	invalidStart.Claim.InboxID = ""
	if validStartPortfolioBuild(invalidStart) {
		t.Fatal("start without full durable execution ownership accepted")
	}
	runCompletion := executionpostgres.CompleteRunCommand{Claim: claim, ExpectedRunVersion: claim.RunVersion, TargetState: statemachine.RunSucceeded, ResultHash: "content-hash", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: "correlation", RunEvent: executionpostgres.PayloadPointer{Ref: "encrypted://run/succeeded", Hash: "run-succeeded"}, AttemptCompletedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://attempt/completed", Hash: "attempt-completed"}}
	complete := CompletePortfolioExportCommand{ExportID: "export", TenantID: "tenant", UserID: "user", ExpectedExportVersion: 2, ObjectRef: "s3://portfolio/export", ObjectVersion: "object-v1", ContentHash: "content-hash", MediaType: "application/pdf", ScanResultHash: "scan-hash", CorrelationID: "correlation", ByteSize: 1024, ExpiresAt: now.Add(24 * time.Hour), Actor: json.RawMessage(`{"kind":"service"}`), CompletedEvent: PayloadPointer{Ref: "encrypted://portfolio/completed", Hash: "portfolio-completed"}, RunCompletion: runCompletion}
	if !validCompletePortfolioExport(complete) {
		t.Fatal("valid portfolio completion rejected")
	}
	wrongResult := complete
	wrongResult.RunCompletion.ResultHash = "substituted"
	if validCompletePortfolioExport(wrongResult) {
		t.Fatal("result hash not bound to export content")
	}
	wrongTerminal := complete
	wrongTerminal.RunCompletion.TargetState = statemachine.RunFailed
	if validCompletePortfolioExport(wrongTerminal) {
		t.Fatal("failed Run accepted as completed export")
	}
	failure := FailPortfolioExportCommand{ExportID: "export", TenantID: "tenant", UserID: "user", ExpectedExportVersion: 2, FailureCode: "renderer_failed", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: "correlation", FailedEvent: PayloadPointer{Ref: "encrypted://portfolio/failed", Hash: "portfolio-failed"}, RunCompletion: executionpostgres.CompleteRunCommand{Claim: claim, ExpectedRunVersion: claim.RunVersion, TargetState: statemachine.RunFailed, ResultHash: "failure-hash", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: "correlation", RunEvent: executionpostgres.PayloadPointer{Ref: "encrypted://run/failed", Hash: "run-failed"}, AttemptCompletedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://attempt/failed", Hash: "attempt-failed"}}}
	if !validFailPortfolioExport(failure) || validFailureCode("Renderer Failed") || !validFailureCode("renderer.timeout-1") {
		t.Fatal("failure code or terminal contract is invalid")
	}
	expire := ExpirePortfolioExportCommand{ExportID: "export", TenantID: "tenant", UserID: "user", ExpectedExportVersion: 3, ExpiredAt: now, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: "correlation", ExpiredEvent: PayloadPointer{Ref: "encrypted://portfolio/expired", Hash: "portfolio-expired"}}
	if !validExpirePortfolioExport(expire) {
		t.Fatal("valid portfolio expiry rejected")
	}
	if _, err := (PortfolioExportStore{}).ListExpirableTenants(t.Context(), PortfolioExpiryTenantScan{Limit: 100, ShardCount: 1, Now: now}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("invalid expiry scanner error=%v", err)
	}
	if _, err := (PortfolioExportStore{}).ListRecoverableTenantIDs(t.Context(), PortfolioRecoveryTenantScan{Limit: 100, ShardCount: 1, StaleAfter: time.Minute}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("invalid recovery tenant scanner error=%v", err)
	}
	if _, err := (PortfolioExportStore{}).ListRecoveryCandidates(t.Context(), PortfolioRecoveryCandidateScan{TenantID: "tenant", Limit: 100, StaleAfter: time.Minute}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("invalid recovery candidate scanner error=%v", err)
	}
	for _, disposition := range []string{PortfolioRecoveryQueuedRedelivery, PortfolioRecoveryExpiredRedelivery, PortfolioRecoveryProjectionLag, PortfolioRecoveryInconsistent} {
		if disposition == "" {
			t.Fatal("empty portfolio recovery disposition")
		}
	}
	for format, mediaType := range map[string]string{"html": "text/html", "pdf": "application/pdf", "zip": "application/zip"} {
		if !mediaTypeMatches(format, mediaType) || mediaTypeMatches(format, "application/octet-stream") {
			t.Fatalf("format/media contract failed for %s", format)
		}
	}
	if _, err := (PortfolioExportStore{}).Complete(t.Context(), complete); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("invalid completion store error=%v", err)
	}
}
