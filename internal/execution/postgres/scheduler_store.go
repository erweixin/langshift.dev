package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/scheduler"
	"github.com/langshift/lites/internal/security/opaque"
)

var (
	ErrSchedulerBusy     = errors.New("scheduler resource is already being planned")
	ErrSchedulerConflict = errors.New("scheduler resource or dispatch lease is stale")
)

type SchedulerStore struct {
	Pool             *pgxpool.Pool
	Epochs           eventpostgres.EpochAuthority
	ResourceTokens   opaque.Manager
	DispatchTokens   opaque.Manager
	ResourceLeaseTTL time.Duration
	DispatchLeaseTTL time.Duration
	RedeliveryDelay  time.Duration
	RetryDelay       time.Duration
	Random           io.Reader
	Now              func() time.Time
}

type SchedulerResourceClaim struct {
	ResourceClass, Owner, StoreEpoch, LeaseToken string
	Version                                      uint64
	LeaseExpiresAt                               time.Time
	State                                        scheduler.State
}

type SchedulerCandidate struct {
	scheduler.Job
	DispatchVersion uint64
	Command         eventpostgres.PublishedCommand
}

type DispatchClaim struct {
	Candidate      SchedulerCandidate
	LeaseToken     string
	LeaseExpiresAt time.Time
}

type dispatchDecision struct {
	JobID           string `json:"job_id"`
	TenantID        string `json:"tenant_id"`
	DispatchVersion uint64 `json:"dispatch_version"`
	LeaseHash       string `json:"lease_hash"`
}

func (store SchedulerStore) PlanResource(ctx context.Context, config scheduler.Config, active scheduler.Active, resourceClass, owner, storeEpoch string, limit int) ([]DispatchClaim, error) {
	if _, exists := config.Resources[resourceClass]; !exists {
		return nil, ErrConfiguration
	}
	claim, err := store.claimResource(ctx, config, resourceClass, owner, storeEpoch)
	if err != nil {
		return nil, err
	}
	abort := true
	defer func() {
		if abort {
			_ = store.abortResource(context.WithoutCancel(ctx), claim)
		}
	}()
	candidates, err := store.listCandidates(ctx, claim, limit)
	if err != nil {
		return nil, err
	}
	jobs := make([]scheduler.Job, len(candidates))
	byID := make(map[string]SchedulerCandidate, len(candidates))
	for index, candidate := range candidates {
		jobs[index] = candidate.Job
		byID[candidate.ID] = candidate
	}
	plan, err := scheduler.Select(store.now(), config, claim.State, active, jobs)
	if err != nil {
		return nil, err
	}
	encodedState, err := scheduler.EncodeState(config, plan.State)
	if err != nil {
		return nil, err
	}
	expiresAt := store.now().Add(store.DispatchLeaseTTL).UTC().Truncate(time.Microsecond)
	decisions := make([]dispatchDecision, 0, len(plan.Decisions))
	dispatches := make([]DispatchClaim, 0, len(plan.Decisions))
	for _, selected := range plan.Decisions {
		candidate, exists := byID[selected.JobID]
		if !exists {
			return nil, ErrSchedulerConflict
		}
		manager := store.DispatchTokens
		manager.Random = store.random()
		credential, issueErr := manager.Issue()
		if issueErr != nil {
			return nil, issueErr
		}
		decisions = append(decisions, dispatchDecision{JobID: candidate.ID, TenantID: candidate.TenantID, DispatchVersion: candidate.DispatchVersion, LeaseHash: hex.EncodeToString(credential.Digest[:])})
		dispatches = append(dispatches, DispatchClaim{Candidate: candidate, LeaseToken: credential.Raw, LeaseExpiresAt: expiresAt})
	}
	encodedDecisions, err := json.Marshal(decisions)
	if err != nil {
		return nil, err
	}
	digest, err := store.ResourceTokens.Digest(claim.LeaseToken)
	if err != nil {
		return nil, ErrSchedulerConflict
	}
	var committed int
	err = store.Pool.QueryRow(ctx, `SELECT agent.scheduler_commit_dispatch($1,$2,$3,$4,$5::jsonb,$6::jsonb,$7,$8)`, claim.ResourceClass, claim.Owner, claim.Version, digest[:], encodedState, encodedDecisions, expiresAt, store.now()).Scan(&committed)
	if err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) && postgresError.Code == "40001" {
			return nil, ErrSchedulerConflict
		}
		return nil, err
	}
	if committed != len(dispatches) {
		return nil, ErrSchedulerConflict
	}
	abort = false
	return dispatches, nil
}

