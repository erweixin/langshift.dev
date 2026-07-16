package referencetool

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRunEmitsExactBoundedArtifact(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := Run([]string{"--tool", "reference_artifact", "--snapshot", "reference_artifact@1.0.0", "--handler", "reference_artifact"}, "sandbox:attempt-0001", strings.NewReader(`{"bytes":1048576,"hold_milliseconds":100}`), &stdout, &stderr)
	if err != nil || stderr.Len() != 0 {
		t.Fatalf("stdout=%d stderr=%q error=%v", stdout.Len(), stderr.String(), err)
	}
	var result Result
	if json.Unmarshal(stdout.Bytes(), &result) != nil || result.SchemaVersion != 1 || result.Status != "succeeded" || result.Failure.Retryable {
		t.Fatalf("result=%#v", result)
	}
	var output struct {
		Artifact string `json:"artifact"`
	}
	if json.Unmarshal(result.Output, &output) != nil || len(output.Artifact) != 1<<20 || strings.Trim(output.Artifact, "a") != "" {
		t.Fatalf("artifact bytes=%d", len(output.Artifact))
	}
}

func TestRunRejectsAuthorityAndInputDrift(t *testing.T) {
	valid := []string{"--tool", "reference_artifact", "--snapshot", "reference_artifact@1.0.0", "--handler", "reference_artifact"}
	for index, test := range []struct {
		arguments []string
		input     string
	}{
		{[]string{"--tool", "other", "--snapshot", "reference_artifact@1.0.0", "--handler", "reference_artifact"}, `{"bytes":1048576,"hold_milliseconds":100}`},
		{valid, `{"bytes":1048575,"hold_milliseconds":100}`},
		{valid, `{"bytes":1048576,"hold_milliseconds":100,"unknown":true}`},
	} {
		if err := Run(test.arguments, "sandbox:attempt-0001", strings.NewReader(test.input), &bytes.Buffer{}, &bytes.Buffer{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("case %d error=%v", index, err)
		}
	}
	if err := Run(valid, "", strings.NewReader(`{"bytes":1048576,"hold_milliseconds":100}`), &bytes.Buffer{}, &bytes.Buffer{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing Runtime request identity accepted: %v", err)
	}
}
