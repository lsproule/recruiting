# Recruiting Platform v1

## Outcome

A multi-tenant recruiting SaaS where an agency (tenant) creates jobs for its
client companies, collects candidate applications, vets candidates through
scheduled phone screens and in-house proctored coding/SQL assessments,
shortlists them, and releases them to a client portal where the client
advances or rejects them. High-quality candidates persist in a per-agency
talent pool and are suggested for future jobs.

## Context

- Greenfield repository: `/home/lucas/wrk/recruiting` contains only
  `mise.toml` pinning Go `latest`. No git history, no existing conventions.
- Stack decided in brainstorming: Go, Postgres, Huma (JSON API + OpenAPI 3.1)
  on chi, templ + htmx for server-rendered pages, Alpine.js for client-side
  reactivity, CodeMirror island for the assessment editor, S3-compatible
  object storage, SMTP email.
- Assessments are built in-house (no HackerRank/CodeSignal integration),
  including a sandboxed code runner and session recording with integrity
  signals.

## User Experience and Behavior

### Actors

| Actor | Belongs to | Auth | Sees |
|---|---|---|---|
| Admin | Org | Password + session | Everything in org; manages users, client companies, org settings |
| Recruiter | Org | Password + session | Jobs, applications, pipeline, talent pool, problem bank |
| Vetter | Org | Password + session | Assigned interviews, assessment reviews, scorecards; read on applications |
| Client user | Client company (within org) | Password + session, scoped to one client company | Only jobs for their company; only *released* applications |
| Candidate | Org (person record) | No account; single-purpose magic links | Apply page, booking page, assessment session |

A user may hold multiple org roles. A client user is never also an org user.

### Primary flows

1. **Job creation.** Recruiter selects client company, enters title,
   description (markdown), required skills (tags), seniority, location /
   remote policy, optional salary band. Pipeline pre-filled from the org's
   default template; recruiter may add/remove/rename/reorder stages.
   Recruiter attaches zero or more assessments to `assessment`-kind stages
   and a scorecard rubric to `interview`-kind stages. On save, a
   **Suggested candidates** panel lists ranked talent-pool matches with a
   one-click *Add to job* that creates an application in the first stage.
2. **Intake.** Each job has a public apply URL: name, email, phone, links,
   resume upload (PDF/DOCX, ≤10 MB), optional screening questions. Submission
   creates or reuses the org's candidate record (keyed by email) and creates
   an application in the first stage. Recruiter can also add candidates
   manually or from the pool. Resume text is extracted for full-text search.
3. **Pipeline.** Recruiter views a job's applications as a board (columns =
   stages) and a list. Moving an application is an audited event: actor,
   from/to stage, timestamp, optional reason. Entering a stage triggers that
   stage kind's behaviour (below). Rejection at any stage is terminal but
   does not remove pool eligibility.
4. **Phone interview.** Vetter maintains weekly recurring availability with
   timezone and buffer. When an application enters an `interview` stage the
   recruiter assigns a vetter (or the stage's default) and the candidate is
   emailed a booking link. Candidate sees open slots in their own timezone
   for the next N days, books one; both parties get confirmation and a
   24h/1h reminder; either can reschedule or cancel until 2h before start.
   After the slot, the vetter completes the stage's scorecard (rubric
   criteria scored 1–5 with notes, plus overall recommendation
   *strong-yes / yes / no / strong-no*). No-show is a recorded outcome.
5. **Assessment.** When an application enters an `assessment` stage the
   candidate is emailed an invite valid for a configurable window (default
   7 days). Opening it starts a server-enforced timer. Candidate sees
   problems in order, an editor with language selector (from the problem's
   allowed set), *Run* (public tests) and *Submit* (all tests). SQL problems
   present a schema description and a query editor. On finish or timer
   expiry the attempt is scored (weighted % of hidden tests passed per
   problem) and integrity signals are computed. Vetter reviews score,
   signals, and replay, then records a verdict (*pass / borderline / fail*)
   with notes.
