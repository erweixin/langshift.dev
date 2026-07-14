package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/identity/anonymousclaim"
	"github.com/langshift/lites/internal/identity/api"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

type OnboardingClaimService struct {
	Pool                 *pgxpool.Pool
	Store                anonymousclaim.Store
	SystemTenantID       string
	Payloads             payload.Store
	IdentityKey          []byte
	IdempotencyKeyPepper []byte
	RequestDigestPepper  []byte
	IdempotencyTTL       time.Duration
	Now                  func() time.Time
}

func (service OnboardingClaimService) ClaimOnboarding(ctx context.Context, command api.OnboardingClaimCommand) (api.OnboardingClaimResult, error) {
	if err := service.validateClaimCommand(command); err != nil {
		return api.OnboardingClaimResult{}, err
	}
	canonical, err := json.Marshal(struct {
		ClientRequestID, OnboardingSessionID, AnonymousSubjectID, TargetTenantID, TargetUserID string
		ExpectedVersion                                                                        uint64
	}{command.ClientRequestID, command.OnboardingSessionID, command.AnonymousSubjectID, command.TenantID, command.UserID, command.ExpectedClaimVersion})
	if err != nil {
		return api.OnboardingClaimResult{}, api.ErrDependencyUnavailable
	}
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return api.OnboardingClaimResult{}, api.ErrDependencyUnavailable
	}
	recordID, err := ids.DeterministicUUID(service.IdentityKey, "idempotency-record:onboarding.claim", command.TenantID+"\x00"+command.UserID+"\x00"+command.IdempotencyKey)
	if err != nil {
		return api.OnboardingClaimResult{}, api.ErrDependencyUnavailable
	}
	input := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: "onboarding.claim"}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	executor := IdempotencyExecutor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	if response, found, loadErr := executor.LoadCompleted(ctx, input); loadErr != nil {
		return api.OnboardingClaimResult{}, mapIdentityError(loadErr)
	} else if found {
		return service.readClaimResponse(ctx, descriptor, response)
	}
	if completed, beginErr := service.beginClaimIdempotency(ctx, input); beginErr != nil {
		return api.OnboardingClaimResult{}, mapIdentityError(beginErr)
	} else if completed {
		response, found, loadErr := executor.LoadCompleted(ctx, input)
		if loadErr != nil || !found {
			return api.OnboardingClaimResult{}, api.ErrDependencyUnavailable
		}
		return service.readClaimResponse(ctx, descriptor, response)
	}
	claimID, err := service.lookupClaimID(ctx, command.OnboardingSessionID, command.AnonymousSubjectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return api.OnboardingClaimResult{}, api.ErrResourceNotFound
		}
		return api.OnboardingClaimResult{}, api.ErrDependencyUnavailable
	}
	current, err := service.Store.Load(ctx, claimID)
	if err != nil {
		return api.OnboardingClaimResult{}, api.ErrDependencyUnavailable
	}
	if current.Status == anonymousclaim.ManualReview {
		return api.OnboardingClaimResult{}, api.ErrClaimManualReview
	}
	if current.Status == anonymousclaim.Expired {
		return api.OnboardingClaimResult{}, api.ErrStateConflict
	}
	missionID, err := ids.DeterministicUUID(service.IdentityKey, "anonymous-claim-target-mission", current.ClaimKey+"\x00"+command.TenantID+"\x00"+command.UserID)
	if err != nil {
		return api.OnboardingClaimResult{}, api.ErrDependencyUnavailable
	}
	reservation := anonymousclaim.Reservation{ClaimID: claimID, ClaimKey: current.ClaimKey, TargetTenantID: command.TenantID, TargetUserID: command.UserID, MissionID: missionID}
	if current.Version != command.ExpectedClaimVersion && (current.Status == anonymousclaim.Available || current.ClaimKey != reservation.ClaimKey || current.TargetTenantID != reservation.TargetTenantID || current.TargetUserID != reservation.TargetUserID || current.MissionID != reservation.MissionID) {
		return api.OnboardingClaimResult{}, api.ErrVersionConflict
	}
	reserved, err := (&anonymousclaim.Service{Store: service.Store, Now: service.Now}).Reserve(ctx, reservation)
	if err != nil {
		return api.OnboardingClaimResult{}, mapClaimServiceError(err)
	}
	now := time.Now().UTC()
	if service.Now != nil {
		now = service.Now().UTC()
	}
	manifest, err := service.putClaimResponse(ctx, descriptor, api.OnboardingClaimResult{ID: reserved.ID, Version: reserved.Version, Status: string(reserved.Status), UpdatedAt: now})
	if err != nil {
		return api.OnboardingClaimResult{}, api.ErrDependencyUnavailable
	}
	response, err := service.completeClaimIdempotency(ctx, input, idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: manifest.Ref, Hash: manifest.Hash, ResourceVersion: reserved.Version})
	if err != nil {
		return api.OnboardingClaimResult{}, mapIdentityError(err)
	}
	return service.readClaimResponse(ctx, descriptor, response)
}

