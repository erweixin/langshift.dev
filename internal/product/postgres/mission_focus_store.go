package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/platform/ids"
	"github.com/langshift/lites/internal/product/mission"
)

var (
	ErrMissionConflict   = errors.New("mission mutation conflicts with durable state")
	ErrMissionNotFound   = errors.New("mission does not exist")
	ErrMissionTransition = errors.New("mission transition is not allowed")
	ErrFocusReplacement  = errors.New("focused mission mutation requires a valid replacement")
)

type MissionFocusStore struct {
	Pool       *pgxpool.Pool
	Appender   eventpostgres.Appender
	IDKey      []byte
	StoreEpoch string
	Epochs     EpochAuthority
	Now        func() time.Time
}

type SetMissionFocusCommand struct {
	MutationID, TenantID, UserID, MissionID string
	ExpectedFocusVersion                    uint64
	CorrelationID                           string
	Actor                                   json.RawMessage
	FocusChangedEvent                       PayloadPointer
}

type ChangeMissionStatusCommand struct {
	MutationID, TenantID, UserID, MissionID string
	NextStatus                              mission.Status
	ExpectedMissionVersion                  uint64
	ExpectedFocusVersion                    uint64
	ReplacementMissionID                    string
	CorrelationID                           string
	Actor                                   json.RawMessage
	StatusChangedEvent                      PayloadPointer
	FocusChangedEvent                       PayloadPointer
}

type MissionFocusResult struct {
	MissionID        string
	MissionStatus    mission.Status
	MissionVersion   uint64
	FocusedMissionID string
	FocusVersion     uint64
	EventIDs         []string
	Noop             bool
}