6. **Shortlist and release.** Recruiter moves applications to *Shortlist*,
   then **releases** selected ones to the client. Release is explicit,
   per-application, reversible, and audited.
7. **Client portal.** Client user sees jobs for their company and, per job,
   released applications only: resume, recruiter summary, scorecards,
   assessment score + vetter verdict (never raw integrity signals or
   replay). Actions: **Advance**, **Reject** (reason required), **Request
   info** (free text to recruiter). Advance/Reject move the application
   only between `client_review`-kind stages or to terminal *Rejected*. If
   the job has **blind mode** on, name, photo, contact details, and links
   are hidden until the application reaches a stage flagged `unblind`.
8. **Talent pool.** An application becomes a pool entry when a recruiter
   flags it *high quality*, or automatically when a scorecard overall is
   *strong-yes* or an assessment verdict is *pass* with score ≥ org
   threshold (default 80). Pool entry aggregates: candidate, skills tags,
   seniority, location/remote, best assessment score per tag, latest
   scorecard summary, free-text notes, source jobs. Recruiters can browse,
   search, edit tags/notes, and remove entries.

### Stage kinds and behaviour

| Kind | Entry behaviour | Who may move out |
|---|---|---|
| `generic` | none | Recruiter, Admin |
| `interview` | booking email; scorecard required to advance (overridable with reason) | Recruiter, Admin; Vetter may advance after scorecard |
| `assessment` | invite email; verdict required to advance (overridable with reason) | Recruiter, Admin; Vetter may advance after verdict |
| `client_review` | visible to client only if released | Client user (advance/reject), Recruiter, Admin |
| `terminal` | application closed (`hired` or `rejected`) | — |

Default template: Applied (generic) → Screened (generic) → Phone Interview
(interview) → Assessment (assessment) → Shortlist (generic) → Client Review
(client_review) → Client Interview (client_review, unblind) → Offer
(client_review) → Hired (terminal) / Rejected (terminal).

## Scope

- Org/tenant model, org users with roles, client companies, client users.
- Jobs, pipeline templates, per-job pipelines, applications, audited moves.
- Public apply page, resume storage and text extraction, candidate records.
- Availability, booking, reminders, reschedule/cancel, scorecards.
- Problem bank (code + SQL), JSON import, assessments, candidate session
  with recording, sandboxed runner, scoring, integrity signals, replay
  viewer, vetter verdict.
- Release, client portal with advance/reject/request-info, blind mode.
- Talent pool entries, rule-based suggestion on job create/edit.
- Templated transactional email via SMTP.
- Huma JSON API under `/api/v1` with OpenAPI 3.1; templ/htmx/Alpine UI.
- Admin bootstrapping of orgs via CLI (no self-serve signup).

## Non-goals

- Billing, plans, self-serve tenant signup.
- Candidate accounts or candidate dashboard.
- Google/Microsoft calendar OAuth, video conferencing.
- Offer letters, e-signature, onboarding.
- ML resume parsing, embeddings, semantic matching.
- Problem content sourcing (bank ships with a small seed of original
  problems; populating it is an operator concern).
- Automatic rejection on integrity score.
- In-app chat; only templated email.
- Mobile apps.

## Proposed Design

### Architecture

Single Go binary with modes: `serve` (HTTP), `worker` (queue consumer for
email, reminders, runner dispatch, signal computation), `runner` (sandboxed
execution service, deployed separately and network-isolated), `migrate`,
`admin` (create org / user). Postgres is the only database; a
Postgres-backed job queue (e.g. `river`) avoids a second datastore. Object
storage holds resumes and session recordings.

Layers: `internal/domain` (entities, state machine, ranker, signal
calculators — pure Go, no I/O), `internal/store` (sqlc or equivalent
against Postgres, RLS-aware), `internal/service` (use cases, transactions,
events), `internal/api` (Huma operations), `internal/web` (templ views +
htmx handlers), `internal/runner` (execution service client + server),
`internal/mail`, `internal/blob`.

