package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestRecoveryDueUsesStateSpecificDurableDeadline(t *testing.T) {
	at := time.Date(2026, 7, 15, 20, 0, 0, 0, time.UTC)
	tests := []struct {
		status    string
		lease     sql.NullTime
		idle      sql.NullTime
		execution time.Time
		kill      sql.NullTime
		want      bool
	}{
		{status: "provisioning", lease: validNullTime(at), execution: at.Add(time.Hour), want: true},
		{status: "ready", idle: validNullTime(at.Add(time.Microsecond)), execution: at.Add(time.Hour), want: false},
		{status: "idle", idle: validNullTime(at), execution: at.Add(time.Hour), want: true},
		{status: "running", execution: at, want: true},
		{status: "termination_requested", execution: at.Add(time.Hour), kill: validNullTime(at), want: true},
		{status: "terminated", execution: at.Add(-time.Hour), want: false},
	}
	for _, test := range tests {
		state := RecoveryState{SessionStatus: test.status, ProvisionLeaseExpiresAt: test.lease, IdleDeadline: test.idle, ExecutionDeadline: test.execution, KillDeadline: test.kill}
		if got := state.Due(at); got != test.want {
			t.Fatalf("status=%s Due()=%v want %v", test.status, got, test.want)
		}
	}
}

func TestRecoveryIdentityRequiresExactDurableAllocationTuple(t *testing.T) {
	valid := RecoveryIdentity{TenantID: "tenant", SessionID: "session", AllocationID: "allocation", ProvisionAttemptID: "attempt", HostID: "host", MachineID: "machine", GuestCID: 3}
	if !validRecoveryIdentity(valid) || !sameRecoveryIdentity(valid, valid) {
		t.Fatal("valid exact recovery identity rejected")
	}
	changed := valid
	changed.ProvisionAttemptID = "other"
	if sameRecoveryIdentity(valid, changed) {
		t.Fatal("different provision attempt accepted as same recovery identity")
	}
	changed = valid
	changed.GuestCID = 2
	if validRecoveryIdentity(changed) {
		t.Fatal("reserved guest CID accepted")
	}
}

func TestRunCancellationRecoveryAuthorityRequiresExactScope(t *testing.T) {
	at := time.Date(2026, 7, 15, 20, 1, 0, 0, time.UTC)
	command := RecoveryTerminationCommand{LifecycleCommand: LifecycleCommand{
		TenantID: "tenant", SessionID: "session", ExpectedVersion: 3, ObservedAt: at,
		Payload: PayloadPointer{Ref: "encrypted://runtime/cancel", Hash: "hash"}, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: "correlation",
	}, Authority: RecoveryCancellation, Identity: RecoveryIdentity{
		TenantID: "tenant", SessionID: "session", AllocationID: "allocation", ProvisionAttemptID: "attempt", HostID: "host", MachineID: "machine", GuestCID: 3,
	}, RunID: "run", CancellationID: "cancellation", Reason: "run_cancelled"}
	if _, err := (Store{}).RequestRecoveryTermination(context.Background(), command); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("valid cancellation authority failed before store validation: %v", err)
	}
	command.CancellationID = ""
	if _, err := (Store{}).RequestRecoveryTermination(context.Background(), command); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("missing cancellation id error=%v", err)
	}
	command.CancellationID = "cancellation"
	command.Authority = RecoveryDeadline
	if _, err := (Store{}).RequestRecoveryTermination(context.Background(), command); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("deadline authority accepted cancellation scope: %v", err)
	}
	command.Authority, command.RunID, command.CancellationID = RecoveryOwned, "", ""
	command.HostControlHash = bytes.Repeat([]byte{0x42}, 31)
	if _, err := (Store{}).RequestRecoveryTermination(context.Background(), command); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("owned authority accepted short control hash: %v", err)
	}
}

func validNullTime(value time.Time) sql.NullTime { return sql.NullTime{Time: value, Valid: true} }
