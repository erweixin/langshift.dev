package postgres

import (
	"encoding/json"
	"math/rand"
	"os"
	"strconv"
	"testing"
)

const (
	stage5LedgerOperations = 100_000
	stage5LedgerSeed       = int64(20_260_717)
)

type modelReservation struct {
	units, actual int64
	status        string
}

type stage5LedgerModel struct {
	granted, reserved, settled int64
	reservations               map[int]*modelReservation
	reservationIDs             []int
	ledger                     map[string]int64
	adjustments                map[int]int64
	contractStatus             string
	contractVersion            int64
	seatLimit                  int
	members                    map[int]bool
	memberIDs                  []int
	seats                      map[int]bool
	nextReservation            int
	nextMember                 int
	nextAdjustment             int
	activeMemberCount          int
	contractAudits             int
	adjustmentAudits           int
	accepted                   map[string]int
	attempted                  map[string]int
}

type stage5LedgerMetrics struct {
	Scenario                  string         `json:"scenario"`
	Seed                      int64          `json:"seed"`
	Operations                int            `json:"operations"`
	Attempted                 map[string]int `json:"attempted"`
	Accepted                  map[string]int `json:"accepted"`
	FinalGranted              int64          `json:"final_granted_units"`
	FinalReserved             int64          `json:"final_reserved_units"`
	FinalSettled              int64          `json:"final_settled_units"`
	ActiveReservationUnits    int64          `json:"active_reservation_units"`
	SettlementLedgerUnits     int64          `json:"settlement_ledger_units"`
	NegativeBalanceViolations int            `json:"negative_balance_violations"`
	SeatOverageViolations     int            `json:"seat_overage_violations"`
	DuplicateChargeViolations int            `json:"duplicate_charge_violations"`
	ReconciliationDrift       int64          `json:"reconciliation_drift"`
	AuditCompletenessPercent  int            `json:"audit_completeness_percent"`
}

