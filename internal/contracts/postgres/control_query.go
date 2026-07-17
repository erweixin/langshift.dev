package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	contractsapi "github.com/langshift/lites/internal/contracts/api"
)

func (service ControlService) ReadUsage(ctx context.Context, query contractsapi.UsageQuery) (contractsapi.UsageSnapshot, error) {
	query.Reason = strings.TrimSpace(query.Reason)
	if !service.valid() || uuid.Validate(query.TenantID) != nil || uuid.Validate(query.UserID) != nil || uuid.Validate(query.MembershipID) != nil || uuid.Validate(query.SessionID) != nil || query.RequestID == "" || len(query.RequestID) > 256 || query.Reason == "" || len(query.Reason) > 500 {
		return contractsapi.UsageSnapshot{}, contractsapi.ErrValidation
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return contractsapi.UsageSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, query.TenantID); err != nil {
		return contractsapi.UsageSnapshot{}, err
	}
	now := service.now()
	allowed, err := service.authorizeAdminRead(ctx, tx, query.TenantID, query.UserID, query.MembershipID, query.SessionID, now)
	if err != nil {
		return contractsapi.UsageSnapshot{}, err
	}
	if !allowed {
		return contractsapi.UsageSnapshot{}, contractsapi.ErrPermissionDenied
	}
	result := contractsapi.UsageSnapshot{TenantID: query.TenantID, AsOf: now}
	if err = tx.QueryRow(ctx, `SELECT COALESCE(sum(granted_units),0),COALESCE(sum(reserved_units),0),COALESCE(sum(settled_units),0) FROM contracts.credit_buckets WHERE tenant_id=$1 AND starts_at<=$2 AND expires_at>$2`, query.TenantID, now).Scan(&result.GrantedUnits, &result.ReservedUnits, &result.SettledUnits); err != nil {
		return contractsapi.UsageSnapshot{}, err
	}
	result.AvailableUnits = result.GrantedUnits - result.ReservedUnits - result.SettledUnits
	if result.AvailableUnits < 0 {
		return contractsapi.UsageSnapshot{}, errors.New("credit bucket invariant violated")
	}
	err = tx.QueryRow(ctx, `SELECT c.seat_limit,(SELECT count(*) FROM contracts.seat_allocations s WHERE s.tenant_id=c.tenant_id AND s.contract_id=c.id AND s.status='active') FROM contracts.contracts c WHERE c.tenant_id=$1 AND c.status IN ('active','suspended')`, query.TenantID).Scan(&result.SeatLimit, &result.ActiveSeats)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return contractsapi.UsageSnapshot{}, err
	}
	if result.ActiveSeats > result.SeatLimit {
		return contractsapi.UsageSnapshot{}, errors.New("seat allocation invariant violated")
	}
	if err = service.recordAdminAccess(ctx, tx, adminAccessInput{RequestID: query.RequestID, TenantID: query.TenantID, UserID: query.UserID, MembershipID: query.MembershipID, SessionID: query.SessionID, Action: "usage_snapshot_read", ResourceKind: "usage_snapshot", Reason: query.Reason, OccurredAt: now}); err != nil {
		return contractsapi.UsageSnapshot{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return contractsapi.UsageSnapshot{}, err
	}
	return result, nil
}
