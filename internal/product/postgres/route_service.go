package postgres

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/idempotency"
	idempotencypostgres "github.com/langshift/lites/internal/idempotency/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	productapi "github.com/langshift/lites/internal/product/api"
)

const (
	routeGenerateOperation = "routes.generate.v2"
	routeAcceptOperation   = "routes.accept.v2"
	routeResponseClass     = "product-route-idempotency"
	routeEventClass        = "event-payload"
	routePayloadClass      = "route-revision"
	routePageSize          = 50
)

var routeDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type RouteService struct {
	Pool                 *pgxpool.Pool
	Store                RouteStore
	Payloads             payload.Store
	IDKey                []byte
	IdempotencyKeyPepper []byte
	RequestDigestPepper  []byte
	CursorKey            []byte
	IdempotencyTTL       time.Duration
	BehaviorProfile      string
	BehaviorEnvironment  string
	OntologySnapshotID   string
	ContentSnapshotID    string
	Now                  func() time.Time
}

type routeInputManifest struct {
	SchemaVersion          int                      `json:"schema_version"`
	Mission                routeMissionSnapshot     `json:"mission"`
	Onboarding             *routeOnboardingSnapshot `json:"onboarding,omitempty"`
	ClaimRevisions         []routeClaimRevision     `json:"claim_revisions"`
	EvidenceRevisions      []routeEvidenceRevision  `json:"evidence_revisions"`
	TargetRequirements     []routeTargetRequirement `json:"target_requirements"`
	AgentProfile           routeBehaviorBinding     `json:"agent_profile"`
	AgentProfileSnapshotID string                   `json:"agent_profile_snapshot_id"`
	OntologySnapshotID     string                   `json:"ontology_snapshot_id"`
	ContentSnapshotID      string                   `json:"content_snapshot_id"`
}

// routeOnboardingSnapshot binds the planner to the exact encrypted intake
// revision without copying sensitive free text into PostgreSQL. The planner
// resolves this immutable manifest only while constructing its encrypted run
// message.
type routeOnboardingSnapshot struct {
	SessionID         string           `json:"session_id"`
	Version           uint64           `json:"version"`
	CurrentRoleInput  json.RawMessage  `json:"current_role_input"`
	TargetRoleInput   json.RawMessage  `json:"target_role_input"`
	ExperiencePayload payload.Manifest `json:"experience_payload"`
}

// routeBehaviorBinding freezes the exact append-only deployment selected at
// route-generation admission. SnapshotID alone is insufficient: a later
// promotion can legitimately reuse the profile/environment while changing
// channel sequence, and a delayed planner command must never drift to it.
type routeBehaviorBinding struct {
	Profile     string    `json:"profile"`
	Environment string    `json:"environment"`
	ChannelID   string    `json:"channel_id"`
	Sequence    uint64    `json:"sequence"`
	SnapshotID  string    `json:"snapshot_id"`
	ActivatedAt time.Time `json:"activated_at"`
}

type routeMissionSnapshot struct {
	ID                  string  `json:"id"`
	Version             uint64  `json:"version"`
	RouteVersion        uint64  `json:"route_version"`
	ClaimSetHash        string  `json:"claim_set_hash"`
	SourceRoleProfileID *string `json:"source_role_profile_id"`
	TargetRoleProfileID string  `json:"target_role_profile_id"`
}

type routeClaimRevision struct {
	ID                string    `json:"id"`
	IdentityID        string    `json:"identity_id"`
	Revision          int       `json:"revision"`
	RowVersion        uint64    `json:"row_version"`
	CapabilityID      string    `json:"capability_id"`
	Status            string    `json:"status"`
	Origin            string    `json:"origin"`
	VerificationLevel string    `json:"verification_level"`
	StatementRef      string    `json:"statement_ref"`
	EvidenceIDs       []string  `json:"evidence_ids"`
	RecordedAt        time.Time `json:"recorded_at"`
}

type routeTargetRequirement struct {
	ID               string `json:"id"`
	RowVersion       uint64 `json:"row_version"`
	RoleProfileID    string `json:"role_profile_id"`
	CapabilityID     string `json:"capability_id"`
	RequirementLevel string `json:"requirement_level"`
	Rationale        string `json:"rationale"`
	Revision         int    `json:"revision"`
}

