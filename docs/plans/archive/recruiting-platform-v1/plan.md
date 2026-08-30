# Recruiting Platform v1

## Objective
Deliver the platform described in `docs/specs/2026-08-29-recruiting-platform.md`: a multi-tenant Go recruiting SaaS with jobs, intake, typed pipeline, booked phone screens with scorecards, release-gated client portal, rule-based talent pool, and in-house proctored code/SQL assessments with a sandboxed runner, session replay, and heuristic integrity signals.

## Global Constraints
- Every domain table carries `org_id`; Postgres RLS is enabled on all of them and the store sets `app.org_id` per transaction. No query path bypasses RLS except the `admin` CLI and migrations.
- `internal/domain` has no I/O imports (no database, net, os beyond errors/time) so state machine, ranker, slot generation, and signal calculators are unit-testable in isolation.
- Web (`internal/web`) and API (`internal/api`) handlers call `internal/service` only; neither imports `internal/store`.
- Code execution happens only in the `runner` mode process; the `serve` and `worker` modes never invoke a language toolchain.
- Integrity signals and risk scores never trigger a pipeline move.
- Client users only ever read applications with `released_at IS NOT NULL` for their own `client_company_id`.
- All side effects on stage entry (email, invites, reminders, scoring) go through the queue, never inline in the request.
- Timestamps stored in UTC; per-user and per-rule timezones are IANA names.
- Files under `docs/specs/` and `docs/plans/` are never edited by a task.

## Acceptance Gate
- `make check`
- `make test`
- `make test-integration`
- `make openapi-lint`

`make check` = `go vet`, `gofmt -l` empty, `templ generate` produces no diff, `sqlc diff` clean. `make test-integration` needs Docker for Postgres and the runner images.

## Shared Interfaces

### I1: Principal
```go
// internal/service/principal.go
type PrincipalKind int
const (
    PrincipalOrgUser PrincipalKind = iota
    PrincipalClientUser
    PrincipalMagicLink
    PrincipalSystem
)
type Principal struct {
    Kind            PrincipalKind
    OrgID           uuid.UUID
    UserID          uuid.UUID   // org_user or client_user id; zero for magic/system
    ClientCompanyID uuid.UUID   // set only for PrincipalClientUser
    Roles           []string    // "admin","recruiter","vetter" for org users
    MagicPurpose    string      // "apply","book","assessment" for PrincipalMagicLink
    SubjectID       uuid.UUID   // job/application/attempt referenced by a magic link
}
```
Every service method takes `(ctx context.Context, p Principal, ...)`. The store opens each transaction with `SET LOCAL app.org_id = p.OrgID` and, for client users, `SET LOCAL app.client_company_id`.

### I2: Stage kinds and move request
```go
// internal/domain/pipeline.go
type StageKind string
const (
    StageGeneric      StageKind = "generic"
    StageInterview    StageKind = "interview"
    StageAssessment   StageKind = "assessment"
    StageClientReview StageKind = "client_review"
    StageTerminal     StageKind = "terminal"
)
type ApplicationStatus string // "active","hired","rejected","withdrawn"
type MoveRequest struct {
    ApplicationID  uuid.UUID
    ToStageID      uuid.UUID
    Reason         string // required for reject and for overrides
    OverridePrereq bool   // bypass scorecard/verdict prerequisite; Reason required
}
// Validate returns a typed error: ErrForbiddenMove, ErrPrereqMissing, ErrReasonRequired, ErrTerminal.
func ValidateMove(actor ActorRole, app Application, from, to Stage, prereqs Prereqs, req MoveRequest) error
```
`ActorRole` ∈ `admin, recruiter, vetter, client, system`. `Prereqs{HasScorecard, HasVerdict bool}`.

### I3: Queue job kinds
Jobs are registered by kind string and JSON payload:
`email.send {template, to, org_id, data}`, `interview.remind {slot_id, offset}`, `assessment.invite {attempt_id}`, `assessment.remind {attempt_id}`, `runner.execute {submission_id}`, `attempt.finalize {attempt_id}`, `signals.compute {attempt_id}`. Each handler is idempotent on its payload.

