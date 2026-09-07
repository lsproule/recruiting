# Recruiter Console v2

## Outcome

The recruiter-facing product becomes the "Cadre" console designed in Claude
Design: one dark, dense shell (Nocturne design system) over a guided client
intake, a HackerRank/LeetCode-shaped problem bank with a live editor (vim and
emacs keymaps), 15 execution languages, HackerRank-grade test integrity
(including webcam proctoring), and a ranked shortlist packet that is what a
client actually receives. Everything built on the v1 platform
(`docs/specs/2026-08-29-recruiting-platform.md`) stays; this spec restyles,
extends, and fills the recruiter workflow the prototype describes.

## Context

- v1 (plan `docs/plans/recruiting-platform-v4`, T01–T20) is implemented: orgs,
  client companies, jobs with typed pipelines and a board, public apply,
  booking + scorecards, problem bank (JSON import, seed), assessments,
  attempts with keystroke recording and replay, sandboxed runner
  (python/node/go/java/sql), integrity signals, release-gated client portal,
  talent pool.
- Org UI is templ + htmx + Alpine with plain light CSS inlined in
  `internal/web/layout/layout.templ`. Candidate editor is CodeMirror 6
  bundled by `web/static/assess/build.sh`; replay by `web/static/replay`.
- Runner images live in `runner/images/{go,java,node,python}` with a Go
  harness in `runner/harness`; problems are stdin/stdout programs (or one
  SQL query) checked against test cases; every allowed language currently
  requires its own reference solution.
- Seed bank: 8 problems in `seed/problems/*.json` (`seed/problems/README.md`
  documents the format). `admin seed-problems` verifies every reference
  solution through the runner, so seeding needs a running runner.
- Local dev today has no runner path: `runner` refuses to start without
  gVisor unless `RUNNER_ALLOW_INSECURE_RUNTIME=1`, and no images are built.
- The design reference is a React prototype
  (`assets/recruiter-console.prototype.html`, runtime `assets/support.js`)
  with invented data. Several prototype concepts are out of scope here (see
  Non-goals).

## User Experience and Behavior

### Shell

Persistent left sidebar (210px): brand "Cadre · recruiter"; nav Work queue,
Clients, Candidates, Problem bank, Assessments, each with a live count;
"New client intake" primary button; signed-in user footer. Admin screens
(users, settings, API tokens) remain reachable from the footer menu. Client
portal and candidate pages use the same tokens with their own minimal
chrome.

### Screens (org users)

1. **Work queue** — one row per pending action across the org, newest-urgent
   first: attempt scored and awaiting review; assessment invite expiring in
   < 24h and not started; client "request info" unanswered; shortlist packet
   drafted but not sent; scorecard overdue after a completed slot. Filters:
   All / Review / Client waiting / Expiring. Each row has one primary action
   deep-linking to the screen that resolves it and a Snooze (24h, per user).
2. **Clients** — table: client, owner, open jobs, in pipeline, awaiting
   client, placed 90d, median days to shortlist; row opens client detail;
   "Onboard client" starts intake.
3. **Client detail** — header (name, tier tag, open jobs, pipeline count);
   tabs *Jobs* (title, seniority, assessment name, applicants, scored,
   shortlisted, board link), *Board* (existing pipeline board scoped to the
   client, all jobs or one), *Shortlists* (packets: job, picks, status,
   sent date, view).
4. **Candidate detail** (an application) — header with stage tag, Reject /
   Add to shortlist / Advance; left: assessment recording (replay viewer,
   speed 1–4×, keyframe scrubber with jump chips: first compile, first
   sample pass, all samples pass, timeout), **integrity timeline** (blur /
   paste / fullscreen-exit / snapshot markers on the same scrubber; clicking
   a snapshot marker shows the image), final submission with LOC and
   language, test results table (case, class, result, ms, points); right:
   Fit score for this job with factor breakdown, résumé summary and PDF
   link. If no assessment: a "Send assessment" card.