type routeEvidenceRevision struct {
	ID          string     `json:"id"`
	RowVersion  uint64     `json:"row_version"`
	Type        string     `json:"type"`
	Status      string     `json:"status"`
	SourceKind  string     `json:"source_kind"`
	SourceID    *string    `json:"source_id"`
	PayloadRef  string     `json:"payload_ref"`
	ContentHash string     `json:"content_hash"`
	RecordedAt  time.Time  `json:"recorded_at"`
	Invalidated *time.Time `json:"invalidated_at"`
}

type routeCursor struct {
	TenantID string    `json:"tenant_id"`
	UserID   string    `json:"user_id"`
	Mission  string    `json:"mission_id"`
	Created  time.Time `json:"created_at"`
	ID       string    `json:"id"`
}

type dailyTaskCommandDocument struct {
	SchemaVersion        int    `json:"schema_version"`
	MissionID            string `json:"mission_id"`
	UserID               string `json:"user_id"`
	RouteRevisionID      string `json:"route_revision_id"`
	ExpectedRouteVersion uint64 `json:"expected_route_version"`
	ExpectedFocusVersion uint64 `json:"expected_focus_version"`
	ScheduledFor         string `json:"scheduled_for"`
	Difficulty           string `json:"difficulty"`
	AvailableMinutes     int    `json:"available_minutes"`
	CorrelationID        string `json:"correlation_id"`
}

func (service RouteService) Generate(ctx context.Context, command productapi.GenerateRouteCommand) (productapi.RouteGenerationResult, error) {
	if !service.valid() || !validMissionMetadata(command.CommandMetadata) || command.MissionID == "" || !routeDigestPattern.MatchString(command.ExpectedClaimSetHash) {
		return productapi.RouteGenerationResult{}, productapi.ErrValidation
	}
	canonical, err := json.Marshal(struct {
		RequestID            string `json:"request_id"`
		MissionID            string `json:"mission_id"`
		ExpectedRouteVersion uint64 `json:"expected_route_version"`
		ExpectedClaimSetHash string `json:"expected_claim_set_hash"`
	}{command.ClientRequestID, command.MissionID, command.ExpectedRouteVersion, command.ExpectedClaimSetHash})
	if err != nil {
		return productapi.RouteGenerationResult{}, productapi.ErrDependencyUnavailable
	}
	input, descriptor, err := service.idempotencyInput(command.CommandMetadata, routeGenerateOperation, canonical)
	if err != nil {
		return productapi.RouteGenerationResult{}, productapi.ErrDependencyUnavailable
	}
	if result, found, loadErr := service.loadGeneration(ctx, input, descriptor); loadErr != nil {
		return productapi.RouteGenerationResult{}, service.mapError(loadErr)
	} else if found {
		return result, nil
	}
	if err = service.Store.requireRouteEpoch(ctx); err != nil {
		return productapi.RouteGenerationResult{}, service.mapError(err)
	}
	revisionID, plannerCommandID, err := service.generationIDs(input.RecordID)
	if err != nil {
		return productapi.RouteGenerationResult{}, productapi.ErrDependencyUnavailable
	}
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		manifest, err := service.resolveInputManifest(ctx, tx, command)
		if err != nil {
			return idempotency.Response{}, err
		}
		manifestJSON, err := json.Marshal(manifest)
		if err != nil {
			return idempotency.Response{}, err
		}
		manifestHash := sha256.Sum256(manifestJSON)
		eventPayload, err := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: revisionID, Class: routeEventClass, ContentType: "application/json"}, map[string]any{
			"subject_id": revisionID, "subject_version": 1, "request_id": command.ClientRequestID, "claim_set_hash": command.ExpectedClaimSetHash, "mission_id": command.MissionID, "route_revision_id": revisionID, "base_route_version": command.ExpectedRouteVersion, "input_manifest_hash": hex.EncodeToString(manifestHash[:]), "profile_snapshot_id": manifest.AgentProfileSnapshotID,
		})
		if err != nil {
			return idempotency.Response{}, err
		}
		stored, err := service.Store.BeginGenerationInTx(ctx, tx, BeginRouteGenerationCommand{MutationID: input.RecordID, RevisionID: revisionID, TenantID: command.TenantID, UserID: command.UserID, MissionID: command.MissionID, ExpectedRouteVersion: command.ExpectedRouteVersion, ExpectedClaimSetHash: command.ExpectedClaimSetHash, PlannerCommandID: plannerCommandID, InputManifest: manifestJSON, AgentProfileSnapshotID: manifest.AgentProfileSnapshotID, OntologySnapshotID: manifest.OntologySnapshotID, ContentSnapshotID: manifest.ContentSnapshotID, CorrelationID: command.RequestID, Actor: missionActor(command.UserID, command.SessionID), RequestedEvent: PayloadPointer{Ref: eventPayload.Ref, Hash: eventPayload.Hash}})
		if err != nil {
			return idempotency.Response{}, err
		}
		result := productapi.RouteGenerationResult{RouteRevisionID: stored.RevisionID, RevisionVersion: stored.RevisionVersion, Status: stored.Status, PlannerCommandID: stored.PlannerCommandID, EventID: stored.EventID}
		return service.storeResponse(ctx, descriptor, http.StatusAccepted, "application/vnd.lites.route-generation.v2+json", stored.RevisionVersion, result)
	})
	if err != nil {
		return productapi.RouteGenerationResult{}, service.mapError(err)
	}
	result, err := service.readGeneration(ctx, descriptor, response)
	if err != nil {
		return productapi.RouteGenerationResult{}, service.mapError(err)
	}
	result.Replayed = replayed
	return result, nil
}