Both API and web handlers call the same service layer; neither reaches the
store directly.

### Tenancy

Every domain table has `org_id`. Requests carry an authenticated principal
(org user, client user, or magic-link token) resolved to `org_id` and, for
client users, `client_company_id`. The store sets `SET LOCAL app.org_id`
per transaction and Postgres RLS policies enforce isolation as a second
layer. Client-user policies additionally filter to their company and to
released applications.

### Auth

- Org and client users: email + password (argon2id), server-side sessions
  in Postgres, secure cookies, CSRF on HTML forms. Password reset via email.
- Magic links: opaque token → row with purpose (`apply`, `book`,
  `assessment`), subject ids, expiry, `used_at`, `revoked_at`. Single
  purpose; assessment tokens become a session cookie for the attempt.
- API under `/api/v1` accepts the same session cookie (browser islands) and
  per-user API tokens (future integrations; issued by admin).

### Pipeline state machine

`Application` has `stage_id`, `status ∈ {active, hired, rejected,
withdrawn}`, `released_at`. Moves are validated by domain rules keyed on
actor role, source stage kind, target stage kind, and stage prerequisites
(scorecard/verdict present unless override reason given). Each move writes
an `application_event`. Stage entry side-effects are enqueued, not run
inline.

### Scheduling

`availability_rule` (vetter, weekday, start, end, tz, buffer, valid range)
and `availability_exception`. Slots are generated on read for a window,
minus existing `interview_slot` rows and buffer. Booking inserts a slot
inside a transaction with a uniqueness constraint on (vetter, start) to
prevent double-booking. Reminders are scheduled queue jobs cancelled on
reschedule/cancel.

### Assessment subsystem

**Problem bank.** `problem`: org (nullable for platform seed), kind
`code|sql`, title, statement (markdown), difficulty, tags, allowed
languages, time limit, memory limit, reference solution, `test_case[]`
(input, expected output, visibility `public|hidden`, weight). SQL problems
carry a schema + seed SQL and expected result sets. Import format: JSON
array matching this shape; validation rejects problems whose reference
solution fails its own tests.

**Assessment** = ordered `problem` ids, total duration, language override,
invite window. Attached to a stage via `stage.assessment_id`.

**Attempt.** Created on stage entry; states `invited → started → submitted
| expired → scored → reviewed`. Per problem: `submission` rows (language,
source, result summary) — many runs, one final submit.

