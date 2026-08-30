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

### First run

From an empty database:

```sh
go run ./cmd/recruiting migrate                                          # apply migrations
go run ./cmd/recruiting admin create-org --name "Acme" --admin-email a@acme.example
go run ./cmd/recruiting admin seed-problems                              # platform problem bank
go run ./cmd/recruiting serve &
go run ./cmd/recruiting worker &
go run ./cmd/recruiting runner &                                         # separate host in production; see below
```

`admin seed-problems` connects as the schema owner and writes under the
platform org, since no tenant may write it; re-running it refreshes the bank
rather than duplicating it.

## Configuration

All settings come from the environment; `.env.example` documents the required
ones with values matching the Compose stack.

| Variable | Purpose |
| -------- | ------- |
| `DATABASE_URL` | Postgres connection string |
| `BLOB_ENDPOINT`, `BLOB_BUCKET`, `BLOB_KEY`, `BLOB_SECRET` | Object storage for resumes and recordings |
| `SMTP_URL` | Outbound email |
| `RUNNER_URL`, `RUNNER_SECRET` | Execution service address and shared secret, as reached from the worker |
| `SESSION_SECRET` | Session cookie signing key |
| `BASE_URL` | Public origin used to build links in email |

The following are optional; each has a default suited to local development.

| Variable | Default | Purpose |
| -------- | ------- | ------- |
| `LISTEN_ADDR` | `:8080` | `serve`'s HTTP listener |
| `METRICS_ADDR` | `:9090` | `worker` and `runner`'s standalone `/metrics` listener (`serve` answers `/metrics` on `LISTEN_ADDR` instead) |
| `RUNNER_LISTEN` | `:8081` | `runner`'s own listener |
| `RUNNER_RUNTIME` | `runsc` | The Docker OCI runtime `runner` executes candidate code under (gVisor) |
| `RUNNER_ALLOW_INSECURE_RUNTIME` | unset | Set to `1` to let `runner` fall back to `runc` (no sandbox) when `RUNNER_RUNTIME` is unavailable — **development only** |
| `RUNNER_IMAGE_PREFIX` | `recruiting-runner-` | Prefix `runner` expects on its per-language execution images |
| `RUNNER_SQL_URL` | unset | A Postgres URL with `CREATEDB`/`CREATEROLE`, used by `runner` to provision a throwaway database per SQL execution; SQL problems are disabled without it |
| `RUNNER_MAX_CONCURRENT` | `2` | `runner`'s concurrent execution cap |

### gVisor requirement

`runner` refuses to start unless Docker reports the `runsc` (gVisor) OCI
runtime, since that runtime is what keeps candidate code from touching the
host. `RUNNER_ALLOW_INSECURE_RUNTIME=1` bypasses the check for local
development, logging a warning on every start; production must install
gVisor rather than set it. `runner` should also run on a network-isolated
host reached only by the worker, with its own `RUNNER_SQL_URL` database
separate from the application's.

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

## Observability

Every mode logs structured JSON to stdout via `log/slog` and answers
`/metrics` in Prometheus text exposition format: `serve` on `LISTEN_ADDR`
alongside its other routes, `worker` and `runner` on their own `METRICS_ADDR`
listener. All three read from the same metrics registry
(`internal/observe`), registered once so the two-listener split can never
double-register a name.

### Log fields

Every HTML-surface request logs one `http_request` line carrying, at least:

| Field | Meaning |
| ----- | ------- |
| `request_id` | Per-request id (chi's `middleware.RequestID`) |
| `org_id` | The signed-in principal's org, once auth has resolved one; empty before that |
| `method`, `path`, `status`, `duration_ms` | The request and how it was answered |

Log lines never carry a resume, source code, a submission, or a token: the
request-logging middleware reads only routing metadata, never a request or
response body, so there is nothing in the handler layer capable of leaking
into a log line by way of it.

### Metrics

| Metric | Kind | Meaning |
| ------ | ---- | ------- |
| `recruiting_queue_depth{kind}` | gauge | `river_job` rows waiting or retrying, by job kind; polled from `river_job` directly every 15s |
| `recruiting_runner_execute_seconds{outcome}` | histogram | `runner.execute` job duration, by `ok`/`error` |
| `recruiting_runner_execute_failures_total` | counter | `runner.execute` jobs whose handler returned an error |
| `recruiting_booking_conflicts_total` | counter | Interview-slot bookings refused because the slot was already taken |
| `recruiting_email_send_failures_total` | counter | `email.send` jobs whose handler returned an error |

`river_job` is the one table Postgres's row-level security exempts: it
carries no `org_id` and no tenant data, only job kinds and JSON payloads
already scoped by whatever transaction enqueued them, so metrics can read it
directly instead of going through a tenant-scoped transaction the way every
other query in this codebase must.

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
| `internal/observe` | Request-scoped logging and the Prometheus metrics registry every mode's `/metrics` serves |
| `db/migrations`, `db/queries` | goose migrations and sqlc queries |