5. **Shortlist builder** (per job) — left: qualified pool (applications with
   a scored attempt, ranked by fit, filter "score ≥ N"); right: ranked picks
   (≤ 5, reorder, remove), note "Why these, in your words", a "Client sees /
   Hidden" legend, Send. Sending creates a packet, releases the picked
   applications, and emails the client's users.
6. **Problem bank** — org problems + platform seed; filters title, tag,
   difficulty, kind; columns problem (cases, recommended minutes), kind,
   difficulty, skills, used in, median score, proven languages, owner.
   Actions: Import JSON, New problem, per row: open, **Try it**, Clone
   (platform problems: use/clone only).
7. **Problem authoring** — four steps with a persistent right-hand *Quality
   review* panel:
   1. Details: title, statement (Markdown, live preview like LeetCode's
      problem pane), difficulty, skill tags, recommended minutes, kind
      (code/sql), interviewer guidelines (internal; never shown to candidate
      or client).
   2. Languages: checkboxes over the supported set plus **Any** (= all
      supported for the kind). Each shows "proven" once a reference
      solution passes.
   3. Test cases: name, class (sample/edge/perf/hidden-core), points,
      visibility public/hidden, input, expected; table with reference-run
      status per case. Total points shown; no partial credit within a case.
   4. Reference solutions: one editor per chosen language (at least one
      required); Verify runs each against every case via the runner; the
      problem cannot be saved with a failing solution.
   Quality review scores: ≥ 6 cases, ≥ 1 public and ≥ 3 hidden, ≥ 1 tag,
   statement ≥ 200 chars, ≥ 2 proven languages. Score < 60: saveable but not
   attachable to an assessment.
8. **Try it** — the full candidate editor against one problem: statement
   pane, editor, language selector (allowed set), keymap control, Run
   (public tests) via the runner, results pane. Creates no attempt or
   recording.
9. **Preview as candidate** — mints a throwaway attempt for an assessment
   (or a single problem) and opens the real candidate session in a new tab.
   Preview attempts are flagged, excluded from scoring stats, pool rules,
   signals, and the work queue, and purged after 24h.
10. **Assessments** — invites in flight: candidate, job/client, set, status,
    progress, integrity flag count, expires, action (remind / review /
    revoke); "Send assessment" picks an application + assessment.
11. **Intake wizard** — five steps, draft saved on every step and resumable
    from the work queue:
    1. Client: company, industry, hiring contact, contact email (becomes the
       first client user), shortlist SLA (business days), "what the client
       said" brief.
    2. Job: title, seniority, location/mode, base range, headcount, pipeline
       template.
    3. Skills to test: ordered multi-select from the org's tag vocabulary
       plus free text; order = priority.
    4. Coding question — three options: **Default set** (auto-picked from
       the bank by skills and seniority: 1 easy + 1 medium for mid, 1 medium
       + 1 hard for senior/staff; shown with a swap control), **Pick from
       bank** (multi-select with filters), **Write your own** (opens problem
       authoring inline; returns to this step). Also: duration, allowed
       languages (inherits from the problems' intersection, editable, "Any"
       available), integrity settings (see below).
    5. Review: everything on one page; Create makes the client company,
       first client user (invited by email), job with pipeline, and
       assessment attached to the job's assessment stage.

### Candidate session

- Editor gains a keymap segmented control: Default / Vim / Emacs. Choice is
  stored in localStorage and written to the recording as a `keymap` event.
  Vim mode shows the mode indicator and supports `:w`-free workflows (the
  editor never blocks on ex commands; `:q`-style commands are no-ops).
- Language modes for every supported language.
- Before start, when any integrity setting is on: a consent + environment
  check screen listing what is recorded; camera check with a live preview
  and **photo ID capture** (one still stored as the identity frame) when
  enabled; fullscreen prompt when required. Declining ends the session
  without starting the timer and notifies the recruiter.
- During the attempt: periodic webcam snapshot at the configured interval
  (default 60s, jittered ±10s) uploaded to blob storage; fullscreen exit
  pauses nothing but is recorded and shown as a banner; paste blocked when
  configured (paste event still recorded with length 0 and `blocked:true`);
  tab exits recorded as today.

