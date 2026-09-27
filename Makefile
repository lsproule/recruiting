SHELL := /bin/bash
# A bare `make` builds. `make check` is the full gate — formatting, vet, lint
# and build — and lint alone builds golangci-lint from source the first time.
.DEFAULT_GOAL := build

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
# The linter has to be built with the module's own Go: `go run pkg@version`
# otherwise picks the oldest toolchain the tool accepts, which then refuses
# to lint a module that targets a newer language version.
MODULE_GO := $(shell $(GO) list -m -f '{{.GoVersion}}')
GOLANGCI_LINT := GOTOOLCHAIN=go$(MODULE_GO) $(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)

COMPOSE ?= docker compose

# Matches the Postgres service in docker-compose.yml.
# The development database, and the throwaway one the integration suite gets.
# They are separate on purpose: the suite migrates, seeds and truncates freely,
# and TestMigrationsRoundTrip drops the app_rw role, which cannot happen while
# another database still carries grants for it.
DEV_DATABASE_URL ?= postgres://recruiting:recruiting@localhost:5433/recruiting?sslmode=disable
TEST_DATABASE_NAME ?= recruiting_test
TEST_DATABASE_URL ?= postgres://recruiting:recruiting@localhost:5433/$(TEST_DATABASE_NAME)?sslmode=disable
PSQL_ADMIN = docker exec -i recruiting-postgres-1 psql -q -U recruiting -d postgres -v ON_ERROR_STOP=1

.PHONY: check test test-integration test-e2e openapi-lint generate migrate migration dev-up dev-down dev-logs runner-images create-org seed-problems seed-demo dev-serve dev-worker dev-runner fmt tidy build help

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
	@$(PSQL_ADMIN) -c "drop database if exists $(TEST_DATABASE_NAME) with (force)" >/dev/null
	@$(PSQL_ADMIN) -c "create database $(TEST_DATABASE_NAME) owner recruiting" >/dev/null
	@status=0; DATABASE_URL="$(TEST_DATABASE_URL)" $(GO) test -tags integration -count=1 ./... || status=$$?; \
	$(PSQL_ADMIN) -c "drop database if exists $(TEST_DATABASE_NAME) with (force)" >/dev/null; \
	exit $$status

## test-e2e: browser end-to-end checks; boots serve/worker/runner and runs Playwright
test-e2e:
	web/e2e/run.sh $(args)

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

## build: build the binary into bin/ (the default)
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

## runner-images: build the sandbox images; RUNNER_LANGUAGES="python rust" restricts the set
runner-images:
	runner/images/build.sh

## migrate: apply migrations to the Compose Postgres
migrate:
	@if compgen -G "db/migrations/*.sql" > /dev/null; then \
		set -a && . ./.env && set +a && DATABASE_URL="$(DEV_DATABASE_URL)" $(GO) run ./cmd/recruiting migrate; \
	else echo "no migrations yet; nothing to apply"; fi

## create-org: bootstrap an org and its first admin, e.g. `make create-org name="Acme" admin_email=a@acme.example`
create-org:
	set -a && . ./.env && set +a && $(GO) run ./cmd/recruiting admin create-org --name "$(name)" --admin-email "$(admin_email)"

## seed-problems: import the platform's built-in problem bank; needs `make dev-runner` running
seed-problems:
	set -a && . ./.env && set +a && $(GO) run ./cmd/recruiting admin seed-problems

## seed-demo: fill a fresh demo org with clients, roles, candidates, sittings, interviews, sprints, shortlists; args="--name 'Acme Talent'" to vary it
seed-demo:
	set -a && . ./.env && set +a && $(GO) run ./cmd/recruiting admin seed-demo $(args)

## dev-serve: run `serve` with .env loaded
dev-serve:
	set -a && . ./.env && set +a && $(GO) run ./cmd/recruiting serve

## dev-worker: run `worker` with .env loaded
dev-worker:
	set -a && . ./.env && set +a && $(GO) run ./cmd/recruiting worker

## dev-runner: run `runner` with .env loaded against the Compose Postgres, no gVisor required
dev-runner:
	set -a && . ./.env && set +a && \
	RUNNER_ALLOW_INSECURE_RUNTIME=1 RUNNER_SQL_URL="$(DEV_DATABASE_URL)" \
	$(GO) run ./cmd/recruiting runner

## migration: scaffold a migration, e.g. `make migration name=create_org`
migration:
	$(GOOSE) -dir db/migrations create $(name) sql

help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## //'
