package postgres

import (
	"database/sql"
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

func validNullTime(value time.Time) sql.NullTime { return sql.NullTime{Time: value, Valid: true} }
