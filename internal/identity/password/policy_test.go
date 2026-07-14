package password

import (
	"context"
	"errors"
	"testing"
)

type checkerFunc func(context.Context, string) (bool, error)

func (function checkerFunc) Compromised(ctx context.Context, value string) (bool, error) {
	return function(ctx, value)
}

func TestPolicyRejectsCommonPasswordWithoutRetainingPlaintext(t *testing.T) {
	const common = "correct horse battery staple"
	set := NewDigestSet([]string{common})
	policy := Policy{Checker: set}
	for _, candidate := range []string{common, "CORRECT HORSE BATTERY STAPLE", "ｃｏｒｒｅｃｔ ｈｏｒｓｅ ｂａｔｔｅｒｙ ｓｔａｐｌｅ"} {
		if err := policy.ValidateNew(context.Background(), candidate); !errors.Is(err, ErrCompromisedPassword) {
			t.Fatalf("candidate=%q error=%v", candidate, err)
		}
	}
	for digest := range set.values {
		if string(digest[:]) == common {
			t.Fatal("digest set retained plaintext password")
		}
	}
}

func TestPolicyFailsClosedWhenScreenIsUnavailable(t *testing.T) {
	dependencyErr := errors.New("breach corpus unavailable")
	policy := Policy{Checker: checkerFunc(func(context.Context, string) (bool, error) { return false, dependencyErr })}
	err := policy.ValidateNew(context.Background(), "a sufficiently long password")
	if !errors.Is(err, ErrScreenUnavailable) || !errors.Is(err, dependencyErr) {
		t.Fatalf("error=%v", err)
	}
	if err = (Policy{}).ValidateNew(context.Background(), "a sufficiently long password"); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("unconfigured policy error=%v", err)
	}
}

func TestMultiCheckerStopsOnMatchAndPropagatesDependencyFailure(t *testing.T) {
	calls := 0
	match := checkerFunc(func(context.Context, string) (bool, error) { calls++; return true, nil })
	unreachable := checkerFunc(func(context.Context, string) (bool, error) { calls++; return false, errors.New("unexpected") })
	compromised, err := (MultiChecker{match, unreachable}).Compromised(context.Background(), "candidate password value")
	if err != nil || !compromised || calls != 1 {
		t.Fatalf("compromised=%v calls=%d error=%v", compromised, calls, err)
	}
	dependencyErr := errors.New("offline corpus unavailable")
	_, err = (MultiChecker{checkerFunc(func(context.Context, string) (bool, error) { return false, dependencyErr })}).Compromised(context.Background(), "candidate password value")
	if !errors.Is(err, dependencyErr) {
		t.Fatalf("error=%v", err)
	}
}
