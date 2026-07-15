package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/platform/ids"
)

var (
	ErrPortfolioExportVersion  = errors.New("portfolio export version does not match")
	ErrPortfolioExecutionRight = errors.New("portfolio export execution right is stale, expired, or inconsistent")
	ErrInvalidPortfolioResult  = errors.New("portfolio export result is invalid")
)

type StartPortfolioBuildCommand struct {
	ExportID, TenantID, UserID string
	ExpectedExportVersion      uint64
	Claim                      executionpostgres.RunClaim
	Actor                      json.RawMessage
	CorrelationID              string
	BuildStartedEvent          PayloadPointer
}

type StartedPortfolioBuild struct {
	ExportID, Status string
	Version          uint64
	StartedAt        time.Time
	Replayed         bool
}

type CompletePortfolioExportCommand struct {
	ExportID, TenantID, UserID                       string
	ExpectedExportVersion                            uint64
	ObjectRef, ObjectVersion, ContentHash, MediaType string
	ScanResultHash, CorrelationID                    string
	ByteSize                                         int64
	ExpiresAt                                        time.Time
	Actor                                            json.RawMessage
	CompletedEvent                                   PayloadPointer
	RunCompletion                                    executionpostgres.CompleteRunCommand
}

type CompletedPortfolioExport struct {
	ExportID, RunID, Status, ContentHash string
	Version                              uint64
	CompletedAt, ExpiresAt               time.Time
	CompletionEventID                    string
	Replayed                             bool
}

type FailPortfolioExportCommand struct {
	ExportID, TenantID, UserID, FailureCode string
	ExpectedExportVersion                   uint64
	Actor                                   json.RawMessage
	CorrelationID                           string
	FailedEvent                             PayloadPointer
	RunCompletion                           executionpostgres.CompleteRunCommand
}

type FailedPortfolioExport struct {
	ExportID, RunID, Status, FailureCode string
	Version                              uint64
	FailedAt                             time.Time
	CompletionEventID                    string
	Replayed                             bool
}

type ExpirePortfolioExportCommand struct {
	ExportID, TenantID, UserID string
	ExpectedExportVersion      uint64
	ExpiredAt                  time.Time
	Actor                      json.RawMessage
	CorrelationID              string
	ExpiredEvent               PayloadPointer
}

type ExpiredPortfolioExport struct {
	ExportID, Status string
	Version          uint64
	ExpiredAt        time.Time
	ExpiryEventID    string
	Replayed         bool
}

type PortfolioExpiryTenantScan struct {
	AfterTenantID *string
	Limit         int
	ShardIndex    int
	ShardCount    int
	Now           time.Time
}

