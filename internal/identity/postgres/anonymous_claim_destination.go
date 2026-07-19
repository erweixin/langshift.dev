package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/identity/anonymousclaim"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	productpostgres "github.com/langshift/lites/internal/product/postgres"
	productroute "github.com/langshift/lites/internal/product/route"
)

type claimRouteSnapshot struct {
	RouteID, MissionID, SourceRoleProfileID, TargetRoleProfileID  string
	SourceRoleSlug, TargetRoleSlug                                string
	ClaimSetHash, RoutePayloadRef, RoutePayloadHash               string
	GoalPayloadRef, GoalPayloadHash                               string
	InputManifest                                                 json.RawMessage
	AgentProfileSnapshotID, OntologySnapshotID, ContentSnapshotID string
}

type anonymousClaimDailyTaskCommand struct {
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

// AnonymousClaimDestination copies an immutable anonymous route snapshot into
// the authenticated tenant. Source reads and destination writes use separate
// transactions; claim_key and deterministic IDs reconcile unknown commits.
type AnonymousClaimDestination struct {
	Pool           *pgxpool.Pool
	SystemTenantID string
	IdentityKey    []byte
	Payloads       payload.Store
	Appender       eventpostgres.Appender
	Content        productpostgres.ContentCatalog
	StoreEpoch     string
	Now            func() time.Time
}

func (destination AnonymousClaimDestination) CommitDestination(ctx context.Context, saga anonymousclaim.Saga) (string, error) {
	if destination.Pool == nil || destination.SystemTenantID == "" || len(destination.IdentityKey) < 32 || destination.Payloads == nil || destination.StoreEpoch == "" || saga.ID == "" || saga.ClaimKey == "" || saga.TargetTenantID == "" || saga.TargetUserID == "" || saga.MissionID == "" || saga.Status != anonymousclaim.Reserved {
		return "", anonymousclaim.ErrInvariant
	}
	if eventID, found, err := destination.loadCommitted(ctx, saga); err != nil || found {
		return eventID, err
	}
	snapshot, err := destination.loadSource(ctx, saga)
	if err != nil {
		return "", err
	}
	routeID, err := ids.DeterministicUUID(destination.IdentityKey, "anonymous-claim-target-route", saga.ClaimKey)
	if err != nil {
		return "", err
	}
	importID, err := ids.DeterministicUUID(destination.IdentityKey, "anonymous-claim-mission-import", saga.ClaimKey)
	if err != nil {
		return "", err
	}
	eventID, err := ids.DeterministicUUID(destination.IdentityKey, "anonymous-claim-destination-event", saga.ClaimKey)
	if err != nil {
		return "", err
	}
	outboxID, err := ids.DeterministicUUID(destination.IdentityKey, "anonymous-claim-destination-outbox", saga.ClaimKey)
	if err != nil {
		return "", err
	}
	commandID, err := ids.DeterministicUUID(destination.IdentityKey, "anonymous-claim-destination-publish", saga.ClaimKey)
	if err != nil {
		return "", err
	}
	dailyOutboxID, err := ids.DeterministicUUID(destination.IdentityKey, "anonymous-claim-daily-outbox", saga.ClaimKey)
	if err != nil {
		return "", err
	}
	dailyCommandID, err := ids.DeterministicUUID(destination.IdentityKey, "anonymous-claim-daily-command", saga.ClaimKey)
	if err != nil {
		return "", err
	}
	sourceRoutePayload, err := destination.readManifest(ctx, payload.Descriptor{TenantID: destination.SystemTenantID, ObjectID: snapshot.RouteID, Class: "route-revision", ContentType: "application/json"}, snapshot.RoutePayloadRef, snapshot.RoutePayloadHash, true)
	if err != nil {
		return "", err
	}
	routeDocument, err := productroute.DecodeDocument(sourceRoutePayload)
	if err != nil || routeDocumentHasEvidence(routeDocument) {
		// Anonymous Claim currently imports inference-only previews. Persisting an
		// evidence citation without copying its immutable evidence record would
		// create a false, dangling capability assertion after source erasure.
		return "", anonymousclaim.ErrInvariant
	}
	// The accepted Route may intentionally select only a subset of the target
	// role requirements, while the immutable planner manifest retains every
	// requirement for later corrections and replanning. The destination must
	// remap both sets; otherwise an unselected requirement would either retain a
	// cross-tenant source ID or make the fail-closed rewrite abort the Claim.
	sourceCapabilityIDs, err := claimContentCapabilityIDs(routeDocument, snapshot.InputManifest)
	if err != nil {
		return "", err
	}
	sourceCapabilitySlugs, err := destination.loadCapabilitySlugs(ctx, sourceCapabilityIDs)
	if err != nil {
		return "", err
	}
	targetGoalPayload, err := destination.copyManifest(ctx, payload.Descriptor{TenantID: destination.SystemTenantID, ObjectID: snapshot.MissionID, Class: "mission-goal", ContentType: "application/json"}, payload.Descriptor{TenantID: saga.TargetTenantID, ObjectID: saga.MissionID, Class: "mission-goal", ContentType: "application/json"}, snapshot.GoalPayloadRef, snapshot.GoalPayloadHash, false)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	if destination.Now != nil {
		now = destination.Now().UTC()
	}
	eventManifest, err := destination.putJSON(ctx, payload.Descriptor{TenantID: saga.TargetTenantID, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{
		"subject_id": saga.ID, "subject_version": saga.Version + 1, "claim_set_hash": snapshot.ClaimSetHash, "claim_id": saga.ID, "claim_key": saga.ClaimKey,
		"target_tenant_id": saga.TargetTenantID, "target_user_id": saga.TargetUserID, "mission_id": saga.MissionID, "route_revision_id": routeID, "commit_event_id": eventID,
	})
	if err != nil {
		return "", err
	}
	tx, err := destination.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, saga.TargetTenantID); err != nil {
		return "", err
	}
	// Serialize the destination side before changing Focus. Unique rows make the
	// copied mission idempotent, but without this lock two concurrent workers
	// could both advance focus_version before one loses the import insert.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, saga.ClaimKey); err != nil {
		return "", err
	}
	var authorized bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM identity.users u JOIN identity.memberships m ON m.user_id=u.id AND m.tenant_id=$2 WHERE u.id=$1 AND u.status='active' AND u.email_verified_at IS NOT NULL AND m.status='active')`, saga.TargetUserID, saga.TargetTenantID).Scan(&authorized); err != nil || !authorized {
		if err != nil {
			return "", err
		}
		return "", anonymousclaim.ErrInvariant
	}
	if existingEvent, found, loadErr := loadMissionImportTx(ctx, tx, saga); loadErr != nil || found {
		return existingEvent, loadErr
	}
	roleSlugs := []string{snapshot.TargetRoleSlug}
	if snapshot.SourceRoleSlug != "" {
		roleSlugs = append(roleSlugs, snapshot.SourceRoleSlug)
	}
	capabilitySlugs := make([]string, 0, len(sourceCapabilitySlugs))
	for _, slug := range sourceCapabilitySlugs {
		capabilitySlugs = append(capabilitySlugs, slug)
	}
	sort.Strings(capabilitySlugs)
	projection, err := destination.Content.ProjectSlugs(ctx, tx, saga.TargetTenantID, roleSlugs, capabilitySlugs)
	if err != nil {
		return "", err
	}
	roleIDs := map[string]string{snapshot.TargetRoleProfileID: projection.RoleIDs[snapshot.TargetRoleSlug]}
	if snapshot.SourceRoleProfileID != "" {
		roleIDs[snapshot.SourceRoleProfileID] = projection.RoleIDs[snapshot.SourceRoleSlug]
	}
	capabilityIDs := make(map[string]string, len(sourceCapabilitySlugs))
	for sourceID, slug := range sourceCapabilitySlugs {
		capabilityIDs[sourceID] = projection.CapabilityIDs[slug]
	}
	remappedRoute, err := remapRouteDocument(routeDocument, capabilityIDs)
	if err != nil {
		return "", err
	}
	targetRoutePayload, err := destination.Payloads.Put(ctx, payload.Descriptor{TenantID: saga.TargetTenantID, ObjectID: routeID, Class: "route-revision", ContentType: "application/json"}, remappedRoute)
	if err != nil {
		return "", err
	}
	targetInputManifest, err := destination.remapInputManifest(ctx, saga, snapshot.InputManifest, roleIDs, capabilityIDs)
	if err != nil {
		return "", err
	}
	targetSourceRoleID := ""
	if snapshot.SourceRoleProfileID != "" {
		targetSourceRoleID = roleIDs[snapshot.SourceRoleProfileID]
	}
	targetRoleID := roleIDs[snapshot.TargetRoleProfileID]
	if targetRoleID == "" {
		return "", anonymousclaim.ErrInvariant
	}
	missionTag, err := tx.Exec(ctx, `INSERT INTO product.missions(id,tenant_id,user_id,status,source_role_profile_id,target_role_profile_id,goal_payload_ref,goal_payload_hash,route_version,claim_set_hash,current_route_revision_id) VALUES($1,$2,$3,'active',NULLIF($4,'')::uuid,$5,NULLIF($6,''),NULLIF($7,''),1,$8,$9) ON CONFLICT (id) DO NOTHING`, saga.MissionID, saga.TargetTenantID, saga.TargetUserID, targetSourceRoleID, targetRoleID, targetGoalPayload.Ref, targetGoalPayload.Hash, snapshot.ClaimSetHash, routeID)
	if err != nil {
		return "", err
	}
	if missionTag.RowsAffected() == 0 {
		var valid bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product.missions WHERE id=$1 AND tenant_id=$2 AND user_id=$3 AND claim_set_hash=$4 AND current_route_revision_id=$5)`, saga.MissionID, saga.TargetTenantID, saga.TargetUserID, snapshot.ClaimSetHash, routeID).Scan(&valid); err != nil || !valid {
			if err != nil {
				return "", err
			}
			return "", anonymousclaim.ErrInvariant
		}
	}
	routeTag, err := tx.Exec(ctx, `INSERT INTO product.route_revisions(id,tenant_id,user_id,mission_id,route_version,status,claim_set_hash,base_route_version,input_manifest,route_payload_ref,route_payload_hash,agent_profile_snapshot_id,ontology_snapshot_id,content_snapshot_id,accepted_at) VALUES($1,$2,$3,$4,1,'accepted',$5,0,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT (id) DO NOTHING`, routeID, saga.TargetTenantID, saga.TargetUserID, saga.MissionID, snapshot.ClaimSetHash, targetInputManifest, targetRoutePayload.Ref, targetRoutePayload.Hash, snapshot.AgentProfileSnapshotID, snapshot.OntologySnapshotID, snapshot.ContentSnapshotID, now)
	if err != nil {
		return "", err
	}
	if routeTag.RowsAffected() == 0 {
		var valid bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product.route_revisions WHERE id=$1 AND tenant_id=$2 AND user_id=$3 AND mission_id=$4 AND claim_set_hash=$5)`, routeID, saga.TargetTenantID, saga.TargetUserID, saga.MissionID, snapshot.ClaimSetHash).Scan(&valid); err != nil || !valid {
			if err != nil {
				return "", err
			}
			return "", anonymousclaim.ErrInvariant
		}
	}
	var focusVersion uint64
	if err = tx.QueryRow(ctx, `INSERT INTO product.mission_focuses(tenant_id,user_id,mission_id,focus_version,updated_at) VALUES($1,$2,$3,1,$4) ON CONFLICT (tenant_id,user_id) DO UPDATE SET mission_id=EXCLUDED.mission_id,focus_version=product.mission_focuses.focus_version+1,updated_at=EXCLUDED.updated_at RETURNING focus_version`, saga.TargetTenantID, saga.TargetUserID, saga.MissionID, now).Scan(&focusVersion); err != nil {
		return "", err
	}
	dailyDocument := anonymousClaimDailyTaskCommand{SchemaVersion: 1, MissionID: saga.MissionID, UserID: saga.TargetUserID, RouteRevisionID: routeID, ExpectedRouteVersion: 1, ExpectedFocusVersion: focusVersion, ScheduledFor: now.Format("2006-01-02"), Difficulty: "standard", AvailableMinutes: 45, CorrelationID: saga.ID}
	dailyEncoded, err := json.Marshal(dailyDocument)
	if err != nil {
		return "", err
	}
	dailyManifest, err := destination.Payloads.Put(ctx, payload.Descriptor{TenantID: saga.TargetTenantID, ObjectID: dailyCommandID, Class: "product-command", ContentType: "application/json"}, dailyEncoded)
	if err != nil {
		return "", err
	}
	importTag, err := tx.Exec(ctx, `INSERT INTO product.mission_imports(id,tenant_id,user_id,claim_key,mission_id,source_route_revision_id,commit_event_id,imported_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (claim_key) DO NOTHING`, importID, saga.TargetTenantID, saga.TargetUserID, saga.ClaimKey, saga.MissionID, snapshot.RouteID, eventID, now)
	if err != nil {
		return "", err
	}
	if importTag.RowsAffected() == 0 {
		existingEvent, found, loadErr := loadMissionImportTx(ctx, tx, saga)
		if loadErr != nil || !found {
			if loadErr != nil {
				return "", loadErr
			}
			return "", anonymousclaim.ErrInvariant
		}
		return existingEvent, nil
	}
	if _, err = destination.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: saga.TargetTenantID, UserID: saga.TargetUserID, EventType: "AnonymousClaimDestinationCommitted", SchemaVersion: 1, AggregateKind: "anonymous_claim", AggregateID: saga.ID, AggregateVersion: saga.Version + 1, StoreEpoch: destination.StoreEpoch, OccurredAt: now, Actor: json.RawMessage(`{"kind":"system","id":"anonymous-claim-worker"}`), CorrelationID: saga.ID, PayloadRef: eventManifest.Ref, PayloadHash: eventManifest.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: commandID, CommandType: "events.publish", PayloadRef: eventManifest.Ref, PayloadHash: eventManifest.Hash}, {ID: dailyOutboxID, CommandID: dailyCommandID, CommandType: "GenerateDailyTask", TargetAggregateKind: "mission", TargetAggregateID: saga.MissionID, PayloadRef: dailyManifest.Ref, PayloadHash: dailyManifest.Hash}}}); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return eventID, nil
}

func (destination AnonymousClaimDestination) loadSource(ctx context.Context, saga anonymousclaim.Saga) (claimRouteSnapshot, error) {
	tx, err := destination.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return claimRouteSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, destination.SystemTenantID); err != nil {
		return claimRouteSnapshot{}, err
	}
	var snapshot claimRouteSnapshot
	err = tx.QueryRow(ctx, `SELECT r.id::text,m.id::text,COALESCE(m.source_role_profile_id::text,''),m.target_role_profile_id::text,COALESCE(source_role.slug,''),target_role.slug,r.claim_set_hash,r.input_manifest,COALESCE(r.route_payload_ref,''),COALESCE(r.route_payload_hash,''),COALESCE(m.goal_payload_ref,''),COALESCE(m.goal_payload_hash,''),r.agent_profile_snapshot_id,r.ontology_snapshot_id,r.content_snapshot_id FROM identity.onboarding_claims c JOIN product.route_revisions r ON r.id=c.source_route_revision_id AND r.tenant_id=c.tenant_id JOIN product.missions m ON m.id=r.mission_id AND m.tenant_id=r.tenant_id JOIN product.role_profiles target_role ON target_role.tenant_id=m.tenant_id AND target_role.id=m.target_role_profile_id LEFT JOIN product.role_profiles source_role ON source_role.tenant_id=m.tenant_id AND source_role.id=m.source_role_profile_id WHERE c.id=$1 AND c.tenant_id=$2 AND c.claim_key=$3 AND c.status='reserved'`, saga.ID, destination.SystemTenantID, saga.ClaimKey).Scan(&snapshot.RouteID, &snapshot.MissionID, &snapshot.SourceRoleProfileID, &snapshot.TargetRoleProfileID, &snapshot.SourceRoleSlug, &snapshot.TargetRoleSlug, &snapshot.ClaimSetHash, &snapshot.InputManifest, &snapshot.RoutePayloadRef, &snapshot.RoutePayloadHash, &snapshot.GoalPayloadRef, &snapshot.GoalPayloadHash, &snapshot.AgentProfileSnapshotID, &snapshot.OntologySnapshotID, &snapshot.ContentSnapshotID)
	if err != nil {
		return claimRouteSnapshot{}, err
	}
	if snapshot.RoutePayloadRef == "" || snapshot.ClaimSetHash == "" || snapshot.TargetRoleProfileID == "" || snapshot.TargetRoleSlug == "" {
		return claimRouteSnapshot{}, anonymousclaim.ErrInvariant
	}
	if err = tx.Commit(ctx); err != nil {
		return claimRouteSnapshot{}, err
	}
	return snapshot, nil
}

func (destination AnonymousClaimDestination) loadCommitted(ctx context.Context, saga anonymousclaim.Saga) (string, bool, error) {
	tx, err := destination.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, saga.TargetTenantID); err != nil {
		return "", false, err
	}
	eventID, found, err := loadMissionImportTx(ctx, tx, saga)
	if err != nil || !found {
		return eventID, found, err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", false, err
	}
	return eventID, true, nil
}

func loadMissionImportTx(ctx context.Context, tx pgx.Tx, saga anonymousclaim.Saga) (string, bool, error) {
	var missionID, userID, eventID string
	err := tx.QueryRow(ctx, `SELECT mission_id::text,user_id::text,commit_event_id::text FROM product.mission_imports WHERE tenant_id=$1 AND claim_key=$2`, saga.TargetTenantID, saga.ClaimKey).Scan(&missionID, &userID, &eventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if missionID != saga.MissionID || userID != saga.TargetUserID || eventID == "" {
		return "", false, anonymousclaim.ErrInvariant
	}
	return eventID, true, nil
}

func (destination AnonymousClaimDestination) loadCapabilitySlugs(ctx context.Context, capabilityIDs []string) (map[string]string, error) {
	if len(capabilityIDs) == 0 {
		return nil, anonymousclaim.ErrInvariant
	}
	tx, err := destination.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, destination.SystemTenantID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id::text,slug FROM product.capabilities WHERE tenant_id=$1 AND id::text=ANY($2::text[]) AND status='active'`, destination.SystemTenantID, capabilityIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]string, len(capabilityIDs))
	for rows.Next() {
		var id, slug string
		if err = rows.Scan(&id, &slug); err != nil {
			return nil, err
		}
		result[id] = slug
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(result) != len(capabilityIDs) {
		return nil, anonymousclaim.ErrInvariant
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func routeDocumentCapabilityIDs(document productroute.Document) []string {
	unique := map[string]struct{}{}
	add := func(values []string) {
		for _, value := range values {
			unique[value] = struct{}{}
		}
	}
	for _, item := range document.TransferableExperience {
		add(item.CapabilityIDs)
	}
	for _, item := range document.Gaps {
		add(item.CapabilityIDs)
	}
	for _, item := range document.Bridge {
		add(item.FromCapabilityIDs)
		add(item.ToCapabilityIDs)
	}
	for _, item := range document.Stages {
		add(item.CapabilityIDs)
	}
	add(document.FirstTask.CapabilityIDs)
	result := make([]string, 0, len(unique))
	for value := range unique {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func claimContentCapabilityIDs(document productroute.Document, inputManifest json.RawMessage) ([]string, error) {
	unique := make(map[string]struct{})
	for _, capabilityID := range routeDocumentCapabilityIDs(document) {
		unique[capabilityID] = struct{}{}
	}
	decoder := json.NewDecoder(bytes.NewReader(inputManifest))
	decoder.UseNumber()
	var root any
	if decoder.Decode(&root) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || root == nil {
		return nil, anonymousclaim.ErrInvariant
	}
	var collect func(any) error
	collect = func(value any) error {
		switch item := value.(type) {
		case map[string]any:
			for key, child := range item {
				switch key {
				case "capability_id":
					capabilityID, ok := child.(string)
					if !ok || capabilityID == "" {
						return anonymousclaim.ErrInvariant
					}
					unique[capabilityID] = struct{}{}
				case "capability_ids", "from_capability_ids", "to_capability_ids":
					values, ok := child.([]any)
					if !ok {
						return anonymousclaim.ErrInvariant
					}
					for _, raw := range values {
						capabilityID, ok := raw.(string)
						if !ok || capabilityID == "" {
							return anonymousclaim.ErrInvariant
						}
						unique[capabilityID] = struct{}{}
					}
				default:
					if err := collect(child); err != nil {
						return err
					}
				}
			}
		case []any:
			for _, child := range item {
				if err := collect(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := collect(root); err != nil || len(unique) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, anonymousclaim.ErrInvariant
	}
	result := make([]string, 0, len(unique))
	for capabilityID := range unique {
		result = append(result, capabilityID)
	}
	sort.Strings(result)
	return result, nil
}

func routeDocumentHasEvidence(document productroute.Document) bool {
	for _, item := range document.TransferableExperience {
		if len(item.EvidenceIDs) != 0 {
			return true
		}
	}
	for _, item := range document.Gaps {
		if len(item.EvidenceIDs) != 0 {
			return true
		}
	}
	return false
}

func remapRouteDocument(document productroute.Document, capabilityIDs map[string]string) ([]byte, error) {
	remap := func(values []string) ([]string, error) {
		result := make([]string, len(values))
		for index, source := range values {
			target := capabilityIDs[source]
			if target == "" {
				return nil, anonymousclaim.ErrInvariant
			}
			result[index] = target
		}
		return result, nil
	}
	var err error
	for index := range document.TransferableExperience {
		document.TransferableExperience[index].CapabilityIDs, err = remap(document.TransferableExperience[index].CapabilityIDs)
		if err != nil {
			return nil, err
		}
	}
	for index := range document.Gaps {
		document.Gaps[index].CapabilityIDs, err = remap(document.Gaps[index].CapabilityIDs)
		if err != nil {
			return nil, err
		}
	}
	for index := range document.Bridge {
		document.Bridge[index].FromCapabilityIDs, err = remap(document.Bridge[index].FromCapabilityIDs)
		if err != nil {
			return nil, err
		}
		document.Bridge[index].ToCapabilityIDs, err = remap(document.Bridge[index].ToCapabilityIDs)
		if err != nil {
			return nil, err
		}
	}
	for index := range document.Stages {
		document.Stages[index].CapabilityIDs, err = remap(document.Stages[index].CapabilityIDs)
		if err != nil {
			return nil, err
		}
	}
	document.FirstTask.CapabilityIDs, err = remap(document.FirstTask.CapabilityIDs)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	if _, err = productroute.DecodeDocument(encoded); err != nil {
		return nil, anonymousclaim.ErrInvariant
	}
	return encoded, nil
}

func (destination AnonymousClaimDestination) remapInputManifest(ctx context.Context, saga anonymousclaim.Saga, encoded json.RawMessage, roleIDs, capabilityIDs map[string]string) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var root map[string]any
	if decoder.Decode(&root) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || root == nil {
		return nil, anonymousclaim.ErrInvariant
	}
	if mission, ok := root["mission"].(map[string]any); ok {
		mission["id"] = saga.MissionID
	}
	if onboarding, ok := root["onboarding"].(map[string]any); ok {
		sessionID, _ := onboarding["session_id"].(string)
		experience, _ := onboarding["experience_payload"].(map[string]any)
		ref, _ := experience["ref"].(string)
		hash, _ := experience["hash"].(string)
		if sessionID == "" || ref == "" || hash == "" {
			return nil, anonymousclaim.ErrInvariant
		}
		body, err := destination.Payloads.Get(ctx, payload.Descriptor{TenantID: destination.SystemTenantID, ObjectID: sessionID, Class: "onboarding-body", ContentType: "application/json"}, payload.Manifest{Ref: ref, Hash: hash})
		if err != nil || !json.Valid(body) {
			return nil, anonymousclaim.ErrInvariant
		}
		target, err := destination.Payloads.Put(ctx, payload.Descriptor{TenantID: saga.TargetTenantID, ObjectID: sessionID, Class: "onboarding-body", ContentType: "application/json"}, body)
		if err != nil {
			return nil, err
		}
		onboarding["experience_payload"] = map[string]any{"ref": target.Ref, "hash": target.Hash}
	}
	if err := rewriteContentReferences(root, roleIDs, capabilityIDs); err != nil {
		return nil, err
	}
	result, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func rewriteContentReferences(value any, roleIDs, capabilityIDs map[string]string) error {
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			switch key {
			case "source_role_profile_id", "target_role_profile_id", "role_profile_id":
				source, ok := child.(string)
				if !ok {
					if child == nil {
						continue
					}
					return anonymousclaim.ErrInvariant
				}
				if source == "" {
					continue
				}
				target := roleIDs[source]
				if target == "" {
					return anonymousclaim.ErrInvariant
				}
				item[key] = target
			case "capability_id":
				source, ok := child.(string)
				if !ok || capabilityIDs[source] == "" {
					return anonymousclaim.ErrInvariant
				}
				item[key] = capabilityIDs[source]
			case "capability_ids", "from_capability_ids", "to_capability_ids":
				values, ok := child.([]any)
				if !ok {
					return anonymousclaim.ErrInvariant
				}
				for index, raw := range values {
					source, ok := raw.(string)
					if !ok || capabilityIDs[source] == "" {
						return anonymousclaim.ErrInvariant
					}
					values[index] = capabilityIDs[source]
				}
			default:
				if err := rewriteContentReferences(child, roleIDs, capabilityIDs); err != nil {
					return err
				}
			}
		}
	case []any:
		for _, child := range item {
			if err := rewriteContentReferences(child, roleIDs, capabilityIDs); err != nil {
				return err
			}
		}
	}
	return nil
}

func (destination AnonymousClaimDestination) copyManifest(ctx context.Context, sourceDescriptor, targetDescriptor payload.Descriptor, ref, hash string, required bool) (payload.Manifest, error) {
	plaintext, err := destination.readManifest(ctx, sourceDescriptor, ref, hash, required)
	if err != nil || plaintext == nil {
		return payload.Manifest{}, err
	}
	return destination.Payloads.Put(ctx, targetDescriptor, plaintext)
}

func (destination AnonymousClaimDestination) readManifest(ctx context.Context, sourceDescriptor payload.Descriptor, ref, hash string, required bool) ([]byte, error) {
	if ref == "" {
		if required {
			return nil, anonymousclaim.ErrInvariant
		}
		return nil, nil
	}
	source := payload.Manifest{Ref: ref, Hash: hash}
	// Migrations prior to payload-integrity columns stored a JSON-encoded
	// manifest in the ref column. Continue to read those rows while all new
	// writes use the canonical ref/hash columns.
	if hash == "" {
		if err := json.Unmarshal([]byte(ref), &source); err != nil || source.Ref == "" || source.Hash == "" {
			return nil, anonymousclaim.ErrInvariant
		}
	}
	plaintext, err := destination.Payloads.Get(ctx, sourceDescriptor, source)
	if err != nil {
		return nil, err
	}
	return plaintext, nil
}

func (destination AnonymousClaimDestination) putJSON(ctx context.Context, descriptor payload.Descriptor, value any) (payload.Manifest, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return payload.Manifest{}, err
	}
	return destination.Payloads.Put(ctx, descriptor, encoded)
}

var _ anonymousclaim.DestinationWriter = AnonymousClaimDestination{}