func (store MissionFocusStore) SetFocus(ctx context.Context, command SetMissionFocusCommand) (MissionFocusResult, error) {
	if !store.validMissionFocus() {
		return MissionFocusResult{}, ErrConfiguration
	}
	if command.MutationID == "" || command.TenantID == "" || command.UserID == "" || command.MissionID == "" || command.CorrelationID == "" || !validJSONObject(command.Actor) || !validPointer(command.FocusChangedEvent) {
		return MissionFocusResult{}, mission.ErrInvalid
	}
	if err := store.requireMissionEpoch(ctx); err != nil {
		return MissionFocusResult{}, err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return MissionFocusResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := store.SetFocusInTx(ctx, tx, command)
	if err != nil {
		return MissionFocusResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return MissionFocusResult{}, err
	}
	return result, nil
}

func (store MissionFocusStore) SetFocusInTx(ctx context.Context, tx pgx.Tx, command SetMissionFocusCommand) (MissionFocusResult, error) {
	if !store.validMissionFocus() || tx == nil {
		return MissionFocusResult{}, ErrConfiguration
	}
	if command.MutationID == "" || command.TenantID == "" || command.UserID == "" || command.MissionID == "" || command.CorrelationID == "" || !validJSONObject(command.Actor) || !validPointer(command.FocusChangedEvent) {
		return MissionFocusResult{}, mission.ErrInvalid
	}
	state, err := store.lockState(ctx, tx, command.TenantID, command.UserID)
	if err != nil {
		return MissionFocusResult{}, err
	}
	next, err := state.SetFocus(mission.FocusCommand{MissionID: command.MissionID, ExpectedFocusVersion: command.ExpectedFocusVersion})
	if err != nil {
		return MissionFocusResult{}, mapMissionError(err)
	}
	selected := next.Missions[command.MissionID]
	if next.Focus == state.Focus {
		return missionFocusResult(selected, next.Focus, true, nil), nil
	}
	if err = updateFocus(ctx, tx, command.TenantID, command.UserID, state.Focus, next.Focus); err != nil {
		return MissionFocusResult{}, err
	}
	if err = store.appendFocusEvent(ctx, tx, command.MutationID, command.TenantID, command.UserID, command.CorrelationID, command.Actor, command.FocusChangedEvent, next.Focus.Version); err != nil {
		return MissionFocusResult{}, err
	}
	identifiers, err := store.missionEventIDs("mission-focus-changed", command.MutationID)
	if err != nil {
		return MissionFocusResult{}, err
	}
	return missionFocusResult(selected, next.Focus, false, []string{identifiers.event}), nil
}

func (store MissionFocusStore) ChangeStatus(ctx context.Context, command ChangeMissionStatusCommand) (MissionFocusResult, error) {
	if !store.validMissionFocus() {
		return MissionFocusResult{}, ErrConfiguration
	}
	if command.MutationID == "" || command.TenantID == "" || command.UserID == "" || command.MissionID == "" || command.CorrelationID == "" || !validJSONObject(command.Actor) || !validPointer(command.StatusChangedEvent) {
		return MissionFocusResult{}, mission.ErrInvalid
	}
	if err := store.requireMissionEpoch(ctx); err != nil {
		return MissionFocusResult{}, err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return MissionFocusResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := store.ChangeStatusInTx(ctx, tx, command)
	if err != nil {
		return MissionFocusResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return MissionFocusResult{}, err
	}
	return result, nil
}

func (store MissionFocusStore) ChangeStatusInTx(ctx context.Context, tx pgx.Tx, command ChangeMissionStatusCommand) (MissionFocusResult, error) {
	if !store.validMissionFocus() || tx == nil {
		return MissionFocusResult{}, ErrConfiguration
	}
	if command.MutationID == "" || command.TenantID == "" || command.UserID == "" || command.MissionID == "" || command.CorrelationID == "" || !validJSONObject(command.Actor) || !validPointer(command.StatusChangedEvent) {
		return MissionFocusResult{}, mission.ErrInvalid
	}
	state, err := store.lockState(ctx, tx, command.TenantID, command.UserID)
	if err != nil {
		return MissionFocusResult{}, err
	}
	next, err := state.ChangeStatus(mission.StatusCommand{MissionID: command.MissionID, Next: command.NextStatus, ExpectedMissionVersion: command.ExpectedMissionVersion, ExpectedFocusVersion: command.ExpectedFocusVersion, ReplacementMissionID: command.ReplacementMissionID})
	if err != nil {
		return MissionFocusResult{}, mapMissionError(err)
	}
	beforeMission := state.Missions[command.MissionID]
	afterMission := next.Missions[command.MissionID]
	if beforeMission == afterMission && state.Focus == next.Focus {
		return missionFocusResult(afterMission, next.Focus, true, nil), nil
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE product.missions SET status=$1,version=$2,updated_at=$3,completed_at=CASE WHEN $1='completed' THEN $3 ELSE completed_at END,archived_at=CASE WHEN $1='archived' THEN $3 ELSE archived_at END WHERE tenant_id=$4 AND user_id=$5 AND id=$6 AND version=$7 AND status=$8`, afterMission.Status, afterMission.Version, store.now(), command.TenantID, command.UserID, command.MissionID, beforeMission.Version, beforeMission.Status); updateErr != nil || tag.RowsAffected() != 1 {
		return MissionFocusResult{}, ErrMissionConflict
	}
	if err = store.appendStatusEvent(ctx, tx, command, afterMission.Version); err != nil {
		return MissionFocusResult{}, err
	}
	eventIDs := make([]string, 0, 2)
	statusIDs, err := store.missionEventIDs("mission-status-changed", command.MutationID)
	if err != nil {
		return MissionFocusResult{}, err
	}
	eventIDs = append(eventIDs, statusIDs.event)
	if state.Focus != next.Focus {
		if !validPointer(command.FocusChangedEvent) {
			return MissionFocusResult{}, mission.ErrInvalid
		}
		if err = updateFocus(ctx, tx, command.TenantID, command.UserID, state.Focus, next.Focus); err != nil {
			return MissionFocusResult{}, err
		}
		if err = store.appendFocusEvent(ctx, tx, command.MutationID+":focus", command.TenantID, command.UserID, command.CorrelationID, command.Actor, command.FocusChangedEvent, next.Focus.Version); err != nil {
			return MissionFocusResult{}, err
		}
		focusIDs, idErr := store.missionEventIDs("mission-focus-changed", command.MutationID+":focus")
		if idErr != nil {
			return MissionFocusResult{}, idErr
		}
		eventIDs = append(eventIDs, focusIDs.event)
	}
	return missionFocusResult(afterMission, next.Focus, false, eventIDs), nil
}

func (store MissionFocusStore) lockState(ctx context.Context, tx pgx.Tx, tenantID, userID string) (mission.State, error) {
	if _, err := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return mission.State{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO product.mission_focuses(tenant_id,user_id,mission_id,focus_version) VALUES($1,$2,NULL,0) ON CONFLICT (tenant_id,user_id) DO NOTHING`, tenantID, userID); err != nil {
		return mission.State{}, err
	}
	var focusedMissionID *string
	var focusVersion uint64
	if err := tx.QueryRow(ctx, `SELECT mission_id::text,focus_version FROM product.mission_focuses WHERE tenant_id=$1 AND user_id=$2 FOR UPDATE`, tenantID, userID).Scan(&focusedMissionID, &focusVersion); err != nil {
		return mission.State{}, err
	}
	state := mission.New()
	state.Focus.Version = focusVersion
	if focusedMissionID != nil {
		state.Focus.MissionID = *focusedMissionID
	}
	rows, err := tx.Query(ctx, `SELECT id::text,status,version FROM product.missions WHERE tenant_id=$1 AND user_id=$2 ORDER BY id FOR UPDATE`, tenantID, userID)
	if err != nil {
		return mission.State{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var item mission.Mission
		if err = rows.Scan(&item.ID, &item.Status, &item.Version); err != nil {
			return mission.State{}, err
		}
		state.Missions[item.ID] = item
	}
	if err = rows.Err(); err != nil {
		return mission.State{}, err
	}
	if err = state.Validate(); err != nil {
		return mission.State{}, ErrMissionConflict
	}
	return state, nil
}

func updateFocus(ctx context.Context, tx pgx.Tx, tenantID, userID string, before, after mission.Focus) error {
	var missionID any
	if after.MissionID != "" {
		missionID = after.MissionID
	}
	tag, err := tx.Exec(ctx, `UPDATE product.mission_focuses SET mission_id=$1,focus_version=$2,updated_at=CURRENT_TIMESTAMP WHERE tenant_id=$3 AND user_id=$4 AND focus_version=$5 AND mission_id IS NOT DISTINCT FROM NULLIF($6,'')::uuid`, missionID, after.Version, tenantID, userID, before.Version, before.MissionID)
	if err != nil || tag.RowsAffected() != 1 {
		return ErrMissionConflict
	}
	return nil
}

func (store MissionFocusStore) appendStatusEvent(ctx context.Context, tx pgx.Tx, command ChangeMissionStatusCommand, version uint64) error {
	identifiers, err := store.missionEventIDs("mission-status-changed", command.MutationID)
	if err != nil {
		return err
	}
	input := eventpostgres.Input{Event: eventpostgres.Event{ID: identifiers.event, TenantID: command.TenantID, UserID: command.UserID, EventType: "MissionStatusChanged", SchemaVersion: 1, AggregateKind: "mission", AggregateID: command.MissionID, AggregateVersion: version, StoreEpoch: store.StoreEpoch, OccurredAt: store.now(), Actor: command.Actor, CorrelationID: command.CorrelationID, PayloadRef: command.StatusChangedEvent.Ref, PayloadHash: command.StatusChangedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: identifiers.outbox, CommandID: identifiers.publish, CommandType: "events.publish", PayloadRef: command.StatusChangedEvent.Ref, PayloadHash: command.StatusChangedEvent.Hash}}}
	_, err = store.Appender.Append(ctx, tx, input)
	return err
}

func (store MissionFocusStore) appendFocusEvent(ctx context.Context, tx pgx.Tx, seed, tenantID, userID, correlationID string, actor json.RawMessage, pointer PayloadPointer, version uint64) error {
	identifiers, err := store.missionEventIDs("mission-focus-changed", seed)
	if err != nil {
		return err
	}
	input := eventpostgres.Input{Event: eventpostgres.Event{ID: identifiers.event, TenantID: tenantID, UserID: userID, EventType: "MissionFocusChanged", SchemaVersion: 1, AggregateKind: "mission_focus", AggregateID: userID, AggregateVersion: version, StoreEpoch: store.StoreEpoch, OccurredAt: store.now(), Actor: actor, CorrelationID: correlationID, PayloadRef: pointer.Ref, PayloadHash: pointer.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: identifiers.outbox, CommandID: identifiers.publish, CommandType: "events.publish", PayloadRef: pointer.Ref, PayloadHash: pointer.Hash}}}
	_, err = store.Appender.Append(ctx, tx, input)
	return err
}

func (store MissionFocusStore) missionEventIDs(domain, seed string) (artifactEventIDs, error) {
	values := make([]string, 3)
	for index, part := range []string{"event", "outbox", "publish"} {
		value, err := ids.DeterministicUUID(store.IDKey, domain+":"+part, seed)
		if err != nil {
			return artifactEventIDs{}, err
		}
		values[index] = value
	}
	return artifactEventIDs{values[0], values[1], values[2]}, nil
}

func (store MissionFocusStore) validMissionFocus() bool {
	return store.Pool != nil && store.Epochs != nil && store.StoreEpoch != "" && len(store.IDKey) >= 32
}

func (store MissionFocusStore) requireMissionEpoch(ctx context.Context) error {
	current, err := store.Epochs.CurrentStoreEpoch(ctx)
	if err != nil || current == "" {
		return ErrConfiguration
	}
	if current != store.StoreEpoch {
		return ErrStaleEpoch
	}
	return nil
}

func (store MissionFocusStore) now() time.Time {
	now := time.Now().UTC()
	if store.Now != nil {
		now = store.Now().UTC()
	}
	return now.Truncate(time.Microsecond)
}

func missionFocusResult(item mission.Mission, focus mission.Focus, noop bool, eventIDs []string) MissionFocusResult {
	return MissionFocusResult{MissionID: item.ID, MissionStatus: item.Status, MissionVersion: item.Version, FocusedMissionID: focus.MissionID, FocusVersion: focus.Version, EventIDs: eventIDs, Noop: noop}
}

func mapMissionError(err error) error {
	switch {
	case errors.Is(err, mission.ErrNotFound):
		return ErrMissionNotFound
	case errors.Is(err, mission.ErrVersionConflict):
		return ErrMissionConflict
	case errors.Is(err, mission.ErrIllegalState):
		return ErrMissionTransition
	case errors.Is(err, mission.ErrReplacement):
		return ErrFocusReplacement
	default:
		return err
	}
}