// StartBuild materializes the portfolio build projection from the durable
// RunStarted fact. The Run event remains the fact source; this update is safe
// to replay even after the Run has already reached a terminal state.
func (store PortfolioExportStore) StartBuild(ctx context.Context, command StartPortfolioBuildCommand) (StartedPortfolioBuild, error) {
	if !store.validWorker() || !validStartPortfolioBuild(command) {
		return StartedPortfolioBuild{}, ErrConfiguration
	}
	if err := store.requireEpoch(ctx); err != nil {
		return StartedPortfolioBuild{}, err
	}
	if command.Claim.StoreEpoch != store.StoreEpoch {
		return StartedPortfolioBuild{}, ErrStaleEpoch
	}
	digest, err := store.RunTokens.Digest(command.Claim.LeaseToken)
	if err != nil {
		return StartedPortfolioBuild{}, ErrPortfolioExecutionRight
	}
	now := store.now()
	buildEventIDs, err := store.buildStartedEventIDs(command.ExportID)
	if err != nil {
		return StartedPortfolioBuild{}, ErrConfiguration
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return StartedPortfolioBuild{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return StartedPortfolioBuild{}, err
	}
	var userID, runID, status string
	var version uint64
	var startedAt *time.Time
	err = tx.QueryRow(ctx, `SELECT user_id::text,run_id::text,status,version,started_at FROM product.portfolio_exports WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, command.ExportID).Scan(&userID, &runID, &status, &version, &startedAt)
	if err != nil || userID != command.UserID || runID != command.Claim.RunID {
		return StartedPortfolioBuild{}, ErrPortfolioExportConflict
	}
	if status != "requested" {
		durableStartedAt, durable := store.buildStartedEventTime(ctx, tx, command, buildEventIDs.event)
		if startedAt == nil || version < command.ExpectedExportVersion+1 || status != "building" && status != "ready" && status != "failed" && status != "expired" || !durable || !startedAt.Equal(durableStartedAt) {
			return StartedPortfolioBuild{}, ErrPortfolioExportVersion
		}
		if err = tx.Commit(ctx); err != nil {
			return StartedPortfolioBuild{}, err
		}
		return StartedPortfolioBuild{ExportID: command.ExportID, Status: status, Version: version, StartedAt: *startedAt, Replayed: true}, nil
	}
	if version != command.ExpectedExportVersion || !command.Claim.LeaseExpiresAt.After(now) {
		return StartedPortfolioBuild{}, ErrPortfolioExportVersion
	}
	runStartedEventID, runStarted := store.runStartedEventID(ctx, tx, command)
	var runActive, inboxActive, attemptActive bool
	err = tx.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM agent.runs WHERE tenant_id=$1 AND id=$2 AND user_id=$3 AND status='executing' AND run_version=$4 AND active_command_id=$5 AND active_attempt_id=$6 AND current_fence=$7 AND lease_token_hash=$8 AND lease_expires_at=$9 AND lease_expires_at>$10),
		EXISTS(SELECT 1 FROM agent.inbox WHERE tenant_id=$1 AND id=$11 AND store_epoch=$12 AND consumer_name=$13 AND command_id=$5 AND request_hash=$14 AND status='running' AND owner_attempt_id=$6 AND fence=$7 AND lease_token_hash=$8 AND lease_expires_at=$9 AND lease_expires_at>$10),
		EXISTS(SELECT 1 FROM agent.job_attempts WHERE tenant_id=$1 AND id=$6 AND job_id=$15 AND command_id=$5 AND status='running' AND fence=$7 AND lease_token_hash=$8 AND lease_expires_at=$9 AND lease_expires_at>$10)`, command.TenantID, command.Claim.RunID, command.UserID, command.Claim.RunVersion, command.Claim.CommandID, command.Claim.AttemptID, command.Claim.Fence, digest[:], command.Claim.LeaseExpiresAt, now, command.Claim.InboxID, command.Claim.StoreEpoch, command.Claim.ConsumerName, command.Claim.RequestHash, command.Claim.JobID).Scan(&runActive, &inboxActive, &attemptActive)
	if err != nil || !runStarted || !runActive || !inboxActive || !attemptActive {
		return StartedPortfolioBuild{}, ErrPortfolioExecutionRight
	}
	nextVersion := version + 1
	if tag, updateErr := tx.Exec(ctx, `UPDATE product.portfolio_exports SET status='building',version=$1,started_at=$2,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND user_id=$5 AND run_id=$6 AND status='requested' AND version=$7`, nextVersion, now, command.TenantID, command.ExportID, command.UserID, command.Claim.RunID, version); updateErr != nil || tag.RowsAffected() != 1 {
		return StartedPortfolioBuild{}, ErrPortfolioExportVersion
	}
	buildCausationID := runStartedEventID
	startedEvent := eventInput(buildEventIDs, "PortfolioExportBuildStarted", 1, command.TenantID, command.UserID, command.ExportID, nextVersion, store.StoreEpoch, now, command.Actor, &buildCausationID, command.CorrelationID, command.BuildStartedEvent)
	if _, err = store.Appender.Append(ctx, tx, startedEvent); err != nil {
		return StartedPortfolioBuild{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return StartedPortfolioBuild{}, err
	}
	return StartedPortfolioBuild{ExportID: command.ExportID, Status: "building", Version: nextVersion, StartedAt: now}, nil
}

func (store PortfolioExportStore) Complete(ctx context.Context, command CompletePortfolioExportCommand) (CompletedPortfolioExport, error) {
	if !store.validWorker() {
		return CompletedPortfolioExport{}, ErrConfiguration
	}
	if !validCompletePortfolioExport(command) {
		return CompletedPortfolioExport{}, ErrInvalidPortfolioResult
	}
	if err := store.requireEpoch(ctx); err != nil {
		return CompletedPortfolioExport{}, err
	}
	command.ExpiresAt = command.ExpiresAt.UTC().Truncate(time.Microsecond)
	eventIDs, err := store.completedEventIDs(command.ExportID)
	if err != nil {
		return CompletedPortfolioExport{}, ErrConfiguration
	}
	now := store.now()
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return CompletedPortfolioExport{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return CompletedPortfolioExport{}, err
	}
	var userID, runID, status, format string
	var version uint64
	err = tx.QueryRow(ctx, `SELECT user_id::text,run_id::text,status,version,export_format FROM product.portfolio_exports WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, command.ExportID).Scan(&userID, &runID, &status, &version, &format)
	if err != nil || userID != command.UserID || runID != command.RunCompletion.Claim.RunID {
		return CompletedPortfolioExport{}, ErrPortfolioExportConflict
	}
	if status == "ready" {
		replay, replayErr := store.loadCompletionReplay(ctx, tx, command, eventIDs.event)
		if replayErr != nil {
			return CompletedPortfolioExport{}, replayErr
		}
		if err = tx.Commit(ctx); err != nil {
			return CompletedPortfolioExport{}, err
		}
		return replay, nil
	}
	if status != "building" || version != command.ExpectedExportVersion || !mediaTypeMatches(format, command.MediaType) {
		return CompletedPortfolioExport{}, ErrPortfolioExportVersion
	}
	if !command.ExpiresAt.After(now) {
		return CompletedPortfolioExport{}, ErrInvalidPortfolioResult
	}
	runStore := executionpostgres.RunStore{Appender: store.Appender, IDKey: store.IDKey, StoreEpoch: store.StoreEpoch, Epochs: store.Epochs, Tokens: store.RunTokens, Now: store.Now}
	completedRun, err := runStore.CompleteRunTerminalInTx(ctx, tx, command.RunCompletion)
	if err != nil {
		return CompletedPortfolioExport{}, err
	}
	nextVersion := version + 1
	if tag, updateErr := tx.Exec(ctx, `UPDATE product.portfolio_exports SET status='ready',version=$1,object_ref=$2,object_version=$3,content_hash=$4,media_type=$5,byte_size=$6,scan_result_hash=$7,completion_event_id=$8,completed_at=$9,expires_at=$10,updated_at=$9 WHERE tenant_id=$11 AND id=$12 AND user_id=$13 AND run_id=$14 AND status='building' AND version=$15`, nextVersion, command.ObjectRef, command.ObjectVersion, command.ContentHash, command.MediaType, command.ByteSize, command.ScanResultHash, eventIDs.event, completedRun.CompletedAt, command.ExpiresAt, command.TenantID, command.ExportID, command.UserID, completedRun.RunID, version); updateErr != nil || tag.RowsAffected() != 1 {
		return CompletedPortfolioExport{}, ErrPortfolioExportVersion
	}
	causationID := completedRun.RunEventID
	completed := eventInput(eventIDs, "PortfolioExportCompleted", 2, command.TenantID, command.UserID, command.ExportID, nextVersion, store.StoreEpoch, completedRun.CompletedAt, command.Actor, &causationID, command.CorrelationID, command.CompletedEvent)
	if _, err = store.Appender.Append(ctx, tx, completed); err != nil {
		return CompletedPortfolioExport{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return CompletedPortfolioExport{}, err
	}
	return CompletedPortfolioExport{ExportID: command.ExportID, RunID: completedRun.RunID, Status: "ready", ContentHash: command.ContentHash, Version: nextVersion, CompletedAt: completedRun.CompletedAt, ExpiresAt: command.ExpiresAt, CompletionEventID: eventIDs.event}, nil
}

func (store PortfolioExportStore) Fail(ctx context.Context, command FailPortfolioExportCommand) (FailedPortfolioExport, error) {
	if !store.validWorker() {
		return FailedPortfolioExport{}, ErrConfiguration
	}
	if !validFailPortfolioExport(command) {
		return FailedPortfolioExport{}, ErrInvalidPortfolioResult
	}
	if err := store.requireEpoch(ctx); err != nil {
		return FailedPortfolioExport{}, err
	}
	eventIDs, err := store.failedEventIDs(command.ExportID)
	if err != nil {
		return FailedPortfolioExport{}, ErrConfiguration
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return FailedPortfolioExport{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return FailedPortfolioExport{}, err
	}
	var userID, runID, status string
	var version uint64
	err = tx.QueryRow(ctx, `SELECT user_id::text,run_id::text,status,version FROM product.portfolio_exports WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, command.ExportID).Scan(&userID, &runID, &status, &version)
	if err != nil || userID != command.UserID || runID != command.RunCompletion.Claim.RunID {
		return FailedPortfolioExport{}, ErrPortfolioExportConflict
	}
	if status == "failed" {
		replay, replayErr := store.loadFailureReplay(ctx, tx, command, eventIDs.event)
		if replayErr != nil {
			return FailedPortfolioExport{}, replayErr
		}
		if err = tx.Commit(ctx); err != nil {
			return FailedPortfolioExport{}, err
		}
		return replay, nil
	}
	if status != "building" || version != command.ExpectedExportVersion {
		return FailedPortfolioExport{}, ErrPortfolioExportVersion
	}
	runStore := executionpostgres.RunStore{Appender: store.Appender, IDKey: store.IDKey, StoreEpoch: store.StoreEpoch, Epochs: store.Epochs, Tokens: store.RunTokens, Now: store.Now}
	completedRun, err := runStore.CompleteRunTerminalInTx(ctx, tx, command.RunCompletion)
	if err != nil {
		return FailedPortfolioExport{}, err
	}
	nextVersion := version + 1
	if tag, updateErr := tx.Exec(ctx, `UPDATE product.portfolio_exports SET status='failed',version=$1,completion_event_id=$2,failed_at=$3,failure_code=$4,updated_at=$3 WHERE tenant_id=$5 AND id=$6 AND user_id=$7 AND run_id=$8 AND status='building' AND version=$9`, nextVersion, eventIDs.event, completedRun.CompletedAt, command.FailureCode, command.TenantID, command.ExportID, command.UserID, completedRun.RunID, version); updateErr != nil || tag.RowsAffected() != 1 {
		return FailedPortfolioExport{}, ErrPortfolioExportVersion
	}
	causationID := completedRun.RunEventID
	failedEvent := eventInput(eventIDs, "PortfolioExportFailed", 1, command.TenantID, command.UserID, command.ExportID, nextVersion, store.StoreEpoch, completedRun.CompletedAt, command.Actor, &causationID, command.CorrelationID, command.FailedEvent)
	if _, err = store.Appender.Append(ctx, tx, failedEvent); err != nil {
		return FailedPortfolioExport{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return FailedPortfolioExport{}, err
	}
	return FailedPortfolioExport{ExportID: command.ExportID, RunID: completedRun.RunID, Status: "failed", FailureCode: command.FailureCode, Version: nextVersion, FailedAt: completedRun.CompletedAt, CompletionEventID: eventIDs.event}, nil
}

// Expire makes retention enforcement an append-only fact. The object binding
// remains immutable so audit and deletion workers can still prove exactly
// which version crossed its retention boundary.
func (store PortfolioExportStore) Expire(ctx context.Context, command ExpirePortfolioExportCommand) (ExpiredPortfolioExport, error) {
	if !store.valid() {
		return ExpiredPortfolioExport{}, ErrConfiguration
	}
	if !validExpirePortfolioExport(command) {
		return ExpiredPortfolioExport{}, ErrInvalidPortfolioResult
	}
	if err := store.requireEpoch(ctx); err != nil {
		return ExpiredPortfolioExport{}, err
	}
	command.ExpiredAt = command.ExpiredAt.UTC().Truncate(time.Microsecond)
	now := store.now()
	if command.ExpiredAt.After(now) {
		return ExpiredPortfolioExport{}, ErrInvalidPortfolioResult
	}
	eventIDs, err := store.expiredEventIDs(command.ExportID)
	if err != nil {
		return ExpiredPortfolioExport{}, ErrConfiguration
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ExpiredPortfolioExport{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return ExpiredPortfolioExport{}, err
	}
	var userID, status string
	var version uint64
	var expiresAt, durableExpiredAt *time.Time
	var completionEventID *string
	err = tx.QueryRow(ctx, `SELECT user_id::text,status,version,expires_at,expired_at,completion_event_id::text FROM product.portfolio_exports WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, command.ExportID).Scan(&userID, &status, &version, &expiresAt, &durableExpiredAt, &completionEventID)
	if err != nil || userID != command.UserID {
		return ExpiredPortfolioExport{}, ErrPortfolioExportConflict
	}
	if status == "expired" {
		if expiresAt == nil || completionEventID == nil || durableExpiredAt == nil || version != command.ExpectedExportVersion+1 || !durableExpiredAt.Equal(command.ExpiredAt) || !store.expiredEventExists(ctx, tx, command, eventIDs.event, *completionEventID) {
			return ExpiredPortfolioExport{}, ErrPortfolioExportConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return ExpiredPortfolioExport{}, err
		}
		return ExpiredPortfolioExport{ExportID: command.ExportID, Status: "expired", Version: version, ExpiredAt: *durableExpiredAt, ExpiryEventID: eventIDs.event, Replayed: true}, nil
	}
	if status != "ready" || version != command.ExpectedExportVersion || expiresAt == nil || completionEventID == nil || expiresAt.After(command.ExpiredAt) || expiresAt.After(now) {
		return ExpiredPortfolioExport{}, ErrPortfolioExportVersion
	}
	nextVersion := version + 1
	if tag, updateErr := tx.Exec(ctx, `UPDATE product.portfolio_exports SET status='expired',version=$1,expired_at=$2,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND user_id=$5 AND status='ready' AND version=$6 AND expires_at<=$2`, nextVersion, command.ExpiredAt, command.TenantID, command.ExportID, command.UserID, version); updateErr != nil || tag.RowsAffected() != 1 {
		return ExpiredPortfolioExport{}, ErrPortfolioExportVersion
	}
	causationID := *completionEventID
	expiredEvent := eventInput(eventIDs, "PortfolioExportExpired", 1, command.TenantID, command.UserID, command.ExportID, nextVersion, store.StoreEpoch, command.ExpiredAt, command.Actor, &causationID, command.CorrelationID, command.ExpiredEvent)
	if _, err = store.Appender.Append(ctx, tx, expiredEvent); err != nil {
		return ExpiredPortfolioExport{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ExpiredPortfolioExport{}, err
	}
	return ExpiredPortfolioExport{ExportID: command.ExportID, Status: "expired", Version: nextVersion, ExpiredAt: command.ExpiredAt, ExpiryEventID: eventIDs.event}, nil
}

func (store PortfolioExportStore) ListExpirableTenants(ctx context.Context, scan PortfolioExpiryTenantScan) ([]string, error) {
	if !store.valid() || scan.Limit < 1 || scan.Limit > 5000 || scan.ShardCount < 1 || scan.ShardIndex < 0 || scan.ShardIndex >= scan.ShardCount || scan.Now.IsZero() {
		return nil, ErrConfiguration
	}
	if err := store.requireEpoch(ctx); err != nil {
		return nil, err
	}
	after := ""
	if scan.AfterTenantID != nil {
		after = *scan.AfterTenantID
	}
	rows, err := store.Pool.Query(ctx, `SELECT tenant_id FROM agent.list_expirable_portfolio_tenants($1,NULLIF($2,'')::uuid,$3,$4,$5,$6)`, store.StoreEpoch, after, scan.Limit, scan.ShardIndex, scan.ShardCount, scan.Now.UTC().Truncate(time.Microsecond))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tenants := make([]string, 0, scan.Limit)
	for rows.Next() {
		var tenantID string
		if err = rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		tenants = append(tenants, tenantID)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return tenants, nil
}

type lifecycleEventIDs struct{ event, outbox, publish string }

func (store PortfolioExportStore) completedEventIDs(exportID string) (lifecycleEventIDs, error) {
	return store.lifecycleEventIDs("portfolio-export-completed", exportID)
}

func (store PortfolioExportStore) buildStartedEventIDs(exportID string) (lifecycleEventIDs, error) {
	return store.lifecycleEventIDs("portfolio-export-build-started", exportID)
}

func (store PortfolioExportStore) failedEventIDs(exportID string) (lifecycleEventIDs, error) {
	return store.lifecycleEventIDs("portfolio-export-failed", exportID)
}

func (store PortfolioExportStore) expiredEventIDs(exportID string) (lifecycleEventIDs, error) {
	return store.lifecycleEventIDs("portfolio-export-expired", exportID)
}

func (store PortfolioExportStore) lifecycleEventIDs(domain, exportID string) (lifecycleEventIDs, error) {
	values := make([]string, 3)
	for index, part := range []string{"event", "outbox", "publish"} {
		value, err := ids.DeterministicUUID(store.IDKey, domain+":"+part, exportID)
		if err != nil {
			return lifecycleEventIDs{}, err
		}
		values[index] = value
	}
	return lifecycleEventIDs{values[0], values[1], values[2]}, nil
}

func eventInput(eventIDs lifecycleEventIDs, eventType string, schemaVersion int, tenantID, userID, exportID string, version uint64, epoch string, occurredAt time.Time, actor json.RawMessage, causationID *string, correlationID string, pointer PayloadPointer) eventpostgres.Input {
	return eventpostgres.Input{Event: eventpostgres.Event{ID: eventIDs.event, TenantID: tenantID, UserID: userID, EventType: eventType, SchemaVersion: schemaVersion, AggregateKind: "portfolio_export", AggregateID: exportID, AggregateVersion: version, StoreEpoch: epoch, OccurredAt: occurredAt, Actor: actor, CausationID: causationID, CorrelationID: correlationID, PayloadRef: pointer.Ref, PayloadHash: pointer.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: eventIDs.outbox, CommandID: eventIDs.publish, CommandType: "events.publish", PayloadRef: pointer.Ref, PayloadHash: pointer.Hash}}}
}

func (store PortfolioExportStore) runStartedEventID(ctx context.Context, tx pgx.Tx, command StartPortfolioBuildCommand) (string, bool) {
	var eventID string
	err := tx.QueryRow(ctx, `SELECT id::text FROM agent.events WHERE tenant_id=$1 AND user_id=$2 AND event_type='RunStarted' AND event_schema_version=1 AND aggregate_kind='run' AND aggregate_id=$3 AND aggregate_version=$4 AND store_epoch=$5`, command.TenantID, command.UserID, command.Claim.RunID, command.Claim.RunVersion, store.StoreEpoch).Scan(&eventID)
	return eventID, err == nil
}

func (store PortfolioExportStore) buildStartedEventTime(ctx context.Context, tx pgx.Tx, command StartPortfolioBuildCommand, eventID string) (time.Time, bool) {
	var occurredAt time.Time
	err := tx.QueryRow(ctx, `SELECT p.occurred_at
		FROM agent.events p
		JOIN agent.events r ON r.tenant_id=p.tenant_id AND r.id=p.causation_id
		  AND r.user_id=p.user_id AND r.event_type='RunStarted' AND r.event_schema_version=1
		  AND r.aggregate_kind='run' AND r.aggregate_id=$7 AND r.aggregate_version=$8 AND r.store_epoch=p.store_epoch
		WHERE p.tenant_id=$1 AND p.id=$2 AND p.user_id=$3 AND p.event_type='PortfolioExportBuildStarted'
		  AND p.event_schema_version=1 AND p.aggregate_kind='portfolio_export' AND p.aggregate_id=$4
		  AND p.aggregate_version=$5 AND p.store_epoch=$6 AND p.correlation_id=$9
		  AND p.payload_ref=$10 AND p.payload_hash=$11`, command.TenantID, eventID, command.UserID, command.ExportID, command.ExpectedExportVersion+1, store.StoreEpoch, command.Claim.RunID, command.Claim.RunVersion, command.CorrelationID, command.BuildStartedEvent.Ref, command.BuildStartedEvent.Hash).Scan(&occurredAt)
	return occurredAt, err == nil
}

func (store PortfolioExportStore) expiredEventExists(ctx context.Context, tx pgx.Tx, command ExpirePortfolioExportCommand, eventID, causationID string) bool {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND user_id=$3 AND event_type='PortfolioExportExpired' AND event_schema_version=1 AND aggregate_kind='portfolio_export' AND aggregate_id=$4 AND aggregate_version=$5 AND store_epoch=$6 AND causation_id=$7 AND correlation_id=$8 AND occurred_at=$9 AND payload_ref=$10 AND payload_hash=$11)`, command.TenantID, eventID, command.UserID, command.ExportID, command.ExpectedExportVersion+1, store.StoreEpoch, causationID, command.CorrelationID, command.ExpiredAt, command.ExpiredEvent.Ref, command.ExpiredEvent.Hash).Scan(&exists)
	return err == nil && exists
}

func (store PortfolioExportStore) loadCompletionReplay(ctx context.Context, tx pgx.Tx, command CompletePortfolioExportCommand, eventID string) (CompletedPortfolioExport, error) {
	var result CompletedPortfolioExport
	var objectRef, objectVersion, mediaType, scanHash, completionEventID string
	var byteSize int64
	err := tx.QueryRow(ctx, `SELECT run_id::text,status,version,object_ref,object_version,content_hash,media_type,byte_size,scan_result_hash,completion_event_id::text,completed_at,expires_at FROM product.portfolio_exports WHERE tenant_id=$1 AND id=$2 AND user_id=$3`, command.TenantID, command.ExportID, command.UserID).Scan(&result.RunID, &result.Status, &result.Version, &objectRef, &objectVersion, &result.ContentHash, &mediaType, &byteSize, &scanHash, &completionEventID, &result.CompletedAt, &result.ExpiresAt)
	if err != nil || result.Status != "ready" || result.Version != command.ExpectedExportVersion+1 || result.RunID != command.RunCompletion.Claim.RunID || objectRef != command.ObjectRef || objectVersion != command.ObjectVersion || result.ContentHash != command.ContentHash || mediaType != command.MediaType || byteSize != command.ByteSize || scanHash != command.ScanResultHash || completionEventID != eventID || !result.ExpiresAt.Equal(command.ExpiresAt) {
		return CompletedPortfolioExport{}, ErrPortfolioExportConflict
	}
	var portfolioEvent, runEvent bool
	err = tx.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND event_type='PortfolioExportCompleted' AND event_schema_version=2 AND aggregate_kind='portfolio_export' AND aggregate_id=$3 AND aggregate_version=$4 AND store_epoch=$5 AND payload_ref=$6 AND payload_hash=$7),
		EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND event_type='RunSucceeded' AND aggregate_kind='run' AND aggregate_id=$8 AND aggregate_version=$9 AND store_epoch=$5 AND payload_ref=$10 AND payload_hash=$11)`, command.TenantID, eventID, command.ExportID, command.ExpectedExportVersion+1, store.StoreEpoch, command.CompletedEvent.Ref, command.CompletedEvent.Hash, result.RunID, command.RunCompletion.ExpectedRunVersion+1, command.RunCompletion.RunEvent.Ref, command.RunCompletion.RunEvent.Hash).Scan(&portfolioEvent, &runEvent)
	if err != nil || !portfolioEvent || !runEvent {
		return CompletedPortfolioExport{}, ErrPortfolioExportConflict
	}
	result.ExportID, result.CompletionEventID, result.Replayed = command.ExportID, eventID, true
	return result, nil
}

func (store PortfolioExportStore) loadFailureReplay(ctx context.Context, tx pgx.Tx, command FailPortfolioExportCommand, failureEventID string) (FailedPortfolioExport, error) {
	var result FailedPortfolioExport
	err := tx.QueryRow(ctx, `SELECT run_id::text,status,version,failure_code,failed_at,completion_event_id::text FROM product.portfolio_exports WHERE tenant_id=$1 AND id=$2 AND user_id=$3`, command.TenantID, command.ExportID, command.UserID).Scan(&result.RunID, &result.Status, &result.Version, &result.FailureCode, &result.FailedAt, &result.CompletionEventID)
	if err != nil || result.Status != "failed" || result.Version != command.ExpectedExportVersion+1 || result.RunID != command.RunCompletion.Claim.RunID || result.FailureCode != command.FailureCode {
		return FailedPortfolioExport{}, ErrPortfolioExportConflict
	}
	var portfolioEvent, runEvent bool
	err = tx.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND event_type='PortfolioExportFailed' AND event_schema_version=1 AND aggregate_kind='portfolio_export' AND aggregate_id=$3 AND aggregate_version=$4 AND store_epoch=$5 AND payload_ref=$6 AND payload_hash=$7),
		EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND event_type='RunFailed' AND aggregate_kind='run' AND aggregate_id=$8 AND aggregate_version=$9 AND store_epoch=$5 AND payload_ref=$10 AND payload_hash=$11)`, command.TenantID, failureEventID, command.ExportID, command.ExpectedExportVersion+1, store.StoreEpoch, command.FailedEvent.Ref, command.FailedEvent.Hash, result.RunID, command.RunCompletion.ExpectedRunVersion+1, command.RunCompletion.RunEvent.Ref, command.RunCompletion.RunEvent.Hash).Scan(&portfolioEvent, &runEvent)
	if err != nil || result.CompletionEventID != failureEventID || !portfolioEvent || !runEvent {
		return FailedPortfolioExport{}, ErrPortfolioExportConflict
	}
	result.ExportID, result.Replayed = command.ExportID, true
	return result, nil
}

func validStartPortfolioBuild(command StartPortfolioBuildCommand) bool {
	claim := command.Claim
	return command.ExportID != "" && command.TenantID != "" && command.UserID != "" && command.ExpectedExportVersion > 0 && claim.RunID != "" && claim.TenantID == command.TenantID && claim.UserID == command.UserID && claim.StoreEpoch != "" && claim.RunVersion > 0 && claim.CommandID != "" && claim.ConsumerName != "" && claim.RequestHash != "" && claim.JobID != "" && claim.InboxID != "" && claim.AttemptID != "" && claim.Fence > 0 && claim.LeaseToken != "" && !claim.LeaseExpiresAt.IsZero() && !claim.Completed && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.BuildStartedEvent)
}

