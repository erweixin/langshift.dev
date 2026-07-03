package llm

import (
	"math"
	"strconv"
)

const costScale = 100_000_000

type CostEstimator interface {
	EstimateCostUSD(req Request, usage Usage) float64
}

func estimateInputTokens(messages []Message) int {
	var chars int
	for _, message := range messages {
		chars += len([]rune(string(message.Role))) + len([]rune(message.Content))
	}
	if chars == 0 {
		return 0
	}
	return ((chars + 3) / 4) + (len(messages) * 4)
}

func estimateTextTokens(text string) int {
	chars := len([]rune(text))
	if chars == 0 {
		return 0
	}
	return (chars + 3) / 4
}

func formatCostUSD(cost float64) string {
	if cost < 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
		cost = 0
	}
	rounded := math.Round(cost*costScale) / costScale
	return strconv.FormatFloat(rounded, 'f', 8, 64)
}