func (service RouteService) Accept(ctx context.Context, command productapi.AcceptRouteCommand) (productapi.RouteAcceptanceResult, error) {
	if !service.valid() || !validMissionMetadata(command.CommandMetadata) || command.RouteRevisionID == "" || command.ExpectedRevisionVersion == 0 || !routeDigestPattern.MatchString(command.ExpectedClaimSetHash) {
		return productapi.RouteAcceptanceResult{}, productapi.ErrValidation
	}
	canonical, err := json.Marshal(struct {
		RequestID               string `json:"request_id"`
		RouteRevisionID         string `json:"route_revision_id"`
		ExpectedRevisionVersion uint64 `json:"expected_revision_version"`
		ExpectedRouteVersion    uint64 `json:"expected_route_version"`
		ExpectedClaimSetHash    string `json:"expected_claim_set_hash"`
	}{command.ClientRequestID, command.RouteRevisionID, command.ExpectedRevisionVersion, command.ExpectedRouteVersion, command.ExpectedClaimSetHash})
	if err != nil {
		return productapi.RouteAcceptanceResult{}, productapi.ErrDependencyUnavailable
	}
	input, descriptor, err := service.idempotencyInput(command.CommandMetadata, routeAcceptOperation, canonical)
	if err != nil {
		return productapi.RouteAcceptanceResult{}, productapi.ErrDependencyUnavailable
	}
	if result, found, loadErr := service.loadAcceptance(ctx, input, descriptor); loadErr != nil {
		return productapi.RouteAcceptanceResult{}, service.mapError(loadErr)
	} else if found {
		return result, nil
	}
	if err = service.Store.requireRouteEpoch(ctx); err != nil {
		return productapi.RouteAcceptanceResult{}, service.mapError(err)
	}
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		var missionID string
		var previousRouteID *string
		var missionStatus string
		err := tx.QueryRow(ctx, `SELECT r.mission_id::text,m.current_route_revision_id::text,m.status FROM product.route_revisions r JOIN product.missions m ON m.tenant_id=r.tenant_id AND m.id=r.mission_id WHERE r.tenant_id=$1 AND r.user_id=$2 AND r.id=$3 FOR UPDATE OF m`, command.TenantID, command.UserID, command.RouteRevisionID).Scan(&missionID, &previousRouteID, &missionStatus)
		if err != nil {
			return idempotency.Response{}, err
		}
		eventObjectID, err := ids.DeterministicUUID(service.IDKey, "route-accepted:event-payload", input.RecordID)
		if err != nil {
			return idempotency.Response{}, err
		}
		eventPayload, err := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: eventObjectID, Class: routeEventClass, ContentType: "application/json"}, map[string]any{
			"subject_id": missionID, "subject_version": command.ExpectedRouteVersion + 1, "request_id": command.ClientRequestID, "claim_set_hash": command.ExpectedClaimSetHash, "mission_id": missionID, "route_revision_id": command.RouteRevisionID, "route_version": command.ExpectedRouteVersion + 1, "previous_route_revision_id": previousRouteID,
		})
		if err != nil {
			return idempotency.Response{}, err
		}
		storeCommand := AcceptRouteCommand{MutationID: input.RecordID, RevisionID: command.RouteRevisionID, TenantID: command.TenantID, UserID: command.UserID, ExpectedRevisionVersion: command.ExpectedRevisionVersion, ExpectedRouteVersion: command.ExpectedRouteVersion, ExpectedClaimSetHash: command.ExpectedClaimSetHash, CorrelationID: command.RequestID, Actor: missionActor(command.UserID, command.SessionID), AcceptedEvent: PayloadPointer{Ref: eventPayload.Ref, Hash: eventPayload.Hash}}
		if missionStatus == "active" {
			var focusedMissionID *string
			var focusVersion uint64
			focusErr := tx.QueryRow(ctx, `SELECT mission_id::text,focus_version FROM product.mission_focuses WHERE tenant_id=$1 AND user_id=$2`, command.TenantID, command.UserID).Scan(&focusedMissionID, &focusVersion)
			if focusErr != nil && !errors.Is(focusErr, pgx.ErrNoRows) {
				return idempotency.Response{}, focusErr
			}
			if focusedMissionID != nil && *focusedMissionID == missionID && focusVersion > 0 {
				dailyCommandID, idErr := ids.DeterministicUUID(service.IDKey, "route-accepted:daily-command", input.RecordID)
				if idErr != nil {
					return idempotency.Response{}, idErr
				}
				dailyDocument, documentErr := service.dailyCommandDocument(ctx, tx, command, missionID, focusVersion)
				if documentErr != nil {
					return idempotency.Response{}, documentErr
				}
				dailyPayload, putErr := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: dailyCommandID, Class: missionCommandClass, ContentType: "application/json"}, dailyDocument)
				if putErr != nil {
					return idempotency.Response{}, putErr
				}
				storeCommand.DailyTaskCommandID = dailyCommandID
				storeCommand.DailyTaskCommand = PayloadPointer{Ref: dailyPayload.Ref, Hash: dailyPayload.Hash}
			}
		}
		stored, err := service.Store.AcceptInTx(ctx, tx, storeCommand)
		if err != nil {
			return idempotency.Response{}, err
		}
		result := productapi.RouteAcceptanceResult{RouteRevisionID: stored.RevisionID, RevisionVersion: stored.RevisionVersion, MissionID: stored.MissionID, MissionVersion: stored.MissionVersion, RouteVersion: stored.RouteVersion, CurrentRouteRevisionID: stored.CurrentRouteRevisionID, Status: stored.Status, EventID: stored.EventID}
		return service.storeResponse(ctx, descriptor, http.StatusOK, "application/vnd.lites.route-acceptance.v2+json", stored.RevisionVersion, result)
	})
	if err != nil {
		return productapi.RouteAcceptanceResult{}, service.mapError(err)
	}
	result, err := service.readAcceptance(ctx, descriptor, response)
	if err != nil {
		return productapi.RouteAcceptanceResult{}, service.mapError(err)
	}
	result.Replayed = replayed
	return result, nil
}

