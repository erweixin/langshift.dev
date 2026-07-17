package projecttest

import "testing"

func TestParseRequiresClosedEvidenceGroundedResult(t *testing.T) {
	valid := []byte(`{"schema_version":1,"result":"passed","summary":"All exact checks passed.","checks":[{"name":"acceptance","status":"pass","evidence":"Evaluator observed the required output at the pinned revision."}],"uncertainty":"No material uncertainty."}`)
	if _, err := Parse(valid); err != nil {
		t.Fatal(err)
	}
	for _, encoded := range [][]byte{
		[]byte(`{"schema_version":1,"result":"passed","summary":"x","checks":[{"name":"a","status":"fail","evidence":"x"}],"uncertainty":"x"}`),
		[]byte(`{"schema_version":1,"result":"passed","summary":"x","checks":[],"uncertainty":"x"}`),
		[]byte(`{"schema_version":1,"result":"failed","summary":"x","checks":[{"name":"a","status":"fail","evidence":"x"}],"uncertainty":"x","extra":true}`),
	} {
		if _, err := Parse(encoded); err == nil {
			t.Fatalf("invalid document accepted: %s", encoded)
		}
	}
}
