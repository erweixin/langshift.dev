package review

import "testing"

func TestParseStrictReview(t *testing.T) {
	valid := []byte(`{"schema_version":1,"verdict":"pass","summary":"Grounded result.","deterministic_results":{"checks":[{"name":"tests","status":"pass","evidence":"The submitted report shows a passing run."}]},"dimensions":[{"id":"correctness","score":90,"rationale":"Evidence is present.","evidence_quotes":["passing run"]}],"strengths":["Clear"],"improvements":[],"capability_evidence":[{"capability_id":"capability-1","level":"demonstrated","statement":"Demonstrated in the submitted artifact."}],"next_action":"Continue.","uncertainty":"Runtime was not independently executed."}`)
	if _, e := Parse(valid); e != nil {
		t.Fatal(e)
	}
	invalid := append(valid[:len(valid)-1], []byte(`,"extra":true}`)...)
	if _, e := Parse(invalid); e == nil {
		t.Fatal("unknown field accepted")
	}
}
