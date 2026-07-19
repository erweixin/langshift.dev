// Package localmodeladapter implements the deterministic OpenAI-compatible
// model used only by the macOS Engineering Release Candidate gate. It turns
// immutable product prompts into contract-valid fixture output; it is not a
// quality substitute for a real model evaluation and never makes a production
// claim.
package localmodeladapter

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/langshift/lites/internal/product/review"
	"github.com/langshift/lites/internal/product/route"
	"github.com/langshift/lites/internal/product/task"
)

var ErrInvalid = errors.New("local model adapter configuration is invalid")

var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

const deterministicCoachAnswer = "Keep this grounded in your focused Mission and current evidence. Name the decision you are making and one constraint that would change it."

type Config struct {
	Model               string
	BearerToken         string
	MaximumRequestBytes int64
	Now                 func() time.Time
}

type Handler struct {
	configuration Config
	tokenHash     [32]byte
}

type wireRequest struct {
	Model             string            `json:"model"`
	Instructions      string            `json:"instructions,omitempty"`
	Input             []json.RawMessage `json:"input"`
	Tools             []json.RawMessage `json:"tools,omitempty"`
	ToolChoice        json.RawMessage   `json:"tool_choice,omitempty"`
	MaxOutputTokens   uint32            `json:"max_output_tokens"`
	Temperature       *float64          `json:"temperature,omitempty"`
	TopP              *float64          `json:"top_p,omitempty"`
	Stream            bool              `json:"stream"`
	Store             bool              `json:"store"`
	ParallelToolCalls bool              `json:"parallel_tool_calls"`
}

func New(configuration Config) (*Handler, error) {
	if configuration.Now == nil {
		configuration.Now = time.Now
	}
	if configuration.MaximumRequestBytes == 0 {
		configuration.MaximumRequestBytes = 8 << 20
	}
	if !modelPattern.MatchString(configuration.Model) || len(configuration.BearerToken) < 32 || len(configuration.BearerToken) > 16<<10 || strings.ContainsAny(configuration.BearerToken, "\x00\r\n") || configuration.MaximumRequestBytes < 1024 || configuration.MaximumRequestBytes > 64<<20 {
		return nil, ErrInvalid
	}
	return &Handler{configuration: configuration, tokenHash: sha256.Sum256([]byte(configuration.BearerToken))}, nil
}

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if request.Method == http.MethodGet && (request.URL.Path == "/live" || request.URL.Path == "/ready") {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	// The same private TLS fixture also supplies an empty k-anonymity range for
	// registration tests. It receives only the five-character SHA-1 prefix and
	// deliberately makes no assertion about production breach screening.
	if request.Method == http.MethodGet && validRangePath(request.URL.Path) {
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.Header().Set("X-Lites-Fixture-Validated", "true")
		writer.WriteHeader(http.StatusOK)
		return
	}
	if request.Method != http.MethodPost || request.URL.Path != "/v1/responses" || !handler.authorized(request.Header.Get("Authorization")) || !strings.HasPrefix(strings.ToLower(request.Header.Get("Content-Type")), "application/json") {
		http.Error(writer, "not found", http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, handler.configuration.MaximumRequestBytes+1))
	if err != nil || len(body) == 0 || int64(len(body)) > handler.configuration.MaximumRequestBytes {
		handler.failure(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	var wire wireRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(&struct{}{}) != io.EOF || wire.Model != handler.configuration.Model || len(wire.Input) == 0 || wire.MaxOutputTokens == 0 || wire.Store || !wire.ParallelToolCalls {
		handler.failure(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	prompt, err := promptText(wire)
	if err != nil {
		handler.failure(writer, http.StatusUnprocessableEntity, "unsupported_prompt")
		return
	}
	output, err := deterministicOutput(prompt)
	if err != nil {
		handler.failure(writer, http.StatusUnprocessableEntity, "unsupported_prompt")
		return
	}
	digest := sha256.Sum256(body)
	response := map[string]any{
		"id":         "resp_macos_" + hex.EncodeToString(digest[:12]),
		"object":     "response",
		"created_at": handler.configuration.Now().UTC().Unix(),
		"model":      wire.Model,
		"status":     "completed",
		"output": []any{map[string]any{
			"type": "message", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": string(output)}},
		}},
		"usage": map[string]uint64{"input_tokens": uint64(max(1, len(body)/4)), "output_tokens": uint64(max(1, len(output)/4))},
	}
	if wire.Stream {
		handler.stream(writer, response, output)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(response)
}

func validRangePath(path string) bool {
	const prefix = "/range/"
	if !strings.HasPrefix(path, prefix) || len(path) != len(prefix)+5 {
		return false
	}
	for _, character := range path[len(prefix):] {
		if character < '0' || character > '9' {
			if character < 'A' || character > 'F' {
				return false
			}
		}
	}
	return true
}

func (handler *Handler) authorized(value string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	digest := sha256.Sum256([]byte(strings.TrimPrefix(value, prefix)))
	return subtle.ConstantTimeCompare(digest[:], handler.tokenHash[:]) == 1
}

func (handler *Handler) failure(writer http.ResponseWriter, status int, code string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]string{"code": code, "message": "controlled macOS engineering adapter failure"}})
}

func (handler *Handler) stream(writer http.ResponseWriter, response map[string]any, output []byte) {
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("X-Accel-Buffering", "no")
	delta, _ := json.Marshal(map[string]any{"type": "response.output_text.delta", "delta": string(output)})
	_, _ = fmt.Fprintf(writer, "event: response.output_text.delta\ndata: %s\n\n", delta)
	terminal, _ := json.Marshal(map[string]any{"type": "response.completed", "response": response})
	_, _ = fmt.Fprintf(writer, "event: response.completed\ndata: %s\n\n", terminal)
}

func promptText(request wireRequest) (string, error) {
	values := []string{request.Instructions}
	for _, item := range request.Input {
		var decoded any
		if json.Unmarshal(item, &decoded) != nil {
			return "", ErrInvalid
		}
		collectText(decoded, &values)
	}
	joined := strings.Join(values, "\n")
	if len(joined) == 0 || len(joined) > 64<<20 {
		return "", ErrInvalid
	}
	return joined, nil
}

func collectText(value any, result *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "text" {
				if text, ok := child.(string); ok {
					*result = append(*result, text)
				}
				continue
			}
			collectText(child, result)
		}
	case []any:
		for _, child := range typed {
			collectText(child, result)
		}
	}
}