### I4: Runner wire protocol
`POST /execute` (header `Authorization: Bearer <shared secret>`)
```json
{"id":"<uuid>","language":"python|node|go|java|sql","source":"...",
 "sql_schema":"...optional seed SQL...",
 "tests":[{"id":"<uuid>","input":"...","expected":"...","weight":1}],
 "limits":{"cpu_ms":2000,"wall_ms":5000,"mem_mb":256,"output_kb":64,"pids":64}}
```
Response 200:
```json
{"id":"<uuid>","status":"ok|compile_error|runtime_error|timeout|error",
 "compile_output":"...",
 "results":[{"test_id":"<uuid>","status":"pass|fail|error|timeout","stdout_hash":"sha256","stderr_tail":"...","time_ms":12,"mem_kb":1024}]}
```
Idempotent by `id`; a repeated `id` returns the stored result.

### I5: Attempt event
```json
{"seq":123,"t":1724900000123,"problem_id":"<uuid>","type":"edit|paste|focus|blur|run|submit|lang_change",
 "data":{}}
```
`edit.data` = CodeMirror changeset JSON; `paste.data` = `{len, sha256, internal:bool}` where `internal` means the clipboard sha256 matched text copied from the page's own editor; `run/submit.data` = `{submission_id}`; `lang_change.data` = `{language}`. Client POSTs `{"attempt_id","events":[...]}` in batches; server rejects a seq ≤ last stored seq for that attempt and stamps `received_at`.

## Tasks

### T01: Project scaffold, modes, and Makefile

- **Goal**: A buildable Go module with `serve`, `worker`, `runner`, `migrate`, `admin` subcommands, Makefile targets used by the Acceptance Gate, and Docker Compose for Postgres + MinIO + Mailpit.
- **Dependencies**: none
- **Inputs**: `mise.toml`, spec Architecture section
- **Scope**: `go.mod`, `go.sum`, `cmd/recruiting/**`, `internal/config/**`, `Makefile`, `docker-compose.yml`, `.golangci.yml`, `sqlc.yaml`, `README.md`, `.gitignore`, `internal/{domain,store,service,api,web,runner,mail,blob,queue}/doc.go`
- **Contract**:
  - Root command dispatches to the five modes; each mode is a stub that loads config and exits 0 with a log line because later tasks fill them in.
  - Config from env with a `.env.example`: `DATABASE_URL`, `BLOB_ENDPOINT/BUCKET/KEY/SECRET`, `SMTP_URL`, `RUNNER_URL`, `RUNNER_SECRET`, `SESSION_SECRET`, `BASE_URL`.
  - Makefile targets `check`, `test`, `test-integration`, `openapi-lint`, `generate`, `dev-up`, `dev-down`; `test-integration` runs tests tagged `integration` with `DATABASE_URL` pointed at the Compose Postgres.
  - `openapi-lint` fetches `/api/v1/openapi.json` from a test server and validates it with a Go OpenAPI validator library, exiting nonzero on invalid; until T19 it may validate an empty-but-valid document.
- **Acceptance**: `go run ./cmd/recruiting serve` on an empty config prints a clear missing-config error; all Makefile targets exist and pass on the empty scaffold; README lists modes, targets, and Compose usage.
- **Non-goals**: Any domain code, migrations, HTTP routes beyond a health endpoint.
- **Implementation notes**: Use chi as the router even for the health endpoint so Huma mounts on it later. Pin tool versions in `mise.toml` (`templ`, `sqlc`, `golangci-lint`, `goose`).
- **Complexity**: standard
- **Validation**: checks-only
- **Checks**: `make check`, `make test`, `make test-integration`, `make openapi-lint`
- **Escalate if**: The Compose stack needs services beyond Postgres, MinIO, Mailpit.

### T02: Schema, migrations, RLS, and store layer

- **Goal**: All v1 tables from the spec exist via goose migrations with RLS policies; the store opens tenant-scoped transactions from a `Principal`.
- **Dependencies**: T01
- **Inputs**: spec Interfaces and Data; I1
- **Scope**: `migrations/**`, `internal/store/**`, `sqlc.yaml`, `internal/service/principal.go`
- **Contract**:
  - One migration per bounded area (tenancy/auth, ats, scheduling, assessment, pool, email) so later tasks can extend without rewriting.
  - Every domain table has `org_id uuid not null`, RLS enabled and forced, policy `org_id = current_setting('app.org_id')::uuid`; client-user policies on `application`, `job`, `candidate`, `resume`, `scorecard`, `review` additionally require `client_company_id = current_setting('app.client_company_id', true)::uuid` and `released_at is not null` where applicable.
  - Application DB role is not the table owner because RLS is bypassed for owners; migrations run as owner, `serve`/`worker` run as `app_rw`.
  - `store.WithTx(ctx, p Principal, fn)` sets the session settings and rolls back on error; `store.WithSystemTx` is only reachable from `admin` and `migrate` because everything else must be tenant-scoped.
  - `interview_slot` has a unique index on `(vetter_id, starts_at)`; `application` unique on `(job_id, candidate_id)`; `candidate` unique on `(org_id, lower(email))`; `attempt_event` unique on `(attempt_id, seq)`.
