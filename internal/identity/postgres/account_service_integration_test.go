//go:build integration

package postgres

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/identity/api"
	"github.com/langshift/lites/internal/identity/password"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	"github.com/langshift/lites/internal/security/opaque"
)

type accountExportCommandOverrideStore struct {
	payload.Store
	commandID string
	body      []byte
}

func (store accountExportCommandOverrideStore) Get(ctx context.Context, descriptor payload.Descriptor, manifest payload.Manifest) ([]byte, error) {
	if descriptor.Class == "account-export-command" && descriptor.ObjectID == store.commandID {
		return append([]byte(nil), store.body...), nil
	}
	return store.Store.Get(ctx, descriptor, manifest)
}

func TestAccountExportAndErasureAreReauthenticatedIdempotentAndEvented(t *testing.T) {
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, requiredEnv(t, "LITES_TEST_ADMIN_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	pool, err := pgxpool.New(ctx, requiredEnv(t, "LITES_TEST_IDENTITY_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	workerPool, err := pgxpool.New(ctx, requiredEnv(t, "LITES_TEST_ERASURE_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer workerPool.Close()
	now := time.Unix(1_800_001_000, 0).UTC()
	const publicTenantID = "20000000-0000-0000-0000-000000001001"
	const userID = "10000000-0000-0000-0000-000000001001"
	const tenantID = "20000000-0000-0000-0000-000000001002"
	const membershipID = "30000000-0000-0000-0000-000000001001"
	const enterpriseTenantID = "20000000-0000-0000-0000-000000001003"
	const enterpriseMembershipID = "30000000-0000-0000-0000-000000001003"
	const credentialID = "31000000-0000-0000-0000-000000001001"
	const email = "account-lifecycle@example.com"
	const passwordValue = "account lifecycle password"
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users (id,normalized_email,email_verified_at,locale,status) VALUES ($1,$2,$3,'en','active')`, []any{userID, email, now}},
		{`INSERT INTO identity.tenants (id,kind,name,status,region) VALUES ($1,'anonymous_system','Account Public','active','US')`, []any{publicTenantID}},
		{`INSERT INTO identity.tenants (id,kind,name,status,region,owner_user_id) VALUES ($1,'personal','Account Lifecycle','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO identity.tenants (id,kind,name,status,region) VALUES ($1,'enterprise','Account Enterprise','active','CN')`, []any{enterpriseTenantID}},
		{`INSERT INTO identity.memberships (id,tenant_id,user_id,role,status,joined_at) VALUES ($1,$2,$3,'owner','active',$4)`, []any{membershipID, tenantID, userID, now}},
		{`INSERT INTO identity.memberships (id,tenant_id,user_id,role,status,joined_at) VALUES ($1,$2,$3,'member','active',$4)`, []any{enterpriseMembershipID, enterpriseTenantID, userID, now.Add(-time.Hour)}},
	} {
		if _, err = admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	hasher := password.Hasher{Parameters: password.Parameters{Version: password.CurrentVersion, Time: 1, MemoryKiB: 8 * 1024, Threads: 1, KeyLength: 32}, Pepper: bytes.Repeat([]byte{0xc1}, 32)}
	passwordHash, parameters, err := hasher.Hash(passwordValue)
	if err != nil {
		t.Fatal(err)
	}
	encodedParameters, _ := json.Marshal(parameters)
	if _, err = admin.Exec(ctx, `INSERT INTO identity.password_credentials (id,user_id,password_hash,algorithm,parameters,password_changed_at) VALUES ($1,$2,$3,'argon2id',$4,$5)`, credentialID, userID, passwordHash, encodedParameters, now); err != nil {
		t.Fatal(err)
	}
	dummyHash, dummyParameters, _ := hasher.Hash("account lifecycle dummy password")
	blobs := &authMemoryBlobs{values: map[string][]byte{}}
	store := payload.EnvelopeStore{Keys: authKeyProvider{key: payload.Key{ID: "account-vault-key-v1", Material: bytes.Repeat([]byte{0xc2}, 32)}}, Blobs: blobs}
	service := AuthService{
		Pool: pool, Passwords: hasher, PasswordPolicy: password.Policy{Checker: password.NewDigestSet([]string{"known compromised password value"})}, DummyPasswordHash: dummyHash, DummyPasswordParameters: dummyParameters,
		VerificationTokens: opaque.Manager{Purpose: "email-verification", Pepper: bytes.Repeat([]byte{0xc3}, 32)}, PasswordResetTokens: opaque.Manager{Purpose: "password-reset", Pepper: bytes.Repeat([]byte{0xc4}, 32)}, EmailChangeTokens: opaque.Manager{Purpose: "email-change", Pepper: bytes.Repeat([]byte{0xc5}, 32)}, InvitationTokens: opaque.Manager{Purpose: "invitation", Pepper: bytes.Repeat([]byte{0xcc}, 32)},
		SessionPepper: bytes.Repeat([]byte{0xc6}, 32), CSRFPepper: bytes.Repeat([]byte{0xc7}, 32), IdempotencyKeyPepper: bytes.Repeat([]byte{0xc8}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xc9}, 32), IdentityKey: bytes.Repeat([]byte{0xca}, 32), CursorKey: bytes.Repeat([]byte{0xcb}, 32),
		PublicTenantID: publicTenantID, StoreEpoch: "40000000-0000-0000-0000-000000001001", Region: "US", VerificationTTL: 24 * time.Hour, PasswordResetTTL: 30 * time.Minute, EmailChangeTTL: 24 * time.Hour, ReauthenticationTTL: 15 * time.Minute, ErasureGracePeriod: 7 * 24 * time.Hour, SessionTTL: 30 * 24 * time.Hour, IdempotencyTTL: 24 * time.Hour,
		Payloads: store, Random: rand.Reader, Now: func() time.Time { return now },
	}
	metadata := func(requestID, key string) api.RequestMetadata {
		serverRequestID, deriveErr := ids.DeterministicUUID(bytes.Repeat([]byte{0xd3}, 32), "identity-account-integration-request", requestID)
		if deriveErr != nil {
			t.Fatal(deriveErr)
		}
		return api.RequestMetadata{RequestID: serverRequestID, ClientRequestID: requestID, IdempotencyKey: key, ClientIPHash: bytes.Repeat([]byte{0xd1}, 32), UserAgentHash: bytes.Repeat([]byte{0xd2}, 32)}
	}
	login, err := service.Login(ctx, api.LoginCommand{RequestMetadata: metadata("account-login-001", "account-login-key-0001"), NormalizedEmail: email, Password: passwordValue})
	if err != nil {
		t.Fatal(err)
	}
	auth := func(requestID, key string) api.AuthenticatedRequestMetadata {
		return api.AuthenticatedRequestMetadata{RequestMetadata: metadata(requestID, key), UserID: userID, TenantID: tenantID, MembershipID: membershipID, SessionID: login.SessionID}
	}
	account, err := service.GetAccount(ctx, api.AccountQuery{AuthenticatedRequestMetadata: auth("account-get-001", "")})
	if err != nil || account.UserID != userID || account.Version != 1 || account.Timezone != "UTC" || account.DisplayName != nil || !account.EmailVerified {
		t.Fatalf("account=%#v err=%v", account, err)
	}
	firstTenants, err := service.ListTenants(ctx, api.TenantsQuery{AuthenticatedRequestMetadata: auth("tenant-list-001", ""), Limit: 1})
	if err != nil || len(firstTenants.Items) != 1 || firstTenants.NextCursor == nil || !firstTenants.Items[0].Active {
		t.Fatalf("first tenants=%#v err=%v", firstTenants, err)
	}
	secondTenants, err := service.ListTenants(ctx, api.TenantsQuery{AuthenticatedRequestMetadata: auth("tenant-list-002", ""), Limit: 1, Cursor: *firstTenants.NextCursor})
	if err != nil || len(secondTenants.Items) != 1 || secondTenants.Items[0].ID != enterpriseTenantID || secondTenants.Items[0].Active || secondTenants.Items[0].Role != "member" {
		t.Fatalf("second tenants=%#v err=%v", secondTenants, err)
	}
	locale, timezone, displayName := "zh-CN", "Asia/Shanghai", "Account Owner"
	profileUpdate := api.AccountUpdateCommand{AuthenticatedRequestMetadata: auth("account-update-001", "account-update-key-0001"), ExpectedVersion: 1, Changes: api.AccountChanges{Locale: &locale, Timezone: &timezone, DisplayName: &displayName, DisplayNameSet: true}}
	updates := concurrentCalls(t, 12, func() (api.AccountMutationResult, error) { return service.UpdateAccount(ctx, profileUpdate) })
	for _, result := range updates {
		if result != updates[0] || result.ID != userID || result.Version != 2 || result.Status != "updated" {
			t.Fatalf("account update=%#v first=%#v", result, updates[0])
		}
	}
	updatedAccount, err := service.GetAccount(ctx, api.AccountQuery{AuthenticatedRequestMetadata: auth("account-get-002", "")})
	if err != nil || updatedAccount.Version != 2 || updatedAccount.Locale != locale || updatedAccount.Timezone != timezone || updatedAccount.DisplayName == nil || *updatedAccount.DisplayName != displayName {
		t.Fatalf("updated account=%#v err=%v", updatedAccount, err)
	}
	staleProfileUpdate := profileUpdate
	staleProfileUpdate.IdempotencyKey = "account-update-key-0002"
	staleProfileUpdate.ClientRequestID = "account-update-stale"
	if _, err = service.UpdateAccount(ctx, staleProfileUpdate); !errors.Is(err, api.ErrVersionConflict) {
		t.Fatalf("stale profile update=%v", err)
	}
	if _, err = admin.Exec(ctx, `UPDATE identity.sessions SET reauthenticated_at=$1 WHERE id=$2`, now.Add(-16*time.Minute), login.SessionID); err != nil {
		t.Fatal(err)
	}
	stale := api.AccountExportCreateCommand{AuthenticatedRequestMetadata: auth("stale-export-001", "stale-export-key-0001"), Scope: []string{"account"}, Format: "json"}
	if _, err = service.CreateAccountExport(ctx, stale); !errors.Is(err, api.ErrReauthenticationRequired) {
		t.Fatalf("stale reauthentication error=%v", err)
	}
	if _, err = admin.Exec(ctx, `UPDATE identity.sessions SET reauthenticated_at=$1 WHERE id=$2`, now, login.SessionID); err != nil {
		t.Fatal(err)
	}
	export := api.AccountExportCreateCommand{AuthenticatedRequestMetadata: auth("account-export-001", "account-export-key-0001"), Scope: []string{"account", "evidence", "projects"}, Format: "zip"}
	exports := concurrentCalls(t, 12, func() (api.AccountMutationResult, error) { return service.CreateAccountExport(ctx, export) })
	for _, result := range exports {
		if result != exports[0] || result.Version != 1 || result.Status != "requested" {
			t.Fatalf("export=%#v first=%#v", result, exports[0])
		}
	}
	conflictingExport := export
	conflictingExport.Format = "json"
	if _, err = service.CreateAccountExport(ctx, conflictingExport); !errors.Is(err, api.ErrIdempotencyConflict) {
		t.Fatalf("export conflict=%v", err)
	}
	requestedExport, err := service.GetAccountExport(ctx, api.AccountExportQuery{AuthenticatedRequestMetadata: auth("account-export-get-001", ""), ExportID: exports[0].ID})
	if err != nil || requestedExport.Status != "requested" || requestedExport.Version != 1 || requestedExport.CompletedAt != nil || requestedExport.ExpiresAt != nil {
		t.Fatalf("requested export=%#v err=%v", requestedExport, err)
	}
	var delivered eventpostgres.DeliveredCommand
	err = admin.QueryRow(ctx, `SELECT tenant_id::text,store_epoch::text,command_id::text,command_type,aggregate_kind,aggregate_id::text,payload_ref,payload_hash FROM agent.outbox WHERE command_type='account.export.prepare' AND aggregate_id=$1`, exports[0].ID).Scan(&delivered.TenantID, &delivered.StoreEpoch, &delivered.CommandID, &delivered.CommandType, &delivered.AggregateKind, &delivered.AggregateID, &delivered.PayloadRef, &delivered.PayloadHash)
	if err != nil {
		t.Fatal(err)
	}
	inbox := eventpostgres.InboxStore{Pool: workerPool, Epochs: identityEpochAuthority{epoch: service.StoreEpoch}, Tokens: opaque.Manager{Purpose: "account-export-inbox-lease", Pepper: bytes.Repeat([]byte{0xd3}, 32)}, LeaseTTL: time.Hour, Now: func() time.Time { return now }}
	exportStore := AccountExportStore{Pool: workerPool, Payloads: store, Appender: eventpostgres.Appender{}, Inbox: inbox, StoreEpoch: service.StoreEpoch, IDKey: service.IdentityKey, Now: func() time.Time { return now }}
	dispatcher := AccountExportDispatcher{Store: exportStore, Payloads: store, Inbox: inbox, StoreEpoch: service.StoreEpoch, ConsumerName: "account-erasure-worker"}
	dispatched, err := dispatcher.Dispatch(ctx, delivered)
	if err != nil || !dispatched.Completed || dispatched.Replayed || dispatched.Export.Status != "ready" || dispatched.Export.Version != 2 {
		t.Fatalf("dispatch=%#v err=%v", dispatched, err)
	}
	replayedExport, err := dispatcher.Dispatch(ctx, delivered)
	if err != nil || !replayedExport.Completed || !replayedExport.Replayed {
		t.Fatalf("replay=%#v err=%v", replayedExport, err)
	}
	readyExport, err := service.GetAccountExport(ctx, api.AccountExportQuery{AuthenticatedRequestMetadata: auth("account-export-get-002", ""), ExportID: exports[0].ID})
	if err != nil || readyExport.Status != "ready" || readyExport.Version != 2 || readyExport.CompletedAt == nil || readyExport.ExpiresAt == nil || !readyExport.ExpiresAt.After(*readyExport.CompletedAt) {
		t.Fatalf("ready export=%#v err=%v", readyExport, err)
	}
	download, err := service.DownloadAccountExport(ctx, api.AccountExportQuery{AuthenticatedRequestMetadata: auth("account-export-download-001", ""), ExportID: exports[0].ID})
	if err != nil || download.MediaType != "application/zip" || download.ContentHash == "" || len(download.Body) == 0 {
		t.Fatalf("download=%#v err=%v", download, err)
	}
	archive, err := zip.NewReader(bytes.NewReader(download.Body), int64(len(download.Body)))
	if err != nil || len(archive.File) != 1 {
		t.Fatalf("archive files=%d err=%v", len(archive.File), err)
	}
	entry, err := archive.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	exportedJSON, err := io.ReadAll(entry)
	_ = entry.Close()
	var exported struct {
		Data struct {
			Account struct {
				Profile struct {
					NormalizedEmail string `json:"normalized_email"`
				} `json:"profile"`
			} `json:"account"`
		} `json:"data"`
	}
	decodeErr := json.Unmarshal(exportedJSON, &exported)
	if err != nil || decodeErr != nil || exported.Data.Account.Profile.NormalizedEmail != "account-lifecycle@example.com" || bytes.Contains(exportedJSON, passwordHash) || bytes.Contains(exportedJSON, []byte("object_ref")) {
		t.Fatalf("exported archive failed privacy/content contract: err=%v body=%s", err, exportedJSON)
	}
	expiredAt := readyExport.ExpiresAt.Add(time.Second)
	service.Now = func() time.Time { return expiredAt }
	if _, err = admin.Exec(ctx, `UPDATE identity.sessions SET reauthenticated_at=$1 WHERE id=$2`, expiredAt, login.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.DownloadAccountExport(ctx, api.AccountExportQuery{AuthenticatedRequestMetadata: auth("account-export-download-expired", ""), ExportID: exports[0].ID}); !errors.Is(err, api.ErrStateConflict) {
		t.Fatalf("expired download err=%v", err)
	}
	service.Now = func() time.Time { return now }
	if _, err = admin.Exec(ctx, `UPDATE identity.sessions SET reauthenticated_at=$1 WHERE id=$2`, now, login.SessionID); err != nil {
		t.Fatal(err)
	}
	failedExport, err := service.CreateAccountExport(ctx, api.AccountExportCreateCommand{AuthenticatedRequestMetadata: auth("account-export-failed-001", "account-export-key-failed-0001"), Scope: []string{"account"}, Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	var failedDelivery eventpostgres.DeliveredCommand
	err = admin.QueryRow(ctx, `SELECT tenant_id::text,store_epoch::text,command_id::text,command_type,aggregate_kind,aggregate_id::text,payload_ref,payload_hash FROM agent.outbox WHERE command_type='account.export.prepare' AND aggregate_id=$1`, failedExport.ID).Scan(&failedDelivery.TenantID, &failedDelivery.StoreEpoch, &failedDelivery.CommandID, &failedDelivery.CommandType, &failedDelivery.AggregateKind, &failedDelivery.AggregateID, &failedDelivery.PayloadRef, &failedDelivery.PayloadHash)
	if err != nil {
		t.Fatal(err)
	}
	invalidDispatcher := dispatcher
	invalidDispatcher.Payloads = accountExportCommandOverrideStore{Store: store, commandID: failedDelivery.CommandID, body: []byte(`{"request_id":"invalid"}`)}
	failedDispatch, err := invalidDispatcher.Dispatch(ctx, failedDelivery)
	if err != nil || !failedDispatch.Completed || !failedDispatch.TerminalFailure || failedDispatch.Replayed || failedDispatch.Export.Status != "failed" || failedDispatch.Export.Version != 2 {
		t.Fatalf("failed dispatch=%#v err=%v", failedDispatch, err)
	}
	failedReplay, err := invalidDispatcher.Dispatch(ctx, failedDelivery)
	if err != nil || !failedReplay.Completed || !failedReplay.Replayed {
		t.Fatalf("failed replay=%#v err=%v", failedReplay, err)
	}
	failedResource, err := service.GetAccountExport(ctx, api.AccountExportQuery{AuthenticatedRequestMetadata: auth("account-export-failed-get", ""), ExportID: failedExport.ID})
	if err != nil || failedResource.Status != "failed" || failedResource.Version != 2 || failedResource.CompletedAt == nil || failedResource.ExpiresAt != nil || failedResource.FailureCode == nil || *failedResource.FailureCode != accountExportFailureInvalidCommand {
		t.Fatalf("failed resource=%#v err=%v", failedResource, err)
	}
	if _, err = service.DownloadAccountExport(ctx, api.AccountExportQuery{AuthenticatedRequestMetadata: auth("account-export-failed-download", ""), ExportID: failedExport.ID}); !errors.Is(err, api.ErrStateConflict) {
		t.Fatalf("failed export download err=%v", err)
	}
	reason := "I want all private data removed"
	erasure := api.AccountErasureCreateCommand{AuthenticatedRequestMetadata: auth("account-erasure-001", "account-erasure-key-0001"), Reason: &reason}
	erasures := concurrentCalls(t, 12, func() (api.AccountMutationResult, error) { return service.CreateAccountErasure(ctx, erasure) })
	firstErasure := erasures[0]
	for _, result := range erasures {
		if result != firstErasure || result.Version != 1 || result.Status != "requested" {
			t.Fatalf("erasure=%#v first=%#v", result, firstErasure)
		}
	}
	secondActive := api.AccountErasureCreateCommand{AuthenticatedRequestMetadata: auth("account-erasure-active", "account-erasure-key-0002")}
	if _, err = service.CreateAccountErasure(ctx, secondActive); !errors.Is(err, api.ErrStateConflict) {
		t.Fatalf("second active erasure=%v", err)
	}
	staleCancel := api.AccountErasureCancelCommand{AuthenticatedRequestMetadata: auth("account-cancel-stale", "account-cancel-key-0001"), ErasureID: firstErasure.ID, ExpectedVersion: 2}
	if _, err = service.CancelAccountErasure(ctx, staleCancel); !errors.Is(err, api.ErrVersionConflict) {
		t.Fatalf("stale cancel=%v", err)
	}
	cancel := api.AccountErasureCancelCommand{AuthenticatedRequestMetadata: auth("account-cancel-001", "account-cancel-key-0002"), ErasureID: firstErasure.ID, ExpectedVersion: 1}
	cancellations := concurrentCalls(t, 12, func() (api.AccountMutationResult, error) { return service.CancelAccountErasure(ctx, cancel) })
	for _, result := range cancellations {
		if result != cancellations[0] || result.Version != 2 || result.Status != "cancelled" {
			t.Fatalf("cancel=%#v", result)
		}
	}
	secondErasure := api.AccountErasureCreateCommand{AuthenticatedRequestMetadata: auth("account-erasure-002", "account-erasure-key-0003")}
	secondResult, err := service.CreateAccountErasure(ctx, secondErasure)
	if err != nil {
		t.Fatal(err)
	}
	later := now.Add(service.ErasureGracePeriod + time.Second)
	service.Now = func() time.Time { return later }
	if _, err = admin.Exec(ctx, `UPDATE identity.sessions SET reauthenticated_at=$1 WHERE id=$2`, later, login.SessionID); err != nil {
		t.Fatal(err)
	}
	overdue := api.AccountErasureCancelCommand{AuthenticatedRequestMetadata: auth("account-cancel-overdue", "account-cancel-key-0003"), ErasureID: secondResult.ID, ExpectedVersion: 1}
	if _, err = service.CancelAccountErasure(ctx, overdue); !errors.Is(err, api.ErrStateConflict) {
		t.Fatalf("overdue cancel=%v", err)
	}
	service.Now = func() time.Time { return now }
	var exportsCount, erasuresCount, profileEvents, exportEvents, exportReadyEvents, exportFailedEvents, requestedEvents, cancelledEvents, exportWork, erasureWork, delayedErasureWork int
	var deletionRequestedAt *time.Time
	if err = admin.QueryRow(ctx, `SELECT deletion_requested_at FROM identity.users WHERE id=$1`, userID).Scan(&deletionRequestedAt); err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		query string
		out   *int
	}{
		{`SELECT count(*) FROM product.data_export_requests WHERE user_id='10000000-0000-0000-0000-000000001001'`, &exportsCount},
		{`SELECT count(*) FROM identity.account_erasure_requests WHERE user_id='10000000-0000-0000-0000-000000001001'`, &erasuresCount},
		{`SELECT count(*) FROM agent.events WHERE user_id='10000000-0000-0000-0000-000000001001' AND event_type='AccountExportRequested'`, &exportEvents},
		{`SELECT count(*) FROM agent.events WHERE user_id='10000000-0000-0000-0000-000000001001' AND event_type='AccountExportReady'`, &exportReadyEvents},
		{`SELECT count(*) FROM agent.events WHERE user_id='10000000-0000-0000-0000-000000001001' AND event_type='AccountExportFailed'`, &exportFailedEvents},
		{`SELECT count(*) FROM agent.events WHERE user_id='10000000-0000-0000-0000-000000001001' AND event_type='AccountProfileUpdated'`, &profileEvents},
		{`SELECT count(*) FROM agent.events WHERE user_id='10000000-0000-0000-0000-000000001001' AND event_type='AccountErasureRequested'`, &requestedEvents},
		{`SELECT count(*) FROM agent.events WHERE user_id='10000000-0000-0000-0000-000000001001' AND event_type='AccountErasureCancelled'`, &cancelledEvents},
		{`SELECT count(*) FROM agent.outbox WHERE command_type='account.export.prepare'`, &exportWork},
		{`SELECT count(*) FROM agent.outbox WHERE command_type='account.erasure.schedule'`, &erasureWork},
		{`SELECT count(*) FROM agent.outbox o JOIN identity.account_erasure_requests r ON r.tenant_id=o.tenant_id AND r.id=o.aggregate_id WHERE o.command_type='account.erasure.schedule' AND o.available_at=r.scheduled_for`, &delayedErasureWork},
	}
	for _, check := range checks {
		if err = admin.QueryRow(ctx, check.query).Scan(check.out); err != nil {
			t.Fatal(err)
		}
	}
	if exportsCount != 2 || erasuresCount != 2 || profileEvents != 1 || exportEvents != 2 || exportReadyEvents != 1 || exportFailedEvents != 1 || requestedEvents != 2 || cancelledEvents != 1 || exportWork != 2 || erasureWork != 2 || delayedErasureWork != 2 || deletionRequestedAt == nil {
		t.Fatalf("exports=%d erasures=%d profile-events=%d export-events=%d export-ready-events=%d export-failed-events=%d requested=%d cancelled=%d export-work=%d erasure-work=%d delayed-erasure-work=%d deletion=%v", exportsCount, erasuresCount, profileEvents, exportEvents, exportReadyEvents, exportFailedEvents, requestedEvents, cancelledEvents, exportWork, erasureWork, delayedErasureWork, deletionRequestedAt)
	}
	var storedScope []byte
	if err = admin.QueryRow(ctx, `SELECT scope FROM product.data_export_requests WHERE id=$1`, exports[0].ID).Scan(&storedScope); err != nil {
		t.Fatalf("stored scope=%s error=%v", storedScope, err)
	}
	var scopeDocument struct {
		Categories []string `json:"categories"`
		Format     string   `json:"format"`
	}
	if err = json.Unmarshal(storedScope, &scopeDocument); err != nil || scopeDocument.Format != "zip" || len(scopeDocument.Categories) != 3 {
		t.Fatalf("stored scope=%s error=%v", storedScope, err)
	}
	var reasonRef, reasonHash string
	if err = admin.QueryRow(ctx, `SELECT details->>'reason_ref',details->>'reason_hash' FROM identity.security_events WHERE event_type='account_erasure_requested' AND request_id=$1`, erasure.RequestID).Scan(&reasonRef, &reasonHash); err != nil {
		t.Fatal(err)
	}
	encodedReason, err := store.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: firstErasure.ID, Class: "account-erasure-reason", ContentType: "application/json"}, payload.Manifest{Ref: reasonRef, Hash: reasonHash})
	if err != nil || !bytes.Contains(encodedReason, []byte(reason)) {
		t.Fatalf("reason payload=%s error=%v", encodedReason, err)
	}
	for _, stored := range blobs.values {
		if bytes.Contains(stored, []byte(reason)) {
			t.Fatal("plaintext erasure reason reached object storage")
		}
	}
	var piiAudit int
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM identity.security_events WHERE subject_user_id=$1 AND details::text LIKE '%I want all private data removed%'`, userID).Scan(&piiAudit); err != nil || piiAudit != 0 {
		t.Fatalf("PII audit rows=%d err=%v", piiAudit, err)
	}
}
