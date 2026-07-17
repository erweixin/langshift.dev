package privacy

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func testMetric() Metric {
	return Metric{Key: "task_completion_rate", DimensionSets: [][]string{{"cohort"}, {"cohort", "locale"}}, TimeBuckets: []string{"week", "month"}, MinimumCellSize: 5, MinimumComplementSize: 5}
}
func query(cohort string) Query {
	return Query{MetricKey: "task_completion_rate", TimeBucket: "week", Dimensions: map[string]string{"cohort": cohort}}
}
func cellKeyFor(q Query) string {
	_, key, _ := validate(testMetric(), Snapshot{ID: "snapshot", MetricKey: "task_completion_rate", Total: 10, Cells: map[string]Cell{}}, "actor", q)
	return key
}

func TestEngineReturnsOnlyAllowlistedSafeCell(t *testing.T) {
	engine, _ := NewEngine(100)
	q := query("cohort-a")
	snapshot := Snapshot{ID: "snapshot-a", MetricKey: "task_completion_rate", Total: 20, Cells: map[string]Cell{cellKeyFor(q): {Count: 10, Value: .75}}}
	decision, err := engine.Evaluate(testMetric(), snapshot, "actor-a", q)
	if err != nil || !decision.Returned || decision.Suppressed || decision.Value != .75 || decision.BudgetBefore != 0 || decision.BudgetAfter != 1 {
		t.Fatalf("decision=%#v err=%v", decision, err)
	}
}

func TestEngineStablySuppressesSmallCellAndComplement(t *testing.T) {
	for _, tc := range []struct {
		name         string
		total, count int
		reason       string
	}{{"cell", 20, 4, "minimum_cell_size"}, {"complement", 10, 6, "minimum_complement_size"}} {
		t.Run(tc.name, func(t *testing.T) {
			engine, _ := NewEngine(100)
			q := query(tc.name)
			snapshot := Snapshot{ID: "snapshot-" + tc.name, MetricKey: "task_completion_rate", Total: tc.total, Cells: map[string]Cell{cellKeyFor(q): {Count: tc.count, Value: .99}}}
			decision, err := engine.Evaluate(testMetric(), snapshot, "actor", q)
			if err != nil || !decision.Suppressed || decision.Returned || decision.Value != 0 || decision.Reason != tc.reason {
				t.Fatalf("decision=%#v err=%v", decision, err)
			}
		})
	}
}

func TestEngineRejectsForbiddenDimensionsBucketsAndDrilldown(t *testing.T) {
	engine, _ := NewEngine(100)
	snapshot := Snapshot{ID: "snapshot", MetricKey: "task_completion_rate", Total: 20, Cells: map[string]Cell{}}
	queries := []Query{
		{MetricKey: "task_completion_rate", TimeBucket: "day", Dimensions: map[string]string{"cohort": "a"}},
		{MetricKey: "task_completion_rate", TimeBucket: "week", Dimensions: map[string]string{"member_id": "person"}},
		{MetricKey: "task_completion_rate", TimeBucket: "week", Dimensions: map[string]string{"cohort": "a", "program": "p"}},
		{MetricKey: "task_completion_rate", TimeBucket: "week", Dimensions: map[string]string{"cohort": "a\nmember"}},
	}
	for index, value := range queries {
		if _, err := engine.Evaluate(testMetric(), snapshot, "actor", value); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("query %d err=%v", index, err)
		}
	}
}

func TestEngineBudgetIsAtomicAtOneHundred(t *testing.T) {
	engine, _ := NewEngine(100)
	q := query("cohort")
	snapshot := Snapshot{ID: "snapshot", MetricKey: "task_completion_rate", Total: 20, Cells: map[string]Cell{cellKeyFor(q): {Count: 10, Value: .5}}}
	var passed atomic.Int64
	var group sync.WaitGroup
	for range 1000 {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := engine.Evaluate(testMetric(), snapshot, "actor", q); err == nil {
				passed.Add(1)
			} else if !errors.Is(err, ErrBudgetExhausted) {
				t.Errorf("unexpected err=%v", err)
			}
		}()
	}
	group.Wait()
	if passed.Load() != 100 {
		t.Fatalf("passed=%d", passed.Load())
	}
}

func TestEngineRejectsMutableSnapshotUnderStableIdentity(t *testing.T) {
	engine, _ := NewEngine(100)
	q := query("cohort")
	key := cellKeyFor(q)
	first := Snapshot{ID: "snapshot", MetricKey: "task_completion_rate", Total: 20, Cells: map[string]Cell{key: {Count: 4, Value: .2}}}
	second := Snapshot{ID: "snapshot", MetricKey: "task_completion_rate", Total: 20, Cells: map[string]Cell{key: {Count: 10, Value: .5}}}
	if _, err := engine.Evaluate(testMetric(), first, "actor", q); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Evaluate(testMetric(), second, "actor", q); !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("err=%v", err)
	}
}

func TestFiveHundredFixedPrivacyAttacksNeverReturnSmallOrComplementCells(t *testing.T) {
	for index := range 500 {
		engine, _ := NewEngine(100)
		q := query(fmt.Sprintf("cohort-%03d", index))
		count := index % 5
		total := count + 4
		snapshot := Snapshot{ID: fmt.Sprintf("snapshot-%03d", index), MetricKey: "task_completion_rate", Total: total, Cells: map[string]Cell{cellKeyFor(q): {Count: count, Value: 1}}}
		decision, err := engine.Evaluate(testMetric(), snapshot, "attacker", q)
		if err != nil || decision.Returned || !decision.Suppressed {
			t.Fatalf("attack=%d decision=%#v err=%v", index, decision, err)
		}
	}
}

func TestOneHundredAdaptiveSequencesCannotBypassBudgetOrSuppression(t *testing.T) {
	for sequence := range 100 {
		engine, _ := NewEngine(100)
		q := query(fmt.Sprintf("adaptive-%03d", sequence))
		snapshot := Snapshot{ID: fmt.Sprintf("adaptive-snapshot-%03d", sequence), MetricKey: "task_completion_rate", Total: 8, Cells: map[string]Cell{cellKeyFor(q): {Count: 4, Value: 1}}}
		for attempt := range 100 {
			decision, err := engine.Evaluate(testMetric(), snapshot, "attacker", q)
			if err != nil || decision.Returned || !decision.Suppressed || decision.BudgetAfter != attempt+1 {
				t.Fatalf("sequence=%d attempt=%d decision=%#v err=%v", sequence, attempt, decision, err)
			}
		}
		if _, err := engine.Evaluate(testMetric(), snapshot, "attacker", q); !errors.Is(err, ErrBudgetExhausted) {
			t.Fatalf("sequence=%d err=%v", sequence, err)
		}
	}
}