func (service RouteService) List(ctx context.Context, query productapi.RouteListQuery) (productapi.RouteListResult, error) {
	if !service.valid() || query.TenantID == "" || query.UserID == "" || query.MissionID == "" {
		return productapi.RouteListResult{}, productapi.ErrValidation
	}
	var cursor *routeCursor
	if query.Cursor != "" {
		decoded, err := service.decodeCursor(query.Cursor)
		if err != nil || decoded.TenantID != query.TenantID || decoded.UserID != query.UserID || decoded.Mission != query.MissionID {
			return productapi.RouteListResult{}, productapi.ErrValidation
		}
		cursor = &decoded
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return productapi.RouteListResult{}, service.mapError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, query.TenantID); err != nil {
		return productapi.RouteListResult{}, service.mapError(err)
	}
	var owner bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product.missions WHERE tenant_id=$1 AND user_id=$2 AND id=$3)`, query.TenantID, query.UserID, query.MissionID).Scan(&owner); err != nil {
		return productapi.RouteListResult{}, service.mapError(err)
	}
	if !owner {
		return productapi.RouteListResult{}, productapi.ErrResourceNotFound
	}
	args := []any{query.TenantID, query.UserID, query.MissionID, routePageSize + 1}
	statement := `SELECT id::text,version,mission_id::text,route_version,base_route_version,status,claim_set_hash,input_manifest,COALESCE(route_payload_ref,''),COALESCE(route_payload_hash,''),agent_profile_snapshot_id,ontology_snapshot_id,content_snapshot_id,planner_command_id::text,planner_run_id::text,accepted_at,stale_reason,failure_reason,created_at,updated_at FROM product.route_revisions WHERE tenant_id=$1 AND user_id=$2 AND mission_id=$3`
	if cursor != nil {
		statement += ` AND (created_at < $5 OR (created_at=$5 AND id < $6::uuid))`
		args = append(args, cursor.Created, cursor.ID)
	}
	statement += ` ORDER BY created_at DESC,id DESC LIMIT $4`
	rows, err := tx.Query(ctx, statement, args...)
	if err != nil {
		return productapi.RouteListResult{}, service.mapError(err)
	}
	defer rows.Close()
	items := make([]productapi.RouteRevisionResource, 0, routePageSize+1)
	for rows.Next() {
		var item productapi.RouteRevisionResource
		var routeRef, routeHash string
		if err = rows.Scan(&item.ID, &item.Version, &item.MissionID, &item.RouteVersion, &item.BaseRouteVersion, &item.Status, &item.ClaimSetHash, &item.InputManifest, &routeRef, &routeHash, &item.AgentProfileSnapshotID, &item.OntologySnapshotID, &item.ContentSnapshotID, &item.PlannerCommandID, &item.PlannerRunID, &item.AcceptedAt, &item.StaleReason, &item.FailureReason, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return productapi.RouteListResult{}, service.mapError(err)
		}
		if routeRef != "" {
			encoded, getErr := service.Payloads.Get(ctx, payload.Descriptor{TenantID: query.TenantID, ObjectID: item.ID, Class: routePayloadClass, ContentType: "application/json"}, payload.Manifest{Ref: routeRef, Hash: routeHash})
			if getErr != nil || !validJSONObject(encoded) {
				return productapi.RouteListResult{}, service.mapError(errors.Join(payload.ErrIntegrity, getErr))
			}
			item.Route = encoded
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return productapi.RouteListResult{}, service.mapError(err)
	}
	var next *string
	if len(items) > routePageSize {
		items = items[:routePageSize]
		last := items[len(items)-1]
		encoded, encodeErr := service.encodeCursor(routeCursor{TenantID: query.TenantID, UserID: query.UserID, Mission: query.MissionID, Created: last.CreatedAt, ID: last.ID})
		if encodeErr != nil {
			return productapi.RouteListResult{}, service.mapError(encodeErr)
		}
		next = &encoded
	}
	if err = tx.Commit(ctx); err != nil {
		return productapi.RouteListResult{}, service.mapError(err)
	}
	return productapi.RouteListResult{Items: items, NextCursor: next}, nil
}

func (service RouteService) resolveInputManifest(ctx context.Context, tx pgx.Tx, command productapi.GenerateRouteCommand) (routeInputManifest, error) {
	manifest := routeInputManifest{SchemaVersion: 4, ClaimRevisions: []routeClaimRevision{}, EvidenceRevisions: []routeEvidenceRevision{}, TargetRequirements: []routeTargetRequirement{}, OntologySnapshotID: service.OntologySnapshotID, ContentSnapshotID: service.ContentSnapshotID}
	err := tx.QueryRow(ctx, `SELECT id::text,version,route_version,claim_set_hash,source_role_profile_id::text,target_role_profile_id::text FROM product.missions WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, command.TenantID, command.UserID, command.MissionID).Scan(&manifest.Mission.ID, &manifest.Mission.Version, &manifest.Mission.RouteVersion, &manifest.Mission.ClaimSetHash, &manifest.Mission.SourceRoleProfileID, &manifest.Mission.TargetRoleProfileID)
	if err != nil {
		return routeInputManifest{}, err
	}
	if manifest.Mission.RouteVersion != command.ExpectedRouteVersion || manifest.Mission.ClaimSetHash != command.ExpectedClaimSetHash {
		return routeInputManifest{}, ErrRouteConflict
	}
	var onboarding routeOnboardingSnapshot
	var encodedExperienceManifest *string
	err = tx.QueryRow(ctx, `SELECT id::text,version,current_role_input,target_role_input,experience_payload_ref FROM identity.onboarding_sessions WHERE tenant_id=$1 AND user_id=$2 AND mission_id=$3 ORDER BY created_at DESC,id DESC LIMIT 1`, command.TenantID, command.UserID, command.MissionID).Scan(&onboarding.SessionID, &onboarding.Version, &onboarding.CurrentRoleInput, &onboarding.TargetRoleInput, &encodedExperienceManifest)
	if err == nil {
		if encodedExperienceManifest == nil || json.Unmarshal([]byte(*encodedExperienceManifest), &onboarding.ExperiencePayload) != nil || onboarding.ExperiencePayload.Ref == "" || onboarding.ExperiencePayload.Hash == "" || !validJSONObject(onboarding.CurrentRoleInput) || !validJSONObject(onboarding.TargetRoleInput) {
			return routeInputManifest{}, payload.ErrIntegrity
		}
		manifest.Onboarding = &onboarding
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return routeInputManifest{}, err
	}
	manifest.AgentProfile.Profile = service.BehaviorProfile
	manifest.AgentProfile.Environment = service.BehaviorEnvironment
	err = tx.QueryRow(ctx, `SELECT channel_id::text,sequence,snapshot_id,activated_at FROM agent.behavior_channel_deployments WHERE tenant_id=$1 AND profile_name=$2 AND environment=$3 ORDER BY sequence DESC LIMIT 1`, command.TenantID, service.BehaviorProfile, service.BehaviorEnvironment).Scan(&manifest.AgentProfile.ChannelID, &manifest.AgentProfile.Sequence, &manifest.AgentProfile.SnapshotID, &manifest.AgentProfile.ActivatedAt)
	if err != nil {
		return routeInputManifest{}, err
	}
	manifest.AgentProfileSnapshotID = manifest.AgentProfile.SnapshotID
	claimRows, err := tx.Query(ctx, `SELECT DISTINCT ON (claim_identity_id) id::text,claim_identity_id::text,claim_revision,version,capability_id::text,status,origin,verification_level,statement_ref,ARRAY(SELECT link.evidence_id::text FROM product.claim_evidence_links link WHERE link.tenant_id=$1 AND link.claim_id=product.capability_claims.id AND link.relation='supports' ORDER BY link.evidence_id),recorded_at FROM product.capability_claims WHERE tenant_id=$1 AND user_id=$2 AND mission_id=$3 ORDER BY claim_identity_id,claim_revision DESC,id DESC`, command.TenantID, command.UserID, command.MissionID)
	if err != nil {
		return routeInputManifest{}, err
	}
	for claimRows.Next() {
		var claim routeClaimRevision
		if err = claimRows.Scan(&claim.ID, &claim.IdentityID, &claim.Revision, &claim.RowVersion, &claim.CapabilityID, &claim.Status, &claim.Origin, &claim.VerificationLevel, &claim.StatementRef, &claim.EvidenceIDs, &claim.RecordedAt); err != nil {
			claimRows.Close()
			return routeInputManifest{}, err
		}
		manifest.ClaimRevisions = append(manifest.ClaimRevisions, claim)
	}
	err = claimRows.Err()
	claimRows.Close()
	if err != nil {
		return routeInputManifest{}, err
	}
	requirementRows, err := tx.Query(ctx, `SELECT id::text,version,role_profile_id::text,capability_id::text,requirement_level,rationale,revision FROM product.role_capability_requirements WHERE tenant_id=$1 AND role_profile_id=$2 ORDER BY capability_id,revision,id`, command.TenantID, manifest.Mission.TargetRoleProfileID)
	if err != nil {
		return routeInputManifest{}, err
	}
	for requirementRows.Next() {
		var requirement routeTargetRequirement
		if err = requirementRows.Scan(&requirement.ID, &requirement.RowVersion, &requirement.RoleProfileID, &requirement.CapabilityID, &requirement.RequirementLevel, &requirement.Rationale, &requirement.Revision); err != nil {
			requirementRows.Close()
			return routeInputManifest{}, err
		}
		manifest.TargetRequirements = append(manifest.TargetRequirements, requirement)
	}
	err = requirementRows.Err()
	requirementRows.Close()
	if err != nil {
		return routeInputManifest{}, err
	}
	evidenceRows, err := tx.Query(ctx, `SELECT id::text,version,evidence_type,status,source_kind,source_id::text,payload_ref,content_hash,recorded_at,invalidated_at FROM product.evidence WHERE tenant_id=$1 AND user_id=$2 AND mission_id=$3 ORDER BY id`, command.TenantID, command.UserID, command.MissionID)
	if err != nil {
		return routeInputManifest{}, err
	}
	for evidenceRows.Next() {
		var evidence routeEvidenceRevision
		if err = evidenceRows.Scan(&evidence.ID, &evidence.RowVersion, &evidence.Type, &evidence.Status, &evidence.SourceKind, &evidence.SourceID, &evidence.PayloadRef, &evidence.ContentHash, &evidence.RecordedAt, &evidence.Invalidated); err != nil {
			evidenceRows.Close()
			return routeInputManifest{}, err
		}
		manifest.EvidenceRevisions = append(manifest.EvidenceRevisions, evidence)
	}
	err = evidenceRows.Err()
	evidenceRows.Close()
	return manifest, err
}

