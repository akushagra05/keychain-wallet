.PHONY: help up down db run stub build test test-race tidy fmt vet

DATABASE_URL ?= postgres://wallet:wallet@localhost:5432/wallet?sslmode=disable
export DATABASE_URL

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

up: ## Start Postgres + wallet service (docker compose)
	docker compose up --build

down: ## Stop everything and drop the DB volume
	docker compose down -v

db: ## Start only Postgres (for local `make run`)
	docker compose up -d postgres

run: ## Run the wallet service locally (needs Postgres; see `make db`)
	go run ./cmd/walletd

stub: ## Run the Order Service stub against a running wallet service
	go run ./cmd/orderstub

build: ## Compile all binaries
	go build ./...

test: ## Run all tests (spins up throwaway Postgres via testcontainers; needs Docker)
	go test ./...

test-race: ## Run all tests with the race detector
	go test -race ./...

tidy: ## go mod tidy
	go mod tidy

fmt: ## Format the code
	go fmt ./...

vet: ## Static checks
	go vet ./...
