package llmgateway

import (
	"errors"
	"math"
	"testing"

	"github.com/langshift/lites/internal/llmgateway/provider"
)

func TestAccountingRoundsUpAndFailsClosedOnOverflow(t *testing.T) {
	model := provider.ModelDefinition{MaximumInputTokens: math.MaxUint64, MaximumOutputTokens: math.MaxUint64, InputMicrounitsPerMillion: 5, OutputMicrounitsPerMillion: 7, InputCreditUnitsPerMillion: 11, OutputCreditUnitsPerMillion: 13}
	accounting, err := calculateAccounting(model, 1, 1, false, "confirmed")
	if err != nil || accounting.CostMicrounits != 2 || accounting.BillableUnits != 2 {
		t.Fatalf("accounting=%#v err=%v", accounting, err)
	}
	accounting, err = calculateAccounting(model, 1_000_000, 1_000_000, true, "confirmed")
	if err != nil || accounting.CostMicrounits != 0 || accounting.BillableUnits != 24 {
		t.Fatalf("BYOK accounting=%#v err=%v", accounting, err)
	}
	model.InputMicrounitsPerMillion = math.MaxUint64
	if _, err = calculateAccounting(model, 2, 0, false, "confirmed"); !errors.Is(err, ErrUsageOverflow) {
		t.Fatalf("overflow accepted: %v", err)
	}
}