- **Acceptance**: `migrate up` and `down` are clean; sqlc generates without error; integration test proves a query under org A never returns org B rows even with no WHERE filter; client-user tx cannot read an unreleased application.
- **Non-goals**: Business logic, seed data beyond a platform `org` row for seed problems.
- **Implementation notes**: Platform seed problems use a fixed `org_id` of the platform org; RLS policy for `problem` allows `org_id = app.org_id OR org_id = platform_org_id`.
- **Complexity**: advanced
- **Validation**: test-first
- **Test scenarios**: cross-org select returns zero rows; client user sees released only; owner role bypass detected and rejected by a test that asserts `app_rw` is not owner; unique constraints reject duplicate slot/application.
- **Checks**: `make check`, `make test-integration`
- **Interfaces**: I1
- **Escalate if**: RLS forces a design where a legitimately cross-org read is needed.

### T03: Authentication, sessions, and magic links

- **Goal**: Org users and client users log in with email/password; magic links resolve to principals; password reset works.
- **Dependencies**: T02
- **Inputs**: I1; `internal/store`
- **Scope**: `internal/service/auth*.go`, `internal/web/auth/**`, `internal/web/middleware/**`, `internal/service/magiclink*.go`
- **Contract**:
  - Passwords hashed with argon2id; sessions stored in `session` with sliding expiry and rotated id on login.
  - Middleware resolves a principal from cookie or magic-link token and rejects a client-user cookie on `/app/*` and an org-user cookie on `/client/*` because the two surfaces must never share a session.
  - Magic link tokens are 32 random bytes, stored hashed, single purpose, expire, and `Consume` marks `used_at` for `apply` while `book` and `assessment` remain reusable until expiry/revocation because candidates return to those pages.
  - CSRF token required on every non-GET HTML form.
- **Acceptance**: login/logout/reset flows via templ pages; expired or revoked link renders a clear error page; wrong-surface cookie is rejected with 403.
- **Non-goals**: API tokens (T19), user management UI (T04).
- **Implementation notes**: Put principal resolution in one middleware used by both surfaces; magic-link resolution reads the token from the path and swaps it for a short-lived cookie on assessment pages.
- **Complexity**: advanced
- **Validation**: test-first
- **Test scenarios**: correct/incorrect password; session rotation; link purpose mismatch; reuse of consumed `apply` link; reuse of `book` link succeeds; CSRF missing → 403.
- **Checks**: `make check`, `make test`, `make test-integration`
- **Interfaces**: I1
- **Escalate if**: Requirement emerges for SSO or MFA.

### T04: Org administration and CLI bootstrap

- **Goal**: Admin CLI creates an org and first admin; admins manage org users, roles, client companies, client users, and org settings in the web UI.
- **Dependencies**: T03
- **Inputs**: `internal/service/auth*.go`, `cmd/recruiting`
- **Scope**: `cmd/recruiting/admin*.go`, `internal/service/org*.go`, `internal/web/admin/**`, `internal/web/layout/**`
- **Contract**:
  - `admin create-org --name --admin-email` prints a one-time password-set link.
  - Org settings include `pool_score_threshold` (default 80), integrity weights (JSON, defaults from spec), `assessment_invite_days` (7).
  - Base templ layout with nav, flash messages, htmx + Alpine assets served locally (no CDN) because candidate assessment pages must work in restrictive networks.
- **Acceptance**: CLI creates org; admin logs in, creates recruiter/vetter/client company/client user; client user logs in to `/client` and sees empty job list.
- **Non-goals**: Jobs, pipeline.
- **Implementation notes**: Seed the default pipeline template in the same transaction as org creation so T05 can rely on it existing.
- **Complexity**: standard
- **Validation**: test-first
- **Test scenarios**: non-admin denied on admin routes; settings validation rejects negative weights; client user creation requires a client company.
- **Checks**: `make check`, `make test`, `make test-integration`
- **Interfaces**: I1
- **Escalate if**: none anticipated.

### T05: Jobs, pipeline templates, and stages

