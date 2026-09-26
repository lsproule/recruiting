# Live Interviews v1

## Objective
Deliver `docs/specs/2026-09-26-live-interviews.md`: video interview rooms with
a shared, runnable editor; clock-driven screening sprints with ratings and a
ranked summary; an editable process library with three sensible defaults; a
take-home assessment format; and unit, integration, and browser coverage of
each.

## Global Constraints
- Every new domain table has `org_id` with forced RLS; the store sets
  `app.org_id` per tx.
- `internal/domain` does no I/O. `internal/web` and `internal/api` call
  `internal/service` only.
- Only the `runner` executes code; a room's *Run* goes through
  `internal/runner/client` with no org context or PII.
- Room state is in-process; nothing about a room is authoritative except
  what is persisted (the editor's language and source).
- Every colour, font, spacing, radius, and shadow comes from a Nocturne
  token; bundles are built by checked-in `build.sh` scripts and committed.
- Existing behaviour stays: the org default process is unchanged, existing
  stage kinds keep their rules, existing tests stay green.

## Acceptance Gate
- `make check`, `make test`, `make test-integration`, `make openapi-lint`
- `make test-e2e` (the four new specs and the three existing ones)

## Tasks

### T01: Schema and domain
- Migration `00030_live_interviews.sql` (stage settings, `sprint` kind,
  process description/library key, assessment format, `sprint` link purpose,
  sprint tables, ratings, `interview_room`, RLS, grants; clean down).
- `db/queries/{processes,sprints,rooms}.sql`; extend `ats.sql`/`tenancy.sql`
  for stage settings; regenerate sqlc.
- `internal/domain`: `StageSprint`, stage settings + validation, `Prereqs.HasRating`,
  `processes.go` library, `sprint.go` planner and clock. Unit tests.

### T02: Services
- `ProcessService` (+ `copyTemplateStages` carrying settings; bootstrap seeds
  the library).
- `JobService` stage inputs with settings.
- `SprintService` (lifecycle, links, invite mail, state, console, lobby,
  rate, summary) and the sprint prerequisite in `defaultPrereqs`.
- `RoomService` + `Hub` (authorisation, SSE members, signal, doc log, code
  persistence, run).
- `AssessmentInput.Format` with take-home defaults; booking confirmation
  join note; `QueueSprintRating`.
- Integration tests for each, including RLS on the new tables.

### T03: Web surfaces
- `internal/web/processes` and the shared stage editor component; jobs
  pipeline editor restyled onto it.
- `internal/web/interviews`, `internal/web/room` (pages + API mounts for
  org users, booked candidates, sprint candidates), `internal/web/sprints`
  (setup, summary, console, state, rate, lobby).
- Application page: booking/join panel, *Written in the interview* panel,
  sprint ratings panel. Booking page: *Join* button. Candidate session:
  take-home copy. Assessment form: format.
- Sidebar menu: *Processes* (recruiter/admin), *Interviews* (everyone).
- Web integration tests for the routes.

### T04: Room bundle
- `web/static/room/{build.sh,src/{signal,rtc,doc,main}.ts}` and node unit
  tests for `signal` reducer and pairing/clock helpers used by the console.
- Reuse `assess/src/{languages,keymap}.ts`.

### T05: API
- `internal/api/{processes,sprints,rooms}.go`; `MountAll`; coverage test
  resources; `statusErrors` for the new sentinels; `openapi-lint`.

### T06: Browser tests
- `web/e2e/tests/{process,interview-room,sprint,take-home}.spec.ts` and the
  fixtures they need (a vetter with a password, a second interviewer);
  `run.sh` unchanged apart from any fixture it must bootstrap.

### T07: Docs
- README: processes, rooms (`RTC_ICE_SERVERS`, single-process note), sprints,
  take-home; e2e section lists the new specs.

## Phase 2: Company API and talent network

Added mid-plan at the user's request: the platform also acts as a data
broker. Companies consume their candidates through a documented API, and
people who have not applied to anything can join the org's talent network,
say what they are looking for, and be contacted for a role that fits.

### T08: Company API
- Per-company API tokens: an admin or a client user issues a token bound to
  a client user, so a company's integration sees exactly what its portal
  sees. `POST /api/v1/portal/tokens` (client user) and the admin screen.
- Company operations (`accessPortal`): list jobs, list candidates per job
  with stage, status, scorecards and assessment outcome (never integrity
  signals), get one candidate, download a résumé, list the shortlist
  packets, act (advance / reject / request info) the way the portal does,
  and poll `GET /portal/events?since=` for changes.
- Documentation: every operation carries a summary, a description, and an
  example; `docs/api.md` is the narrative guide (auth, pagination,
  polling, the talent search); the client portal gains a *Developer* page
  with the token screen, the OpenAPI link, and curl examples.

### T09: Talent network
- Public `/talent/{org_slug}`: name, email, résumé, skills, seniority,
  preferred roles, location and remote policy, salary floor, earliest start,
  and explicit consent to be contacted about matching roles. Creates or
  reuses the candidate, stores a `talent_profile`, mails a manage link
  (magic link purpose `profile`) to update or withdraw.
- `talent_profile` (candidate_id, skills, seniority, roles, location,
  remote_policy, salary_min, available_from, consent_at, withdrawn_at,
  updated_at) with RLS; full-text search joins the résumé text already
  indexed on `candidate.search`.
- Matching reuses the pool ranker's terms (skills Jaccard, seniority,
  location) plus résumé text hits, over profiles with consent and over
  pool entries.

### T10: Talent requests and outreach
- A company describes what it wants (`talent_request`: title, skills,
  seniority, location, remote, note) through the API or the portal and
  reads back anonymised matches (score, skills, seniority, location; no
  name, no contact, no résumé) with a *Request introduction* action.
- The recruiter's work queue gains *talent request waiting*; the
  recruiter's screen shows the identified matches and sends an
  `opportunity` email with a one-click *I'm interested* link that creates
  the application on the job (or a job the recruiter picks). Consent,
  contact, and outcome are recorded on the profile.
- Browser tests: a person joins the network, a company searches through
  the API and asks for an introduction, the recruiter sends the
  opportunity, the person accepts, the company sees the new candidate.