### Client portal

For a sent packet the client sees: the recruiter's ranking and note, each
pick's résumé, final code and test results, and a **read-only replay
viewer**. Never shown: integrity signals, snapshots, identity frame, other
applicants, interviewer guidelines, internal notes.

## Scope

- Nocturne design system vendored and applied to org, client, and candidate
  surfaces; all existing screens restyled; new screens above.
- Shortlist packet entity, builder, send flow, client view.
- Intake wizard with drafts; default-set picker.
- Problem authoring UI, Try it, Preview as candidate, quality review, clone.
- Editor keymaps (vim, emacs) and language modes for 15 languages.
- Runner images + harness for: python, javascript, typescript, go, java, c,
  cpp, rust, php, ruby, haskell, lua, kotlin, csharp; sql unchanged.
  Reference-solution rule relaxed (see Resolved Decisions).
- Integrity: per-assessment settings; consent screen; photo ID; webcam
  snapshots with retention + purge; fullscreen; paste block; reviewer
  timeline; new signals `fullscreen_exits`, `snapshot_gaps`.
- Seed bank of ~12 original LeetCode-style problems verified on seed.
- Local runner developer path (`make runner-images`, `make dev-runner`).
- Work queue.

## Non-goals

- Candidate accounts, résumé-built profiles, one-click apply, lifetime
  "talent" score (separate spec).
- SLA/decay-ranked queue, fee models, tiers beyond a free-text tag.
- Function-signature stub generation; a `frontend` problem kind.
- AI image analysis of snapshots, multi-monitor detection, desktop app.
- Rendering replay to a video file; "video" in the client portal is the
  replay viewer.
- Mobile layouts beyond not breaking at 1024px.

## Proposed Design

### Design system

`web/static/nocturne.css` = `assets/nocturne/styles.css`, served by the
existing static embed; `layout.templ` drops its inline CSS and links it plus
one `app.css` for layout-only rules (sidebar grid, board columns, editor
frame) written with the tokens. Phosphor icons inlined as templ components.
UI work uses the `frontend-design` skill; the prototype is the reference for
hierarchy and copy, not pixel truth.

### Screens

Each screen is a templ page under `internal/web/<area>` calling
`internal/service` only, following the existing `Mount`/`Deps` pattern.
Interactive parts (board, shortlist reorder, wizard steps, filters) use htmx
partials; Alpine only for purely local state.

### Data

- `shortlist_packet` (org_id, job_id, note, status draft|sent, sent_at,
  sent_by) and `shortlist_pick` (packet_id, application_id, rank). Sending
  runs the existing release per pick inside one transaction and enqueues
  `email.send` to every client user of the company. Un-sending is not
  supported; a new packet supersedes.
- `intake_draft` (org_id, created_by, step, payload jsonb, updated_at);
  deleted on Create.
- `problem` gains `recommended_minutes`, `guidelines` (internal text),
  `origin_problem_id` (clone source). `test_case` gains `name`, `class`.
  `reference_solution` rows remain per language; a problem records
  `proven_languages` as those with a passing solution.
- `assessment` gains `allowed_languages text[]` (empty = any) and
  `integrity jsonb`: `{fullscreen, block_paste, webcam:{enabled,
  interval_s}, photo_id}`.
- `attempt` gains `preview bool`, `consent_at`, `identity_blob_key`.
- `attempt_snapshot` (attempt_id, taken_at, blob_key, seq). Retention: org
  setting `snapshot_retention_days` (default 90); worker job
  `snapshot.purge` daily deletes blobs and rows past retention, and
  `attempt.purge_preview` hourly removes preview attempts > 24h.
- New attempt event kinds: `keymap`, `fullscreen_enter`, `fullscreen_exit`,
  `snapshot`, `consent`.
- Work queue is a read model (SQL view or service query) over attempts,
  magic links, client requests, packets, and slots — no table.

### Runner and languages