func (service RouteService) dailyCommandDocument(ctx context.Context, tx pgx.Tx, command productapi.AcceptRouteCommand, missionID string, focusVersion uint64) (dailyTaskCommandDocument, error) {
	return dailyCommandDocumentFor(ctx, tx, command.TenantID, command.UserID, missionID, command.RouteRevisionID, command.ExpectedRouteVersion+1, focusVersion, command.RequestID, service.Now)
}

func dailyCommandDocumentFor(ctx context.Context, tx pgx.Tx, tenantID, userID, missionID, routeRevisionID string, routeVersion, focusVersion uint64, correlationID string, nowFn func() time.Time) (dailyTaskCommandDocument, error) {
	timezone := "UTC"
	difficulty := "standard"
	availableMinutes := 45
	var storedTimezone string
	var coachPreferences json.RawMessage
	err := tx.QueryRow(ctx, `SELECT timezone,coach_preferences FROM product.user_preferences WHERE tenant_id=$1 AND user_id=$2`, tenantID, userID).Scan(&storedTimezone, &coachPreferences)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return dailyTaskCommandDocument{}, err
	}
	if err == nil {
		timezone = storedTimezone
		var selected struct {
			DailyMinutes int    `json:"daily_minutes"`
			Difficulty   string `json:"difficulty"`
		}
		if len(coachPreferences) > 0 && json.Unmarshal(coachPreferences, &selected) != nil {
			return dailyTaskCommandDocument{}, payload.ErrIntegrity
		}
		if selected.DailyMinutes != 0 {
			if selected.DailyMinutes < 5 || selected.DailyMinutes > 480 {
				return dailyTaskCommandDocument{}, payload.ErrIntegrity
			}
			availableMinutes = selected.DailyMinutes
		}
		if selected.Difficulty != "" {
			if selected.Difficulty != "easier" && selected.Difficulty != "standard" && selected.Difficulty != "harder" {
				return dailyTaskCommandDocument{}, payload.ErrIntegrity
			}
			difficulty = selected.Difficulty
		}
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return dailyTaskCommandDocument{}, payload.ErrIntegrity
	}
	now := time.Now().UTC()
	if nowFn != nil {
		now = nowFn().UTC()
	}
	return dailyTaskCommandDocument{
		SchemaVersion: 1, MissionID: missionID, UserID: userID,
		RouteRevisionID: routeRevisionID, ExpectedRouteVersion: routeVersion,
		ExpectedFocusVersion: focusVersion, ScheduledFor: now.In(location).Format("2006-01-02"),
		Difficulty: difficulty, AvailableMinutes: availableMinutes, CorrelationID: correlationID,
	}, nil
}

