package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/platform/ids"
)

type CreateConversationCommand struct {
	ConversationID     string
	TenantID           string
	UserID             string
	MissionID          string
	RouteRevisionID    string
	FocusVersion       uint64
	SubmissionID       string
	SubmissionRevision int
	RubricVersionID    string
	TaskVersion        uint64
	ProjectID          string
	ProjectVersion     uint64
	MilestoneID        string
	MilestoneVersion   uint64
	WorkspaceRevision  string
	Title              *string
	Mode               string
	CorrelationID      string
	Actor              json.RawMessage
	CreatedEvent       PayloadPointer
}

type ConversationResult struct {
	ID        string
	Version   uint64
	Status    string
	UpdatedAt time.Time
	EventID   string
	Replayed  bool
}

type conversationIdentifiers struct {
	event, publishOutbox, publishCommand string
}

// CreateConversation commits the mission-bound conversation projection and
// its immutable creation fact in one tenant-scoped transaction. A caller may
// safely retry the same derived ConversationID, but conflicting material is
// rejected instead of being treated as a replay.
func (store RunStore) CreateConversation(ctx context.Context, command CreateConversationCommand) (ConversationResult, error) {
	if !store.validCore() || store.Pool == nil || !validCreateConversation(command) {
		return ConversationResult{}, ErrInvalidCommand
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ConversationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := store.CreateConversationInTx(ctx, tx, command)
	if err != nil {
		return ConversationResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ConversationResult{}, err
	}
	return result, nil
}

// CreateConversationInTx lets a public control-plane operation commit its
// idempotency response and the Conversation aggregate atomically. The caller
// owns commit or rollback.
func (store RunStore) CreateConversationInTx(ctx context.Context, tx pgx.Tx, command CreateConversationCommand) (ConversationResult, error) {
	if command.RouteRevisionID != "" || command.FocusVersion != 0 || command.SubmissionID != "" || command.SubmissionRevision != 0 || command.RubricVersionID != "" || command.TaskVersion != 0 || hasProjectEvaluationBinding(command) {
		return ConversationResult{}, ErrInvalidCommand
	}
	return store.createConversationInTx(ctx, tx, command, conversationAdmissionPublic)
}

// CreatePlannerConversationInTx admits the internal route-planning
// conversation for an owned draft or active Mission. It is intentionally a
// separate entry point so public conversation admission can never widen its
// active-Mission rule by accidentally setting a flag from request data.
func (store RunStore) CreatePlannerConversationInTx(ctx context.Context, tx pgx.Tx, command CreateConversationCommand) (ConversationResult, error) {
	if command.RouteRevisionID != "" || command.FocusVersion != 0 || command.SubmissionID != "" || command.SubmissionRevision != 0 || command.RubricVersionID != "" || command.TaskVersion != 0 || hasProjectEvaluationBinding(command) {
		return ConversationResult{}, ErrInvalidCommand
	}
	return store.createConversationInTx(ctx, tx, command, conversationAdmissionRoutePlanner)
}

// CreateDailyPlannerConversationInTx is the only admission path for an
// internal daily-planning conversation. It binds the conversation to the
// exact accepted route and Focus version that caused generation, preventing a
// delayed command from silently planning against a newer user intent.
func (store RunStore) CreateDailyPlannerConversationInTx(ctx context.Context, tx pgx.Tx, command CreateConversationCommand) (ConversationResult, error) {
	if command.RouteRevisionID == "" || command.FocusVersion == 0 || command.SubmissionID != "" || command.SubmissionRevision != 0 || command.RubricVersionID != "" || command.TaskVersion != 0 || hasProjectEvaluationBinding(command) {
		return ConversationResult{}, ErrInvalidCommand
	}
	return store.createConversationInTx(ctx, tx, command, conversationAdmissionDailyPlanner)
}

// CreateEvaluatorConversationInTx admits only an exact immutable submission,
// active rubric revision and the submitted DailyTask version being reviewed.
func (store RunStore) CreateEvaluatorConversationInTx(ctx context.Context, tx pgx.Tx, command CreateConversationCommand) (ConversationResult, error) {
	if command.RouteRevisionID != "" || command.FocusVersion != 0 || command.SubmissionID == "" || command.SubmissionRevision < 1 || command.RubricVersionID == "" || command.TaskVersion < 1 || hasProjectEvaluationBinding(command) {
		return ConversationResult{}, ErrInvalidCommand
	}
	return store.createConversationInTx(ctx, tx, command, conversationAdmissionEvaluator)
}

// CreateProjectEvaluatorConversationInTx is the only admission path for a
// Project test evaluator. It fences the exact Project, submitted Milestone and
// Workspace head that were placed in the encrypted evaluator prompt.
func (store RunStore) CreateProjectEvaluatorConversationInTx(ctx context.Context, tx pgx.Tx, command CreateConversationCommand) (ConversationResult, error) {
	if command.RouteRevisionID != "" || command.FocusVersion != 0 || command.SubmissionID != "" || command.SubmissionRevision != 0 || command.RubricVersionID != "" || command.TaskVersion != 0 || !completeProjectEvaluationBinding(command) {
		return ConversationResult{}, ErrInvalidCommand
	}
	return store.createConversationInTx(ctx, tx, command, conversationAdmissionProjectEvaluator)
}

// CreatePortfolioBuilderConversationInTx admits an artifact_builder only for
// a completed owned Project at the exact immutable Workspace head selected by
// the export manifest. It deliberately excludes Milestone fields so this path
// cannot be confused with evaluator admission.
func (store RunStore) CreatePortfolioBuilderConversationInTx(ctx context.Context, tx pgx.Tx, command CreateConversationCommand) (ConversationResult, error) {
	if command.RouteRevisionID != "" || command.FocusVersion != 0 || command.SubmissionID != "" || command.SubmissionRevision != 0 || command.RubricVersionID != "" || command.TaskVersion != 0 || !completePortfolioBuilderBinding(command) {
		return ConversationResult{}, ErrInvalidCommand
	}
	return store.createConversationInTx(ctx, tx, command, conversationAdmissionPortfolioBuilder)
}

type conversationAdmission uint8

const (
	conversationAdmissionPublic conversationAdmission = iota
	conversationAdmissionRoutePlanner
	conversationAdmissionDailyPlanner
	conversationAdmissionEvaluator
	conversationAdmissionProjectEvaluator
	conversationAdmissionPortfolioBuilder
)

func (store RunStore) createConversationInTx(ctx context.Context, tx pgx.Tx, command CreateConversationCommand, admission conversationAdmission) (ConversationResult, error) {
	if tx == nil || !store.validCore() || !validCreateConversation(command) {
		return ConversationResult{}, ErrInvalidCommand
	}
	identifiers, err := store.conversationIdentifiers(command.ConversationID)
	if err != nil {
		return ConversationResult{}, ErrConfiguration
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if store.Now != nil {
		now = store.Now().UTC().Truncate(time.Microsecond)
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return ConversationResult{}, err
	}
	var missionAdmissible bool
	var admissionQuery string
	var admissionArgs []any
	switch admission {
	case conversationAdmissionPublic:
		if command.Mode == "coach" {
			admissionQuery = `SELECT agent.lock_active_focused_mission($1,$2,$3)`
		} else {
			admissionQuery = `SELECT agent.lock_active_owned_mission($1,$2,$3)`
		}
		admissionArgs = []any{command.TenantID, command.UserID, command.MissionID}
	case conversationAdmissionRoutePlanner:
		admissionQuery = `SELECT agent.lock_owned_route_planning_mission($1,$2,$3)`
		admissionArgs = []any{command.TenantID, command.UserID, command.MissionID}
	case conversationAdmissionDailyPlanner:
		admissionQuery = `SELECT agent.lock_owned_daily_planning_mission($1,$2,$3,$4,$5)`
		admissionArgs = []any{command.TenantID, command.UserID, command.MissionID, command.RouteRevisionID, command.FocusVersion}
	case conversationAdmissionEvaluator:
		admissionQuery = `SELECT agent.lock_owned_submission_review($1,$2,$3,$4,$5,$6)`
		admissionArgs = []any{command.TenantID, command.UserID, command.SubmissionID, command.SubmissionRevision, command.RubricVersionID, command.TaskVersion}
	case conversationAdmissionProjectEvaluator:
		admissionQuery = `SELECT agent.lock_owned_project_evaluation($1,$2,$3,$4,$5,$6,$7)`
		admissionArgs = []any{command.TenantID, command.UserID, command.ProjectID, command.ProjectVersion, command.MilestoneID, command.MilestoneVersion, command.WorkspaceRevision}
	case conversationAdmissionPortfolioBuilder:
		admissionQuery = `SELECT agent.lock_owned_portfolio_export($1,$2,$3,$4,$5)`
		admissionArgs = []any{command.TenantID, command.UserID, command.ProjectID, command.ProjectVersion, command.WorkspaceRevision}
	default:
		return ConversationResult{}, ErrInvalidCommand
	}
	if err = tx.QueryRow(ctx, admissionQuery, admissionArgs...).Scan(&missionAdmissible); err != nil {
		return ConversationResult{}, err
	}
	if !missionAdmissible {
		return ConversationResult{}, ErrRunConflict
	}
	tag, err := tx.Exec(ctx, `INSERT INTO agent.conversations(id,tenant_id,user_id,mission_id,version,title,mode,status,created_at,updated_at) VALUES($1,$2,$3,$4,1,$5,$6,'active',$7,$7) ON CONFLICT DO NOTHING`, command.ConversationID, command.TenantID, command.UserID, command.MissionID, command.Title, command.Mode, now)
	if err != nil {
		return ConversationResult{}, err
	}
	replayed := tag.RowsAffected() == 0
	durableTime := now
	if replayed {
		if err = tx.QueryRow(ctx, `SELECT updated_at FROM agent.conversations WHERE id=$1 AND tenant_id=$2 AND user_id=$3 AND mission_id=$4 AND version=1 AND title IS NOT DISTINCT FROM $5 AND mode=$6 AND status='active' AND last_run_id IS NULL`, command.ConversationID, command.TenantID, command.UserID, command.MissionID, command.Title, command.Mode).Scan(&durableTime); errors.Is(err, pgx.ErrNoRows) {
			return ConversationResult{}, ErrRunConflict
		} else if err != nil {
			return ConversationResult{}, err
		}
	}
	created := eventpostgres.Input{
		Event: eventpostgres.Event{
			ID: identifiers.event, TenantID: command.TenantID, UserID: command.UserID,
			EventType: "ConversationCreated", SchemaVersion: 1, AggregateKind: "conversation",
			AggregateID: command.ConversationID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch,
			OccurredAt: durableTime, Actor: command.Actor, CorrelationID: command.CorrelationID,
			PayloadRef: command.CreatedEvent.Ref, PayloadHash: command.CreatedEvent.Hash,
		},
		Commands: []eventpostgres.OutboxCommand{{ID: identifiers.publishOutbox, CommandID: identifiers.publishCommand, CommandType: "events.publish", PayloadRef: command.CreatedEvent.Ref, PayloadHash: command.CreatedEvent.Hash}},
	}
	if _, err = store.Appender.Append(ctx, tx, created); err != nil {
		return ConversationResult{}, err
	}
	return ConversationResult{ID: command.ConversationID, Version: 1, Status: "active", UpdatedAt: durableTime, EventID: identifiers.event, Replayed: replayed}, nil
}

func hasProjectEvaluationBinding(command CreateConversationCommand) bool {
	return command.ProjectID != "" || command.ProjectVersion != 0 || command.MilestoneID != "" || command.MilestoneVersion != 0 || command.WorkspaceRevision != ""
}

func completeProjectEvaluationBinding(command CreateConversationCommand) bool {
	return command.ProjectID != "" && command.ProjectVersion > 0 && command.MilestoneID != "" && command.MilestoneVersion > 0 && command.WorkspaceRevision != ""
}

func completePortfolioBuilderBinding(command CreateConversationCommand) bool {
	return command.ProjectID != "" && command.ProjectVersion > 0 && command.MilestoneID == "" && command.MilestoneVersion == 0 && command.WorkspaceRevision != ""
}

func (store RunStore) conversationIdentifiers(conversationID string) (conversationIdentifiers, error) {
	values := make([]string, 3)
	for index, domain := range []string{"conversation-created-event", "conversation-created-publish-outbox", "conversation-created-publish-command"} {
		value, err := ids.DeterministicUUID(store.IDKey, domain, conversationID)
		if err != nil {
			return conversationIdentifiers{}, err
		}
		values[index] = value
	}
	return conversationIdentifiers{event: values[0], publishOutbox: values[1], publishCommand: values[2]}, nil
}

func validCreateConversation(command CreateConversationCommand) bool {
	validTitle := command.Title == nil || utf8.ValidString(*command.Title) && utf8.RuneCountInString(*command.Title) >= 1 && utf8.RuneCountInString(*command.Title) <= 200
	return command.ConversationID != "" && command.TenantID != "" && command.UserID != "" && command.MissionID != "" && validTitle &&
		(command.Mode == "coach" || command.Mode == "task" || command.Mode == "project") && command.CorrelationID != "" &&
		validJSONObject(command.Actor) && validPointer(command.CreatedEvent)
}
