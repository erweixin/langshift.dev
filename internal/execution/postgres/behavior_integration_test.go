//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/behavior"
	behaviorpostgres "github.com/langshift/lites/internal/behavior/postgres"
	"github.com/langshift/lites/internal/integrationfixture"
)

var integrationBehaviorResolver BehaviorResolver = behaviorpostgres.Store{}

type failingBehaviorResolver struct{}

func (failingBehaviorResolver) ResolveCurrent(context.Context, pgx.Tx, string, behavior.Profile, string) (behavior.ChannelBinding, error) {
	return behavior.ChannelBinding{}, errors.New("resolver must not run for an accepted Run replay")
}

func seedExecutionBehavior(t *testing.T, ctx context.Context, admin *pgxpool.Pool, tenantID, userID string, profile behavior.Profile, at time.Time) behavior.ChannelBinding {
	t.Helper()
	binding, err := integrationfixture.SeedBehaviorChannel(ctx, admin, tenantID, userID, profile, "production", at)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}