func (service RouteService) idempotencyInput(metadata productapi.CommandMetadata, operation string, canonical []byte) (idempotencypostgres.Input, payload.Descriptor, error) {
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return idempotencypostgres.Input{}, payload.Descriptor{}, err
	}
	recordID, err := ids.DeterministicUUID(service.IDKey, "product-idempotency:"+operation, metadata.TenantID+"\x00"+metadata.UserID+"\x00"+metadata.IdempotencyKey)
	if err != nil {
		return idempotencypostgres.Input{}, payload.Descriptor{}, err
	}
	input := idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: metadata.TenantID, UserID: metadata.UserID, OperationID: operation}, RawKey: metadata.IdempotencyKey, RequestHash: requestHash, RequestID: metadata.RequestID}
	return input, payload.Descriptor{TenantID: metadata.TenantID, ObjectID: recordID, Class: routeResponseClass, ContentType: "application/json"}, nil
}

func (service RouteService) generationIDs(seed string) (string, string, error) {
	revisionID, err := ids.DeterministicUUID(service.IDKey, "route-generation:revision", seed)
	if err != nil {
		return "", "", err
	}
	plannerCommandID, err := ids.DeterministicUUID(service.IDKey, "route-generation:planner-command", seed)
	return revisionID, plannerCommandID, err
}

