package llmgateway

import (
	"errors"
	"math"

	"github.com/langshift/lites/internal/llmgateway/provider"
)

var ErrUsageOverflow = errors.New("provider usage cannot be represented")

type Accounting struct {
	InputTokens, OutputTokens uint64
	CostMicrounits            uint64
	BillableUnits             uint64
	UsageStatus               string
}

func calculateAccounting(model provider.ModelDefinition, inputTokens, outputTokens uint64, byok bool, status string) (Accounting, error) {
	if inputTokens > model.MaximumInputTokens || outputTokens > model.MaximumOutputTokens || status != "confirmed" && status != "estimated" {
		return Accounting{}, ErrUsageOverflow
	}
	inputCost, err := ceilRate(inputTokens, model.InputMicrounitsPerMillion)
	if err != nil {
		return Accounting{}, err
	}
	outputCost, err := ceilRate(outputTokens, model.OutputMicrounitsPerMillion)
	if err != nil || inputCost > math.MaxUint64-outputCost {
		return Accounting{}, ErrUsageOverflow
	}
	inputUnits, err := ceilRate(inputTokens, model.InputCreditUnitsPerMillion)
	if err != nil {
		return Accounting{}, err
	}
	outputUnits, err := ceilRate(outputTokens, model.OutputCreditUnitsPerMillion)
	if err != nil || inputUnits > math.MaxUint64-outputUnits {
		return Accounting{}, ErrUsageOverflow
	}
	cost := inputCost + outputCost
	if byok {
		cost = 0
	}
	return Accounting{InputTokens: inputTokens, OutputTokens: outputTokens, CostMicrounits: cost, BillableUnits: inputUnits + outputUnits, UsageStatus: status}, nil
}

func ceilRate(tokens, rate uint64) (uint64, error) {
	if tokens == 0 || rate == 0 {
		return 0, nil
	}
	if tokens > math.MaxUint64/rate {
		return 0, ErrUsageOverflow
	}
	product := tokens * rate
	return product/1_000_000 + boolUint(product%1_000_000 != 0), nil
}

func boolUint(value bool) uint64 {
	if value {
		return 1
	}
	return 0
}