Per language: `runner/images/<lang>/Dockerfile` and a harness entry
(compile command, run command, file name, timeout multiplier for compiled
languages). Image prefix unchanged. `make runner-images` builds all; CI
builds only changed ones. Compiled languages compile once per submission
before tests. Language ids: `python, javascript, typescript, go, java, c,
cpp, rust, php, ruby, haskell, lua, kotlin, csharp, sql`; existing `node`
id is migrated to `javascript`.

### Editor

`web/static/assess` adds `@replit/codemirror-vim`, `@replit/codemirror-emacs`,
and `@codemirror/lang-*` (or legacy-modes) for every language; keymap and
language live in Compartments. Try it and Preview reuse the same bundle with
a `mode` flag (`try` disables the recorder).

### Integrity

Client: `getUserMedia` video only, snapshots via canvas → JPEG (~640px, ≤
80KB) posted to `/assess/api/snapshots` with the attempt cookie; on failure
the gap is recorded. Fullscreen via the Fullscreen API with re-prompt on
exit. Paste block cancels the paste event in the editor. Server validates
size/type, stores under `snapshots/<attempt>/<seq>.jpg`, RLS-scoped rows.
Signals: `fullscreen_exits` (count and total seconds outside),
`snapshot_gaps` (missed intervals ÷ expected). Weights join the org's
existing signal weights. Signals still never move pipeline cards.

### Seed

`seed/problems/*.json` extended to ~12 problems: 3 easy, 5 medium, 2 hard
code problems spanning arrays/strings, hashing, two pointers, intervals,
BFS/graph, DP, simulation; 3 SQL. Each code problem carries reference
solutions in at least python, javascript, go (more where cheap), ≥ 6 cases
(≥ 2 public), named cases with classes. Seeding remains runner-verified.

### Local development

`make dev-runner` starts `runner` with `RUNNER_ALLOW_INSECURE_RUNTIME=1`,
`RUNNER_SQL_URL` pointed at the Compose Postgres; `.env.example` documents
`DATABASE_URL_APP` (app_rw) vs owner URL; README "Getting started" reaches a
working editor in: `make dev-up`, `make runner-images`, `migrate`,
`create-org`, `dev-runner`, `seed-problems`, `serve`, `worker`.

## Interfaces and Data

- Routes (org): `/app/queue`, `/app/clients`, `/app/clients/{id}` (+
  `?tab=`), `/app/intake/{draft}` (steps 1–5), `/app/jobs/{id}/shortlist`
  (builder), `/app/problems` (+ `/new`, `/{id}`, `/{id}/try`,
  `/{id}/clone`), `/app/assessments`, `/app/assessments/{id}/preview`.
- Routes (candidate): existing `/assess/...` plus `/assess/api/snapshots`,
  `/assess/consent`.
- Routes (client): `/client/jobs/{id}/shortlist` (packet view),
  `/client/applications/{id}/replay`.
- API (`/api/v1`): `POST /shortlists`, `POST /shortlists/{id}/send`,
  `GET /shortlists/{id}`, `POST /problems/{id}/try` (run against public
  tests), `GET /languages`. Existing operations updated for new fields;
  OpenAPI regenerated.
- Runner wire protocol gains `compile` step results (`compile_error`
  outcome) — remains PII-free.

## Failure and Edge Cases

- Runner down: Try it / Verify / Preview show "runner unavailable" with the
  runner's health status; authoring can save unverified as a **draft**
  (never attachable) and re-verify later.
- Camera denied or absent with webcam required: candidate cannot start;
  recruiter notified; recruiter can waive webcam per attempt (audited).
- Snapshot upload failures do not interrupt the session; gaps become a
  signal.
- Fullscreen exit: recorded; session continues; banner until re-entered.
- Blocked paste: editor ignores clipboard; event recorded.
- Packet send with a pick already released or rejected: rejected picks are
  refused with a message; already-released picks are included as-is.
- Intake Create fails midway: single transaction; draft retained.
- Cloning a platform problem copies cases and solutions; edits never touch
  the original.
- Preview attempts never enter the pool, signals, work queue, or median
  score.