- **Goal**: Recruiters create/edit jobs with a per-job pipeline derived from the org default template.
- **Dependencies**: T04
- **Inputs**: I2; spec stage table
- **Scope**: `internal/domain/pipeline.go`, `internal/service/job*.go`, `internal/web/jobs/**`
- **Contract**:
  - Org gets the spec default template seeded on org creation; recruiter can add/remove/rename/reorder stages per job with kind and `unblind` flag.
  - A job must have exactly one `hired` terminal and one `rejected` terminal stage and at least one non-terminal stage.
  - Stage edits after applications exist may not delete a stage that holds applications.
  - Job fields: title, description markdown, skills tags, seniority (`junior|mid|senior|staff`), location, `remote_policy` (`remote|hybrid|onsite`), salary band, `blind_mode`, status (`draft|open|closed`).
- **Acceptance**: job CRUD pages; pipeline editor with Alpine reorder; validation errors shown inline.
- **Non-goals**: Applications, suggestions panel (T12).
- **Implementation notes**: Copy template stages into `stage` rows at job creation; never reference template rows from applications.
- **Complexity**: standard
- **Validation**: test-first
- **Test scenarios**: template copy on job create; deleting occupied stage rejected; two hired stages rejected; reorder persists.
- **Checks**: `make check`, `make test`, `make test-integration`
- **Interfaces**: I2
- **Escalate if**: none.

### T06: Candidates, resumes, and public apply page

- **Goal**: Public apply URL creates a candidate + application; resumes stored in blob storage with extracted text searchable.
- **Dependencies**: T05
- **Inputs**: `internal/blob/doc.go`, spec Intake
- **Scope**: `internal/blob/**`, `internal/service/candidate*.go`, `internal/service/resume*.go`, `internal/web/apply/**`, `internal/web/candidates/**`, `internal/domain/textextract*.go`
- **Contract**:
  - Apply page at `/apply/{job_slug}` is unauthenticated; job must be `open`; file ≤10 MB and PDF/DOCX by sniffing, not extension.
  - Candidate upsert keyed `(org_id, lower(email))`; a second application to the same job returns a friendly "already applied".
  - Text extraction failure does not fail the application; `resume.text_status = failed`.
  - Full-text search over `candidate.name`, `resume.text` via Postgres `tsvector` column maintained by trigger.
- **Acceptance**: apply flow end-to-end into stage 1; recruiter manual add; candidate list with search; resume download via signed URL.
- **Non-goals**: Pipeline moves.
- **Implementation notes**: Use a pure-Go PDF text extractor and a DOCX (zip+XML) reader; run extraction in the request path but bounded to 10s, marking `failed` on timeout.
- **Complexity**: standard
- **Validation**: test-first
- **Test scenarios**: oversize file rejected; renamed `.exe` rejected; duplicate apply; extraction failure path; search hit on resume text.
- **Checks**: `make check`, `make test`, `make test-integration`
- **Escalate if**: PDF extraction library choice requires cgo.

### T07: Queue, worker mode, and email

- **Goal**: Postgres-backed job queue with worker mode; templated SMTP email with logging and retry.
- **Dependencies**: T02
- **Inputs**: I3; spec Email
- **Scope**: `internal/queue/**`, `internal/mail/**`, `cmd/recruiting/worker*.go`, `templates/email/**`
- **Contract**:
  - Use `river` (or equivalent Postgres queue) with per-kind handlers registered from I3; unknown kinds fail loudly at startup.
  - `email.send` renders a named template with org branding (name only), writes `email_log` with `queued|sent|failed` and error, retries with backoff up to 5 times.
  - All spec-listed templates exist with subject and text+HTML bodies.
- **Acceptance**: worker processes a queued email into Mailpit in Compose; failure logged; scheduled jobs (`interview.remind` with future `run_at`) fire at the right time.
- **Non-goals**: Domain triggers that enqueue (owned by later tasks).
- **Implementation notes**: Register handlers in a single table keyed by I3 kind; the worker fails fast at startup if any I3 kind lacks a handler.
- **Complexity**: standard
- **Validation**: test-first
- **Test scenarios**: retry then success; permanent failure after max attempts; template missing var fails render before send.
- **Checks**: `make check`, `make test`, `make test-integration`
- **Interfaces**: I3
- **Escalate if**: The queue library needs its own migrations conflicting with RLS conventions (queue tables are not domain tables; document the exemption in notes).

### T08: Pipeline state machine, moves, events, and board UI