func (store SchedulerStore) claimResource(ctx context.Context, config scheduler.Config, resourceClass, owner, storeEpoch string) (SchedulerResourceClaim, error) {
	if !store.valid() || resourceClass == "" || owner == "" || storeEpoch == "" {
		return SchedulerResourceClaim{}, ErrConfiguration
	}
	current, err := store.Epochs.CurrentStoreEpoch(ctx)
	if err != nil || current == "" {
		return SchedulerResourceClaim{}, ErrConfiguration
	}
	if current != storeEpoch {
		return SchedulerResourceClaim{}, ErrStaleEpoch
	}
	manager := store.ResourceTokens
	manager.Random = store.random()
	credential, err := manager.Issue()
	if err != nil {
		return SchedulerResourceClaim{}, err
	}
	now := store.now()
	expiresAt := now.Add(store.ResourceLeaseTTL).UTC().Truncate(time.Microsecond)
	claim := SchedulerResourceClaim{ResourceClass: resourceClass, Owner: owner, StoreEpoch: storeEpoch, LeaseToken: credential.Raw, LeaseExpiresAt: expiresAt}
	var encodedState []byte
	err = store.Pool.QueryRow(ctx, `SELECT claim_version,scheduler_state FROM agent.scheduler_claim_resource($1,$2,$3,$4,$5)`, resourceClass, owner, credential.Digest[:], expiresAt, now).Scan(&claim.Version, &encodedState)
	if errors.Is(err, pgx.ErrNoRows) {
		return SchedulerResourceClaim{}, ErrSchedulerBusy
	}
	if err != nil {
		return SchedulerResourceClaim{}, err
	}
	claim.State, err = scheduler.DecodeState(config, encodedState)
	if err != nil {
		_ = store.abortResource(context.WithoutCancel(ctx), claim)
		return SchedulerResourceClaim{}, err
	}
	return claim, nil
}