func (service RouteService) storeResponse(ctx context.Context, descriptor payload.Descriptor, status int, contentType string, version uint64, value any) (idempotency.Response, error) {
	manifest, err := service.putJSON(ctx, descriptor, value)
	if err != nil {
		return idempotency.Response{}, err
	}
	return idempotency.Response{Status: status, ContentType: contentType, PayloadRef: manifest.Ref, Hash: manifest.Hash, ResourceVersion: version}, nil
}

func (service RouteService) putJSON(ctx context.Context, descriptor payload.Descriptor, value any) (payload.Manifest, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return payload.Manifest{}, err
	}
	return service.Payloads.Put(ctx, descriptor, encoded)
}

func (service RouteService) loadGeneration(ctx context.Context, input idempotencypostgres.Input, descriptor payload.Descriptor) (productapi.RouteGenerationResult, bool, error) {
	response, found, err := service.executor().LoadCompleted(ctx, input)
	if err != nil || !found {
		return productapi.RouteGenerationResult{}, found, err
	}
	result, err := service.readGeneration(ctx, descriptor, response)
	result.Replayed = err == nil
	return result, true, err
}

func (service RouteService) loadAcceptance(ctx context.Context, input idempotencypostgres.Input, descriptor payload.Descriptor) (productapi.RouteAcceptanceResult, bool, error) {
	response, found, err := service.executor().LoadCompleted(ctx, input)
	if err != nil || !found {
		return productapi.RouteAcceptanceResult{}, found, err
	}
	result, err := service.readAcceptance(ctx, descriptor, response)
	result.Replayed = err == nil
	return result, true, err
}

