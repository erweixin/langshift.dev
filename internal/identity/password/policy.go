package password

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"

	"golang.org/x/text/unicode/norm"
)

var (
	ErrCompromisedPassword = errors.New("password is present in the compromise blocklist")
	ErrScreenUnavailable   = errors.New("password compromise screen is unavailable")
)

// CompromiseChecker checks a candidate against common and breached password
// material. Implementations must not log or persist the candidate plaintext.
type CompromiseChecker interface {
	Compromised(context.Context, string) (bool, error)
}

// Policy is the service-boundary policy for a newly selected password. The
// structural rules remain separate so HTTP handlers can reject malformed input
// without treating that client-side check as the security boundary.
type Policy struct{ Checker CompromiseChecker }

func (policy Policy) Configured() bool { return policy.Checker != nil }

func (policy Policy) ValidateNew(ctx context.Context, value string) error {
	if err := ValidateForRegistration(value); err != nil {
		return err
	}
	if policy.Checker == nil {
		return ErrInvalidConfiguration
	}
	compromised, err := policy.Checker.Compromised(ctx, value)
	if err != nil {
		return errors.Join(ErrScreenUnavailable, err)
	}
	if compromised {
		return ErrCompromisedPassword
	}
	return nil
}

// DigestSet is a bounded in-memory checker suitable for the high-confidence
// common-password tier. It retains only SHA-256 digests of NFKC/case-folded
// values. A production deployment composes this tier with the full breached
// corpus checker through MultiChecker.
type DigestSet struct {
	values map[[sha256.Size]byte]struct{}
}

func NewDigestSet(values []string) *DigestSet {
	set := &DigestSet{values: make(map[[sha256.Size]byte]struct{}, len(values))}
	for _, value := range values {
		if value == "" {
			continue
		}
		set.values[screenDigest(value)] = struct{}{}
	}
	return set
}

func (set *DigestSet) Compromised(ctx context.Context, value string) (bool, error) {
	if set == nil || len(set.values) == 0 {
		return false, ErrInvalidConfiguration
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	_, found := set.values[screenDigest(value)]
	return found, nil
}

// MultiChecker fails closed: an unavailable tier cannot silently weaken the
// policy even when another tier reports no match.
type MultiChecker []CompromiseChecker

func (checkers MultiChecker) Compromised(ctx context.Context, value string) (bool, error) {
	if len(checkers) == 0 {
		return false, ErrInvalidConfiguration
	}
	for _, checker := range checkers {
		if checker == nil {
			return false, ErrInvalidConfiguration
		}
		compromised, err := checker.Compromised(ctx, value)
		if err != nil {
			return false, err
		}
		if compromised {
			return true, nil
		}
	}
	return false, nil
}

func screenDigest(value string) [sha256.Size]byte {
	canonical := norm.NFKC.String(strings.ToLower(value))
	return sha256.Sum256([]byte(canonical))
}
