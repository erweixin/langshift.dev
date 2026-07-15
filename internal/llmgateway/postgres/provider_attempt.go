package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
)

type BYOKBinding struct {
	CredentialID  string
	Version       uint64
	SecretVersion string
}

type PrepareProviderAttemptCommand struct {
	AttemptID, ProviderAttemptID, LLMAttemptID string
	UsageReservationID                         string
	TenantID, UserID, AttemptKey               string
	Ordinal                                    int
	Candidate                                  ModelCandidate
	RequestHash, ContextManifestHash           string
	FallbackFromID, PrepareToken               string
	PrepareTokenExpiresAt                      time.Time
	BYOK                                       *BYOKBinding
	CorrelationID                              string
	Actor                                      json.RawMessage
	PreparedEvent                              PayloadPointer
}

type PreparedProviderAttempt struct {
	AttemptID, ProviderAttemptID, Status, RequestHash string
	Version                                           uint64
	Ordinal                                           int
	PrepareTokenExpiresAt                             time.Time
	Replayed                                          bool
}

func (store Store) PrepareProviderAttempt(ctx context.Context, command PrepareProviderAttemptCommand) (PreparedProviderAttempt, error) {
	if !store.valid() || !validPrepare(command) {
		return PreparedProviderAttempt{}, ErrInvalidCommand
	}
	if err := store.requireEpoch(ctx); err != nil {
		return PreparedProviderAttempt{}, err
	}
	digest, err := store.prepareTokens().Digest(command.PrepareToken)
	if err != nil {
		return PreparedProviderAttempt{}, ErrInvalidCommand
	}
	command.PrepareTokenExpiresAt = command.PrepareTokenExpiresAt.UTC().Truncate(time.Microsecond)
	now := store.now()
	if !command.PrepareTokenExpiresAt.After(now) {
		return PreparedProviderAttempt{}, ErrInvalidCommand
	}
	identifiers, err := store.eventIDs("provider-attempt-prepared", command.AttemptID, 1)
	if err != nil {
		return PreparedProviderAttempt{}, ErrConfiguration
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return PreparedProviderAttempt{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return PreparedProviderAttempt{}, err
	}
	var replay PreparedProviderAttempt
	var replayLLM, replayUser, replayReservation, replayProvider, replayModel, replayModelVersion, replayContext, replayPricing, replayHost, replayEvent string
	var replayFallback, replayCredential, replaySecret *string
	var replayCredentialVersion *uint64
	var replayBYOK bool
	var replayDigest []byte
	err = tx.QueryRow(ctx, `SELECT id::text,provider_attempt_id,llm_attempt_id::text,user_id::text,usage_reservation_id::text,ordinal,provider_id,model_id,model_version,request_hash,context_manifest_hash,pricing_version,fallback_from_id::text,byok,byok_credential_id::text,byok_credential_version,bound_host,secret_version,prepare_token_hash,prepare_token_expires_at,prepared_event_id::text,status,version FROM agent.llm_provider_attempts WHERE tenant_id=$1 AND (id=$2 OR provider_attempt_id=$3 OR (llm_attempt_id=$4 AND ordinal=$5)) FOR UPDATE`, command.TenantID, command.AttemptID, command.ProviderAttemptID, command.LLMAttemptID, command.Ordinal).Scan(&replay.AttemptID, &replay.ProviderAttemptID, &replayLLM, &replayUser, &replayReservation, &replay.Ordinal, &replayProvider, &replayModel, &replayModelVersion, &replay.RequestHash, &replayContext, &replayPricing, &replayFallback, &replayBYOK, &replayCredential, &replayCredentialVersion, &replayHost, &replaySecret, &replayDigest, &replay.PrepareTokenExpiresAt, &replayEvent, &replay.Status, &replay.Version)
	if err == nil {
		credentialID, credentialVersion, secretVersion := byokValues(command.BYOK)
		if replay.AttemptID != command.AttemptID || replay.ProviderAttemptID != command.ProviderAttemptID || replayLLM != command.LLMAttemptID || replayUser != command.UserID || replayReservation != command.UsageReservationID || replay.Ordinal != command.Ordinal || replayProvider != command.Candidate.ProviderID || replayModel != command.Candidate.ModelID || replayModelVersion != command.Candidate.ModelVersion || replay.RequestHash != command.RequestHash || replayContext != command.ContextManifestHash || replayPricing != command.Candidate.PricingVersion || !equalOptional(replayFallback, command.FallbackFromID) || replayBYOK != (command.BYOK != nil) || !equalOptional(replayCredential, credentialID) || !equalOptionalUint(replayCredentialVersion, credentialVersion) || replayHost != command.Candidate.BoundHost || !equalOptional(replaySecret, secretVersion) || !bytes.Equal(replayDigest, digest[:]) || !replay.PrepareTokenExpiresAt.Equal(command.PrepareTokenExpiresAt) || replayEvent != identifiers.Event {
			return PreparedProviderAttempt{}, ErrProviderConflict
		}
		replay.Replayed = true
		if err = tx.Commit(ctx); err != nil {
			return PreparedProviderAttempt{}, err
		}
		return replay, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return PreparedProviderAttempt{}, err
	}
	var manifestJSON json.RawMessage
	var llmUser, attemptKey, manifestHash, llmStatus, llmRunAttempt, runStatus, activeRunAttempt string
	var llmRunFence, activeRunFence uint64
	var runCancelRequested *time.Time
	var runLeaseExpires time.Time
	err = tx.QueryRow(ctx, `SELECT l.user_id::text,l.attempt_key,l.context_manifest,l.context_manifest_hash,l.status,l.run_attempt_id::text,l.run_fence,
		r.status,r.active_attempt_id::text,r.current_fence,r.cancel_requested_at,r.lease_expires_at
		FROM agent.llm_attempts l JOIN agent.runs r ON r.tenant_id=l.tenant_id AND r.id=l.run_id
		WHERE l.tenant_id=$1 AND l.id=$2 FOR UPDATE OF l,r`, command.TenantID, command.LLMAttemptID).
		Scan(&llmUser, &attemptKey, &manifestJSON, &manifestHash, &llmStatus, &llmRunAttempt, &llmRunFence, &runStatus, &activeRunAttempt, &activeRunFence, &runCancelRequested, &runLeaseExpires)
	if err != nil || runStatus != "executing" || activeRunAttempt != llmRunAttempt || activeRunFence != llmRunFence || runCancelRequested != nil || !runLeaseExpires.After(now) {
		return PreparedProviderAttempt{}, ErrRunFence
	}
	var manifest ContextManifest
	if llmUser != command.UserID || attemptKey != command.AttemptKey || manifestHash != command.ContextManifestHash || llmStatus != "running" || json.Unmarshal(manifestJSON, &manifest) != nil || !containsCandidate(manifest, command.Candidate) {
		return PreparedProviderAttempt{}, ErrModelNotCandidate
	}
	if command.Ordinal == 1 && command.FallbackFromID != "" || command.Ordinal > 1 && command.FallbackFromID == "" {
		return PreparedProviderAttempt{}, ErrInvalidCommand
	}
	if command.Ordinal > 1 {
		var previousOrdinal int
		var previousStatus string
		err = tx.QueryRow(ctx, `SELECT ordinal,status FROM agent.llm_provider_attempts WHERE tenant_id=$1 AND id=$2 AND llm_attempt_id=$3 FOR SHARE`, command.TenantID, command.FallbackFromID, command.LLMAttemptID).Scan(&previousOrdinal, &previousStatus)
		if err != nil || previousOrdinal != command.Ordinal-1 || previousStatus == "prepared" || previousStatus == "dispatching" {
			return PreparedProviderAttempt{}, ErrProviderConflict
		}
	}
	var reservationUser, subjectKind, subjectID, reservationStatus string
	var subjectVersion uint64
	var reservationExpires time.Time
	err = tx.QueryRow(ctx, `SELECT user_id::text,subject_kind,subject_id::text,subject_version,status,expires_at FROM contracts.usage_reservations WHERE tenant_id=$1 AND id=$2 FOR SHARE`, command.TenantID, command.UsageReservationID).Scan(&reservationUser, &subjectKind, &subjectID, &subjectVersion, &reservationStatus, &reservationExpires)
	if err != nil || reservationUser != command.UserID || subjectKind != "provider_attempt" || subjectID != command.AttemptID || subjectVersion != 1 || reservationStatus != "reserved" || !reservationExpires.After(now) {
		return PreparedProviderAttempt{}, ErrProviderConflict
	}
	credentialID, credentialVersion, secretVersion := byokValues(command.BYOK)
	if command.BYOK != nil {
		var active bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product.byok_credential_versions WHERE tenant_id=$1 AND credential_id=$2 AND user_id=$3 AND provider_id=$4 AND version=$5 AND bound_host=$6 AND secret_version=$7 AND status='active')`, command.TenantID, command.BYOK.CredentialID, command.UserID, command.Candidate.ProviderID, command.BYOK.Version, command.Candidate.BoundHost, command.BYOK.SecretVersion).Scan(&active)
		if err != nil || !active {
			return PreparedProviderAttempt{}, ErrProviderConflict
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO agent.llm_provider_attempts(id,tenant_id,user_id,llm_attempt_id,provider_attempt_id,provider_id,model_id,model_version,provider_request_id,fallback_from_id,status,input_tokens,output_tokens,cost_microunits,started_at,finished_at,ordinal,request_hash,context_manifest_hash,pricing_version,usage_status,byok,byok_credential_id,byok_credential_version,bound_host,secret_version,prepare_token_hash,prepare_token_expires_at,prepared_event_id,usage_reservation_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,NULL,NULLIF($9,'')::uuid,'prepared',0,0,0,$10,NULL,$11,$12,$13,$14,'pending',$15,NULLIF($16,'')::uuid,$17,$18,NULLIF($19,''),$20,$21,$22,$23,$10,$10)`, command.AttemptID, command.TenantID, command.UserID, command.LLMAttemptID, command.ProviderAttemptID, command.Candidate.ProviderID, command.Candidate.ModelID, command.Candidate.ModelVersion, command.FallbackFromID, now, command.Ordinal, command.RequestHash, command.ContextManifestHash, command.Candidate.PricingVersion, command.BYOK != nil, credentialID, credentialVersion, command.Candidate.BoundHost, secretVersion, digest[:], command.PrepareTokenExpiresAt, identifiers.Event, command.UsageReservationID)
	if err != nil {
		return PreparedProviderAttempt{}, ErrProviderConflict
	}
	event := publishEvent(identifiers, eventpostgres.Event{TenantID: command.TenantID, UserID: command.UserID, EventType: "ProviderAttemptPrepared", SchemaVersion: 1, AggregateKind: "provider_attempt", AggregateID: command.AttemptID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID}, command.PreparedEvent)
	if _, err = store.Appender.Append(ctx, tx, event); err != nil {
		return PreparedProviderAttempt{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PreparedProviderAttempt{}, err
	}
	return PreparedProviderAttempt{AttemptID: command.AttemptID, ProviderAttemptID: command.ProviderAttemptID, Status: "prepared", RequestHash: command.RequestHash, Version: 1, Ordinal: command.Ordinal, PrepareTokenExpiresAt: command.PrepareTokenExpiresAt}, nil
}

type AuthorizeDispatchCommand struct {
	AttemptID, TenantID, RequestHash, PrepareToken, CompletionToken string
	CompletionDeadline                                              time.Time
	CorrelationID                                                   string
	Actor                                                           json.RawMessage
	DispatchEvent                                                   PayloadPointer
}

type DispatchAuthorization struct {
	AttemptID, LLMAttemptID, ProviderAttemptID string
	Candidate                                  ModelCandidate
	RequestHash                                string
	Ordinal                                    int
	DispatchFence                              uint64
	BYOK                                       *BYOKBinding
	CompletionDeadline                         time.Time
}

func (store Store) AuthorizeDispatch(ctx context.Context, command AuthorizeDispatchCommand) (DispatchAuthorization, error) {
	if !store.valid() || command.AttemptID == "" || command.TenantID == "" || command.RequestHash == "" || command.PrepareToken == "" || command.CompletionToken == "" || command.CorrelationID == "" || !validActor(command.Actor) || !validPointer(command.DispatchEvent) {
		return DispatchAuthorization{}, ErrInvalidCommand
	}
	if err := store.requireEpoch(ctx); err != nil {
		return DispatchAuthorization{}, err
	}
	prepareDigest, err := store.prepareTokens().Digest(command.PrepareToken)
	if err != nil {
		return DispatchAuthorization{}, ErrDispatchToken
	}
	completionDigest, err := store.completionTokens().Digest(command.CompletionToken)
	if err != nil {
		return DispatchAuthorization{}, ErrInvalidCommand
	}
	now := store.now()
	command.CompletionDeadline = command.CompletionDeadline.UTC().Truncate(time.Microsecond)
	if !command.CompletionDeadline.After(now) {
		return DispatchAuthorization{}, ErrInvalidCommand
	}
	identifiers, err := store.eventIDs("provider-attempt-dispatch-authorized", command.AttemptID, 2)
	if err != nil {
		return DispatchAuthorization{}, ErrConfiguration
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return DispatchAuthorization{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return DispatchAuthorization{}, err
	}
	var result DispatchAuthorization
	var llmStatus, llmRunAttempt, runStatus, activeRunAttempt string
	var llmRunFence, activeRunFence uint64
	var runCancelRequested *time.Time
	var runLeaseExpires time.Time
	err = tx.QueryRow(ctx, `SELECT l.id::text,l.status,l.run_attempt_id::text,l.run_fence,
		r.status,r.active_attempt_id::text,r.current_fence,r.cancel_requested_at,r.lease_expires_at
		FROM agent.llm_attempts l JOIN agent.runs r ON r.tenant_id=l.tenant_id AND r.id=l.run_id
		WHERE l.tenant_id=$1 AND l.id=(SELECT p.llm_attempt_id FROM agent.llm_provider_attempts p WHERE p.tenant_id=$1 AND p.id=$2)
		FOR UPDATE OF l,r`, command.TenantID, command.AttemptID).
		Scan(&result.LLMAttemptID, &llmStatus, &llmRunAttempt, &llmRunFence, &runStatus, &activeRunAttempt, &activeRunFence, &runCancelRequested, &runLeaseExpires)
	if err != nil || llmStatus != "running" || runStatus != "executing" || activeRunAttempt != llmRunAttempt || activeRunFence != llmRunFence || runCancelRequested != nil || !runLeaseExpires.After(now) {
		return DispatchAuthorization{}, ErrRunFence
	}
	var status, host, pricing string
	var version uint64
	var storedDigest []byte
	var expiresAt time.Time
	var byok bool
	var credentialID, secretVersion *string
	var credentialVersion *uint64
	var lockedLLMAttempt string
	err = tx.QueryRow(ctx, `SELECT llm_attempt_id::text,provider_attempt_id,ordinal,provider_id,model_id,model_version,bound_host,pricing_version,request_hash,status,version,prepare_token_hash,prepare_token_expires_at,byok,byok_credential_id::text,byok_credential_version,secret_version FROM agent.llm_provider_attempts WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, command.AttemptID).Scan(&lockedLLMAttempt, &result.ProviderAttemptID, &result.Ordinal, &result.Candidate.ProviderID, &result.Candidate.ModelID, &result.Candidate.ModelVersion, &host, &pricing, &result.RequestHash, &status, &version, &storedDigest, &expiresAt, &byok, &credentialID, &credentialVersion, &secretVersion)
	if err != nil {
		return DispatchAuthorization{}, ErrProviderConflict
	}
	if lockedLLMAttempt != result.LLMAttemptID {
		return DispatchAuthorization{}, ErrProviderConflict
	}
	if status != "prepared" || version != 1 {
		return DispatchAuthorization{}, ErrAlreadyDispatched
	}
	if result.RequestHash != command.RequestHash || !bytes.Equal(storedDigest, prepareDigest[:]) || !expiresAt.After(now) {
		return DispatchAuthorization{}, ErrDispatchToken
	}
	result.AttemptID, result.Candidate.BoundHost, result.Candidate.PricingVersion = command.AttemptID, host, pricing
	if byok {
		result.BYOK = &BYOKBinding{CredentialID: deref(credentialID), Version: derefUint(credentialVersion), SecretVersion: deref(secretVersion)}
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.llm_provider_attempts SET version=2,status='dispatching',dispatch_fence=1,prepare_token_hash=NULL,completion_token_hash=$1,completion_deadline=$2,dispatched_at=$3,dispatch_event_id=$4,updated_at=$3 WHERE tenant_id=$5 AND id=$6 AND status='prepared' AND version=1 AND prepare_token_hash=$7 AND prepare_token_expires_at>$3`, completionDigest[:], command.CompletionDeadline, now, identifiers.Event, command.TenantID, command.AttemptID, prepareDigest[:])
	if err != nil || tag.RowsAffected() != 1 {
		return DispatchAuthorization{}, ErrDispatchToken
	}
	event := publishEvent(identifiers, eventpostgres.Event{TenantID: command.TenantID, UserID: "", EventType: "ProviderAttemptDispatchAuthorized", SchemaVersion: 1, AggregateKind: "provider_attempt", AggregateID: command.AttemptID, AggregateVersion: 2, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID}, command.DispatchEvent)
	if err = tx.QueryRow(ctx, `SELECT user_id::text FROM agent.llm_provider_attempts WHERE tenant_id=$1 AND id=$2`, command.TenantID, command.AttemptID).Scan(&event.Event.UserID); err != nil {
		return DispatchAuthorization{}, err
	}
	if _, err = store.Appender.Append(ctx, tx, event); err != nil {
		return DispatchAuthorization{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return DispatchAuthorization{}, err
	}
	result.DispatchFence, result.CompletionDeadline = 1, command.CompletionDeadline
	return result, nil
}

func validPrepare(command PrepareProviderAttemptCommand) bool {
	return command.AttemptID != "" && command.ProviderAttemptID != "" && command.LLMAttemptID != "" && command.UsageReservationID != "" && command.TenantID != "" && command.UserID != "" && command.AttemptKey != "" && command.Ordinal > 0 && command.RequestHash != "" && command.ContextManifestHash != "" && command.PrepareToken != "" && command.CorrelationID != "" && validActor(command.Actor) && validPointer(command.PreparedEvent) && command.Candidate.ProviderID != "" && command.Candidate.ModelID != "" && command.Candidate.ModelVersion != "" && command.Candidate.BoundHost == canonicalHost(command.Candidate.BoundHost) && command.Candidate.PricingVersion != "" && (command.BYOK == nil || command.BYOK.CredentialID != "" && command.BYOK.Version > 0 && command.BYOK.SecretVersion != "")
}

func byokValues(binding *BYOKBinding) (string, *uint64, string) {
	if binding == nil {
		return "", nil, ""
	}
	version := binding.Version
	return binding.CredentialID, &version, binding.SecretVersion
}
func equalOptional(actual *string, expected string) bool {
	return actual == nil && expected == "" || actual != nil && *actual == expected
}
func equalOptionalUint(actual, expected *uint64) bool {
	return actual == nil && expected == nil || actual != nil && expected != nil && *actual == *expected
}
func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func derefUint(value *uint64) uint64 {
	if value == nil {
		return 0
	}
	return *value
}