- **Goal**: Applications move between stages under domain rules with audit events; recruiter board and list views.
- **Dependencies**: T06, T07
- **Inputs**: I2, I3; spec stage table and state machine
- **Scope**: `internal/domain/pipeline*.go`, `internal/service/application*.go`, `internal/web/pipeline/**`
- **Contract**:
  - `ValidateMove` is pure and encodes the spec's role × kind matrix; the service loads prereqs and enqueues stage-entry side effects (`interview` → booking invite, `assessment` → `assessment.invite`) after commit.
  - Every move writes `application_event{actor_kind, actor_id, from_stage, to_stage, reason, override}`.
  - Moving to `rejected` requires a reason; `withdrawn` is recruiter-only.
  - Release/unrelease are separate events, not moves.
- **Acceptance**: board with drag (Alpine) posting htmx moves; list with filters; event timeline on application page; override prompt when prerequisite missing.
- **Non-goals**: Booking, scorecards, client actions.
- **Implementation notes**: Load prereqs (scorecard/verdict presence) in the service, not the domain; enqueue side effects with an after-commit hook so a rolled-back move never emails.
- **Complexity**: advanced
- **Validation**: test-first
- **Test scenarios**: table-driven matrix of (role, from kind, to kind, prereqs, override) → expected error; terminal cannot move; client cannot move out of generic; side effect enqueued exactly once per entry.
- **Checks**: `make check`, `make test`, `make test-integration`
- **Interfaces**: I2, I3
- **Escalate if**: Matrix in spec is ambiguous for a combination.

### T09: Availability and booking

- **Goal**: Vetters set availability; candidates book/reschedule/cancel phone screens via magic link; reminders sent.
- **Dependencies**: T08
- **Inputs**: spec Scheduling; `internal/domain`
- **Scope**: `internal/domain/schedule*.go`, `internal/service/schedule*.go`, `internal/web/availability/**`, `internal/web/book/**`
- **Contract**:
  - Slot generation is a pure function of rules, exceptions, existing slots, buffer, window, and viewer timezone.
  - Booking inserts under the unique index; conflict returns a retryable error rendered as "slot taken, refreshed".
  - Reschedule/cancel allowed until 2h before start; reminders at 24h and 1h are queued on booking and cancelled on change.
  - No-show recorded by vetter as a slot outcome.
- **Acceptance**: vetter availability UI with weekly grid and exceptions; booking page shows slots in candidate tz (Alpine picker); confirmation emails show both parties' local times.
- **Non-goals**: Scorecards (T10), calendar sync.
- **Implementation notes**: Generate slots in UTC then present in the viewer timezone; compute buffers on UTC instants to avoid DST arithmetic errors.
- **Complexity**: advanced
- **Validation**: test-first
- **Test scenarios**: DST boundary week; buffer removes adjacent slot; exception blocks day; concurrent bookings → one wins; cancel within 2h rejected.
- **Checks**: `make check`, `make test`, `make test-integration`
- **Interfaces**: I3
- **Escalate if**: Timezone library behaviour diverges from IANA expectations.

### T10: Scorecard rubrics and scorecards

- **Goal**: Per-stage rubrics; vetters submit scorecards that satisfy the interview prerequisite.
- **Dependencies**: T09
- **Inputs**: `internal/service/application*.go`, I2 `Prereqs`
- **Scope**: `internal/service/scorecard*.go`, `internal/web/scorecards/**`
- **Contract**:
  - Rubric = ordered criteria (name, description); scorecard = per-criterion 1–5 + notes + overall `strong_yes|yes|no|strong_no`.
  - One scorecard per (application, stage, vetter); editable by author only.
  - Submitting an overall `strong_yes` emits a domain event consumed by T12.
- **Acceptance**: vetter sees assigned interviews; submits scorecard; recruiter sees summary on application; advancing now passes prerequisite.
- **Non-goals**: Pool entry creation (T12).
- **Implementation notes**: Store criteria scores as JSONB alongside the rubric snapshot so later rubric edits do not rewrite historical scorecards.
- **Complexity**: standard
- **Validation**: test-first
- **Test scenarios**: score out of range rejected; non-author edit denied; prereq satisfied after submit.
- **Checks**: `make check`, `make test`, `make test-integration`
- **Interfaces**: I2
- **Escalate if**: none.

### T11: Release and client portal