func (service RouteService) readGeneration(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (productapi.RouteGenerationResult, error) {
	var result productapi.RouteGenerationResult
	err := service.readResponse(ctx, descriptor, response, &result)
	return result, err
}

func (service RouteService) readAcceptance(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (productapi.RouteAcceptanceResult, error) {
	var result productapi.RouteAcceptanceResult
	err := service.readResponse(ctx, descriptor, response, &result)
	return result, err
}

func (service RouteService) readResponse(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response, target any) error {
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return payload.ErrIntegrity
	}
	return nil
}

func (service RouteService) executor() idempotencypostgres.Executor {
	return idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
}

func (service RouteService) encodeCursor(cursor routeCursor) (string, error) {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, service.CursorKey)
	_, _ = mac.Write(encoded)
	return base64.RawURLEncoding.EncodeToString(append(encoded, mac.Sum(nil)...)), nil
}

func (service RouteService) decodeCursor(value string) (routeCursor, error) {
	if len(value) > 2048 {
		return routeCursor{}, productapi.ErrValidation
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) <= sha256.Size {
		return routeCursor{}, productapi.ErrValidation
	}
	body, signature := decoded[:len(decoded)-sha256.Size], decoded[len(decoded)-sha256.Size:]
	mac := hmac.New(sha256.New, service.CursorKey)
	_, _ = mac.Write(body)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return routeCursor{}, productapi.ErrValidation
	}
	var cursor routeCursor
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cursor) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || cursor.TenantID == "" || cursor.UserID == "" || cursor.Mission == "" || cursor.ID == "" || cursor.Created.IsZero() {
		return routeCursor{}, productapi.ErrValidation
	}
	return cursor, nil
}

func (service RouteService) valid() bool {
	return service.Pool != nil && service.Store.Pool == service.Pool && service.Payloads != nil && len(service.IDKey) >= 32 && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && len(service.CursorKey) >= 32 && service.IdempotencyTTL > 0 && service.BehaviorProfile != "" && service.BehaviorEnvironment != "" && service.OntologySnapshotID != "" && service.ContentSnapshotID != ""
}

func (service RouteService) mapError(err error) error {
	switch {
	case errors.Is(err, pgx.ErrNoRows), errors.Is(err, ErrRouteNotFound):
		return productapi.ErrResourceNotFound
	case errors.Is(err, idempotency.ErrKeyConflict), errors.Is(err, idempotency.ErrInProgress):
		return productapi.ErrIdempotencyConflict
	case errors.Is(err, ErrRouteConflict):
		return productapi.ErrStateConflict
	case errors.Is(err, ErrRouteResultStale):
		return productapi.ErrRouteResultStale
	case errors.Is(err, ErrInvalidCommand):
		return productapi.ErrValidation
	default:
		return errors.Join(productapi.ErrDependencyUnavailable, err)
	}
}

var _ productapi.RouteService = RouteService{}