func (store SchedulerStore) listCandidates(ctx context.Context, claim SchedulerResourceClaim, limit int) ([]SchedulerCandidate, error) {
	if limit < 1 || limit > 10000 {
		return nil, ErrConfiguration
	}
	digest, err := store.ResourceTokens.Digest(claim.LeaseToken)
	if err != nil {
		return nil, ErrSchedulerConflict
	}
	rows, err := store.Pool.Query(ctx, `SELECT * FROM agent.scheduler_list_ready_jobs($1,$2,$3,$4,$5,$6,$7)`, claim.ResourceClass, claim.Owner, claim.Version, digest[:], claim.StoreEpoch, store.now(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := make([]SchedulerCandidate, 0, limit)
	for rows.Next() {
		var candidate SchedulerCandidate
		var queueClass string
		if err = rows.Scan(&candidate.ID, &candidate.TenantID, &candidate.Command.CommandID, &queueClass, &candidate.Priority, &candidate.CostUnits, &candidate.EnqueuedAt, &candidate.AvailableAt, &candidate.DueAt, &candidate.RetryCount, &candidate.DispatchVersion, &candidate.Command.OutboxID, &candidate.Command.CommandType, &candidate.Command.AggregateKind, &candidate.Command.AggregateID, &candidate.Command.StoreEpoch, &candidate.Command.PayloadRef, &candidate.Command.PayloadHash); err != nil {
			return nil, err
		}
		candidate.ResourceClass = claim.ResourceClass
		candidate.QueueClass = scheduler.QueueClass(queueClass)
		candidate.Command.TenantID = candidate.TenantID
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func (store SchedulerStore) MarkDispatched(ctx context.Context, claim DispatchClaim) error {
	return store.finishDispatch(ctx, claim, "", true)
}

func (store SchedulerStore) DeferDispatch(ctx context.Context, claim DispatchClaim, errorCode string) error {
	if errorCode == "" {
		return ErrConfiguration
	}
	return store.finishDispatch(ctx, claim, errorCode, false)
}

func (store SchedulerStore) finishDispatch(ctx context.Context, claim DispatchClaim, errorCode string, published bool) error {
	if !store.validDispatch(claim) {
		return ErrConfiguration
	}
	current, err := store.Epochs.CurrentStoreEpoch(ctx)
	if err != nil || current == "" {
		return ErrConfiguration
	}
	if current != claim.Candidate.Command.StoreEpoch {
		return ErrStaleEpoch
	}
	digest, err := store.DispatchTokens.Digest(claim.LeaseToken)
	if err != nil {
		return ErrSchedulerConflict
	}
	now := store.now()
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.Candidate.TenantID); err != nil {
		return err
	}
	var tag pgconn.CommandTag
	if published {
		tag, err = tx.Exec(ctx, `UPDATE agent.jobs SET dispatch_lease_hash=NULL,dispatch_lease_expires_at=NULL,last_dispatched_at=$1,available_at=CASE WHEN status='pending' THEN $2 ELSE available_at END,updated_at=$1 WHERE id=$3 AND tenant_id=$4 AND command_id=$5 AND dispatch_version=$6 AND status IN ('pending','running') AND dispatch_lease_hash=$7 AND dispatch_lease_expires_at>$1`, now, now.Add(store.RedeliveryDelay), claim.Candidate.ID, claim.Candidate.TenantID, claim.Candidate.Command.CommandID, claim.Candidate.DispatchVersion+1, digest[:])
	} else {
		tag, err = tx.Exec(ctx, `UPDATE agent.jobs SET dispatch_lease_hash=NULL,dispatch_lease_expires_at=NULL,available_at=$1,last_error_code=$2,updated_at=$3 WHERE id=$4 AND tenant_id=$5 AND command_id=$6 AND dispatch_version=$7 AND status='pending' AND dispatch_lease_hash=$8 AND dispatch_lease_expires_at>$3`, now.Add(store.RetryDelay), errorCode, now, claim.Candidate.ID, claim.Candidate.TenantID, claim.Candidate.Command.CommandID, claim.Candidate.DispatchVersion+1, digest[:])
	}
	if err != nil || tag.RowsAffected() != 1 {
		if published && err == nil {
			var consumed bool
			queryErr := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM agent.jobs WHERE id=$1 AND tenant_id=$2 AND command_id=$3 AND dispatch_version=$4 AND status='running' AND dispatch_lease_hash IS NULL AND dispatch_lease_expires_at IS NULL)`, claim.Candidate.ID, claim.Candidate.TenantID, claim.Candidate.Command.CommandID, claim.Candidate.DispatchVersion+1).Scan(&consumed)
			if queryErr == nil && consumed {
				return tx.Commit(ctx)
			}
		}
		return ErrSchedulerConflict
	}
	return tx.Commit(ctx)
}

func (store SchedulerStore) abortResource(ctx context.Context, claim SchedulerResourceClaim) error {
	digest, err := store.ResourceTokens.Digest(claim.LeaseToken)
	if err != nil {
		return ErrSchedulerConflict
	}
	var aborted bool
	if err = store.Pool.QueryRow(ctx, `SELECT agent.scheduler_abort_resource($1,$2,$3,$4,$5)`, claim.ResourceClass, claim.Owner, claim.Version, digest[:], store.now()).Scan(&aborted); err != nil {
		return err
	}
	if !aborted {
		return ErrSchedulerConflict
	}
	return nil
}

func (store SchedulerStore) valid() bool {
	return store.Pool != nil && store.Epochs != nil && store.ResourceTokens.Purpose != "" && len(store.ResourceTokens.Pepper) >= 32 && store.DispatchTokens.Purpose != "" && store.DispatchTokens.Purpose != store.ResourceTokens.Purpose && len(store.DispatchTokens.Pepper) >= 32 && store.ResourceLeaseTTL > 0 && store.DispatchLeaseTTL > 0 && store.RedeliveryDelay > 0 && store.RetryDelay > 0
}

func (store SchedulerStore) validDispatch(claim DispatchClaim) bool {
	return store.valid() && validDispatchClaim(claim)
}

func validDispatchClaim(claim DispatchClaim) bool {
	command := claim.Candidate.Command
	return claim.Candidate.ID != "" && claim.Candidate.TenantID != "" && claim.Candidate.ResourceClass != "" && command.CommandID != "" && command.TenantID == claim.Candidate.TenantID && command.StoreEpoch != "" && command.OutboxID != "" && command.CommandType != "" && command.AggregateKind != "" && command.AggregateID != "" && command.PayloadRef != "" && command.PayloadHash != "" && claim.LeaseToken != "" && !claim.LeaseExpiresAt.IsZero()
}

func (store SchedulerStore) now() time.Time {
	now := time.Now().UTC()
	if store.Now != nil {
		now = store.Now().UTC()
	}
	return now.Truncate(time.Microsecond)
}

func (store SchedulerStore) random() io.Reader {
	if store.Random != nil {
		return store.Random
	}
	return rand.Reader
}