- **Goal**: Recruiters release applications; client users review and Advance/Reject/Request-info with blind mode.
- **Dependencies**: T10
- **Inputs**: spec Client portal; I2
- **Scope**: `internal/service/release*.go`, `internal/service/clientportal*.go`, `internal/web/client/**`
- **Contract**:
  - Release sets `released_at` and enqueues `email.send` client release notice; unrelease clears it and hides the application immediately.
  - Client actions map to `MoveRequest` with `ActorRole=client` and are validated by I2 so no separate permission code path exists.
  - Blind mode redaction happens in the view model, and the redacted fields are never serialized to the client surface (including API) until the application's stage has `unblind`.
  - Client never receives integrity signals, replay, or vetter notes marked internal.
- **Acceptance**: client job list, application detail with resume/scorecards/assessment summary; Advance/Reject (reason required)/Request info; request info emails recruiter; blind mode verified.
- **Non-goals**: Assessment summary content beyond score + verdict (fields may be empty until T14–T18).
- **Implementation notes**: Build a dedicated client view model type; do not pass domain entities to client templates so a new field cannot leak by default.
- **Complexity**: advanced
- **Validation**: test-first
- **Test scenarios**: unreleased hidden; other company's job 404; reject without reason 422; blind redaction on list, detail, and resume download (download denied while blind); unrelease revokes access.
- **Checks**: `make check`, `make test`, `make test-integration`
- **Interfaces**: I2
- **Escalate if**: Blind mode needs redaction inside the resume PDF itself.

### T12: Talent pool and suggestions

- **Goal**: High-quality applications become pool entries; new/edited jobs show ranked suggestions with one-click add.
- **Dependencies**: T11
- **Inputs**: spec Talent pool matching; scorecard event from T10
- **Scope**: `internal/domain/pool*.go`, `internal/service/pool*.go`, `internal/web/pool/**`, `internal/web/jobs/suggestions*.templ`
- **Contract**:
  - Entry created/updated on: recruiter flag, scorecard `strong_yes`, assessment review `pass` with score ≥ org threshold (hook consumed by T18).
  - Ranker is pure: implements the spec formula, threshold 0.2, top 20, exclusions (already in job; rejected by same client company within 6 months).
  - Each suggestion carries a breakdown of the four components.
- **Acceptance**: pool browse/search/edit/remove; suggestions panel on job save; add creates application in stage 1.
- **Non-goals**: Embeddings.
- **Implementation notes**: Emit pool-worthy events from scorecard/review services and consume them in one pool service; keep the ranker in domain with a table-driven test.
- **Complexity**: standard
- **Validation**: test-first
- **Test scenarios**: exact formula values for fixtures; exclusion window edges; threshold cut; idempotent entry update.
- **Checks**: `make check`, `make test`, `make test-integration`
- **Escalate if**: none.

### T13: Problem bank, import, and seed

- **Goal**: Org-scoped problem bank with JSON import validated against reference solutions, plus platform seed set.
- **Dependencies**: T12, T15
- **Inputs**: spec Problem bank; I4
- **Scope**: `internal/service/problem*.go`, `internal/web/problems/**`, `seed/problems/**`, `internal/domain/problemimport*.go`
- **Contract**:
  - Import format is a JSON array of problems with test cases; schema documented in README.
  - Import runs every reference solution through the runner and rejects the whole batch if any fails, reporting per-problem errors.
  - Seed set (≥5 code across difficulties, ≥3 SQL, original content) loads under the platform org and is readable by all orgs.
- **Acceptance**: problem CRUD; import with error report; seed import passes.
- **Non-goals**: Assessment composition (T14).
- **Implementation notes**: Import validation calls the runner synchronously with a bounded concurrency of 4; reject the batch on the first hard failure but continue collecting per-problem errors.
- **Complexity**: standard
- **Validation**: test-first
- **Test scenarios**: batch with one failing reference rejected; unknown language rejected; SQL problem seed executes.
- **Checks**: `make check`, `make test`, `make test-integration`
- **Interfaces**: I4
- **Escalate if**: none.

### T14: Assessments, attempts, candidate session UI, and recording ingest

- **Goal**: Assessments attach to stages; candidates take timed sessions in a CodeMirror island that streams events; runs/submits enqueue execution.
- **Dependencies**: T13
- **Inputs**: I3, I5; spec Attempt and Recording
- **Scope**: `internal/service/assessment*.go`, `internal/service/attempt*.go`, `internal/web/assess/**`, `web/static/assess/**`, `internal/api/attempts*.go`
- **Contract**:
  - Attempt lifecycle `invited→started→submitted|expired→scored→reviewed`; timer enforced server-side from `started_at + duration`; requests after expiry are rejected and last synced source auto-submitted.
  - Event ingest per I5; out-of-order/duplicate seq rejected; gaps flagged on finalize.
  - Paste `internal` detection: page records sha256 of text copied from its own editor and marks pastes matching that set.
  - Island is vanilla TS/JS built with esbuild into `web/static`, no CDN.