- Retention purge removes blobs before rows; a failed blob delete retries
  and leaves the row until it succeeds.

## Validation

- Unit: fit ranking, default-set picker, quality score, work-queue rules,
  new signals, keymap event parsing, snapshot gap math.
- Integration: packet send releases picks + emails; client sees packet and
  replay but no signals; preview attempts excluded everywhere; intake
  Create produces company/user/job/assessment atomically; snapshot upload
  + purge; harness passes each seed problem's reference solutions in every
  language it ships (`make test-integration` builds needed images).
- Browser (Playwright, tagged): Try it runs python and rust against public
  tests; vim keymap `dd` deletes a line and is recorded; emacs `C-k` kills
  to end of line; consent screen with camera mocked; fullscreen exit
  recorded.
- `make check test test-integration openapi-lint` green; `templ generate`
  and `sqlc` no diff.

## Acceptance Criteria

1. Every org, client, and candidate page renders with Nocturne tokens; no
   hard-coded colors outside `nocturne.css`/`app.css`.
2. From an empty database, the README path yields a signed-in recruiter who
   can open a seeded problem, click Try it, switch to Vim, write and Run a
   solution in each of the 15 languages, and see per-case results.
3. Intake wizard creates client, first client user (invite email in
   Mailpit), job with pipeline, and assessment with chosen problems; drafts
   resume from the work queue.
4. Authoring refuses to save a problem whose reference solution fails; a
   problem scoring < 60 cannot be attached to an assessment; "Any" language
   expands to the full set for the kind.
5. A candidate on an assessment with webcam + photo ID sees consent, captures
   an ID frame, and snapshots appear on the recruiter's integrity timeline;
   after retention they are gone from blob and DB.
6. Fullscreen exits, blocked pastes, and keymap choice are visible in the
   timeline and in signals; no signal moves a card.
7. Sending a packet releases the picks, emails client users, and the client
   sees ranking, note, résumés, code, tests, and replay — and nothing
   hidden — as verified by an integration test.
8. Preview as candidate opens a real session and leaves no trace in
   stats, pool, queue, or signals after purge.
9. Seed bank has ≥ 12 problems, each verified on `seed-problems` in every
   language it ships.
10. Work queue rows match the five rule definitions and clear when resolved.

## Resolved Decisions

- Scope A of the brief (recruiter console); candidate portal/talent score
  and client-side extras are separate specs.
- Nocturne applied app-wide; server-rendered templ/htmx, not the prototype's
  React runtime.
- Editor demo via Try it **and** Preview as candidate (option C).
- Integrity: behavioural toggles **and** webcam snapshots + photo ID, in
  this spec.
- Languages: the 15 listed plus "Any"; reference rule relaxed to ≥ 1
  solution, proven-languages tracked per problem.
- Problem model stays stdin/stdout + SQL; no stub generation.
- "Video" for clients = read-only replay viewer.
- Snapshot retention default 90 days, org-configurable.
- Default set: 1 easy + 1 medium (mid), 1 medium + 1 hard (senior/staff).

## Open Questions

- Fit score factors for the candidate detail panel: v1 uses the pool
  ranker's four terms; whether résumé signals (years, schooling) join it is
  decided with the candidate-score spec. Until then the panel shows the
  ranker's terms plus assessment score. Owner: Lucas, when scope B starts.
- Kotlin/C#/Haskell image sizes may be large; if `make runner-images`
  exceeds ~10 minutes cold, the planner may mark those images optional in
  dev with `RUNNER_LANGUAGES` env filter.

## Assets

- `assets/recruiter-console.prototype.html` — the Claude Design prototype
  (illustrative: hierarchy, copy, and flows; data and out-of-scope panels
  are not requirements). Runtime `assets/support.js` (illustrative, not
  shipped).
- `assets/nocturne/styles.css` — design tokens and component classes
  (normative; vendored verbatim).
- `assets/nocturne/readme.md` — design rules (normative).
- Prototype and Nocturne source: claude.ai/design project
  `b8f74b15-671d-45bc-8a33-642a9a468743`.
