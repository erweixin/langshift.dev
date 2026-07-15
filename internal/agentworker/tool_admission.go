package agentworker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/authorization"
	"github.com/langshift/lites/internal/toolregistry"
	"github.com/langshift/lites/internal/toolworker"
)

var ErrToolPermission = errors.New("current membership does not authorize tool")

type ToolOverlayEvaluator interface {
	EvaluateSnapshot(context.Context, string, toolregistry.Snapshot) (toolworker.PolicyDecision, error)
}

// PostgresToolAdmission evaluates the current membership rather than trusting
// the role that existed when the Run was accepted. Permission names use the
// reviewed Stage 2 resource.action vocabulary; unknown names fail closed.
type PostgresToolAdmission struct {
	Pool    *pgxpool.Pool
	Overlay ToolOverlayEvaluator
}

func (admission PostgresToolAdmission) Evaluate(ctx context.Context, request ToolAdmissionRequest) (ToolAdmissionDecision, error) {
	if admission.Pool == nil || admission.Overlay == nil || request.TenantID == "" || request.UserID == "" || request.RunID == "" || request.Snapshot.SnapshotID == "" || request.RequestHash == "" || request.NormalizedInputHash == "" {
		return ToolAdmissionDecision{}, ErrToolAdmission
	}
	tx, err := admission.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return ToolAdmissionDecision{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, request.TenantID); err != nil {
		return ToolAdmissionDecision{}, err
	}
	var membershipID, role string
	var version uint64
	err = tx.QueryRow(ctx, `SELECT id::text,version,role FROM identity.memberships WHERE tenant_id=$1 AND user_id=$2 AND status='active'`, request.TenantID, request.UserID).Scan(&membershipID, &version, &role)
	if err != nil || membershipID == "" || version == 0 || !allowsToolPermissions(request.TenantID, request.UserID, role, request.Snapshot.Descriptor.RequiredPermissions) {
		return ToolAdmissionDecision{}, errors.Join(ErrToolPermission, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return ToolAdmissionDecision{}, err
	}
	overlay, err := admission.Overlay.EvaluateSnapshot(ctx, request.TenantID, request.Snapshot)
	if err != nil || !overlay.Allowed || overlay.SnapshotID == "" || !policyDigestPattern.MatchString(overlay.SnapshotHash) || overlay.OverlayVersion == 0 {
		return ToolAdmissionDecision{}, errors.Join(ErrToolAdmission, err)
	}
	decision := ToolAdmissionDecision{
		Decision: "allow", PolicySnapshotID: overlay.SnapshotID, PolicyHash: overlay.SnapshotHash,
		PolicyVersion:      overlay.OverlayVersion,
		PermissionSnapshot: fmt.Sprintf("membership:%s:v%d:role:%s", membershipID, version, role),
	}
	descriptor := request.Snapshot.Descriptor
	if descriptor.EffectClass != "read_only" {
		decision.EffectKey = request.RequestHash
		decision.EffectScope = "tenant:" + request.TenantID
		decision.ProviderID = descriptor.Handler
	}
	return decision, nil
}

func allowsToolPermissions(tenantID, userID, role string, permissions []string) bool {
	if len(permissions) == 0 {
		return false
	}
	for _, permission := range permissions {
		parts := strings.Split(permission, ".")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" || !authorization.Allow(authorization.Request{
			SubjectTenantID: tenantID, SubjectUserID: userID, Role: authorization.Role(role),
			ResourceTenantID: tenantID, ResourceOwnerID: userID,
			Resource: authorization.Resource(parts[0]), Action: authorization.Action(parts[1]),
		}) {
			return false
		}
	}
	return true
}