- **Acceptance**: invite email → session page → run public tests → submit → finish; timer expiry path; events visible in DB.
- **Non-goals**: Execution results (T16), signals (T17), replay (T18).
- **Implementation notes**: Batch events every 2s and on visibilitychange with `navigator.sendBeacon` fallback; keep the seq counter in the island and persist last-acked seq in sessionStorage.
- **Complexity**: advanced
- **Validation**: test-first
- **Test scenarios**: expiry auto-submit; seq rejection; internal vs external paste; invite window expiry.
- **Checks**: `make check`, `make test`, `make test-integration`
- **Interfaces**: I3, I5
- **Escalate if**: CodeMirror licensing or bundling blocks no-CDN requirement.

### T15: Sandboxed runner service

- **Goal**: `runner` mode executes code and SQL submissions per I4 inside gVisor-isolated containers with hard limits.
- **Dependencies**: T01
- **Inputs**: I4; spec Runner
- **Scope**: `internal/runner/server/**`, `cmd/recruiting/runner*.go`, `runner/images/**`, `runner/harness/**`
- **Contract**:
  - Per-language images: python, node, go, java; SQL uses an ephemeral database in a Postgres container seeded per request.
  - Each execution: fresh container, `--runtime=runsc`, `--network=none`, read-only rootfs, `/tmp` tmpfs size-capped, cpu/mem/pids/wall limits, stdout capped at `output_kb`.
  - Results stored by `id` for idempotency; shared-secret auth; runner has no database credentials for the app DB.
  - Harness per language compares trimmed stdout to expected; SQL compares ordered result set unless problem marks unordered.
- **Acceptance**: hello-world and failing test per language; escape tests fail (network, fork bomb, write outside /tmp, memory, wall time); SQL timeout enforced.
- **Non-goals**: Queue integration (T16).
- **Implementation notes**: Build images from pinned base tags; harness is a small Go binary copied into each image that reads tests from stdin JSON and prints result JSON; SQL path uses a sidecar Postgres per execution with `statement_timeout`.
- **Complexity**: advanced
- **Validation**: test-first
- **Test scenarios**: as Acceptance; repeated `id` returns cached result.
- **Checks**: `make check`, `make test-integration`
- **Interfaces**: I4
- **Escalate if**: Host lacks gVisor; propose Firecracker or a hosted sandbox with the same I4 contract.

### T16: Runner dispatch, scoring, and finalize

- **Goal**: Submissions execute via queue → runner; results stored; attempts scored on finish.
- **Dependencies**: T14, T15
- **Inputs**: I3, I4; spec Scoring
- **Scope**: `internal/runner/client/**`, `internal/service/execution*.go`, `internal/service/scoring*.go`
- **Contract**:
  - `runner.execute` handler calls the runner, stores results per test, marks submission `ok|error`; runner unreachable → retry then `error` without failing the attempt.
  - `attempt.finalize` computes problem and attempt scores per spec, compacts events to blob storage, flags `incomplete_recording` on seq gaps, then enqueues `signals.compute`.
  - Runner receives no PII: payload contains only source, tests, limits.
- **Acceptance**: full attempt with Python + SQL scores correctly; runner down yields resubmittable error state visible to vetter.
- **Non-goals**: Signals.
- **Implementation notes**: Score per spec: problem = Σ weight(passed)/Σ weight; attempt = mean over problems; store both on `attempt` for portal reads.
- **Complexity**: standard
- **Validation**: test-first
- **Test scenarios**: weighted score math; error count surfaced; compaction round-trip.
- **Checks**: `make check`, `make test`, `make test-integration`
- **Interfaces**: I3, I4
- **Escalate if**: none.

### T17: Integrity signals

- **Goal**: Compute the seven spec signals with evidence and an org-weighted 0–100 risk score.
- **Dependencies**: T16
- **Inputs**: spec Integrity signals; I5; org settings weights
- **Scope**: `internal/domain/signals/**`, `internal/service/signals*.go`
- **Contract**:
  - Each signal is a pure function over the event stream + submissions + problem metadata returning `{name, value ∈ [0,1], evidence[], confidence}`.
  - Aggregation clamps Σ weight×value to 0–100; `incomplete_recording` lowers confidence but never blocks.
  - `reference_similarity` uses normalized token similarity against reference solution and other submissions for the same problem within the org.
  - Never writes to `application` or enqueues moves.
