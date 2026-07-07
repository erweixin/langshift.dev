package content

import (
	"encoding/json"
	"fmt"
	"strings"
)

type preparedArtifactSave struct {
	request SaveArtifactRequest
	record  ArtifactRecord
	encoded json.RawMessage
}

func prepareArtifactSave(request SaveArtifactRequest) (preparedArtifactSave, error) {
	if err := validateArtifact(request.Artifact); err != nil {
		return preparedArtifactSave{}, err
	}
	reviewStatus := strings.TrimSpace(request.ReviewStatus)
	if reviewStatus == "" {
		reviewStatus = ReviewStatusAutoOK
	}
	if !validReviewStatus(reviewStatus) {
		return preparedArtifactSave{}, fmt.Errorf("%w: review_status is invalid", ErrInvalidRequest)
	}
	if request.ValidationAttempts < 0 {
		return preparedArtifactSave{}, fmt.Errorf("%w: validation_attempts must be nonnegative", ErrInvalidRequest)
	}

	hash, encoded, err := artifactHash(request.Artifact)
	if err != nil {
		return preparedArtifactSave{}, err
	}
	request.ReviewStatus = reviewStatus
	return preparedArtifactSave{
		request: request,
		record: ArtifactRecord{
			Artifact:           request.Artifact,
			ArtifactHash:       hash,
			ReviewStatus:       reviewStatus,
			ValidationAttempts: request.ValidationAttempts,
		},
		encoded: encoded,
	}, nil
}

func (p preparedArtifactSave) args() []any {
	return []any{
		p.request.Artifact.ContentKey,
		string(p.encoded),
		p.record.ArtifactHash,
		p.request.Artifact.TaskTemplateID,
		p.request.Artifact.TargetStack,
		p.request.Artifact.LevelBand,
		p.request.Artifact.ContentVersion,
		p.request.Artifact.PromptVersion,
		p.record.ReviewStatus,
		p.request.ValidationAttempts,
		p.request.LLMLedgerID,
		p.request.SourceRunID,
		p.request.SourceAttemptKey,
	}
}

func validReviewStatus(status string) bool {
	switch status {
	case "auto_ok", "needs_review", "human_ok", "rejected":
		return true
	default:
		return false
	}
}
