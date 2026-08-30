# Recruiting Platform

A single Go binary that runs every process mode of the recruiting platform,
backed by Postgres, S3-compatible object storage, and SMTP.

## Requirements

- Go (pinned in `mise.toml`; `mise install` provisions it)
- Docker with Compose v2, for the local dependency stack

Every other tool — `golangci-lint`, `templ`, `sqlc`, `goose` — is pinned in the
`Makefile` and fetched by `go run`, so nothing needs a global install.

## Getting started

```sh
cp .env.example .env      # defaults match the Compose stack
make dev-up               # Postgres, MinIO, Mailpit
make check test
```

## Modes

`recruiting <mode>` dispatches to one of five modes. Every mode reads its
configuration from the environment and fails with a message naming each missing
variable.

| Mode      | Purpose |
| --------- | ------- |
| `serve`   | HTTP server: web UI and the JSON API under `/api/v1` |
| `worker`  | Queue consumer: email, reminders, runner dispatch, signal computation |
| `runner`  | Sandboxed code execution service; deployed separately and network-isolated |
| `migrate` | Applies database migrations |
| `admin`   | Administrative commands (create org, create user) |

```sh
go run ./cmd/recruiting serve
```

## Configuration

All settings come from the environment; `.env.example` documents each one with
values matching the Compose stack.

| Variable | Purpose |
| -------- | ------- |
| `DATABASE_URL` | Postgres connection string |
| `BLOB_ENDPOINT`, `BLOB_BUCKET`, `BLOB_KEY`, `BLOB_SECRET` | Object storage for resumes and recordings |
| `SMTP_URL` | Outbound email |
| `RUNNER_URL`, `RUNNER_SECRET` | Execution service address and shared secret |
| `SESSION_SECRET` | Session cookie signing key |
| `BASE_URL` | Public origin used to build links in email |

## Make targets

| Target | What it does |
| ------ | ------------ |
| `check` | gofmt check, `go vet`, golangci-lint, `go build ./...` |
| `test` | Unit tests |
| `test-integration` | Tests tagged `integration` against the Compose Postgres |
| `openapi-lint` | Serves `/api/v1/openapi.json` from a test server and validates it |
| `generate` | Runs `templ` and `sqlc` for whichever inputs exist |
| `migrate` | Applies migrations to the Compose Postgres with `goose` |
| `migration` | Scaffolds a migration: `make migration name=create_org` |
| `build` | Builds `bin/recruiting` |
| `dev-up`, `dev-down`, `dev-logs` | Manage the Compose stack |
| `tidy` | `go mod tidy` |

## Local dependency stack

`make dev-up` starts three services and waits for them to become healthy;
`make dev-down` stops them and removes their volumes.

| Service | Address | Notes |
| ------- | ------- | ----- |
| Postgres | `localhost:5433` | user/password/database all `recruiting` |
| MinIO | `localhost:9000`, console `localhost:9001` | user `recruiting`, password `recruiting-secret` |
| Mailpit | SMTP `localhost:1025`, web `localhost:8025` | captures all outbound mail |

`make test-integration` points `DATABASE_URL` at the Compose Postgres, so run
`make dev-up` first.

## Layout

| Path | Contents |
| ---- | -------- |
| `cmd/recruiting` | Mode dispatch |
| `internal/config` | Environment configuration |
| `internal/domain` | Entities, state machine, ranker, signals — pure Go, no I/O |
| `internal/store` | RLS-aware Postgres access |
| `internal/service` | Use cases; the only layer `api` and `web` call |
| `internal/api` | JSON API (Huma) and the chi router |
| `internal/web` | templ views and htmx handlers |
| `internal/runner` | Execution service client and server |
| `internal/mail`, `internal/blob`, `internal/queue` | Email, object storage, job queue |
| `db/migrations`, `db/queries` | goose migrations and sqlc queries |
