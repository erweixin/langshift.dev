package postgres

import idempotencypostgres "github.com/langshift/lites/internal/idempotency/postgres"

// Compatibility aliases keep Identity's public construction sites stable
// while the transaction boundary is shared by all control-plane services.
type IdempotencyExecutor = idempotencypostgres.Executor
type IdempotencyInput = idempotencypostgres.Input
type IdempotentMutation = idempotencypostgres.Mutation
