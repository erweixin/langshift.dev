// Package lease validates the short-lived execution right presented by a
// Worker. A fence is never sufficient on its own: command, attempt, exact
// fence, opaque token digest, and expiry must all match.
package lease

import (
	"crypto/hmac"
	"errors"
	"time"

	"github.com/langshift/lites/internal/security/opaque"
)

var (
	ErrInvalidRight = errors.New("execution right is invalid")
	ErrStaleRight   = errors.New("execution right is stale")
	ErrExpiredRight = errors.New("execution right is expired")
)

type StoredRight struct {
	CommandID      string
	AttemptID      string
	Fence          uint64
	LeaseTokenHash [32]byte
	LeaseExpiresAt time.Time
}

type PresentedRight struct {
	CommandID  string
	AttemptID  string
	Fence      uint64
	LeaseToken string
}

type Verifier struct {
	Tokens opaque.Manager
	Now    func() time.Time
}

func (verifier Verifier) Verify(stored StoredRight, presented PresentedRight) error {
	if stored.CommandID == "" || stored.AttemptID == "" || stored.Fence == 0 || stored.LeaseExpiresAt.IsZero() || zeroDigest(stored.LeaseTokenHash) || presented.CommandID == "" || presented.AttemptID == "" || presented.Fence == 0 || presented.LeaseToken == "" {
		return ErrInvalidRight
	}
	now := time.Now()
	if verifier.Now != nil {
		now = verifier.Now()
	}
	if !now.Before(stored.LeaseExpiresAt) {
		return ErrExpiredRight
	}
	if stored.CommandID != presented.CommandID || stored.AttemptID != presented.AttemptID || stored.Fence != presented.Fence {
		return ErrStaleRight
	}
	digest, err := verifier.Tokens.Digest(presented.LeaseToken)
	if err != nil {
		return ErrInvalidRight
	}
	if !hmac.Equal(stored.LeaseTokenHash[:], digest[:]) {
		return ErrStaleRight
	}
	return nil
}

func zeroDigest(digest [32]byte) bool {
	var zero [32]byte
	return hmac.Equal(digest[:], zero[:])
}
