// Package privacy enforces the Stage 5 enterprise aggregation boundary.
// It only evaluates immutable, precomputed snapshot cells; it never accepts
// free-form SQL, member identifiers, or private product payloads.
package privacy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
)

var (
	ErrInvalidCatalog   = errors.New("enterprise aggregate catalog is invalid")
	ErrInvalidQuery     = errors.New("enterprise aggregate query is not allowlisted")
	ErrBudgetExhausted  = errors.New("enterprise aggregate query budget is exhausted")
	ErrSnapshotConflict = errors.New("enterprise aggregate snapshot is not immutable")
	ErrCellNotFound     = errors.New("enterprise aggregate cell is not precomputed")
)

var allowedMetrics = map[string]struct{}{
	"active_members": {}, "task_completion_rate": {}, "weekly_loop_completion_rate": {},
	"project_completion_rate": {}, "aggregate_credit_usage": {},
}
var allowedDimensions = map[string]struct{}{
	"program": {}, "cohort": {}, "role_pack": {}, "locale": {}, "coarse_week": {},
}
var allowedBuckets = map[string]struct{}{"week": {}, "month": {}, "quarter": {}}

type Metric struct {
	Key                   string
	DimensionSets         [][]string
	TimeBuckets           []string
	MinimumCellSize       int
	MinimumComplementSize int
}

type Query struct {
	MetricKey  string
	TimeBucket string
	Dimensions map[string]string
}

type Cell struct {
	Count int
	Value float64
}

type Snapshot struct {
	ID        string
	MetricKey string
	Total     int
	Cells     map[string]Cell
}

type Decision struct {
	QueryHash      string
	CellKeyHash    string
	Returned       bool
	Suppressed     bool
	Reason         string
	Value          float64
	CellSize       int
	ComplementSize int
	BudgetBefore   int
	BudgetAfter    int
}

type sessionKey struct{ snapshotID, actorID string }
type suppressionKey struct{ snapshotID, cellHash string }
type frozenDecision struct {
	suppressed bool
	reason     string
	count      int
	complement int
}

type Engine struct {
	mu           sync.Mutex
	limit        int
	consumed     map[sessionKey]int
	suppressions map[suppressionKey]frozenDecision
}

// CellKey returns the canonical precomputation key for an allowlisted query.
// Snapshot builders and query processors use the same function so a caller
// cannot select a cell through an alternative encoding.
func CellKey(metric Metric, snapshot Snapshot, query Query) (string, error) {
	_, cellKey, err := validate(metric, snapshot, "catalog", query)
	return cellKey, err
}

func NewEngine(limit int) (*Engine, error) {
	if limit < 1 || limit > 100 {
		return nil, ErrInvalidCatalog
	}
	return &Engine{limit: limit, consumed: make(map[sessionKey]int), suppressions: make(map[suppressionKey]frozenDecision)}, nil
}

func (engine *Engine) Evaluate(metric Metric, snapshot Snapshot, actorID string, query Query) (Decision, error) {
	queryKey, cellKey, err := validate(metric, snapshot, actorID, query)
	if err != nil {
		return Decision{}, err
	}
	cell, ok := snapshot.Cells[cellKey]
	if !ok {
		return Decision{}, ErrCellNotFound
	}
	queryHash, cellHash := digest(queryKey), digest(cellKey)
	complement := snapshot.Total - cell.Count
	if cell.Count < 0 || complement < 0 {
		return Decision{}, ErrSnapshotConflict
	}
	suppressed, reason := false, "returned"
	if cell.Count < metric.MinimumCellSize {
		suppressed, reason = true, "minimum_cell_size"
	}
	if complement < metric.MinimumComplementSize {
		suppressed, reason = true, "minimum_complement_size"
	}

	engine.mu.Lock()
	defer engine.mu.Unlock()
	budgetKey := sessionKey{snapshot.ID, actorID}
	before := engine.consumed[budgetKey]
	if before >= engine.limit {
		return Decision{QueryHash: queryHash, CellKeyHash: cellHash, Reason: "query_budget_exhausted", BudgetBefore: before, BudgetAfter: before}, ErrBudgetExhausted
	}
	stableKey := suppressionKey{snapshot.ID, cellHash}
	current := frozenDecision{suppressed, reason, cell.Count, complement}
	if frozen, exists := engine.suppressions[stableKey]; exists && frozen != current {
		return Decision{QueryHash: queryHash, CellKeyHash: cellHash, Reason: "snapshot_conflict", BudgetBefore: before, BudgetAfter: before}, ErrSnapshotConflict
	}
	engine.suppressions[stableKey] = current
	engine.consumed[budgetKey] = before + 1
	decision := Decision{QueryHash: queryHash, CellKeyHash: cellHash, Returned: !suppressed, Suppressed: suppressed, Reason: reason, CellSize: cell.Count, ComplementSize: complement, BudgetBefore: before, BudgetAfter: before + 1}
	if !suppressed {
		decision.Value = cell.Value
	}
	return decision, nil
}

func validate(metric Metric, snapshot Snapshot, actorID string, query Query) (string, string, error) {
	if _, ok := allowedMetrics[metric.Key]; !ok || metric.MinimumCellSize < 5 || metric.MinimumComplementSize < 5 || len(metric.DimensionSets) == 0 || len(metric.TimeBuckets) == 0 {
		return "", "", ErrInvalidCatalog
	}
	if snapshot.ID == "" || snapshot.MetricKey != metric.Key || snapshot.Total < 0 || snapshot.Cells == nil || actorID == "" || query.MetricKey != metric.Key {
		return "", "", ErrInvalidQuery
	}
	bucketAllowed := false
	for _, bucket := range metric.TimeBuckets {
		if _, ok := allowedBuckets[bucket]; !ok {
			return "", "", ErrInvalidCatalog
		}
		if bucket == query.TimeBucket {
			bucketAllowed = true
		}
	}
	if !bucketAllowed {
		return "", "", ErrInvalidQuery
	}
	names := make([]string, 0, len(query.Dimensions))
	for name, value := range query.Dimensions {
		if _, ok := allowedDimensions[name]; !ok || strings.TrimSpace(value) == "" || len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
			return "", "", ErrInvalidQuery
		}
		names = append(names, name)
	}
	sort.Strings(names)
	setAllowed := false
	for _, declared := range metric.DimensionSets {
		copySet := append([]string(nil), declared...)
		sort.Strings(copySet)
		for _, name := range copySet {
			if _, ok := allowedDimensions[name]; !ok {
				return "", "", ErrInvalidCatalog
			}
		}
		if strings.Join(copySet, "\x1f") == strings.Join(names, "\x1f") {
			setAllowed = true
		}
	}
	if !setAllowed {
		return "", "", ErrInvalidQuery
	}
	parts := []string{"metric=" + metric.Key, "bucket=" + query.TimeBucket}
	for _, name := range names {
		parts = append(parts, name+"="+query.Dimensions[name])
	}
	cellKey := strings.Join(parts, "\x1e")
	return "snapshot=" + snapshot.ID + "\x1e" + cellKey, cellKey, nil
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