- **Acceptance**: fixture event streams (clean, paste-heavy, blur-then-solution, burst) yield expected signal values within tolerance; weights from org settings applied.
- **Non-goals**: UI (T18); auto-actions.
- **Implementation notes**: Process the event stream once into per-problem timelines, then run signals over the timeline; keep thresholds (60s, 30s, 2min, 8 chars/s) as named constants for calibration.
- **Complexity**: advanced
- **Validation**: test-first
- **Test scenarios**: one fixture per signal plus a clean baseline; weight change alters score; gap → confidence drop.
- **Checks**: `make check`, `make test`
- **Interfaces**: I5
- **Escalate if**: A signal cannot be computed from I5 data without extending the event schema.

### T18: Replay viewer, vetter review, and pool hook

- **Goal**: Vetters review attempts with score, signals + evidence, and a scrubbable replay; record verdicts satisfying the assessment prerequisite and feeding the pool.
- **Dependencies**: T17
- **Inputs**: I2 `Prereqs`; T12 hook; compacted event blobs
- **Scope**: `internal/service/review*.go`, `internal/web/reviews/**`, `web/static/replay/**`, `internal/api/replay*.go`
- **Contract**:
  - Replay island reconstructs editor state from the compacted changeset stream with a timeline scrubber that marks paste/blur/run/submit events.
  - Verdict `pass|borderline|fail` + notes; one review per attempt; `pass` with score ≥ threshold creates pool entry via T12.
  - Client portal shows only score + verdict from this task's data.
- **Acceptance**: review page end-to-end; prerequisite satisfied after verdict; pool entry created.
- **Non-goals**: none.
- **Implementation notes**: Replay applies changesets sequentially with CodeMirror `ChangeSet`; precompute snapshots every 500 events for fast scrubbing.
- **Complexity**: standard
- **Validation**: test-first
- **Test scenarios**: replay reconstructs final source exactly; verdict below threshold no pool entry; second review rejected.
- **Checks**: `make check`, `make test`, `make test-integration`
- **Interfaces**: I2
- **Escalate if**: none.

### T19: Huma API coverage and OpenAPI

- **Goal**: `/api/v1` exposes every resource listed in the spec with RFC 9457 errors, session + API-token auth, and a valid OpenAPI 3.1 document.
- **Dependencies**: T18
- **Inputs**: `internal/service/**`; spec API section
- **Scope**: `internal/api/**`, `internal/service/apitoken*.go`, `internal/web/admin/tokens*.templ`
- **Contract**:
  - Every operation resolves a `Principal` via the same middleware as the web surface; client-user tokens only reach portal read endpoints.
  - Operations wrap service methods only; no logic duplicated.
  - Error mapping: domain typed errors → 403/404/409/422 problem details.
- **Acceptance**: coverage test asserts each spec-listed resource has list/get/create where applicable; `make openapi-lint` passes; admin issues/revokes API tokens.
- **Non-goals**: Webhooks, versioning beyond v1.
- **Implementation notes**: Group Huma operations by resource file; share request/response structs with the web layer where sensible; add a test that walks the OpenAPI paths against the spec resource list.
- **Complexity**: standard
- **Validation**: test-first
- **Test scenarios**: token scoping; error mapping table; OpenAPI validity.
- **Checks**: `make check`, `make test`, `make test-integration`, `make openapi-lint`
- **Interfaces**: I1
- **Escalate if**: none.

### T20: Observability

- **Goal**: Structured logs with `org_id`/`request_id`, Prometheus metrics, and README ops notes.
- **Dependencies**: T19
- **Inputs**: `cmd/recruiting`, `internal/queue`, `internal/runner`
- **Scope**: `internal/observe/**`, `cmd/recruiting/**`, `README.md`
- **Contract**: `/metrics` on serve/worker/runner; metrics for queue depth per kind, runner latency and failure rate, booking conflicts, email failures; request logs never include resume text, source code, or tokens.
- **Acceptance**: metrics endpoint scrapeable; log line for a request carries both ids; README documents metrics and log fields.
- **Non-goals**: Dashboards, alerting.
- **Implementation notes**: Use `log/slog` with a request-scoped handler; register metrics in one package to avoid duplicate registration across modes.
- **Complexity**: mechanical
- **Validation**: checks-only
- **Checks**: `make check`, `make test`
- **Escalate if**: none.
