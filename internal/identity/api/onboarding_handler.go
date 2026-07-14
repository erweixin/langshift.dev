package api

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/identity/anonymoussession"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

type OnboardingCreateCommand struct {
	RequestMetadata
	PrincipalKind      trustedcontext.PrincipalKind
	UserID             string
	TenantID           string
	AnonymousSubjectID string
	CurrentRole        string
	TargetRole         string
	ExperienceSummary  string
	WeeklyMinutes      int
}

type OnboardingCreateResult struct {
	ID              string    `json:"id"`
	Version         uint64    `json:"version"`
	Status          string    `json:"status"`
	UpdatedAt       time.Time `json:"updated_at"`
	AnonymousHandle string    `json:"anonymous_handle"`
	HandleExpiresAt time.Time `json:"handle_expires_at"`
}

type OnboardingService interface {
	CreateOnboarding(context.Context, OnboardingCreateCommand) (OnboardingCreateResult, error)
}

func (handler Handler) onboardingCreate(writer http.ResponseWriter, request *http.Request) {
	if handler.Onboarding == nil {
		handler.internalError(writer, request)
		return
	}
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if !ok || !claims.CSRFVerified || claims.PrincipalKind != trustedcontext.PublicRequest && claims.PrincipalKind != trustedcontext.AnonymousUser && claims.PrincipalKind != trustedcontext.AuthenticatedUser {
		handler.writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "Authentication required", false)
		return
	}
	idempotencyValues := request.Header.Values(transport.IdempotencyHeader)
	if len(idempotencyValues) != 1 || idempotency.ValidateRawKey(idempotencyValues[0]) != nil {
		handler.validationFailed(writer, request)
		return
	}
	ipHash, ipErr := decodeHash(claims.ClientIPHash)
	userAgentHash, userAgentErr := decodeHash(claims.UserAgentHash)
	if ipErr != nil || userAgentErr != nil {
		handler.internalError(writer, request)
		return
	}
	if claims.PrincipalKind == trustedcontext.PublicRequest {
		if !handler.allowRequest(writer, request, "identity-onboarding-ip", onboardingIPLimit, ipHash) {
			return
		}
	} else if !handler.allowRequest(writer, request, "identity-onboarding-actor", onboardingActorLimit, []byte(claims.TenantID), []byte(claims.SubjectID)) {
		return
	}
	var body struct {
		RequestID         string `json:"request_id"`
		CurrentRole       string `json:"current_role"`
		TargetRole        string `json:"target_role"`
		ExperienceSummary string `json:"experience_summary"`
		WeeklyMinutes     int    `json:"weekly_minutes"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	body.CurrentRole = strings.TrimSpace(body.CurrentRole)
	body.TargetRole = strings.TrimSpace(body.TargetRole)
	body.ExperienceSummary = strings.TrimSpace(body.ExperienceSummary)
	if !validClientRequestID(body.RequestID) || body.CurrentRole == "" || body.TargetRole == "" || utf8.RuneCountInString(body.CurrentRole) > 500 || utf8.RuneCountInString(body.TargetRole) > 500 || utf8.RuneCountInString(body.ExperienceSummary) > 4000 || body.WeeklyMinutes < 30 || body.WeeklyMinutes > 2400 {
		handler.validationFailed(writer, request)
		return
	}
	metadata := RequestMetadata{RequestID: claims.RequestID, ClientRequestID: body.RequestID, IdempotencyKey: idempotencyValues[0], ClientIPHash: ipHash, UserAgentHash: userAgentHash}
	result, err := handler.Onboarding.CreateOnboarding(request.Context(), OnboardingCreateCommand{RequestMetadata: metadata, PrincipalKind: claims.PrincipalKind, UserID: claims.SubjectID, TenantID: claims.TenantID, AnonymousSubjectID: claims.AnonymousSubjectID, CurrentRole: body.CurrentRole, TargetRole: body.TargetRole, ExperienceSummary: body.ExperienceSummary, WeeklyMinutes: body.WeeklyMinutes})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	if result.ID == "" || result.Version < 1 || result.Status == "" || result.UpdatedAt.IsZero() {
		handler.internalError(writer, request)
		return
	}
	if result.AnonymousHandle != "" {
		handleCookie, cookieErr := anonymoussession.Cookie(result.AnonymousHandle, result.HandleExpiresAt)
		csrfCookie, csrfErr := anonymoussession.CSRFCookie(result.AnonymousHandle, handler.AnonymousCSRFKey, result.HandleExpiresAt)
		result.AnonymousHandle = ""
		if cookieErr != nil || csrfErr != nil {
			handler.internalError(writer, request)
			return
		}
		http.SetCookie(writer, handleCookie)
		http.SetCookie(writer, csrfCookie)
	}
	handler.writeJSON(writer, http.StatusOK, struct {
		ID        string    `json:"id"`
		Version   uint64    `json:"version"`
		Status    string    `json:"status"`
		UpdatedAt time.Time `json:"updated_at"`
	}{ID: result.ID, Version: result.Version, Status: result.Status, UpdatedAt: result.UpdatedAt.UTC()})
}