func validCompletePortfolioExport(command CompletePortfolioExportCommand) bool {
	claim := command.RunCompletion.Claim
	return command.ExportID != "" && command.TenantID != "" && command.UserID != "" && command.ExpectedExportVersion > 0 && command.ObjectRef != "" && command.ObjectVersion != "" && command.ContentHash != "" && command.MediaType != "" && command.ByteSize > 0 && command.ByteSize <= 1<<30 && command.ScanResultHash != "" && !command.ExpiresAt.IsZero() && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.CompletedEvent) && command.RunCompletion.TargetState == statemachine.RunSucceeded && command.RunCompletion.ResultHash == command.ContentHash && claim.TenantID == command.TenantID && claim.UserID == command.UserID && command.RunCompletion.CorrelationID == command.CorrelationID
}

func validFailPortfolioExport(command FailPortfolioExportCommand) bool {
	claim := command.RunCompletion.Claim
	return command.ExportID != "" && command.TenantID != "" && command.UserID != "" && command.ExpectedExportVersion > 0 && validFailureCode(command.FailureCode) && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.FailedEvent) && command.RunCompletion.TargetState == statemachine.RunFailed && claim.TenantID == command.TenantID && claim.UserID == command.UserID && command.RunCompletion.CorrelationID == command.CorrelationID
}

func validExpirePortfolioExport(command ExpirePortfolioExportCommand) bool {
	return command.ExportID != "" && command.TenantID != "" && command.UserID != "" && command.ExpectedExportVersion > 0 && !command.ExpiredAt.IsZero() && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.ExpiredEvent)
}

func validFailureCode(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character < 'a' || character > 'z' {
			if character < '0' || character > '9' {
				if character != '_' && character != '-' && character != '.' {
					return false
				}
			}
		}
	}
	return true
}

func mediaTypeMatches(format, mediaType string) bool {
	return format == "html" && mediaType == "text/html" || format == "pdf" && mediaType == "application/pdf" || format == "zip" && mediaType == "application/zip"
}

func (store PortfolioExportStore) validWorker() bool {
	return store.valid() && store.RunTokens.Purpose != "" && len(store.RunTokens.Pepper) >= 32
}
