package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/langshift/lites/internal/behavior"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/platform/ids"
)

type AcceptMessageRunCommand struct {
	Run                         AcceptRunCommand
	MessageID                   string
	ExpectedConversationVersion uint64
	ExpectedConversationMode    string
	ExpectedConversationProfile behavior.Profile
	Message                     PayloadPointer
	ContentHash                 string
	AppendedEvent               PayloadPointer
	CoachContext                *CoachContextSource
	Actor                       json.RawMessage
}

type AcceptedMessageRun struct {
	AcceptedRun
	MessageID           string
	MessageEventID      string
	ConversationVersion uint64
	AcceptedAt          time.Time
	Replayed            bool
}

type conversationMessageIdentifiers struct {
	event, publishOutbox, publishCommand string
}

// AcceptMessageRun is the public Message admission commit point. The
// Conversation CAS, immutable user context source, MessageAppended fact,
// RunAccepted/RunQueued facts, Job, and StartAgentRun outbox command become
// visible together. Concurrent requests against one expected Conversation
// version have exactly one winner.
func (store RunStore) AcceptMessageRun(ctx context.Context, command AcceptMessageRunCommand) (AcceptedMessageRun, error) {
	if store.Pool == nil || store.Behavior == nil || !store.validCore() || !validAcceptMessageRun(command) {
		return AcceptedMessageRun{}, ErrInvalidCommand
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return AcceptedMessageRun{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	accepted, err := store.AcceptMessageRunInTx(ctx, tx, command)
	if err != nil {
		return AcceptedMessageRun{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return AcceptedMessageRun{}, err
	}
	return accepted, nil
}

// AcceptMessageRunInTx lets the public idempotency record, Conversation CAS,
// immutable context source, Run, Job, events, and commands share one commit.
// The caller owns commit or rollback.
func (store RunStore) AcceptMessageRunInTx(ctx context.Context, tx pgx.Tx, command AcceptMessageRunCommand) (AcceptedMessageRun, error) {
	if tx == nil || store.Behavior == nil || !store.validCore() || !validAcceptMessageRun(command) {
		return AcceptedMessageRun{}, ErrInvalidCommand
	}
	identifiers, err := store.conversationMessageIdentifiers(command.MessageID)
	if err != nil {
		return AcceptedMessageRun{}, ErrConfiguration
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if store.Now != nil {
		now = store.Now().UTC().Truncate(time.Microsecond)
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.Run.TenantID); err != nil {
		return AcceptedMessageRun{}, err
	}
	var currentVersion uint64
	var status string
	var conversationMode string
	var missionID string
	var lastRunID *string
	var durableTime time.Time
	err = tx.QueryRow(ctx, `SELECT version,status,mode,mission_id::text,last_run_id::text,updated_at FROM agent.conversations WHERE tenant_id=$1 AND id=$2 AND user_id=$3 FOR UPDATE`, command.Run.TenantID, command.Run.ConversationID, command.Run.UserID).Scan(&currentVersion, &status, &conversationMode, &missionID, &lastRunID, &durableTime)
	if errors.Is(err, pgx.ErrNoRows) {
		return AcceptedMessageRun{}, ErrRunConflict
	}
	if err != nil {
		return AcceptedMessageRun{}, err
	}
	if status != "active" || conversationMode != command.ExpectedConversationMode || command.ExpectedConversationProfile != command.Run.BehaviorProfile {
		return AcceptedMessageRun{}, ErrRunConflict
	}
	if conversationMode == "coach" {
		var focused bool
		if err = tx.QueryRow(ctx, `SELECT agent.lock_active_focused_mission($1,$2,$3)`, command.Run.TenantID, command.Run.UserID, missionID).Scan(&focused); err != nil {
			return AcceptedMessageRun{}, err
		}
		if !focused {
			return AcceptedMessageRun{}, ErrRunConflict
		}
	}
	if command.CoachContext != nil {
		if conversationMode != "coach" || command.Run.BehaviorProfile != behavior.Coach || command.CoachContext.Fence.ConversationID != command.Run.ConversationID || command.CoachContext.Fence.MissionID != missionID {
			return AcceptedMessageRun{}, ErrRunConflict
		}
		var exactFence bool
		fence := command.CoachContext.Fence
		if err = tx.QueryRow(ctx, `SELECT agent.lock_coach_context_fence($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,'')::uuid,NULLIF($10,0),NULLIF($11,'')::uuid,NULLIF($12,0),$13)`, command.Run.TenantID, command.Run.UserID, command.Run.ConversationID, fence.MissionID, fence.FocusVersion, fence.MissionVersion, fence.RouteVersion, fence.ClaimSetHash, fence.RouteRevisionID, fence.RouteRevisionVersion, fence.DailyTaskID, fence.DailyTaskVersion, fence.PreferencesVersion).Scan(&exactFence); err != nil {
			return AcceptedMessageRun{}, err
		}
		if !exactFence {
			return AcceptedMessageRun{}, ErrRunConflict
		}
		contextIdentifiers, identifierErr := store.conversationMessageIdentifiers(command.CoachContext.ID)
		if identifierErr != nil {
			return AcceptedMessageRun{}, ErrConfiguration
		}
		contextEvent := eventpostgres.Input{Event: eventpostgres.Event{
			ID: contextIdentifiers.event, TenantID: command.Run.TenantID, UserID: command.Run.UserID,
			EventType: "CoachContextSnapshotted", SchemaVersion: 1, AggregateKind: "coach_context",
			AggregateID: command.CoachContext.ID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch,
			OccurredAt: now, Actor: command.Actor, CorrelationID: command.Run.CorrelationID,
			PayloadRef: command.CoachContext.SnapshotEvent.Ref, PayloadHash: command.CoachContext.SnapshotEvent.Hash,
		}, Commands: []eventpostgres.OutboxCommand{{ID: contextIdentifiers.publishOutbox, CommandID: contextIdentifiers.publishCommand, CommandType: "events.publish", PayloadRef: command.CoachContext.SnapshotEvent.Ref, PayloadHash: command.CoachContext.SnapshotEvent.Hash}}}
		if _, err = store.Appender.Append(ctx, tx, contextEvent); err != nil {
			return AcceptedMessageRun{}, err
		}
	}
	nextVersion := command.ExpectedConversationVersion + 1
	replayed := currentVersion == nextVersion && lastRunID != nil && *lastRunID == command.Run.RunID
	if currentVersion != command.ExpectedConversationVersion && !replayed {
		return AcceptedMessageRun{}, ErrRunConflict
	}
	if !replayed {
		durableTime = now
	}
	messageEvent := eventpostgres.Input{
		Event: eventpostgres.Event{
			ID: identifiers.event, TenantID: command.Run.TenantID, UserID: command.Run.UserID,
			EventType: "MessageAppended", SchemaVersion: 1, AggregateKind: "conversation",
			AggregateID: command.Run.ConversationID, AggregateVersion: nextVersion, StoreEpoch: store.StoreEpoch,
			OccurredAt: durableTime, Actor: command.Actor, CorrelationID: command.Run.CorrelationID,
			PayloadRef: command.AppendedEvent.Ref, PayloadHash: command.AppendedEvent.Hash,
		},
		Commands: []eventpostgres.OutboxCommand{{ID: identifiers.publishOutbox, CommandID: identifiers.publishCommand, CommandType: "events.publish", PayloadRef: command.AppendedEvent.Ref, PayloadHash: command.AppendedEvent.Hash}},
	}
	if _, err = store.Appender.Append(ctx, tx, messageEvent); err != nil {
		return AcceptedMessageRun{}, err
	}
	accepted, err := store.AcceptInTx(ctx, tx, command.Run)
	if err != nil {
		return AcceptedMessageRun{}, err
	}
	if replayed != accepted.Replayed {
		return AcceptedMessageRun{}, ErrRunConflict
	}
	if replayed {
		var exact bool
		userIndex := 0
		if command.CoachContext != nil {
			userIndex = 1
		}
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.run_messages WHERE id=$1 AND tenant_id=$2 AND user_id=$3 AND run_id=$4 AND role='user' AND message_index=$5 AND payload_ref=$6 AND payload_hash=$7 AND content_hash=$8 AND content_type='application/json' AND source_kind='conversation_user' AND trust_label='user_asserted' AND finalized_event_id=$9 AND finalized_at=$10)`, command.MessageID, command.Run.TenantID, command.Run.UserID, command.Run.RunID, userIndex, command.Message.Ref, command.Message.Hash, command.ContentHash, identifiers.event, durableTime).Scan(&exact)
		if err != nil {
			return AcceptedMessageRun{}, err
		}
		if !exact {
			return AcceptedMessageRun{}, ErrRunMessageConflict
		}
		if command.CoachContext != nil {
			exact, err = store.exactCoachContextReplay(ctx, tx, command, durableTime)
			if err != nil {
				return AcceptedMessageRun{}, err
			}
			if !exact {
				return AcceptedMessageRun{}, ErrRunMessageConflict
			}
		}
	} else {
		tag, updateErr := tx.Exec(ctx, `UPDATE agent.conversations SET version=$1,last_run_id=$2,updated_at=$3 WHERE tenant_id=$4 AND id=$5 AND user_id=$6 AND version=$7 AND status='active'`, nextVersion, command.Run.RunID, durableTime, command.Run.TenantID, command.Run.ConversationID, command.Run.UserID, command.ExpectedConversationVersion)
		if updateErr != nil || tag.RowsAffected() != 1 {
			return AcceptedMessageRun{}, ErrRunConflict
		}
		userIndex := 0
		if command.CoachContext != nil {
			userIndex = 1
			if err = store.insertCoachContext(ctx, tx, command, durableTime); err != nil {
				return AcceptedMessageRun{}, err
			}
		}
		tag, err = tx.Exec(ctx, `INSERT INTO agent.run_messages(id,tenant_id,user_id,run_id,role,message_index,payload_ref,payload_hash,content_hash,content_type,source_kind,trust_label,finalized_event_id,finalized_at,created_at,updated_at) VALUES($1,$2,$3,$4,'user',$5,$6,$7,$8,'application/json','conversation_user','user_asserted',$9,$10,$10,$10)`, command.MessageID, command.Run.TenantID, command.Run.UserID, command.Run.RunID, userIndex, command.Message.Ref, command.Message.Hash, command.ContentHash, identifiers.event, durableTime)
		if err != nil || tag.RowsAffected() != 1 {
			return AcceptedMessageRun{}, ErrRunMessageConflict
		}
	}
	return AcceptedMessageRun{AcceptedRun: accepted, MessageID: command.MessageID, MessageEventID: identifiers.event, ConversationVersion: nextVersion, AcceptedAt: durableTime, Replayed: replayed}, nil
}

func (store RunStore) conversationMessageIdentifiers(messageID string) (conversationMessageIdentifiers, error) {
	values := make([]string, 3)
	for index, domain := range []string{"conversation-message-appended-event", "conversation-message-publish-outbox", "conversation-message-publish-command"} {
		value, err := ids.DeterministicUUID(store.IDKey, domain, messageID)
		if err != nil {
			return conversationMessageIdentifiers{}, err
		}
		values[index] = value
	}
	return conversationMessageIdentifiers{event: values[0], publishOutbox: values[1], publishCommand: values[2]}, nil
}

func validAcceptMessageRun(command AcceptMessageRunCommand) bool {
	validContext := command.CoachContext == nil || command.ExpectedConversationMode == "coach" && command.ExpectedConversationProfile == behavior.Coach && validCoachContextSource(*command.CoachContext)
	return validContext && command.ExpectedConversationMode != "" && command.ExpectedConversationProfile.Valid() && command.ExpectedConversationProfile == command.Run.BehaviorProfile && validAcceptRun(command.Run) && command.ExpectedConversationVersion > 0 && command.ExpectedConversationVersion < math.MaxUint64 && command.MessageID != "" &&
		validPointer(command.Message) && validSHA256(command.Message.Hash) && validSHA256(command.ContentHash) &&
		validPointer(command.AppendedEvent) && validJSONObject(command.Actor) && equivalentJSONObject(command.Actor, command.Run.Actor)
}

func (store RunStore) insertCoachContext(ctx context.Context, tx pgx.Tx, command AcceptMessageRunCommand, at time.Time) error {
	source := *command.CoachContext
	identifiers, err := store.conversationMessageIdentifiers(source.ID)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO agent.run_messages(id,tenant_id,user_id,run_id,role,message_index,payload_ref,payload_hash,content_hash,content_type,source_kind,trust_label,finalized_event_id,finalized_at,created_at,updated_at) VALUES($1,$2,$3,$4,'user',0,$5,$6,$7,'application/json','product_context','derived',$8,$9,$9,$9)`, source.ID, command.Run.TenantID, command.Run.UserID, command.Run.RunID, source.Message.Ref, source.Message.Hash, source.ContentHash, identifiers.event, at)
	if err != nil || tag.RowsAffected() != 1 {
		return ErrRunMessageConflict
	}
	fence := source.Fence
	tag, err = tx.Exec(ctx, `INSERT INTO agent.coach_context_snapshots(id,tenant_id,user_id,run_id,conversation_id,mission_id,focus_version,mission_version,route_version,claim_set_hash,route_revision_id,route_revision_version,daily_task_id,daily_task_version,preferences_version,manifest,manifest_hash,payload_ref,payload_hash,snapshotted_event_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,'')::uuid,NULLIF($12,0),NULLIF($13,'')::uuid,NULLIF($14,0),$15,$16,$17,$18,$19,$20,$21)`, source.ID, command.Run.TenantID, command.Run.UserID, command.Run.RunID, command.Run.ConversationID, fence.MissionID, fence.FocusVersion, fence.MissionVersion, fence.RouteVersion, fence.ClaimSetHash, fence.RouteRevisionID, fence.RouteRevisionVersion, fence.DailyTaskID, fence.DailyTaskVersion, fence.PreferencesVersion, source.Manifest, source.ManifestHash, source.Message.Ref, source.Message.Hash, identifiers.event, at)
	if err != nil || tag.RowsAffected() != 1 {
		return ErrRunMessageConflict
	}
	return nil
}

func (store RunStore) exactCoachContextReplay(ctx context.Context, tx pgx.Tx, command AcceptMessageRunCommand, at time.Time) (bool, error) {
	source := *command.CoachContext
	identifiers, err := store.conversationMessageIdentifiers(source.ID)
	if err != nil {
		return false, err
	}
	fence := source.Fence
	var exact bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM agent.run_messages m JOIN agent.coach_context_snapshots s ON s.tenant_id=m.tenant_id AND s.id=m.id
		WHERE m.id=$1 AND m.tenant_id=$2 AND m.user_id=$3 AND m.run_id=$4 AND m.role='user' AND m.message_index=0
		  AND m.payload_ref=$5 AND m.payload_hash=$6 AND m.content_hash=$7 AND m.content_type='application/json' AND m.source_kind='product_context' AND m.trust_label='derived' AND m.finalized_event_id=$8 AND m.finalized_at=$9
		  AND s.conversation_id=$10 AND s.mission_id=$11 AND s.focus_version=$12 AND s.mission_version=$13 AND s.route_version=$14 AND s.claim_set_hash=$15
		  AND s.route_revision_id IS NOT DISTINCT FROM NULLIF($16,'')::uuid AND s.route_revision_version IS NOT DISTINCT FROM NULLIF($17,0)
		  AND s.daily_task_id IS NOT DISTINCT FROM NULLIF($18,'')::uuid AND s.daily_task_version IS NOT DISTINCT FROM NULLIF($19,0)
		  AND s.preferences_version=$20 AND s.manifest=$21::jsonb AND s.manifest_hash=$22 AND s.payload_ref=$5 AND s.payload_hash=$6 AND s.snapshotted_event_id=$8 AND s.created_at=$9
	)`, source.ID, command.Run.TenantID, command.Run.UserID, command.Run.RunID, source.Message.Ref, source.Message.Hash, source.ContentHash, identifiers.event, at, command.Run.ConversationID, fence.MissionID, fence.FocusVersion, fence.MissionVersion, fence.RouteVersion, fence.ClaimSetHash, fence.RouteRevisionID, fence.RouteRevisionVersion, fence.DailyTaskID, fence.DailyTaskVersion, fence.PreferencesVersion, source.Manifest, source.ManifestHash).Scan(&exact)
	return exact, err
}

func equivalentJSONObject(left, right json.RawMessage) bool {
	var leftValue, rightValue map[string]any
	return json.Unmarshal(left, &leftValue) == nil && json.Unmarshal(right, &rightValue) == nil && reflect.DeepEqual(leftValue, rightValue)
}
