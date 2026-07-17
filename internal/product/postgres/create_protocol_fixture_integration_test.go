//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
)

type activeProjectFixture struct {
	ProjectID, TenantID, UserID, MissionID, RouteID string
	ProjectEventID, StoreEpoch, CorrelationID       string
	WorkspaceID, BindingID, WorkspaceEventID        string
	At                                              time.Time
}

func seedActiveProject(t *testing.T, ctx context.Context, admin *pgxpool.Pool, fixture activeProjectFixture) string {
	t.Helper()
	appender := eventpostgres.Appender{Now: func() time.Time { return fixture.At }}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, fixture.TenantID); err != nil {
		t.Fatal(err)
	}
	projectEvent := eventpostgres.Event{ID: fixture.ProjectEventID, TenantID: fixture.TenantID, UserID: fixture.UserID, EventType: "ProjectCreated", SchemaVersion: 1, AggregateKind: "project", AggregateID: fixture.ProjectID, AggregateVersion: 1, StoreEpoch: fixture.StoreEpoch, OccurredAt: fixture.At, Actor: json.RawMessage(`{"kind":"integration_fixture"}`), CorrelationID: fixture.CorrelationID, PayloadRef: "encrypted://integration/project-created", PayloadHash: createFixtureHash(fixture.ProjectID, "created")}
	if _, err = appender.Append(ctx, tx, fixtureEventInput(projectEvent)); err != nil {
		t.Fatal(err)
	}
	briefHash := createFixtureHash(fixture.ProjectID, "brief")
	briefManifestHash := createFixtureHash(fixture.ProjectID, "brief-manifest")
	if _, err = tx.Exec(ctx, `INSERT INTO product.projects(id,tenant_id,user_id,mission_id,accepted_route_revision_id,status,project_kind,title,brief_ref,brief_hash,brief_manifest_hash,created_event_id,last_event_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,'active','writing','Integration Project','encrypted://brief',$6,$7,$8,$8,$9,$9)`, fixture.ProjectID, fixture.TenantID, fixture.UserID, fixture.MissionID, fixture.RouteID, briefHash, briefManifestHash, fixture.ProjectEventID, fixture.At); err != nil {
		t.Fatal(err)
	}
	workspaceHash := ""
	if fixture.BindingID != "" {
		workspaceHash = createFixtureHash(fixture.ProjectID, "workspace-manifest")
		workspaceEvent := eventpostgres.Event{ID: fixture.WorkspaceEventID, TenantID: fixture.TenantID, UserID: fixture.UserID, EventType: "ProjectWorkspaceBound", SchemaVersion: 1, AggregateKind: "project_workspace", AggregateID: fixture.BindingID, AggregateVersion: 1, StoreEpoch: fixture.StoreEpoch, OccurredAt: fixture.At, Actor: json.RawMessage(`{"kind":"integration_fixture"}`), CorrelationID: fixture.CorrelationID, PayloadRef: "encrypted://integration/workspace-bound", PayloadHash: createFixtureHash(fixture.BindingID, "bound")}
		if _, err = appender.Append(ctx, tx, fixtureEventInput(workspaceEvent)); err != nil {
			t.Fatal(err)
		}
		manifest := json.RawMessage(`{"schema_version":1,"branch":"project/b5","base_revision":"git:base","head_revision":"git:head"}`)
		if _, err = tx.Exec(ctx, `INSERT INTO product.project_workspace_bindings(id,tenant_id,user_id,project_id,workspace_id,branch_name,base_revision,head_revision,binding_manifest_hash,binding_manifest,created_event_id,last_event_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,'project/b5','git:base','git:head',$6,$7,$8,$8,$9,$9)`, fixture.BindingID, fixture.TenantID, fixture.UserID, fixture.ProjectID, fixture.WorkspaceID, workspaceHash, manifest, fixture.WorkspaceEventID, fixture.At); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return workspaceHash
}

func completeProjectFixture(t *testing.T, ctx context.Context, admin *pgxpool.Pool, fixture activeProjectFixture, completionEventID string) {
	t.Helper()
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	appender := eventpostgres.Appender{Now: func() time.Time { return fixture.At }}
	event := eventpostgres.Event{ID: completionEventID, TenantID: fixture.TenantID, UserID: fixture.UserID, EventType: "ProjectCompleted", SchemaVersion: 1, AggregateKind: "project", AggregateID: fixture.ProjectID, AggregateVersion: 2, StoreEpoch: fixture.StoreEpoch, OccurredAt: fixture.At, Actor: json.RawMessage(`{"kind":"integration_fixture"}`), CorrelationID: fixture.CorrelationID, PayloadRef: "encrypted://integration/project-completed", PayloadHash: createFixtureHash(fixture.ProjectID, "completed")}
	if _, err = appender.Append(ctx, tx, fixtureEventInput(event)); err != nil {
		t.Fatal(err)
	}
	completionManifest := json.RawMessage(`{"schema_version":1,"fixture":true}`)
	if _, err = tx.Exec(ctx, `UPDATE product.projects SET version=2,status='completed',reflection_ref='encrypted://reflection',reflection_hash=$1,reflection_manifest_hash=$2,completion_manifest=$3,completion_manifest_hash=$4,completion_event_id=$5,last_event_id=$5,completed_at=$6,updated_at=$6 WHERE tenant_id=$7 AND user_id=$8 AND id=$9 AND version=1`, createFixtureHash(fixture.ProjectID, "reflection"), createFixtureHash(fixture.ProjectID, "reflection-manifest"), completionManifest, createFixtureHash(string(completionManifest)), completionEventID, fixture.At, fixture.TenantID, fixture.UserID, fixture.ProjectID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func createFixtureHash(parts ...string) string {
	digest := sha256.Sum256([]byte(parts[0] + "\x00" + jsonString(parts[1:])))
	return hex.EncodeToString(digest[:])
}

func fixtureEventInput(event eventpostgres.Event) eventpostgres.Input {
	return eventpostgres.Input{Event: event, Commands: []eventpostgres.OutboxCommand{{
		ID: createFixtureUUID(event.ID, "outbox"), CommandID: createFixtureUUID(event.ID, "publish"),
		CommandType: "events.publish", PayloadRef: event.PayloadRef, PayloadHash: event.PayloadHash,
	}}}
}

func createFixtureUUID(parts ...string) string {
	digest := sha256.Sum256([]byte(parts[0] + "\x00" + jsonString(parts[1:])))
	value := digest[:16]
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

func jsonString(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
