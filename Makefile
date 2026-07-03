ifneq (,$(wildcard .env))
include .env
export
endif

DATABASE_URL ?= postgres://lites:lites@localhost:5432/lites?sslmode=disable
LITES_HTTP_ADDR ?= :8080
LITES_LLM_CONFIG ?= config/llm.example.yaml
LITES_MIGRATIONS_DIR ?= backend/migrations
PNPM ?= pnpm
BACKEND_LITES_LLM_CONFIG := $(abspath $(LITES_LLM_CONFIG))

ifeq ($(SKIP_DOCKER),1)
MIGRATE_DEPS :=
else
MIGRATE_DEPS := wait-db
endif

.PHONY: dev dev-db wait-db migrate typegen test lint build build-backend build-frontend clean

dev: wait-db migrate
	@echo "Starting backend on $(LITES_HTTP_ADDR) and frontend on http://localhost:5173"
	@trap 'kill 0' INT TERM EXIT; \
	(cd backend && DATABASE_URL="$(DATABASE_URL)" LITES_HTTP_ADDR="$(LITES_HTTP_ADDR)" LITES_LLM_CONFIG="$(BACKEND_LITES_LLM_CONFIG)" LITES_MIGRATIONS_DIR="migrations" go run ./cmd/lites serve) & \
	$(PNPM) --filter @lites/frontend dev --host 0.0.0.0

dev-db:
	docker compose -f docker-compose.dev.yml up -d postgres

wait-db: dev-db
	@echo "Waiting for Postgres..."
	@for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do \
		docker compose -f docker-compose.dev.yml exec -T postgres pg_isready -U lites -d lites >/dev/null 2>&1 && exit 0; \
		sleep 1; \
	done; \
	echo "Postgres did not become ready in time" >&2; \
	exit 1

migrate: $(MIGRATE_DEPS)
	cd backend && DATABASE_URL="$(DATABASE_URL)" LITES_MIGRATIONS_DIR="migrations" go run ./cmd/lites migrate

typegen:
	$(PNPM) typegen

test: typegen
	cd backend && go test ./...
	$(PNPM) --filter @lites/frontend typecheck
	$(PNPM) --filter @lites/frontend build

lint:
	cd backend && test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './bin/*'))"
	cd backend && go vet ./...
	$(PNPM) --filter @lites/frontend typecheck

build: build-backend build-frontend

build-backend:
	cd backend && go build -o bin/lites-backend ./cmd/lites

build-frontend:
	$(PNPM) --filter @lites/frontend build

clean:
	rm -rf backend/bin frontend/dist
