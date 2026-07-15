// Package sweeper converts durable Runtime deadlines into fenced termination
// requests. It never kills host processes or releases capacity; the owning
// runtime-host-agent must prove cleanup before completion.
package sweeper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/langshift/lites/internal/payload"
	runtimepostgres "github.com/langshift/lites/internal/runtime/postgres"
)

var ErrConfiguration = errors.New("runtime sweeper configuration is invalid")

type Store interface {
	ListRecoveryTenants(context.Context, string, int, int, int, time.Time) ([]string, error)
	ListDueSessions(context.Context, string, string, int, time.Time) ([]runtimepostgres.RecoveryState, error)
	RequestRecoveryTermination(context.Context, runtimepostgres.RecoveryTerminationCommand) (runtimepostgres.LifecycleResult, error)
}

type TenantLocker interface {
	WithTenantLock(context.Context, string, func(context.Context) error) (bool, error)
}

type Sweeper struct {
	Store       Store
	Payloads    payload.Store
	Locker      TenantLocker
	Now         func() time.Time
	ShardIndex  int
	ShardCount  int
	TenantPage  int
	SessionPage int
}

type Result struct{ Tenants, Requested, Contended int }

type deadlineReceipt struct {
	SchemaVersion int       `json:"schema_version"`
	SessionID     string    `json:"session_id"`
	PreviousState string    `json:"previous_state"`
	Reason        string    `json:"reason"`
	Deadline      time.Time `json:"deadline"`
}

func (sweeper Sweeper) RunOnce(ctx context.Context) (Result, error) {
	if sweeper.Store == nil || sweeper.Payloads == nil || sweeper.Locker == nil || sweeper.ShardCount < 1 || sweeper.ShardIndex < 0 || sweeper.ShardIndex >= sweeper.ShardCount || sweeper.TenantPage < 1 || sweeper.TenantPage > 5000 || sweeper.SessionPage < 1 || sweeper.SessionPage > 1000 {
		return Result{}, ErrConfiguration
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	if sweeper.Now != nil {
		at = sweeper.Now().UTC().Truncate(time.Microsecond)
	}
	result := Result{}
	var tenantCursor string
	for {
		tenants, err := sweeper.Store.ListRecoveryTenants(ctx, tenantCursor, sweeper.TenantPage, sweeper.ShardIndex, sweeper.ShardCount, at)
		if err != nil {
			return result, err
		}
		for _, tenantID := range tenants {
			locked, lockErr := sweeper.Locker.WithTenantLock(ctx, tenantID, func(lockCtx context.Context) error {
				result.Tenants++
				var sessionCursor string
				for {
					states, listErr := sweeper.Store.ListDueSessions(lockCtx, tenantID, sessionCursor, sweeper.SessionPage, at)
					if listErr != nil {
						return listErr
					}
					for _, state := range states {
						if !state.Due(at) || state.TenantID != tenantID {
							return ErrConfiguration
						}
						if requestErr := sweeper.request(lockCtx, state); requestErr != nil {
							return requestErr
						}
						result.Requested++
						sessionCursor = state.SessionID
					}
					if len(states) < sweeper.SessionPage {
						break
					}
				}
				return nil
			})
			if lockErr != nil {
				return result, lockErr
			}
			if !locked {
				result.Contended++
			}
			tenantCursor = tenantID
		}
		if len(tenants) < sweeper.TenantPage {
			break
		}
	}
	return result, nil
}

func (sweeper Sweeper) request(ctx context.Context, state runtimepostgres.RecoveryState) error {
	deadline, reason, err := durableDeadline(state)
	if err != nil {
		return err
	}
	receipt := deadlineReceipt{SchemaVersion: 1, SessionID: state.SessionID, PreviousState: state.SessionStatus, Reason: reason, Deadline: deadline}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	manifest, err := sweeper.Payloads.Put(ctx, payload.Descriptor{TenantID: state.TenantID, ObjectID: fmt.Sprintf("%s:deadline:%d", state.SessionID, state.SessionVersion), Class: "runtime-control-evidence", ContentType: "application/json"}, encoded)
	if err != nil || manifest.Ref == "" || manifest.Hash == "" {
		return errors.Join(err, ErrConfiguration)
	}
	_, err = sweeper.Store.RequestRecoveryTermination(ctx, runtimepostgres.RecoveryTerminationCommand{LifecycleCommand: runtimepostgres.LifecycleCommand{
		TenantID: state.TenantID, SessionID: state.SessionID, ExpectedVersion: state.SessionVersion, ObservedAt: deadline,
		Payload: runtimepostgres.PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, Actor: json.RawMessage(`{"kind":"system","component":"runtime-sweeper"}`), CorrelationID: state.SessionID,
	}, Authority: runtimepostgres.RecoveryDeadline, Identity: state.RecoveryIdentity, Reason: reason})
	return err
}

func durableDeadline(state runtimepostgres.RecoveryState) (time.Time, string, error) {
	switch state.SessionStatus {
	case "provisioning":
		if state.ProvisionLeaseExpiresAt.Valid {
			return state.ProvisionLeaseExpiresAt.Time.UTC().Truncate(time.Microsecond), "provision_lease_expired", nil
		}
	case "ready", "idle":
		if state.IdleDeadline.Valid {
			return state.IdleDeadline.Time.UTC().Truncate(time.Microsecond), "idle_deadline_exceeded", nil
		}
	case "running":
		if !state.ExecutionDeadline.IsZero() {
			return state.ExecutionDeadline.UTC().Truncate(time.Microsecond), "execution_deadline_exceeded", nil
		}
	}
	return time.Time{}, "", ErrConfiguration
}
