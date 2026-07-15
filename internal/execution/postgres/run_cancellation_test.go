package postgres

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRequestRunCancellationValidationAndIdentifiers(t *testing.T) {
	valid := RequestRunCancellationCommand{
		CancellationID: "cancel", TenantID: "tenant", UserID: "user", RunID: "run", ExpectedRunVersion: 3,
		Reason: "user_requested", RequestHash: strings.Repeat("a", 64), CorrelationID: "correlation", Actor: json.RawMessage(`{"kind":"user"}`),
		RequestEvent: PayloadPointer{Ref: "encrypted://request", Hash: strings.Repeat("1", 64)}, SettlementEvent: PayloadPointer{Ref: "encrypted://settlement", Hash: strings.Repeat("2", 64)},
		AttemptCancelledEvent: PayloadPointer{Ref: "encrypted://attempt", Hash: strings.Repeat("3", 64)}, ReconcileCommand: PayloadPointer{Ref: "encrypted://reconcile", Hash: strings.Repeat("4", 64)},
	}
	if !validRequestRunCancellation(valid) {
		t.Fatal("valid cancellation command rejected")
	}
	invalidHash := valid
	invalidHash.RequestHash = strings.Repeat("A", 64)
	if validRequestRunCancellation(invalidHash) {
		t.Fatal("uppercase request hash accepted")
	}
	invalidPointer := valid
	invalidPointer.SettlementEvent.Hash = strings.Repeat("z", 64)
	if validRequestRunCancellation(invalidPointer) {
		t.Fatal("non-hex payload hash accepted")
	}

	store := RunStore{IDKey: bytes.Repeat([]byte{0x31}, 32)}
	first, err := store.cancellationIdentifiers(valid.CancellationID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.cancellationIdentifiers(valid.CancellationID)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.requestEvent == first.settlementEvent || first.reconcileCommand == first.reconcileJob {
		t.Fatalf("identifiers are not deterministic and domain-separated: first=%#v second=%#v", first, second)
	}
}
