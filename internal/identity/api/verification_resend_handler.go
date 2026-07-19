package api

import (
	"net/http"

	identityemail "github.com/langshift/lites/internal/identity/email"
)

func (handler Handler) resendVerification(writer http.ResponseWriter, request *http.Request) {
	if handler.Service == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.publicMetadata(writer, request)
	if !ok {
		return
	}
	if !handler.allowRequest(writer, request, "identity-verification-resend-ip", passwordMailIPLimit, metadata.ClientIPHash) {
		return
	}
	var body struct {
		RequestID string `json:"request_id"`
		Email     string `json:"email"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	normalizedEmail, err := identityemail.Normalize(body.Email)
	if err != nil || !validClientRequestID(body.RequestID) {
		handler.validationFailed(writer, request)
		return
	}
	if !handler.allowRequest(writer, request, "identity-verification-resend-subject", mailTargetLimit, metadata.ClientIPHash, []byte(normalizedEmail)) {
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.ResendVerification(request.Context(), ResendVerificationCommand{RequestMetadata: metadata, NormalizedEmail: normalizedEmail})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	if result.Status != "accepted" || result.NextAllowedAt.IsZero() {
		handler.internalError(writer, request)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	handler.writeJSON(writer, http.StatusOK, ResendVerificationResult{Status: result.Status, NextAllowedAt: result.NextAllowedAt.UTC()})
}