// Claim reservation cannot run inside the target-tenant idempotency
// transaction: doing so would hold one pool connection while requesting a
// second source-tenant connection and can starve a bounded pool. These two
// short transactions durably bracket a replay-safe domain reservation.
func (service OnboardingClaimService) beginClaimIdempotency(ctx context.Context, input IdempotencyInput) (bool, error) {
	keyDigest, err := idempotency.KeyDigest(input.RawKey, service.IdempotencyKeyPepper)
	if err != nil {
		return false, err
	}
	now := time.Now().UTC()
	if service.Now != nil {
		now = service.Now().UTC()
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, input.Scope.TenantID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.idempotency_responses (id,tenant_id,user_id,operation_id,idempotency_key_hash,request_hash,request_id,status,expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7,'in_progress',$8) ON CONFLICT DO NOTHING`, input.RecordID, input.Scope.TenantID, input.Scope.UserID, input.Scope.OperationID, keyDigest[:], input.RequestHash, input.RequestID, now.Add(service.IdempotencyTTL)); err != nil {
		return false, err
	}
	var storedHash, status string
	if err = tx.QueryRow(ctx, `SELECT request_hash,status FROM agent.idempotency_responses WHERE tenant_id=$1 AND user_id=$2 AND operation_id=$3 AND idempotency_key_hash=$4`, input.Scope.TenantID, input.Scope.UserID, input.Scope.OperationID, keyDigest[:]).Scan(&storedHash, &status); err != nil {
		return false, err
	}
	if storedHash != input.RequestHash {
		return false, idempotency.ErrKeyConflict
	}
	if status != "in_progress" && status != "completed" {
		return false, idempotency.ErrInProgress
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return status == "completed", nil
}

func (service OnboardingClaimService) completeClaimIdempotency(ctx context.Context, input IdempotencyInput, candidate idempotency.Response) (idempotency.Response, error) {
	keyDigest, err := idempotency.KeyDigest(input.RawKey, service.IdempotencyKeyPepper)
	if err != nil {
		return idempotency.Response{}, err
	}
	now := time.Now().UTC()
	if service.Now != nil {
		now = service.Now().UTC()
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return idempotency.Response{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, input.Scope.TenantID); err != nil {
		return idempotency.Response{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.idempotency_responses SET status='completed',response_status=$1,response_content_type=$2,response_payload_ref=$3,response_hash=$4,resource_version=$5,completed_at=$6,version=version+1,updated_at=$6 WHERE id=$7 AND tenant_id=$8 AND request_hash=$9 AND status='in_progress'`, candidate.Status, candidate.ContentType, candidate.PayloadRef, candidate.Hash, candidate.ResourceVersion, now, input.RecordID, input.Scope.TenantID, input.RequestHash)
	if err != nil {
		return idempotency.Response{}, err
	}
	response := candidate
	if tag.RowsAffected() == 0 {
		var storedHash, status string
		err = tx.QueryRow(ctx, `SELECT request_hash,status,response_status,response_content_type,response_payload_ref,response_hash,COALESCE(resource_version,0) FROM agent.idempotency_responses WHERE tenant_id=$1 AND user_id=$2 AND operation_id=$3 AND idempotency_key_hash=$4`, input.Scope.TenantID, input.Scope.UserID, input.Scope.OperationID, keyDigest[:]).Scan(&storedHash, &status, &response.Status, &response.ContentType, &response.PayloadRef, &response.Hash, &response.ResourceVersion)
		if err != nil {
			return idempotency.Response{}, err
		}
		if storedHash != input.RequestHash {
			return idempotency.Response{}, idempotency.ErrKeyConflict
		}
		if status != "completed" {
			return idempotency.Response{}, idempotency.ErrInProgress
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return idempotency.Response{}, err
	}
	return response, nil
}

func (service OnboardingClaimService) validateClaimCommand(command api.OnboardingClaimCommand) error {
	if service.Pool == nil || service.Store == nil || service.SystemTenantID == "" || service.Payloads == nil || len(service.IdentityKey) < 32 || len(service.IdempotencyKeyPepper) < 32 || len(service.RequestDigestPepper) < 32 || service.IdempotencyTTL <= 0 {
		return api.ErrDependencyUnavailable
	}
	if command.ClientRequestID == "" || command.RequestID == "" || command.IdempotencyKey == "" || command.OnboardingSessionID == "" || command.AnonymousSubjectID == "" || command.UserID == "" || command.TenantID == "" || command.ExpectedClaimVersion == 0 {
		return api.ErrValidation
	}
	return nil
}

func (service OnboardingClaimService) lookupClaimID(ctx context.Context, sessionID, anonymousSubjectID string) (string, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, service.SystemTenantID); err != nil {
		return "", err
	}
	var claimID string
	err = tx.QueryRow(ctx, `SELECT c.id::text FROM identity.onboarding_claims c JOIN identity.onboarding_sessions s ON s.id=c.onboarding_session_id AND s.tenant_id=c.tenant_id WHERE c.tenant_id=$1 AND c.onboarding_session_id=$2 AND c.anonymous_subject_id=$3 AND s.anonymous_subject_id=$3 ORDER BY c.created_at DESC LIMIT 1`, service.SystemTenantID, sessionID, anonymousSubjectID).Scan(&claimID)
	if err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return claimID, nil
}

func (service OnboardingClaimService) putClaimResponse(ctx context.Context, descriptor payload.Descriptor, result api.OnboardingClaimResult) (payload.Manifest, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return payload.Manifest{}, err
	}
	return service.Payloads.Put(ctx, descriptor, encoded)
}

func (service OnboardingClaimService) readClaimResponse(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (api.OnboardingClaimResult, error) {
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	if err != nil {
		return api.OnboardingClaimResult{}, api.ErrDependencyUnavailable
	}
	var result api.OnboardingClaimResult
	if err = json.Unmarshal(encoded, &result); err != nil || result.ID == "" || result.Version == 0 || result.Status == "" || result.UpdatedAt.IsZero() {
		return api.OnboardingClaimResult{}, api.ErrDependencyUnavailable
	}
	return result, nil
}

func mapClaimServiceError(err error) error {
	switch {
	case errors.Is(err, anonymousclaim.ErrClaimTaken), errors.Is(err, anonymousclaim.ErrInvalidTransition):
		return api.ErrStateConflict
	case errors.Is(err, anonymousclaim.ErrVersionConflict):
		return api.ErrVersionConflict
	default:
		return api.ErrDependencyUnavailable
	}
}

var _ api.OnboardingClaimService = OnboardingClaimService{}
