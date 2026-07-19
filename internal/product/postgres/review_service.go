package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/behavior"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/idempotency"
	idempotencypostgres "github.com/langshift/lites/internal/idempotency/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	productapi "github.com/langshift/lites/internal/product/api"
)

const reviewGenerateOperation = "reviews.generate.v2"
const evaluatorOutputContract = `Return exactly one JSON object and no markdown fences. Closed shape: {"schema_version":1,"verdict":"pass|needs_revision","summary":"string","deterministic_results":{"checks":[{"name":"string","status":"pass|fail|not_applicable","evidence":"string"}]},"dimensions":[{"id":"string","score":0..100,"rationale":"string","evidence_quotes":["string"]}],"strengths":["string"],"improvements":["string"],"capability_evidence":[{"capability_id":"string","level":"demonstrated|applied|reviewer_verified","statement":"string"}],"next_action":"string","uncertainty":"string"}. Every judgment must cite submission evidence; never invent execution results.`

type ReviewService struct {
	Pool                                             *pgxpool.Pool
	Runs                                             executionpostgres.RunStore
	Payloads                                         payload.Store
	IDKey, IdempotencyKeyPepper, RequestDigestPepper []byte
	BehaviorEnvironment                              string
	RunTimeout                                       time.Duration
	RunMaxSteps                                      int
	RunMaxCostMicrounits                             int64
	RunMaxAttempts                                   int
	IdempotencyTTL                                   time.Duration
	Now                                              func() time.Time
}

type reviewSnapshot struct {
	TaskID, MissionID, SubmissionKind, ContentRef, ContentHash, ContentManifestHash, UnderstandingRef, UnderstandingHash, UnderstandingManifestHash string
	TaskVersion                                                                                                                                     uint64
	SubmissionRevision                                                                                                                              int
	RubricSpec, RubricDimensions, RubricScoring                                                                                                     json.RawMessage
	RubricHash                                                                                                                                      string
	Binding                                                                                                                                         routeBehaviorBinding
	Content, Understanding                                                                                                                          []byte
}
type reviewIDs struct{ generation, conversation, message, run, startCommand string }
type reviewPrepared struct {
	conversationEvent, message, messageEvent, acceptedEvent, queuedEvent, startCommand executionpostgres.PayloadPointer
	messageHash                                                                        string
}

