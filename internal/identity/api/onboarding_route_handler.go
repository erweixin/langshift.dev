package api

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

var onboardingUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var onboardingCapabilityIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,127}$`)

type OnboardingRouteCommand struct {
	RequestMetadata
	OnboardingSessionID, TenantID, UserID, AnonymousSubjectID string
	SourceRoleProfileID, TargetRoleProfileID                  string
	ConfirmedClaimIDs                                         []string
	ExpectedOnboardingVersion                                 uint64
}

type OnboardingRouteRequestResult struct {
	RunID      string    `json:"run_id"`
	Status     string    `json:"status"`
	AcceptedAt time.Time `json:"accepted_at"`
	Version    uint64    `json:"-"`
	Replayed   bool      `json:"-"`
}

type OnboardingRouteResource struct {
	ID              string          `json:"id"`
	Version         uint64          `json:"version"`
	Status          string          `json:"status"`
	MissionID       *string         `json:"mission_id"`
	RouteRevisionID *string         `json:"route_revision_id"`
	Route           json.RawMessage `json:"route"`
	ClaimVersion    *uint64         `json:"claim_version"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type OnboardingRouteService interface {
	RequestRoute(context.Context, OnboardingRouteCommand) (OnboardingRouteRequestResult, error)
	GetRoute(context.Context, string, string, string, string) (OnboardingRouteResource, error)
}

func (handler Handler) onboardingRoutePreview(writer http.ResponseWriter, request *http.Request) {
	if handler.Routes == nil {
		handler.internalError(writer, request)
		return
	}
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if !ok || !claims.CSRFVerified || claims.PrincipalKind != trustedcontext.AnonymousUser && claims.PrincipalKind != trustedcontext.AuthenticatedUser {
		handler.writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "Authentication required", false)
		return
	}
	id, ok := onboardingRoutePathID(request.URL.Path, "/route-preview")
	if !ok {
		handler.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
		return
	}
	keys := request.Header.Values(transport.IdempotencyHeader)
	if len(keys) != 1 || idempotency.ValidateRawKey(keys[0]) != nil {
		handler.validationFailed(writer, request)
		return
	}
	expected, ok := handler.ifMatch(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID                 string   `json:"request_id"`
		SourceRoleProfileID       string   `json:"source_role_profile_id"`
		TargetRoleProfileID       string   `json:"target_role_profile_id"`
		ConfirmedClaimIDs         []string `json:"confirmed_claim_ids"`
		ExpectedOnboardingVersion uint64   `json:"expected_onboarding_version"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || body.ExpectedOnboardingVersion != expected || !onboardingUUIDPattern.MatchString(body.TargetRoleProfileID) || body.SourceRoleProfileID != "" && !onboardingUUIDPattern.MatchString(body.SourceRoleProfileID) || body.ConfirmedClaimIDs == nil || len(body.ConfirmedClaimIDs) > 128 {
		handler.validationFailed(writer, request)
		return
	}
	seen := map[string]struct{}{}
	for _, claimID := range body.ConfirmedClaimIDs {
		if !onboardingUUIDPattern.MatchString(claimID) && !onboardingCapabilityIDPattern.MatchString(claimID) {
			handler.validationFailed(writer, request)
			return
		}
		if _, duplicate := seen[claimID]; duplicate {
			handler.validationFailed(writer, request)
			return
		}
		seen[claimID] = struct{}{}
	}
	if !handler.allowRequest(writer, request, "identity-onboarding-route", onboardingActorLimit, []byte(claims.TenantID), []byte(claims.SubjectID)) {
		return
	}
	result, err := handler.Routes.RequestRoute(request.Context(), OnboardingRouteCommand{RequestMetadata: RequestMetadata{RequestID: claims.RequestID, ClientRequestID: body.RequestID, IdempotencyKey: keys[0]}, OnboardingSessionID: id, TenantID: claims.TenantID, UserID: claims.SubjectID, AnonymousSubjectID: claims.AnonymousSubjectID, SourceRoleProfileID: body.SourceRoleProfileID, TargetRoleProfileID: body.TargetRoleProfileID, ConfirmedClaimIDs: body.ConfirmedClaimIDs, ExpectedOnboardingVersion: expected})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	if result.RunID == "" || result.Status != "accepted" || result.AcceptedAt.IsZero() || result.Version != expected+1 {
		handler.internalError(writer, request)
		return
	}
	writer.Header().Set("ETag", strconv.Quote(strconv.FormatUint(result.Version, 10)))
	writer.Header().Set("Cache-Control", "no-store")
	handler.writeJSON(writer, http.StatusAccepted, result)
}

func (handler Handler) onboardingRouteGet(writer http.ResponseWriter, request *http.Request) {
	if handler.Routes == nil {
		handler.internalError(writer, request)
		return
	}
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if !ok || claims.PrincipalKind != trustedcontext.AnonymousUser && claims.PrincipalKind != trustedcontext.AuthenticatedUser {
		handler.writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "Authentication required", false)
		return
	}
	id, ok := onboardingRoutePathID(request.URL.Path, "")
	if !ok {
		handler.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
		return
	}
	result, err := handler.Routes.GetRoute(request.Context(), id, claims.TenantID, claims.SubjectID, claims.AnonymousSubjectID)
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	if result.ID != id || result.Version < 1 || result.Status == "" || result.UpdatedAt.IsZero() || len(result.Route) == 0 || !json.Valid(result.Route) {
		handler.internalError(writer, request)
		return
	}
	writer.Header().Set("ETag", strconv.Quote(strconv.FormatUint(result.Version, 10)))
	writer.Header().Set("Cache-Control", "private, no-store")
	handler.writeJSON(writer, http.StatusOK, result)
}

func onboardingRoutePathID(path, suffix string) (string, bool) {
	prefix := "/v1/onboarding-sessions/"
	if !strings.HasPrefix(path, prefix) || suffix != "" && !strings.HasSuffix(path, suffix) {
		return "", false
	}
	value := strings.TrimPrefix(path, prefix)
	if suffix != "" {
		value = strings.TrimSuffix(value, suffix)
	}
	return value, onboardingUUIDPattern.MatchString(value)
}