func deterministicOutput(prompt string) ([]byte, error) {
	switch {
	case strings.Contains(prompt, "Generate a production route revision"):
		return routeOutput(prompt)
	case strings.Contains(prompt, "Generate today's production practice task"):
		return dailyTaskOutput(prompt)
	case strings.Contains(prompt, "Evaluate this immutable submission"):
		return reviewOutput()
	case strings.Contains(prompt, "deterministic coach"):
		return []byte(deterministicCoachAnswer), nil
	default:
		return nil, ErrInvalid
	}
}

func routeOutput(prompt string) ([]byte, error) {
	var manifest struct {
		ClaimRevisions []struct {
			CapabilityID string `json:"capability_id"`
			Status       string `json:"status"`
		} `json:"claim_revisions"`
		TargetRequirements []struct {
			CapabilityID string `json:"capability_id"`
		} `json:"target_requirements"`
	}
	if extractJSON(prompt, "INPUT_MANIFEST=", &manifest) != nil {
		return nil, ErrInvalid
	}
	capabilityID := ""
	// A user correction is an explicit planner input, not merely another
	// catalog option. Keep the macOS adapter behaviorally equivalent to the
	// production contract by grounding the next route in the current active
	// claim before considering unclaimed target requirements.
	for _, claim := range manifest.ClaimRevisions {
		if claim.Status == "active" && claim.CapabilityID != "" {
			capabilityID = claim.CapabilityID
			break
		}
	}
	if capabilityID == "" {
		for _, requirement := range manifest.TargetRequirements {
			if requirement.CapabilityID != "" {
				capabilityID = requirement.CapabilityID
				break
			}
		}
	}
	if capabilityID == "" {
		return nil, ErrInvalid
	}
	document := route.Document{
		SchemaVersion: 1, Summary: "A focused, evidence-first transition route generated by the deterministic macOS engineering adapter.",
		TransferableExperience: []route.GroundedAssessment{},
		Gaps:                   []route.GroundedAssessment{{Statement: "Build current evidence for the target capability.", CapabilityIDs: []string{capabilityID}, EvidenceIDs: []string{}, Confidence: "inferred"}},
		Bridge:                 []route.BridgeStep{{ID: "evidence_bridge", Title: "Create a bridge artifact", Rationale: "Turn prior experience into an observable target-role work sample.", FromCapabilityIDs: []string{}, ToCapabilityIDs: []string{capabilityID}}},
		Stages: []route.Stage{
			{ID: "practice_stage", Title: "Guided practice", Outcome: "Complete a bounded practice artifact.", CapabilityIDs: []string{capabilityID}, EvidenceRequired: []string{"A reviewable practice submission"}},
			{ID: "evidence_stage", Title: "Evidence review", Outcome: "Revise the artifact using explicit review feedback.", CapabilityIDs: []string{capabilityID}, EvidenceRequired: []string{"An immutable reviewed revision"}},
		},
		FirstTask: route.FirstTask{Title: "Create the first transition artifact", Objective: "Produce a small artifact that demonstrates one target-role judgment.", EstimatedMinutes: 20, Difficulty: "standard", CapabilityIDs: []string{capabilityID}, SuccessCriteria: []string{"The artifact states the decision and its rationale"}},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	if _, err = route.DecodeDocument(encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

func dailyTaskOutput(prompt string) ([]byte, error) {
	var manifest struct {
		Command struct {
			Difficulty       string `json:"difficulty"`
			AvailableMinutes int    `json:"available_minutes"`
		} `json:"command"`
	}
	var accepted route.Document
	if extractJSON(prompt, "INPUT_MANIFEST=", &manifest) != nil || extractJSON(prompt, "ACCEPTED_ROUTE=", &accepted) != nil || len(accepted.FirstTask.CapabilityIDs) == 0 {
		return nil, ErrInvalid
	}
	document := task.Document{
		SchemaVersion: 1, Title: "Explain a target-role decision", Objective: "Create a concise work sample that makes one professional judgment inspectable.",
		WhyThisTask: "It converts the accepted route into evidence that can be reviewed after refresh or sign-in.", KeyJudgment: "Choose one approach and explain the trade-off.",
		EstimatedMinutes: manifest.Command.AvailableMinutes, Difficulty: manifest.Command.Difficulty, PracticeKind: "writing",
		Explanation:   []task.ExplanationStep{{Title: "Make the reasoning visible", Content: "State the context, decision, alternative, and expected consequence."}},
		Example:       "Context: a team needs a repeatable workflow. Decision: document one bounded path. Trade-off: less flexibility in exchange for clearer review.",
		Practice:      task.Practice{Instructions: "Write a short decision note with context, decision, one alternative, and a measurable outcome.", StarterContent: "Context:\nDecision:\nAlternative:\nExpected outcome:\n", DeterministicChecks: []string{"All four headings contain non-placeholder text"}, SuccessCriteria: []string{"The decision and trade-off are explicit"}},
		CapabilityIDs: []string{accepted.FirstTask.CapabilityIDs[0]}, EvidenceTargets: []string{"A reviewable decision note"}, NextTaskHint: "Use reviewer feedback to tighten the decision rationale.",
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	if _, err = task.ParseDocument(encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

func reviewOutput() ([]byte, error) {
	document := review.Document{
		SchemaVersion: 1, Verdict: "pass", Summary: "The submission provides a reviewable decision and rationale for this engineering journey.",
		DeterministicResults: review.DeterministicResults{Checks: []review.Check{{Name: "local engineering contract check", Status: "not_applicable", Evidence: "No production execution result is asserted by the deterministic adapter."}}},
		Dimensions:           []review.Dimension{{ID: "reasoning_clarity", Score: 80, Rationale: "The submission is sufficient for exercising the immutable review and evidence workflow.", EvidenceQuotes: []string{}}},
		Strengths:            []string{"The work can be reviewed as a durable revision."}, Improvements: []string{"Add more domain-specific evidence during a real model pilot."},
		CapabilityEvidence: []review.CapabilityEvidence{}, NextAction: "Revise the artifact with one additional concrete constraint.", Uncertainty: "This is deterministic fixture feedback and is not a real model quality judgment.",
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	if _, err = review.Parse(encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

func extractJSON(prompt, marker string, target any) error {
	index := strings.Index(prompt, marker)
	if index < 0 {
		return ErrInvalid
	}
	decoder := json.NewDecoder(strings.NewReader(prompt[index+len(marker):]))
	if decoder.Decode(target) != nil {
		return ErrInvalid
	}
	return nil
}
