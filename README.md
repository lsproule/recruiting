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

From an empty database, this sequence reaches a running `serve`, `worker`,
and `runner` with the platform's problem bank seeded, using only make
targets:

```sh
make dev-up          # Postgres, MinIO, Mailpit
make runner-images   # sandbox images; RUNNER_LANGUAGES="python rust" to restrict
make migrate         # apply migrations (schema owner)
make create-org name="Acme" admin_email=a@acme.example
make dev-runner &    # runner, no gVisor required locally (RUNNER_ALLOW_INSECURE_RUNTIME=1)
make seed-problems   # platform problem bank; runs through the runner above
make dev-serve &
make dev-worker &
```

`create-org` and `seed-problems` connect as the schema owner (`DATABASE_URL`),
same as `migrate`; `serve` and `worker` refuse that role and connect as
`app_rw` (`DATABASE_URL_APP`) instead — see [Configuration](#configuration).

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

See [Getting started](#getting-started) for the exact command sequence from
an empty database. `admin seed-problems` connects as the schema owner and
writes under the platform org, since no tenant may write it; re-running it
refreshes the bank rather than duplicating it. In production, `runner` runs
on a separate, network-isolated host; see below.

## Configuration

All settings come from the environment; `.env.example` documents the required
ones with values matching the Compose stack.

| Variable | Purpose |
| -------- | ------- |
| `DATABASE_URL` | Postgres connection string, as the schema owner; used by `migrate` and `admin` |
| `BLOB_ENDPOINT`, `BLOB_BUCKET`, `BLOB_KEY`, `BLOB_SECRET` | Object storage for resumes and recordings |
| `SMTP_URL` | Outbound email |
| `RUNNER_URL`, `RUNNER_SECRET` | Execution service address and shared secret, as reached from the worker |
| `SESSION_SECRET` | Session cookie signing key |
| `BASE_URL` | Public origin used to build links in email |

The following are optional; each has a default suited to local development.

| Variable | Default | Purpose |
| -------- | ------- | ------- |
| `DATABASE_URL_APP` | `DATABASE_URL` with the user/password swapped to `app_rw`/`app_rw` | Postgres connection string `serve` and `worker` use; the schema owner role (`DATABASE_URL`) bypasses row-level security and both refuse to start with it |
| `LISTEN_ADDR` | `:8080` | `serve`'s HTTP listener |
| `METRICS_ADDR` | `:9090` | `worker` and `runner`'s standalone `/metrics` listener (`serve` answers `/metrics` on `LISTEN_ADDR` instead) |
| `RUNNER_LISTEN` | `:8081` | `runner`'s own listener |
| `RUNNER_RUNTIME` | `runsc` | The Docker OCI runtime `runner` executes candidate code under (gVisor) |
| `RUNNER_ALLOW_INSECURE_RUNTIME` | unset | Set to `1` to let `runner` fall back to `runc` (no sandbox) when `RUNNER_RUNTIME` is unavailable — **development only** |
| `RUNNER_IMAGE_PREFIX` | `recruiting-runner-` | Prefix `runner` expects on its per-language execution images |
| `RUNNER_SQL_URL` | unset | A Postgres URL with `CREATEDB`/`CREATEROLE`, used by `runner` to provision a throwaway database per SQL execution; SQL problems are disabled without it |
| `RUNNER_MAX_CONCURRENT` | `2` | `runner`'s concurrent execution cap |
| `RTC_ICE_SERVERS` | unset | JSON array of ICE servers interview rooms hand to the browser, e.g. `[{"urls":"stun:stun.example.org:3478"}]`; unset leaves host candidates only, which works on one network and needs STUN or TURN beyond it |

### Sandbox images

`runner` executes each submission in `recruiting-runner-<language>`, one image
per code language in the registry (`internal/domain/languages.go`). `make
runner-images` builds them all from `runner/images/<language>/Dockerfile`;
a cold build of the whole set takes a while, so during development restrict it:

```sh
RUNNER_LANGUAGES="python rust" make runner-images   # or: runner/images/build.sh python rust
```

Every image pins its base by digest, runs as `65534`, and ships the same
harness. Compiled languages warm their toolchain cache at build time because
the sandbox runs with no network and a small memory cap. A language whose
image is missing simply fails its executions; the runner integration tests
skip it with a message.

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
| `test-e2e` | Playwright browser checks against a freshly booted stack |
| `openapi-lint` | Serves `/api/v1/openapi.json` from a test server and validates it |
| `generate` | Runs `templ` and `sqlc` for whichever inputs exist |
| `migrate` | Applies migrations to the Compose Postgres (schema and job queue) |
| `migration` | Scaffolds a migration: `make migration name=create_org` |
| `build` | Builds `bin/recruiting` |
| `dev-up`, `dev-down`, `dev-logs` | Manage the Compose stack |
| `runner-images` | Builds a sandbox image per language for `runner` |
| `create-org` | Bootstraps an org and its first admin: `make create-org name="Acme" admin_email=a@acme.example` |
| `seed-problems` | Imports the platform's built-in problem bank (needs `dev-runner` running) |
| `dev-serve`, `dev-worker` | Run `serve`/`worker` with `.env` loaded |
| `dev-runner` | Runs `runner` with `.env` loaded, `RUNNER_ALLOW_INSECURE_RUNTIME=1`, and `RUNNER_SQL_URL` set to the Compose Postgres |
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

## Browser end-to-end checks

`make test-e2e` covers what neither the unit nor the integration layer can
reach: the CodeMirror keymaps, the Try it sandbox against the real runner, the
consent screen with a camera, a fullscreen exit, the client's view of a
shortlist packet, the process library and stage editor, a video interview
room end to end (booking, both sides joining, a shared screen, the shared
editor, a python run, the scorecard), a screening sprint rotating three
candidates past two interviewers on twenty-second rounds, and a take-home
sitting. It needs the Compose stack (`make dev-up`), the sandbox images
(`make runner-images`), and npm. When no object store answers at
`BLOB_ENDPOINT`, the runner starts `tools/fakes3`, an in-memory S3 stand-in
for development hosts that cannot run MinIO, on a port of its own.

The target runs `web/e2e/run.sh`, which builds the binary, applies
migrations, starts `runner`, `serve`, and `worker` on ports of their own
(8090/8091, so a running `make dev-serve` is left alone), seeds the problem
bank, bootstraps a fresh org, and only then runs Playwright headless. Every
process is stopped again on the way out; logs of the run are left in
`web/e2e/.run/`.

```sh
make test-e2e                                  # the whole suite
make test-e2e args="tests/try-it.spec.ts"      # one file
make test-e2e args="--headed --debug"          # watch it, or step through it
```

The host has no camera, so Chromium synthesises one with
`--use-fake-device-for-media-stream` and grants the prompt with
`--use-fake-ui-for-media-stream`; the consent screen's ID photo is a frame of
that synthetic stream.

The first run installs `@playwright/test` (pinned in `web/e2e/package.json`)
and its Chromium build under `~/.cache/ms-playwright`.

## Hiring processes

A process is the pipeline a job starts from. Every new org is seeded with
three built-in processes (`internal/domain/processes.go`): *Agency
standard* (the default: screen, phone interview, proctored assessment,
shortlist, client review), *Engineering loop* (recruiter call, take-home,
screening sprint, technical interview, HR interview, offer), and *Fast
track* (screening sprint straight from the application, then one
technical interview). `/app/processes` lists them, copies a built-in or an
existing one, and edits any of them stage by stage; `/app/jobs/{id}/pipeline`
is the same editor for one job's own copy. Each stage carries what its kind
needs: an interview's format (phone call or video room) and length, a
sprint's round and break length, a terminal's outcome. Jobs copy the
process at creation (`POST /api/v1/jobs` takes `process_id`; the intake
wizard offers the list) and are never touched by later edits.

## Live interview rooms

An interview stage in the `video` format hosts the interview in the
browser. The candidate joins from their booking page from ten minutes
before the slot; the interviewer joins from `/app/interviews` or the
application page. A room carries every participant's camera and
microphone, a shared screen from anyone who shares one, and a shared
CodeMirror editor with a *Run* button that executes the text through the
sandboxed runner with a stdin of the candidate's choosing. What was written
is saved with the interview and shown on the application page afterwards
(`GET /api/v1/rooms/slot/{slot_id}/code`).

Media travels peer to peer over WebRTC; the server only relays signalling
over an event stream (`GET …/events`) and small posts. The editor is a Yjs
document relayed through the same stream, so it keeps working when media
cannot connect. Room state lives in the memory of the `serve` process that
owns it: a deployment with more than one `serve` replica must route a
room's participants to the same replica (sticky sessions). The editor's
text is persisted on every pause in typing, so a restart loses a couple
of seconds at most. The browser bundle is `web/static/room/room.js`, built
by `web/static/room/build.sh`.

## Screening sprints

A `sprint` stage is speed screening: every candidate in the stage meets
every interviewer for a few minutes each, on a clock. A recruiter sets one
up from the stage's column on the board (`/app/sprints/new`), picks the
interviewers and the candidates, and the platform plans the rotation
(`domain.PlanRounds`): with *n* candidates and *k* interviewers there are
`max(n, k)` rounds and no one is booked twice in a round. Scheduling emails
every candidate a lobby link. From the start time the interviewer's console
(`/app/sprints/{id}/console`) and the candidate's lobby (`/sprint/{token}`)
read the same clock, move into each round's room as it starts, and ask the
interviewer for a one-line rating (score 1–5, a recommendation, a note)
when it ends. The summary (`/app/sprints/{id}`) ranks the candidates by
their ratings and advances or rejects them from there; leaving a sprint
stage needs at least one rating unless a recruiter overrides it with a
reason. An unrated conversation lands in the work queue after five minutes.
The API covers the whole lifecycle under `/api/v1/sprints`.

## Take-home assessments

An assessment has a format: `timed` is the proctored sitting; `take_home`
is the same set with a window in days (three by default, thirty at most),
proctoring off, and candidate copy that reads a due date rather than a
countdown. The candidate may close the page and return through the same
link until the deadline.

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
| `web/static/room` | The live-room island: WebRTC mesh, shared editor, sprint clock |
| `tools/fakes3` | In-memory S3 stand-in for development hosts without MinIO |
