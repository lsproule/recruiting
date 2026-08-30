SHELL := /bin/bash
.DEFAULT_GOAL := check

GO ?= go
GOFMT := $(shell $(GO) env GOROOT)/bin/gofmt

# Code generators are pinned here and run via `go run` so no global install is
# required and go.mod stays free of their dependency trees.
TEMPL_VERSION ?= v0.3.1020
SQLC_VERSION  ?= v1.31.1
GOOSE_VERSION ?= v3.27.3
TEMPL := $(GO) run github.com/a-h/templ/cmd/templ@$(TEMPL_VERSION)
SQLC  := $(GO) run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
GOOSE := $(GO) run github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)

GOLANGCI_VERSION ?= v2.12.2
GOLANGCI_LINT := $(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)

COMPOSE ?= docker compose

# Matches the Postgres service in docker-compose.yml.
TEST_DATABASE_URL ?= postgres://recruiting:recruiting@localhost:5433/recruiting?sslmode=disable

.PHONY: check test test-integration openapi-lint generate migrate migration dev-up dev-down dev-logs fmt tidy build help

## check: formatting, vet, lint, and build
check: fmt
	$(GO) vet ./...
	$(GOLANGCI_LINT) run
	$(GO) build ./...

## fmt: fail if any file is not gofmt-clean
fmt:
	@unformatted=$$($(GOFMT) -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt-clean:"; echo "$$unformatted"; exit 1; \
	fi

## test: unit tests
test:
	$(GO) test ./...

## test-integration: tests tagged `integration` against the Compose Postgres
test-integration:
	DATABASE_URL="$(TEST_DATABASE_URL)" $(GO) test -tags integration -count=1 ./...

## openapi-lint: validate the served OpenAPI document
openapi-lint:
	$(GO) test -tags openapilint -count=1 -run TestOpenAPIDocumentIsValid ./internal/api

## generate: run the code generators that have inputs
generate:
	@if compgen -G "internal/web/**/*.templ" > /dev/null || compgen -G "internal/web/*.templ" > /dev/null; then \
		$(TEMPL) generate; \
	else echo "no .templ files yet; skipping templ"; fi
	@if compgen -G "db/queries/*.sql" > /dev/null; then \
		$(SQLC) generate; \
	else echo "no sqlc queries yet; skipping sqlc"; fi
	$(GO) generate ./...

## build: build the binary into bin/
build:
	$(GO) build -o bin/recruiting ./cmd/recruiting

## tidy: tidy the module
tidy:
	$(GO) mod tidy

## dev-up: start Postgres, MinIO, and Mailpit
dev-up:
	$(COMPOSE) up -d --wait

## dev-down: stop the dev stack and remove its volumes
dev-down:
	$(COMPOSE) down -v

## dev-logs: follow the dev stack logs
dev-logs:
	$(COMPOSE) logs -f

## migrate: apply migrations to the Compose Postgres
migrate:
	@if compgen -G "db/migrations/*.sql" > /dev/null; then \
		$(GOOSE) -dir db/migrations postgres "$(TEST_DATABASE_URL)" up; \
	else echo "no migrations yet; nothing to apply"; fi

## migration: scaffold a migration, e.g. `make migration name=create_org`
migration:
	$(GOOSE) -dir db/migrations create $(name) sql

help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## //'