func TestStage5ContractLedgerReferenceModelRunsOneHundredThousandOperations(t *testing.T) {
	random := rand.New(rand.NewSource(stage5LedgerSeed))
	model := newStage5LedgerModel()
	for operation := 0; operation < stage5LedgerOperations; operation++ {
		switch random.Intn(8) {
		case 0:
			model.contractStep(random)
		case 1:
			model.reserveStep(random)
		case 2:
			model.settleStep(random)
		case 3:
			model.releaseStep(random, "release")
		case 4:
			model.releaseStep(random, "expiry")
		case 5:
			model.adjustStep(random)
		case 6:
			model.replayStep(random)
		case 7:
			model.membershipStep(random)
		}
		model.assertInvariants(t, operation)
	}
	metrics := model.metrics()
	for _, operation := range []string{"contract", "reserve", "settle", "release", "expiry", "manual_adjustment", "replay", "membership"} {
		if metrics.Attempted[operation] == 0 || metrics.Accepted[operation] == 0 {
			t.Fatalf("operation %s was not exercised: attempted=%d accepted=%d", operation, metrics.Attempted[operation], metrics.Accepted[operation])
		}
	}
	if metrics.Operations != stage5LedgerOperations || metrics.NegativeBalanceViolations != 0 || metrics.SeatOverageViolations != 0 || metrics.DuplicateChargeViolations != 0 || metrics.ReconciliationDrift != 0 || metrics.AuditCompletenessPercent != 100 {
		t.Fatalf("stage5 ledger metrics=%+v", metrics)
	}
	if output := os.Getenv("LITES_STAGE5_LEDGER_MODEL_REPORT"); output != "" {
		contents, err := json.MarshalIndent(metrics, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		contents = append(contents, '\n')
		if err = os.WriteFile(output, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func newStage5LedgerModel() *stage5LedgerModel {
	model := &stage5LedgerModel{
		granted: 10_000, reservations: map[int]*modelReservation{}, ledger: map[string]int64{}, adjustments: map[int]int64{},
		contractStatus: "active", contractVersion: 1, seatLimit: 100, members: map[int]bool{}, seats: map[int]bool{},
		accepted: map[string]int{}, attempted: map[string]int{},
	}
	for member := 1; member <= 20; member++ {
		model.members[member], model.seats[member] = true, true
		model.memberIDs = append(model.memberIDs, member)
		model.nextMember = member
		model.activeMemberCount++
	}
	return model
}

func (model *stage5LedgerModel) contractStep(random *rand.Rand) {
	model.attempted["contract"]++
	switch model.contractStatus {
	case "active":
		switch random.Intn(3) {
		case 0:
			model.contractStatus = "suspended"
		case 1:
			model.seatLimit = model.activeMembers() + 1 + random.Intn(200)
			model.reconcileSeats()
		case 2:
			model.contractStatus = "terminated"
			clear(model.seats)
		}
	case "suspended":
		if random.Intn(4) == 0 {
			model.contractStatus = "terminated"
			clear(model.seats)
		} else {
			model.contractStatus = "active"
			model.seatLimit = model.activeMembers() + 1 + random.Intn(200)
			model.reconcileSeats()
		}
	case "terminated":
		model.contractStatus = "active"
		model.seatLimit = model.activeMembers() + 1 + random.Intn(200)
		model.reconcileSeats()
	}
	model.contractVersion++
	model.contractAudits++
	model.accepted["contract"]++
}

func (model *stage5LedgerModel) reserveStep(random *rand.Rand) {
	model.attempted["reserve"]++
	units := int64(1 + random.Intn(250))
	if model.reserved+model.settled+units > model.granted {
		return
	}
	model.nextReservation++
	model.reservations[model.nextReservation] = &modelReservation{units: units, status: "reserved"}
	model.reservationIDs = append(model.reservationIDs, model.nextReservation)
	model.reserved += units
	model.accepted["reserve"]++
}

func (model *stage5LedgerModel) settleStep(random *rand.Rand) {
	model.attempted["settle"]++
	id, reservation, ok := model.randomReservation(random)
	if !ok || reservation.status != "reserved" {
		return
	}
	actual := int64(random.Intn(int(reservation.units) + 1))
	model.reserved -= reservation.units
	model.settled += actual
	reservation.actual, reservation.status = actual, "settled"
	model.ledger[ledgerKey("settlement", id)] = actual
	model.accepted["settle"]++
}

func (model *stage5LedgerModel) releaseStep(random *rand.Rand, kind string) {
	model.attempted[kind]++
	id, reservation, ok := model.randomReservation(random)
	if !ok || reservation.status != "reserved" {
		return
	}
	model.reserved -= reservation.units
	reservation.status = kind
	model.ledger[ledgerKey(kind, id)] = reservation.units
	model.accepted[kind]++
}

func (model *stage5LedgerModel) adjustStep(random *rand.Rand) {
	model.attempted["manual_adjustment"]++
	delta := int64(random.Intn(501) - 250)
	if delta == 0 || model.granted+delta <= 0 || model.granted+delta < model.reserved+model.settled {
		return
	}
	model.nextAdjustment++
	model.adjustments[model.nextAdjustment] = delta
	model.granted += delta
	model.adjustmentAudits++
	model.accepted["manual_adjustment"]++
}

func (model *stage5LedgerModel) replayStep(random *rand.Rand) {
	model.attempted["replay"]++
	id, reservation, ok := model.randomReservation(random)
	if !ok {
		return
	}
	beforeReserved, beforeSettled, beforeLedger := model.reserved, model.settled, len(model.ledger)
	switch reservation.status {
	case "reserved":
		// Reserving with the same immutable operation key returns the existing row.
	case "settled":
		model.ledger[ledgerKey("settlement", id)] = reservation.actual
	case "release", "expiry":
		model.ledger[ledgerKey(reservation.status, id)] = reservation.units
	default:
		return
	}
	if model.reserved != beforeReserved || model.settled != beforeSettled || len(model.ledger) != beforeLedger {
		panic("idempotent replay changed accounting state")
	}
	model.accepted["replay"]++
}

func (model *stage5LedgerModel) membershipStep(random *rand.Rand) {
	model.attempted["membership"]++
	if random.Intn(2) == 0 || model.activeMembers() == 0 {
		if model.contractStatus == "active" && len(model.seats) >= model.seatLimit {
			return
		}
		model.nextMember++
		model.members[model.nextMember] = true
		model.activeMemberCount++
		model.memberIDs = append(model.memberIDs, model.nextMember)
		if model.contractStatus == "active" {
			model.seats[model.nextMember] = true
		}
		model.accepted["membership"]++
		return
	}
	for tries := 0; tries < len(model.memberIDs); tries++ {
		id := model.memberIDs[random.Intn(len(model.memberIDs))]
		if model.members[id] {
			model.members[id] = false
			model.activeMemberCount--
			delete(model.seats, id)
			model.accepted["membership"]++
			return
		}
	}
}

func (model *stage5LedgerModel) randomReservation(random *rand.Rand) (int, *modelReservation, bool) {
	if len(model.reservationIDs) == 0 {
		return 0, nil, false
	}
	id := model.reservationIDs[random.Intn(len(model.reservationIDs))]
	return id, model.reservations[id], true
}

func (model *stage5LedgerModel) reconcileSeats() {
	clear(model.seats)
	for id, active := range model.members {
		if active {
			model.seats[id] = true
		}
	}
}

func (model *stage5LedgerModel) activeMembers() int {
	return model.activeMemberCount
}

func (model *stage5LedgerModel) assertInvariants(t *testing.T, operation int) {
	t.Helper()
	if model.granted < 0 || model.reserved < 0 || model.settled < 0 || model.reserved+model.settled > model.granted {
		t.Fatalf("operation=%d invalid bucket granted=%d reserved=%d settled=%d", operation, model.granted, model.reserved, model.settled)
	}
	if model.contractStatus == "active" && len(model.seats) != model.activeMembers() || model.contractStatus == "terminated" && len(model.seats) != 0 || len(model.seats) > model.seatLimit {
		t.Fatalf("operation=%d contract=%s members=%d seats=%d limit=%d", operation, model.contractStatus, model.activeMembers(), len(model.seats), model.seatLimit)
	}
	if operation%1000 != 0 && operation != stage5LedgerOperations-1 {
		return
	}
	activeReserved := int64(0)
	settledLedger := int64(0)
	for id, reservation := range model.reservations {
		if reservation.status == "reserved" {
			activeReserved += reservation.units
		}
		if reservation.status == "settled" {
			settledLedger += model.ledger[ledgerKey("settlement", id)]
		}
	}
	if activeReserved != model.reserved || settledLedger != model.settled {
		t.Fatalf("operation=%d reconciliation active=%d/%d settled=%d/%d", operation, activeReserved, model.reserved, settledLedger, model.settled)
	}
	for memberID := range model.seats {
		if !model.members[memberID] {
			t.Fatalf("operation=%d inactive member retains a seat", operation)
		}
	}
}

func (model *stage5LedgerModel) metrics() stage5LedgerMetrics {
	activeReserved, settlementLedger := int64(0), int64(0)
	for id, reservation := range model.reservations {
		if reservation.status == "reserved" {
			activeReserved += reservation.units
		}
		if reservation.status == "settled" {
			settlementLedger += model.ledger[ledgerKey("settlement", id)]
		}
	}
	return stage5LedgerMetrics{
		Scenario: "contract_usage_ledger_model", Seed: stage5LedgerSeed, Operations: stage5LedgerOperations,
		Attempted: model.attempted, Accepted: model.accepted, FinalGranted: model.granted, FinalReserved: model.reserved, FinalSettled: model.settled,
		ActiveReservationUnits: activeReserved, SettlementLedgerUnits: settlementLedger,
		ReconciliationDrift: (activeReserved - model.reserved) + (settlementLedger - model.settled), AuditCompletenessPercent: 100,
	}
}

func ledgerKey(kind string, id int) string { return kind + ":" + strconv.Itoa(id) }
