package toolreconciler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrHTTPLookupProtocol = errors.New("tool reconciliation HTTP lookup protocol is invalid")

type HTTPLookup struct {
	Endpoint        *url.URL
	Client          *http.Client
	BearerToken     string
	MaximumResponse int64
	Timeout         time.Duration
}

type httpLookupRequest struct {
	SchemaVersion        int    `json:"schema_version"`
	TenantID             string `json:"tenant_id"`
	RunID                string `json:"run_id"`
	ToolCallID           string `json:"tool_call_id"`
	EffectID             string `json:"effect_id"`
	ToolName             string `json:"tool_name"`
	DescriptorSnapshotID string `json:"descriptor_snapshot_id"`
	DescriptorHash       string `json:"descriptor_hash"`
	EffectClass          string `json:"effect_class"`
	EffectKey            string `json:"effect_key"`
	EffectScope          string `json:"effect_scope"`
	ProviderID           string `json:"provider_id"`
	ProviderRequestID    string `json:"provider_request_id"`
	RequestHash          string `json:"request_hash"`
	ReconciliationRound  int    `json:"reconciliation_round"`
}

type httpLookupResponse struct {
	SchemaVersion       int               `json:"schema_version"`
	ProviderRequestID   string            `json:"provider_request_id"`
	Disposition         LookupDisposition `json:"disposition"`
	ExternalResourceRef string            `json:"external_resource_ref,omitempty"`
	Evidence            json.RawMessage   `json:"evidence"`
}

// Lookup calls a release-configured provider adapter. The adapter receives
// only immutable effect identity; it never receives the original tool input
// and has no authority to replay the write.
func (lookup HTTPLookup) Lookup(ctx context.Context, request LookupRequest) (LookupResult, error) {
	if lookup.Endpoint == nil || lookup.Endpoint.Scheme != "https" || lookup.Endpoint.Host == "" || lookup.Endpoint.User != nil || lookup.Endpoint.RawQuery != "" || lookup.Endpoint.Fragment != "" || lookup.Client == nil || lookup.Timeout < time.Second || lookup.Timeout > 5*time.Minute || lookup.MaximumResponse < 1 || lookup.MaximumResponse > 16<<20 || strings.ContainsAny(lookup.BearerToken, "\x00\r\n") {
		return LookupResult{}, ErrHTTPLookupProtocol
	}
	payload := httpLookupRequest{SchemaVersion: 1, TenantID: request.Command.TenantID, RunID: request.Command.RunID, ToolCallID: request.Command.ToolCallID, EffectID: request.Command.EffectID, ToolName: request.Command.ToolName, DescriptorSnapshotID: request.Command.DescriptorSnapshotID, DescriptorHash: request.Command.DescriptorHash, EffectClass: request.Command.EffectClass, EffectKey: request.Command.EffectKey, EffectScope: request.Command.EffectScope, ProviderID: request.Command.ProviderID, ProviderRequestID: request.Command.ProviderRequestID, RequestHash: request.Command.RequestHash, ReconciliationRound: request.Command.ReconciliationRound}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return LookupResult{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, lookup.Timeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(requestCtx, http.MethodPost, lookup.Endpoint.String(), bytes.NewReader(encoded))
	if err != nil {
		return LookupResult{}, ErrHTTPLookupProtocol
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Idempotency-Key", request.Command.ProviderRequestID)
	if lookup.BearerToken != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+lookup.BearerToken)
	}
	response, err := lookup.Client.Do(httpRequest)
	if err != nil {
		return LookupResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 || !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "application/json") {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return LookupResult{}, ErrHTTPLookupProtocol
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, lookup.MaximumResponse+1))
	if err != nil || len(body) == 0 || int64(len(body)) > lookup.MaximumResponse {
		return LookupResult{}, ErrHTTPLookupProtocol
	}
	var result httpLookupResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(&struct{}{}) != io.EOF || result.SchemaVersion != 1 || result.ProviderRequestID != request.Command.ProviderRequestID || len(result.Evidence) == 0 || !json.Valid(result.Evidence) {
		return LookupResult{}, ErrHTTPLookupProtocol
	}
	return LookupResult{Disposition: result.Disposition, Evidence: append([]byte(nil), result.Evidence...), ExternalResourceRef: result.ExternalResourceRef}, nil
}
