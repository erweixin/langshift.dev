// Package referencetool implements the hermetic sandbox payload used by the
// Stage 3 Runtime/Artifact capacity scenario.
package referencetool

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

var ErrInvalid = errors.New("reference artifact tool invocation is invalid")

var snapshotPattern = regexp.MustCompile(`^reference_artifact@[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)

type Input struct {
	Bytes            int `json:"bytes"`
	HoldMilliseconds int `json:"hold_milliseconds"`
}

type Result struct {
	SchemaVersion int             `json:"schema_version"`
	Status        string          `json:"status"`
	Output        json.RawMessage `json:"output"`
	Failure       struct {
		Retryable bool `json:"retryable"`
	} `json:"failure"`
}

func Run(arguments []string, requestID string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("tool-runner", flag.ContinueOnError)
	flags.SetOutput(stderr)
	tool := flags.String("tool", "", "immutable tool name")
	snapshot := flags.String("snapshot", "", "immutable descriptor snapshot")
	handler := flags.String("handler", "", "immutable handler name")
	if flags.Parse(arguments) != nil || flags.NArg() != 0 || *tool != "reference_artifact" || *handler != "reference_artifact" || !snapshotPattern.MatchString(*snapshot) || !strings.HasPrefix(requestID, "sandbox:") || len(requestID) < 16 || len(requestID) > 256 || strings.ContainsAny(requestID, "\x00\r\n") || stdin == nil || stdout == nil {
		return ErrInvalid
	}
	encoded, err := io.ReadAll(io.LimitReader(stdin, 64<<10+1))
	if err != nil || len(encoded) == 0 || len(encoded) > 64<<10 {
		return ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var input Input
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || input.Bytes < 1<<20 || input.Bytes > 8<<20 || input.HoldMilliseconds < 100 || input.HoldMilliseconds > 10_000 {
		return ErrInvalid
	}
	timer := time.NewTimer(time.Duration(input.HoldMilliseconds) * time.Millisecond)
	<-timer.C
	output, err := json.Marshal(map[string]string{"artifact": strings.Repeat("a", input.Bytes)})
	if err != nil {
		return err
	}
	result := Result{SchemaVersion: 1, Status: "succeeded", Output: output}
	if err = json.NewEncoder(stdout).Encode(result); err != nil {
		return fmt.Errorf("write reference tool result: %w", err)
	}
	return nil
}
