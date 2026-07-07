package outline

import "github.com/jackc/pgx/v5/pgxpool"

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}