func (service ReviewService) Get(ctx context.Context, query productapi.ReviewGetQuery) (productapi.ReviewResource, error) {
	if !service.valid() || query.TenantID == "" || query.UserID == "" || query.ReviewID == "" {
		return productapi.ReviewResource{}, productapi.ErrValidation
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return productapi.ReviewResource{}, service.mapError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, query.TenantID); err != nil {
		return productapi.ReviewResource{}, service.mapError(err)
	}
	var out productapi.ReviewResource
	var ref, hash string
	err = tx.QueryRow(ctx, `SELECT id::text,version,submission_id::text,rubric_version_id::text,daily_task_id::text,submission_revision,status,review_payload_ref,review_payload_hash,evidence_id::text,reviewed_at FROM product.reviews WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, query.TenantID, query.UserID, query.ReviewID).Scan(&out.ID, &out.Version, &out.SubmissionID, &out.RubricVersionID, &out.DailyTaskID, &out.SubmissionRevision, &out.Status, &ref, &hash, &out.EvidenceID, &out.ReviewedAt)
	if err != nil {
		return out, service.mapError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return out, service.mapError(err)
	}
	encoded, err := service.Payloads.Get(ctx, payload.Descriptor{TenantID: query.TenantID, ObjectID: query.ReviewID, Class: "product-review", ContentType: "application/json"}, payload.Manifest{Ref: ref, Hash: hash})
	if err != nil || !validJSONObject(encoded) {
		return out, service.mapError(errors.Join(err, payload.ErrIntegrity))
	}
	out.Review = encoded
	return out, nil
}

func (service ReviewService) Generate(ctx context.Context, command productapi.GenerateReviewCommand) (productapi.ReviewGenerationResult, error) {
	if !service.valid() || !validMissionMetadata(command.CommandMetadata) || command.SubmissionID == "" || command.RubricVersionID == "" || command.ExpectedSubmissionRevision < 1 || command.ExpectedTaskVersion < 1 {
		return productapi.ReviewGenerationResult{}, productapi.ErrValidation
	}
	canonical, _ := json.Marshal(command)
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return productapi.ReviewGenerationResult{}, productapi.ErrDependencyUnavailable
	}
	recordID, err := ids.DeterministicUUID(service.IDKey, "product-idempotency:"+reviewGenerateOperation, command.TenantID+"\x00"+command.UserID+"\x00"+command.IdempotencyKey)
	if err != nil {
		return productapi.ReviewGenerationResult{}, productapi.ErrDependencyUnavailable
	}
	identifiers, err := service.identifiers(recordID)
	if err != nil {
		return productapi.ReviewGenerationResult{}, productapi.ErrDependencyUnavailable
	}
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "product-review-idempotency", ContentType: "application/json"}
	input := idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: reviewGenerateOperation}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	if response, found, e := executor.LoadCompleted(ctx, input); e != nil {
		return productapi.ReviewGenerationResult{}, service.mapError(e)
	} else if found {
		result, re := service.read(ctx, descriptor, response)
		result.Replayed = re == nil
		return result, service.mapError(re)
	}
	snapshot, manifestJSON, err := service.snapshot(ctx, command)
	if err != nil {
		return productapi.ReviewGenerationResult{}, service.mapError(err)
	}
	prepared, err := service.prepare(ctx, command, identifiers, snapshot, manifestJSON)
	if err != nil {
		return productapi.ReviewGenerationResult{}, service.mapError(err)
	}
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if _, e := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); e != nil {
			return idempotency.Response{}, e
		}
		var admissible bool
		if e := tx.QueryRow(ctx, `SELECT agent.lock_owned_submission_review($1,$2,$3,$4,$5,$6)`, command.TenantID, command.UserID, command.SubmissionID, command.ExpectedSubmissionRevision, command.RubricVersionID, command.ExpectedTaskVersion).Scan(&admissible); e != nil || !admissible {
			if e != nil {
				return idempotency.Response{}, e
			}
			return idempotency.Response{}, ErrRouteConflict
		}
		var currentSpec, currentDimensions, currentScoring json.RawMessage
		if e := tx.QueryRow(ctx, `SELECT spec,dimensions,scoring_rules FROM product.rubric_versions WHERE tenant_id=$1 AND id=$2 AND status='active'`, command.TenantID, command.RubricVersionID).Scan(&currentSpec, &currentDimensions, &currentScoring); e != nil {
			return idempotency.Response{}, e
		}
		currentRubricHash := rubricDigest(currentSpec, currentDimensions, currentScoring)
		if currentRubricHash != snapshot.RubricHash {
			return idempotency.Response{}, ErrRouteConflict
		}
		title := "Submission evaluator"
		conversation, e := service.Runs.CreateEvaluatorConversationInTx(ctx, tx, executionpostgres.CreateConversationCommand{ConversationID: identifiers.conversation, TenantID: command.TenantID, UserID: command.UserID, MissionID: snapshot.MissionID, SubmissionID: command.SubmissionID, SubmissionRevision: command.ExpectedSubmissionRevision, RubricVersionID: command.RubricVersionID, TaskVersion: command.ExpectedTaskVersion, Title: &title, Mode: "task", CorrelationID: command.RequestID, Actor: missionActor(command.UserID, command.SessionID), CreatedEvent: prepared.conversationEvent})
		if e != nil {
			return idempotency.Response{}, e
		}
		now := service.now()
		budget := json.RawMessage(fmt.Sprintf(`{"max_steps":%d,"max_cost_microunits":%d}`, service.RunMaxSteps, service.RunMaxCostMicrounits))
		accepted, e := service.Runs.AcceptMessageRunInTx(ctx, tx, executionpostgres.AcceptMessageRunCommand{Run: executionpostgres.AcceptRunCommand{RunID: identifiers.run, TenantID: command.TenantID, UserID: command.UserID, ConversationID: identifiers.conversation, CorrelationID: command.RequestID, DueAt: now.Add(service.RunTimeout), BehaviorProfile: behavior.Evaluator, BehaviorEnvironment: snapshot.Binding.Environment, ExpectedProfileSnapshotID: snapshot.Binding.SnapshotID, ExpectedBehaviorChannelID: snapshot.Binding.ChannelID, ExpectedBehaviorSequence: snapshot.Binding.Sequence, PinnedBehaviorBinding: true, BudgetSnapshot: budget, Actor: missionActor(command.UserID, command.SessionID), AcceptedEvent: prepared.acceptedEvent, QueuedEvent: prepared.queuedEvent, StartCommand: prepared.startCommand, QueueClass: "background", ResourceClass: "llm", Priority: 100, CostUnits: int64(service.RunMaxSteps), MaxAttempts: service.RunMaxAttempts}, MessageID: identifiers.message, ExpectedConversationVersion: conversation.Version, ExpectedConversationMode: "task", ExpectedConversationProfile: behavior.Evaluator, Message: prepared.message, ContentHash: prepared.messageHash, AppendedEvent: prepared.messageEvent, Actor: missionActor(command.UserID, command.SessionID)})
		if e != nil {
			return idempotency.Response{}, e
		}
		if accepted.RunID != identifiers.run || accepted.ProfileSnapshotID != snapshot.Binding.SnapshotID || accepted.BehaviorChannelID != snapshot.Binding.ChannelID || accepted.BehaviorChannelSequence != snapshot.Binding.Sequence {
			return idempotency.Response{}, ErrRouteConflict
		}
		manifestHash := sha256.Sum256(manifestJSON)
		_, e = tx.Exec(ctx, `INSERT INTO product.submission_review_generations(id,tenant_id,user_id,version,daily_task_id,task_version,submission_id,submission_revision,rubric_version_id,status,input_manifest,input_manifest_hash,behavior_profile,behavior_environment,behavior_channel_id,behavior_channel_sequence,behavior_snapshot_id,behavior_activated_at,review_run_id,created_at,updated_at) VALUES($1,$2,$3,1,$4,$5,$6,$7,$8,'generating',$9,$10,'evaluator',$11,$12,$13,$14,$15,$16,$17,$17)`, identifiers.generation, command.TenantID, command.UserID, snapshot.TaskID, command.ExpectedTaskVersion, command.SubmissionID, command.ExpectedSubmissionRevision, command.RubricVersionID, manifestJSON, hex.EncodeToString(manifestHash[:]), snapshot.Binding.Environment, snapshot.Binding.ChannelID, snapshot.Binding.Sequence, snapshot.Binding.SnapshotID, snapshot.Binding.ActivatedAt, identifiers.run, now)
		if e != nil {
			return idempotency.Response{}, e
		}
		result := productapi.ReviewGenerationResult{GenerationID: identifiers.generation, RunID: identifiers.run, Status: "queued", AcceptedAt: now}
		stored, e := service.putJSON(ctx, descriptor, result)
		if e != nil {
			return idempotency.Response{}, e
		}
		return idempotency.Response{Status: http.StatusAccepted, ContentType: "application/vnd.lites.review-generation.v2+json", PayloadRef: stored.Ref, Hash: stored.Hash, ResourceVersion: command.ExpectedTaskVersion}, nil
	})
	if err != nil {
		return productapi.ReviewGenerationResult{}, service.mapError(err)
	}
	result, err := service.read(ctx, descriptor, response)
	if err != nil {
		return result, service.mapError(err)
	}
	result.Replayed = replayed
	return result, nil
}

func (service ReviewService) snapshot(ctx context.Context, command productapi.GenerateReviewCommand) (reviewSnapshot, []byte, error) {
	tx, e := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if e != nil {
		return reviewSnapshot{}, nil, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, e = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); e != nil {
		return reviewSnapshot{}, nil, e
	}
	var s reviewSnapshot
	var rubricStatus string
	e = tx.QueryRow(ctx, `SELECT d.id::text,d.mission_id::text,d.version,s.submission_revision,s.submission_kind,s.payload_ref,s.content_hash,s.payload_manifest_hash,s.understanding_ref,s.understanding_hash,s.understanding_manifest_hash,rv.status,rv.spec,rv.dimensions,rv.scoring_rules FROM product.submissions s JOIN product.daily_tasks d ON d.tenant_id=s.tenant_id AND d.id=s.daily_task_id JOIN product.rubric_versions rv ON rv.tenant_id=s.tenant_id AND rv.id=$4 WHERE s.tenant_id=$1 AND s.user_id=$2 AND s.id=$3`, command.TenantID, command.UserID, command.SubmissionID, command.RubricVersionID).Scan(&s.TaskID, &s.MissionID, &s.TaskVersion, &s.SubmissionRevision, &s.SubmissionKind, &s.ContentRef, &s.ContentHash, &s.ContentManifestHash, &s.UnderstandingRef, &s.UnderstandingHash, &s.UnderstandingManifestHash, &rubricStatus, &s.RubricSpec, &s.RubricDimensions, &s.RubricScoring)
	if e != nil {
		return s, nil, e
	}
	if s.TaskVersion != command.ExpectedTaskVersion || s.SubmissionRevision != command.ExpectedSubmissionRevision || rubricStatus != "active" {
		return s, nil, ErrRouteConflict
	}
	s.RubricHash = rubricDigest(s.RubricSpec, s.RubricDimensions, s.RubricScoring)
	s.Binding.Profile = string(behavior.Evaluator)
	s.Binding.Environment = service.BehaviorEnvironment
	e = tx.QueryRow(ctx, `SELECT channel_id::text,sequence,snapshot_id,activated_at FROM agent.behavior_channel_deployments WHERE tenant_id=$1 AND profile_name=$2 AND environment=$3 ORDER BY sequence DESC LIMIT 1`, command.TenantID, behavior.Evaluator, service.BehaviorEnvironment).Scan(&s.Binding.ChannelID, &s.Binding.Sequence, &s.Binding.SnapshotID, &s.Binding.ActivatedAt)
	if e != nil {
		return s, nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return s, nil, e
	}
	s.Content, e = service.Payloads.Get(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: command.SubmissionID, Class: "product-submission-content", ContentType: "text/plain; charset=utf-8"}, payload.Manifest{Ref: s.ContentRef, Hash: s.ContentManifestHash})
	if e != nil {
		return s, nil, e
	}
	s.Understanding, e = service.Payloads.Get(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: command.SubmissionID, Class: "product-submission-understanding", ContentType: "text/plain; charset=utf-8"}, payload.Manifest{Ref: s.UnderstandingRef, Hash: s.UnderstandingManifestHash})
	if e != nil {
		return s, nil, e
	}
	manifest := map[string]any{"schema_version": 1, "daily_task_id": s.TaskID, "task_version": s.TaskVersion, "submission_id": command.SubmissionID, "submission_revision": s.SubmissionRevision, "submission_kind": s.SubmissionKind, "content_hash": s.ContentHash, "understanding_hash": s.UnderstandingHash, "rubric_version_id": command.RubricVersionID, "rubric_hash": s.RubricHash, "agent_profile": s.Binding}
	encoded, e := json.Marshal(manifest)
	return s, encoded, e
}

func rubricDigest(parts ...json.RawMessage) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write(part)
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (service ReviewService) prepare(ctx context.Context, command productapi.GenerateReviewCommand, id reviewIDs, s reviewSnapshot, manifest []byte) (reviewPrepared, error) {
	prompt := map[string]any{"schema_version": 1, "role": "user", "content": []map[string]any{{"type": "text", "text": "Evaluate this immutable submission against the exact rubric. " + evaluatorOutputContract + "\nINPUT_MANIFEST=" + string(manifest) + "\nRUBRIC_SPEC=" + string(s.RubricSpec) + "\nRUBRIC_DIMENSIONS=" + string(s.RubricDimensions) + "\nRUBRIC_SCORING=" + string(s.RubricScoring) + "\nSUBMISSION=" + string(s.Content) + "\nUSER_UNDERSTANDING=" + string(s.Understanding)}}}
	messageJSON, e := json.Marshal(prompt)
	if e != nil {
		return reviewPrepared{}, e
	}
	digest := sha256.Sum256(messageJSON)
	put := func(objectID, class string, v any) (executionpostgres.PayloadPointer, error) {
		b, er := json.Marshal(v)
		if er != nil {
			return executionpostgres.PayloadPointer{}, er
		}
		m, er := service.Payloads.Put(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: objectID, Class: class, ContentType: "application/json"}, b)
		return executionpostgres.PayloadPointer{Ref: m.Ref, Hash: m.Hash}, er
	}
	putRaw := func(objectID, class string, b []byte) (executionpostgres.PayloadPointer, error) {
		m, er := service.Payloads.Put(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: objectID, Class: class, ContentType: "application/json"}, b)
		return executionpostgres.PayloadPointer{Ref: m.Ref, Hash: m.Hash}, er
	}
	var p reviewPrepared
	if p.conversationEvent, e = put(id.conversation, "event-payload", map[string]any{"subject_id": id.conversation, "subject_version": 1, "mission_id": s.MissionID, "submission_id": command.SubmissionID, "purpose": "evaluator"}); e != nil {
		return p, e
	}
	if p.message, e = putRaw(id.message, "run-message", messageJSON); e != nil {
		return p, e
	}
	p.messageHash = hex.EncodeToString(digest[:])
	if p.messageEvent, e = put(id.message, "event-payload", map[string]any{"subject_id": id.conversation, "subject_version": 2, "message_id": id.message, "generation_id": id.generation}); e != nil {
		return p, e
	}
	binding := map[string]any{"profile": behavior.Evaluator, "environment": s.Binding.Environment, "snapshot_id": s.Binding.SnapshotID, "channel_id": s.Binding.ChannelID, "sequence": s.Binding.Sequence}
	if p.acceptedEvent, e = put(id.run, "event-payload", map[string]any{"subject_id": id.run, "subject_version": 1, "run_id": id.run, "generation_id": id.generation, "behavior": binding}); e != nil {
		return p, e
	}
	if p.queuedEvent, e = put(id.startCommand, "event-payload", map[string]any{"subject_id": id.run, "subject_version": 2, "run_id": id.run, "pending_command_id": id.startCommand}); e != nil {
		return p, e
	}
	p.startCommand, e = put(id.startCommand, "agent-run-command", map[string]any{"schema_version": 1, "run_id": id.run, "correlation_id": command.RequestID})
	return p, e
}

func (service ReviewService) identifiers(seed string) (reviewIDs, error) {
	domains := []string{"review-generation", "review-conversation", "review-message", "review-run"}
	values := make([]string, len(domains))
	for i, d := range domains {
		v, e := ids.DeterministicUUID(service.IDKey, d, seed)
		if e != nil {
			return reviewIDs{}, e
		}
		values[i] = v
	}
	startCommand, err := executionpostgres.RunStartCommandID(service.Runs.IDKey, values[3])
	if err != nil {
		return reviewIDs{}, err
	}
	return reviewIDs{values[0], values[1], values[2], values[3], startCommand}, nil
}
func (service ReviewService) putJSON(ctx context.Context, d payload.Descriptor, v any) (payload.Manifest, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return payload.Manifest{}, e
	}
	return service.Payloads.Put(ctx, d, b)
}
func (service ReviewService) read(ctx context.Context, d payload.Descriptor, r idempotency.Response) (productapi.ReviewGenerationResult, error) {
	b, e := service.Payloads.Get(ctx, d, payload.Manifest{Ref: r.PayloadRef, Hash: r.Hash})
	if e != nil {
		return productapi.ReviewGenerationResult{}, e
	}
	var out productapi.ReviewGenerationResult
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(&out) != nil || !errors.Is(dec.Decode(&struct{}{}), io.EOF) {
		return out, payload.ErrIntegrity
	}
	return out, nil
}
func (service ReviewService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}
func (service ReviewService) valid() bool {
	return service.Pool != nil && service.Payloads != nil && service.Runs.Pool != nil && len(service.IDKey) >= 32 && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && service.BehaviorEnvironment != "" && service.RunTimeout > 0 && service.RunMaxSteps > 0 && service.RunMaxCostMicrounits > 0 && service.RunMaxAttempts > 0 && service.IdempotencyTTL > 0
}
func (service ReviewService) mapError(e error) error {
	switch {
	case e == nil:
		return nil
	case errors.Is(e, pgx.ErrNoRows):
		return productapi.ErrResourceNotFound
	case errors.Is(e, idempotency.ErrKeyConflict), errors.Is(e, idempotency.ErrInProgress):
		return productapi.ErrIdempotencyConflict
	case errors.Is(e, ErrRouteConflict), errors.Is(e, executionpostgres.ErrRunConflict):
		return productapi.ErrStateConflict
	default:
		return errors.Join(productapi.ErrDependencyUnavailable, e)
	}
}

var _ productapi.ReviewService = ReviewService{}
