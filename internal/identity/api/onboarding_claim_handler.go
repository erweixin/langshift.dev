package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/langshift/lites/internal/identity/anonymoussession"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

type OnboardingClaimCommand struct {
	AuthenticatedRequestMetadata
	OnboardingSessionID  string
	AnonymousSubjectID   string
	ExpectedClaimVersion uint64
}

type OnboardingClaimResult struct {
	ID        string    `json:"id"`
	Version   uint64    `json:"version"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

type OnboardingClaimService interface {
	ClaimOnboarding(context.Context, OnboardingClaimCommand) (OnboardingClaimResult, error)
}

func (handler Handler) onboardingClaim(writer http.ResponseWriter, request *http.Request) {
	if handler.Claims == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.authenticatedMetadata(writer, request, true)
	if !ok {
		return
	}
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.AnonymousSubjectID == "" {
		handler.writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "Authentication required", false)
		return
	}
	sessionID, ok := onboardingClaimPathID(request.URL.Path)
	if !ok {
		handler.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
		return
	}
	expected, ok := handler.ifMatch(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID            string `json:"request_id"`
		TargetTenantID       string `json:"target_tenant_id"`
		ExpectedClaimVersion uint64 `json:"expected_claim_version"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || body.TargetTenantID != metadata.TenantID || body.ExpectedClaimVersion == 0 || body.ExpectedClaimVersion != expected {
		handler.validationFailed(writer, request)
		return
	}
	if !handler.allowRequest(writer, request, "identity-onboarding-claim", onboardingActorLimit, []byte(metadata.TenantID), []byte(metadata.UserID)) {
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Claims.ClaimOnboarding(request.Context(), OnboardingClaimCommand{AuthenticatedRequestMetadata: metadata, OnboardingSessionID: sessionID, AnonymousSubjectID: claims.AnonymousSubjectID, ExpectedClaimVersion: expected})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	if result.ID == "" || result.Version == 0 || result.Status == "" || result.UpdatedAt.IsZero() {
		handler.internalError(writer, request)
		return
	}
	http.SetCookie(writer, anonymoussession.ClearCookie())
	// The anonymous and authenticated flows intentionally use the same CSRF
	// cookie name. Login has already replaced the anonymous value with the
	// session-bound value required by every subsequent authenticated write, so
	// clearing it here would silently break the signed-in session. Removing the
	// HttpOnly anonymous bearer is sufficient to end the anonymous browser
	// session after the claim has converged.
	writer.Header().Set("ETag", strconv.Quote(strconv.FormatUint(result.Version, 10)))
	handler.writeJSON(writer, http.StatusOK, struct {
		ID        string    `json:"id"`
		Version   uint64    `json:"version"`
		Status    string    `json:"status"`
		UpdatedAt time.Time `json:"updated_at"`
	}{result.ID, result.Version, result.Status, result.UpdatedAt.UTC()})
}

func onboardingClaimPathID(path string) (string, bool) {
	value := strings.TrimPrefix(path, "/v1/onboarding-sessions/")
	value = strings.TrimSuffix(value, "/claim")
	return value, value != "" && len(value) <= 200 && !strings.Contains(value, "/")
}