**Recording.** Client batches events every 2s and on visibility change:
`edit` (CodeMirror changeset), `paste` (length, sha256 of content, whether
it matched a known clipboard from the page's own editor), `focus`,
`blur`, `run`, `submit`, `lang_change`. Events are appended to
`attempt_event` (sequence-numbered, server-timestamped); after finish the
stream is compacted to object storage and the replay viewer reads from
there. Gaps in sequence numbers mark the recording `incomplete`.

**Runner.** Separate service; API server enqueues
`{attempt_id, submission_id, language, source, tests}` and the runner
reports results. Each execution: fresh container from a per-language image,
gVisor runtime, no network, read-only rootfs except `/tmp` (size-capped),
CPU/memory/wall/PID/output limits, stdin/stdout test harness per language.
SQL: ephemeral Postgres database created from problem seed, query executed
with `statement_timeout`, result set compared to expected. Runner never
receives org context or PII. Supported languages v1: Python, JavaScript
(Node), Go, Java, SQL (Postgres dialect). Adding a language = adding an
image + harness.

**Scoring.** Problem score = weighted hidden tests passed / total weight.
Attempt score = mean of problem scores, 0–100.

**Integrity signals.** Computed by the worker after finish; each signal
produces `{name, value, evidence[], weight}`:

- `paste_ratio` — chars pasted from outside the page / final source chars
- `paste_then_pass` — a paste followed by a passing submit within 60s
- `burst_typing` — proportion of edits in bursts > 8 chars/s sustained > 5s
- `edit_ratio` — deletions / insertions (very low = suspicious)
- `blur_then_solution` — blur > 30s followed by ≥ 40% of final source appearing within 2 min
- `speed_vs_difficulty` — time to first pass vs difficulty band percentile
- `reference_similarity` — normalised token similarity to reference solution and to other org submissions for the same problem

Risk score = clamp(Σ weight × normalised value, 0, 100). Weights are org
settings with defaults. Displayed with evidence and replay links; never
triggers automatic pipeline moves.

### Talent pool matching

Score(pool_entry, job) = 0.6 × Jaccard(entry.skills, job.skills)
+ 0.2 × seniority_fit (1 exact, 0.5 adjacent, 0 otherwise)
+ 0.1 × location_fit (remote-ok or same location)
+ 0.1 × normalised best assessment score. Entries below 0.2 are not shown.
Top 20 shown, with a "why" breakdown. Excludes candidates already in the
job or rejected by the same client company within 6 months.

### Email

Templates: apply received, booking invite, booking confirmation, reminder,
reschedule/cancel, assessment invite, assessment reminder, client release
notice, client request-info to recruiter, password reset. All sent through
the queue with retry; `email_log` records status.

### Observability

Structured logs with `org_id`/`request_id`; Prometheus metrics for queue
depth, runner latency/failures, booking conflicts; audit log via
`application_event` and `admin_event`.

## Interfaces and Data

### Core tables (abridged)

`org`, `org_user`, `org_user_role`, `client_company`, `client_user`,
`session`, `magic_link`, `api_token`, `candidate`, `resume`, `job`,
`pipeline_template`, `pipeline_template_stage`, `stage`, `application`,
`application_event`, `scorecard_rubric`, `scorecard`,
`availability_rule`, `availability_exception`, `interview_slot`,
`problem`, `test_case`, `assessment`, `assessment_problem`, `attempt`,
`submission`, `attempt_event`, `integrity_signal`, `review`,
`talent_pool_entry`, `email_log`, `org_setting`.

All carry `org_id` (except `org`, platform-seed `problem` rows, and
runner-side tables). Timestamps in UTC; user timezones stored per user
and per availability rule.

### API (Huma, `/api/v1`)

Resource-oriented JSON, OpenAPI 3.1 at `/api/v1/openapi.json`. v1 must
expose at minimum: jobs, applications (+ move), candidates, resumes
(signed upload/download URLs), stages, scorecards, availability + slots +
bookings, problems (+ import), assessments, attempts (+ events ingest,
runs, submits, replay manifest), reviews, talent pool (+ suggestions),
client-portal read endpoints. Errors use RFC 9457 problem details (Huma
default).

### Runner interface

`POST /execute` `{id, language, source, tests[], limits}` →
`{id, results[]{test_id, status, stdout_hash, stderr_tail, time_ms, mem_kb}, status}`.
Authenticated by shared secret over private network. Idempotent by `id`.

### Web routes (templ/htmx)

`/login`, `/app/*` (org users), `/client/*` (client users),
`/apply/:job`, `/book/:token`, `/assess/:token`. htmx partials return
fragments; Alpine handles local state; the assessment page mounts the
CodeMirror island which talks to the API.

## Failure and Edge Cases

- Runner timeout, crash, or unreachable → submission `error`; candidate
  may resubmit within the attempt window; vetter sees error count.
- Recording gaps → attempt flagged `incomplete_recording`; signals shown
  as low confidence.
- Timer expiry mid-edit → last synced source auto-submitted; attempt
  `expired`, scored as-is.
- Two candidates booking the same slot → second fails with a conflict and
  reloads slots.
- Vetter deletes availability covering a booked slot → booked slot kept;
  warning shown.
- Candidate applies to multiple jobs in one org → one candidate record,
  multiple applications; cross-org never merged.
- Resume text extraction fails → application still created; search on
  metadata only; flagged.
- Client rejects a candidate → application terminal; pool entry unaffected;
  suggestion excludes them for that client company for 6 months.
- Release revoked → client loses access immediately; prior client actions
  remain in the audit log.
- Magic link reused or expired → clear error page, recruiter can reissue.
- Problem import with a failing reference solution → whole import
  rejected with per-problem errors.

## Validation

- **Domain unit tests:** state machine transitions × roles × prerequisites;
  slot generation with buffers/exceptions/timezones; each integrity signal
  against fixture event streams; ranker scoring and exclusions.
- **Store tests:** RLS policies — cross-org and cross-client reads return
  zero rows even with a broken app-layer filter.
- **Runner tests:** each language executes a hello-world and a failing
  test; sandbox denies network, fork bomb, filesystem writes outside
  `/tmp`, memory and wall-time overruns; SQL statement timeout.
- **Integration tests:** apply → screen → book → scorecard → assessment →
  review → release → client advance → hired, exercised through both API and
  web routes with role checks.
- **Import validation test:** seed problems round-trip through import.

## Acceptance Criteria

1. Admin CLI creates an org and first admin user; that user can create
   recruiters, vetters, client companies, and client users.
2. A recruiter creates a job with a custom pipeline; a public apply URL
   accepts a PDF resume and the application appears in stage 1.
3. Moving an application into an `interview` stage emails a booking link;
   the candidate books a slot in a different timezone and both parties'
   confirmations show the correct local times; the vetter cannot
   double-book that slot.
4. Advancing past an `interview` stage without a scorecard is blocked
   unless an override reason is provided; the override is visible in the
   event log.
5. A problem bank import of the seed JSON succeeds; an import containing a
   problem whose reference solution fails its tests is rejected with the
   problem identified.
6. A candidate completes an assessment with one Python and one SQL
   problem; submissions execute in the sandbox; score is computed; replay
   scrubs the full edit history; a large external paste appears in the
   integrity panel with the paste evidence.
7. Sandbox tests demonstrate network, fork, and filesystem escapes fail and
   time/memory limits terminate the process.
8. A client user sees only released applications for their company; blind
   mode hides identity until the unblind stage; Advance and Reject move the
   pipeline and are audited; Reject requires a reason.
9. A client user cannot access any application, job, or endpoint of another
   client company or org (verified at RLS level).
10. A *strong-yes* scorecard creates a talent-pool entry; creating a new job
    with overlapping skills lists that entry with a score breakdown; adding
    them creates an application.
11. OpenAPI document validates and covers every `/api/v1` route.
12. All emails in the template list are sent through the queue with retries
    and logged.

## Resolved Decisions

- Multi-tenant SaaS; org = agency; client company is a record inside org.
- Full platform in one spec; sequencing left to the plan.
- Assessments in-house: LeetCode-style code + SQL, session recording,
  paste detection, heuristic integrity signals with human judgment; no
  provider integration; no LLM judging in v1.
- Built-in availability booking; no calendar OAuth in v1.
- Client portal acts only on released applications and only within
  `client_review` stages; blind mode per job.
- Talent pool: explicit flag or threshold entry; rule-based tag/score
  matching; no embeddings.
- Pipeline: fixed default template, per-job customisable, typed stage kinds.
- Stack: Go, Postgres (+ RLS, Postgres-backed queue), Huma on chi for
  `/api/v1`, templ + htmx + Alpine.js for pages, CodeMirror island for the
  editor, gVisor-sandboxed runner, S3-compatible blob storage, SMTP.
- Candidates have no accounts; magic links only.
- "Vet the client" interpreted as vet the candidate.

## Open Questions

- **Runner host environment** (owner: user, resolve before the runner task
  is planned): gVisor requires a Linux host with `runsc`; if the
  deployment target cannot provide it, fall back to Firecracker or a
  managed sandbox — the runner interface is unchanged.
- **Problem seed content** (owner: user, resolve before beta): who writes
  the initial original problems; spec assumes ~20 across difficulties plus
  5 SQL.
- **Org-level integrity weight defaults** (owner: implementation, resolve
  during signal calibration with fixture data): initial weights are a
  starting point and must be tuned against recorded sessions.
