# Recruiter Console v2

## Objective
Deliver `docs/specs/2026-08-30-recruiter-console-v2/spec.md`: the Nocturne-styled recruiter console (work queue, clients, client detail, candidate detail, shortlist builder, problem bank + authoring, assessments, intake wizard), a candidate editor with vim/emacs keymaps and 15 execution languages, HackerRank-grade integrity including webcam proctoring, a ranked shortlist packet for clients, a verified LeetCode-style seed bank, and a local runner path that makes all of it demonstrable from an empty database.

## Global Constraints
- Every new domain table has `org_id` with forced RLS; the store sets `app.org_id` per tx. Only `admin` CLI and migrations bypass RLS.
- `internal/domain` does no I/O. `internal/web` and `internal/api` call `internal/service` only.
- Only the `runner` process executes candidate code; the runner never receives org context or PII.
- Integrity signals never move pipeline cards.
- Client users read only released applications of their own company; never integrity signals, snapshots, identity frames, interviewer guidelines, or internal notes.
- Preview attempts (`attempt.preview = true`) are excluded from scoring stats, pool rules, signals, the work queue, and problem medians.
- Every color, font, spacing, radius, and shadow in `internal/web/**` comes from a Nocturne token (`var(--*)`); no hard-coded hex or px the tokens carry.
- Front-end bundles are built by the checked-in `build.sh` scripts and committed; pages never load from a CDN.
- Never edit `docs/specs/` or `docs/plans/`.
- UI tasks load the `frontend-design` skill before writing markup or CSS.

## Acceptance Gate
- `make check`
- `make test`
- `make test-integration`
- `make openapi-lint`
- From an empty database the README "Getting started" path yields a signed-in recruiter who opens a seeded problem, clicks Try it, switches to Vim, and runs a passing solution in python and rust

`make test-integration` needs Docker (Compose Postgres, MinIO) and the runner images.

## Shared Interfaces

### I1: Language registry
```go
// internal/domain/languages.go
type Language struct{ ID, Label, Kind string; Compiled bool } // Kind: "code"|"sql"
var Languages []Language // order: python,javascript,typescript,go,java,c,cpp,rust,php,ruby,haskell,lua,kotlin,csharp,sql
func LanguageByID(id string) (Language, bool)
func CodeLanguageIDs() []string // Kind=="code" ids, in order
const LanguageAny = "any" // expands to CodeLanguageIDs(), or ["sql"] for sql problems
```
`domain.ProblemLanguages` and `runner/server.Languages` derive from this list; `node` is accepted on input and normalised to `javascript`.

### I2: Runner harness language table
```go
// runner/harness/main.go
type langSpec struct {
    File    string   // source file written into the work dir, e.g. "main.rs"
    Compile []string // argv run once; empty when interpreted; {work},{src} substituted
    Run     []string // argv per test; {work},{bin} substituted
}
var langs = map[string]langSpec{ /* one entry per I1 code language */ }
```
`prepare(lang, source, work)` looks up `langs` instead of switching; unknown id → `unsupported language %q`.

### I3: Integrity settings
```go
// internal/service/assessment.go
type IntegritySettings struct {
    Fullscreen  bool `json:"fullscreen"`
    BlockPaste  bool `json:"block_paste"`
    Webcam      bool `json:"webcam"`
    WebcamEvery int  `json:"webcam_interval_s"` // default 60, min 15, max 600
    PhotoID     bool `json:"photo_id"`
}
```
Stored as `assessment.integrity jsonb`; `Assessment` and `AssessmentInput` carry `Integrity IntegritySettings` and `AllowedLanguages []string` (empty = any). The candidate session config (`assess` island boot JSON) carries `integrity` with the same JSON keys plus `snapshot_url` and `consent_url`.

### I4: Attempt event kinds (extends v1 I5)
`type` also accepts `keymap|fullscreen_enter|fullscreen_exit|snapshot|consent`. Data: `keymap {keymap:"default"|"vim"|"emacs"}`; `fullscreen_*` `{}`; `snapshot {seq:int, ok:bool}`; `consent {webcam:bool, photo_id:bool}`. The `EventType` union in `web/static/assess/src/recorder.ts` and `validateEvent` in `internal/service/attempt.go` list exactly this set.

### I5: Shortlist packet
```go
// internal/service/shortlist.go
type ShortlistPick struct { ApplicationID uuid.UUID; Rank int; CandidateName string; Score *float64 }
type ShortlistPacket struct {
    ID, OrgID, JobID uuid.UUID
    Status string // "draft"|"sent"
    Note   string
    Picks  []ShortlistPick // rank-ordered, ≤ 5
    SentAt *time.Time
    SentBy uuid.UUID
}
type ShortlistInput struct { JobID uuid.UUID; Note string; ApplicationIDs []uuid.UUID } // order = rank
// Save(ctx, p, id *uuid.UUID, in ShortlistInput), Send(ctx, p, id), Get(ctx, p, id),
// ListByJob(ctx, p, jobID), ListByClient(ctx, p, companyID) — all on *ShortlistService
```
Tables `shortlist_packet(id, org_id, job_id, status, note, sent_at, sent_by, created_by, created_at, updated_at)` and `shortlist_pick(packet_id, application_id, rank, unique(packet_id, application_id), unique(packet_id, rank))`. Client users read only `sent` packets of their company.

### I6: Work queue item
```go
// internal/service/workqueue.go
type QueueKind string // "review"|"expiring"|"client_waiting"|"shortlist_draft"|"scorecard_overdue"
type QueueItem struct {
    Kind QueueKind
    Who, Detail, JobTitle, ClientName string
    Due *time.Time
    ActionLabel, ActionURL string
    SubjectID uuid.UUID // attempt/application/packet/slot id; snooze key
}
// *WorkQueueService: List(ctx, p, filter QueueKind) ([]QueueItem, error) — "" = all;
// Counts(ctx, p) (map[QueueKind]int, error); Snooze(ctx, p, kind, subjectID, until)
```

### I7: Try-it execution
`POST /api/v1/problems/{id}/try` body `{"language":"python","source":"..."}` → `{"status":"ok|compile_error|runtime_error|timeout|error","compile_output":"...","results":[{"test_index":0,"status":"pass|fail|error|timeout","time_ms":12,"expected":"...","actual_hash":"..."}]}`; runs only the problem's public test cases through the runner; org-user principals only; no attempt or submission row is written. Service: `func (s *ProblemService) Try(ctx, p Principal, id uuid.UUID, language, source string) (TryResult, error)`.

### I8: Editor island boot
The assess bundle exports `window.RecruitingEditor.mount(el, cfg)` where `cfg.mode ∈ "attempt" | "try"`; in `try` mode the recorder, beacon, timer, and consent flow are disabled and Run posts to I7. Keymap control values: `default|vim|emacs`, persisted under localStorage key `recruiting.keymap`.

## Tasks

### T01: Language registry and runner harness table

- **Goal**: One ordered language list drives import validation, the runner protocol, and the harness; all 15 ids are accepted end to end.
- **Dependencies**: none
- **Inputs**: `internal/domain/problemimport.go`, `internal/runner/server/protocol.go`, `runner/harness/main.go`, `db/migrations/00017_problem_bank.sql`
- **Scope**: `db/**`, `internal/**`, `web/static/**`, `seed/**`, `cmd/**`, `templates/**`, `runner/**`, `./Makefile`, `README.md`, `.env.example`, `docker-compose.yml`, `.gitignore`, `sqlc.yaml`, `go.mod`, `go.sum`
- **Contract**:
  - I1 is the only place language ids are listed; `ProblemLanguages` and `server.Languages` derive from it because two hand-kept lists drifted in v1.
  - Inputs accept `node` and store `javascript`; `any` expands per I1.
  - Migration `00023` rewrites `node` → `javascript` in `problem.allowed_languages`, `problem_reference.language`, `submission.language`, `attempt_source.language`, and in seed JSON; the down migration reverses it.
  - The harness uses I2; existing python/javascript/go/java entries keep their current argv and env exactly.
  - A `code` problem needs ≥ 1 reference solution in an allowed language; not one per allowed language, because 15 languages make that rule unauthorable.
- **Acceptance**: `LanguageByID("cpp")` resolves; `any` expands to 14 code ids and to `["sql"]` for sql problems; a problem with `allowed_languages:["any"]` and one python reference imports; a reference in a non-allowed language is rejected; `prepare("rust", ...)` writes `main.rs` and returns the compiled argv.
- **Non-goals**: Docker images; editor modes; UI.
- **Implementation notes**: keep the `unsupported language %q` error text.
- **Complexity**: standard
- **Validation**: test-first
- **Test scenarios**: registry order and lookup; `any` expansion per kind; import with one reference, with none, and with a foreign-language reference; `prepare` per language returns I2 argv with substitutions (no execution).
- **Checks**: `make generate`; `make check`; `make test`
- **Notes**: N3
- **Interfaces**: I1, I2
- **Escalate if**: a table stores language ids not listed in I1.

### T02: Runner images for eleven new languages

- **Goal**: `runner/images/build.sh` builds a working `recruiting-runner-<lang>` for every I1 code language and the runner executes a hello-world in each.
- **Dependencies**: T01
- **Inputs**: `runner/images/**`, `internal/runner/server/{integration_test.go,docker.go}`
- **Scope**: `db/**`, `internal/**`, `web/static/**`, `seed/**`, `cmd/**`, `templates/**`, `runner/**`, `./Makefile`, `README.md`, `.env.example`, `docker-compose.yml`, `.gitignore`, `sqlc.yaml`, `go.mod`, `go.sum`
- **Contract**:
  - Each image follows the python image shape: harness stage, base pinned by digest, non-root `65534`, `ENTRYPOINT harness`.
  - Compiled languages pre-warm the compiler's cache at build time because the sandbox has no network and a small memory cap (the go image's GOCACHE pattern).
  - `make runner-images` and bare `build.sh` build all I1 code languages; `RUNNER_LANGUAGES="python rust"` restricts the set.
  - The hello-world table covers every I1 code language and skips an absent image with a clear message rather than failing.
- **Acceptance**: each image passes the hello-world (`stdin → "<line> world"`), a compile error surfaces as `compile_error` with output, and the wall-clock timeout kills a busy loop in c, rust, haskell, kotlin.
- **Non-goals**: gVisor installation; CI caching.
- **Implementation notes**: typescript via `node --experimental-strip-types` or a bundled esbuild transform; kotlin pre-warms its compile at build; csharp needs a pre-restored project template; haskell uses `ghc -O0`. JVM/dotnet memory flags mirror the java `-Xmx` handling in `runTest`.
- **Complexity**: advanced
- **Validation**: checks-only
- **Checks**: `make runner-images`; `make check`; `make test-integration`
- **Notes**: N2
- **Interfaces**: I1, I2
- **Escalate if**: a language cannot run under the 256MB/5s default limits.

### T03: Local runner path, env split, README

- **Goal**: A developer reaches a running `serve`, `worker`, and `runner` with seeded problems using only documented make targets.
- **Dependencies**: T02
- **Inputs**: `./Makefile`, `.env.example`, `README.md`, `cmd/recruiting/modes.go`, `internal/config/config.go`, `docker-compose.yml`
- **Scope**: `db/**`, `internal/**`, `web/static/**`, `seed/**`, `cmd/**`, `templates/**`, `runner/**`, `./Makefile`, `README.md`, `.env.example`, `docker-compose.yml`, `.gitignore`, `sqlc.yaml`, `go.mod`, `go.sum`
- **Contract**:
  - `DATABASE_URL` stays the owner URL for `migrate`/`admin`; new `DATABASE_URL_APP` (default: `DATABASE_URL` with user/password swapped to `app_rw`) is what `serve`/`worker` use, because the store rejects the owner role (N1).
  - `make dev-runner` runs `runner` with `RUNNER_ALLOW_INSECURE_RUNTIME=1` and `RUNNER_SQL_URL` set to the Compose URL; `make dev-serve`, `make dev-worker` load `.env`.
  - README "Getting started" lists the exact sequence: `dev-up`, `runner-images`, `migrate`, `create-org`, `dev-runner`, `seed-problems`, `dev-serve`, `dev-worker`.
- **Acceptance**: with `.env` copied from `.env.example`, `serve` starts without editing the URL; `seed-problems` succeeds against `make dev-runner`; README documents `DATABASE_URL_APP`.
- **Non-goals**: production deployment docs.
- **Implementation notes**: derive `DATABASE_URL_APP` in `config` with a `url.URL` user swap; keep `RUNNER_SQL_URL` optional.
- **Complexity**: mechanical
- **Validation**: checks-only
- **Checks**: `make check`; `make test`
- **Notes**: N1, N2
- **Escalate if**: `store.Open` cannot accept a separate URL without changing its signature for callers outside `cmd/`.

### T04: Nocturne design system and app shell

- **Goal**: Every org, client, and candidate page renders inside the Nocturne shell with the sidebar nav from the spec.
- **Dependencies**: none
- **Inputs**: `docs/specs/2026-08-30-recruiter-console-v2/assets/nocturne/styles.css`, `.../nocturne/readme.md`, `.../recruiter-console.prototype.html` (sidebar markup only), `internal/web/layout/layout.templ`, `internal/web/layout/page.go`, `internal/web/layout/static.go`, `web/static/embed.go`
- **Scope**: `db/**`, `internal/**`, `web/static/**`, `seed/**`, `cmd/**`, `templates/**`, `runner/**`, `./Makefile`, `README.md`, `.env.example`, `docker-compose.yml`, `.gitignore`, `sqlc.yaml`, `go.mod`, `go.sum`
- **Contract**:
  - `nocturne.css` is the spec asset verbatim; `app.css` holds layout-only rules (sidebar grid, page header, board columns, editor frame, integrity timeline) written with tokens.
  - `embed.go` serves `.css` as `text/css; charset=utf-8` and `.js` as today, keyed by extension.
  - `layout.Base` drops the inline `styles()` block; sidebar shows Work queue, Clients, Candidates, Problem bank, Assessments with counts supplied through `layout.Page.NavCounts map[string]int`, plus "New client intake" and the user footer with an Admin menu.
  - Client portal and candidate pages use the tokens with a slim top bar, not the sidebar.
  - Existing screens are restyled with the component classes; no visual regression test is added, but every existing integration test still passes because markup ids/names used by tests are unchanged.
- **Acceptance**: `grep -rE '#[0-9a-f]{3,6}' internal/web --include=*.templ` returns nothing; `/static/nocturne.css` served with the CSS content type and ETag; sidebar renders nav counts when provided and hides them when zero.
- **Non-goals**: new screens; icons beyond the five nav marks.
- **Implementation notes**: load `frontend-design` first. Phosphor icons as small templ components in `internal/web/layout/icons.templ`.
- **Complexity**: standard
- **Validation**: test-first
- **Test scenarios**: static handler content types for `.css`/`.js`; `layout.Base` renders sidebar entries with `aria-current` on the active one; nav counts rendering.
- **Checks**: `make generate`; `make check`; `make test`; `make test-integration`
- **Notes**: N4
- **Escalate if**: an existing page's test depends on the removed inline CSS.

### T05: Editor keymaps, language modes, and try-mode island

- **Goal**: The candidate editor offers Default/Vim/Emacs, highlights all 15 languages, records the keymap choice, and can mount in try mode.
- **Dependencies**: T01
- **Inputs**: `web/static/assess/{src/*.ts,build.sh}`, `internal/web/assess/pages.templ`, `validateEvent` in `internal/service/attempt.go`
- **Scope**: `db/**`, `internal/**`, `web/static/**`, `seed/**`, `cmd/**`, `templates/**`, `runner/**`, `./Makefile`, `README.md`, `.env.example`, `docker-compose.yml`, `.gitignore`, `sqlc.yaml`, `go.mod`, `go.sum`
- **Contract**:
  - Keymap lives in a CodeMirror Compartment; switching never resets the document or history.
  - Vim mode shows the mode indicator; `:q`, `:wq`, `:x` are no-ops because the session must never close from an ex command.
  - Emacs keymap does not shadow Run/Submit; the toolbar names both shortcuts.
  - Choosing a keymap writes an I4 `keymap` event and localStorage `recruiting.keymap`.
  - Language support per I1 via `@codemirror/lang-*`, else `@codemirror/legacy-modes`.
  - `mount(el, cfg)` per I8; `try` mode is client-side only apart from Run.
- **Acceptance**: vim `dd` removes a line and the stream carries that changeset plus a `keymap` event; emacs `C-k` kills to line end; reload restores the keymap; `validateEvent` rejects an unknown keymap value.
- **Non-goals**: Try it page (T06); integrity UI (T09).
- **Implementation notes**: `build.sh` adds `@replit/codemirror-vim@6`, `@replit/codemirror-emacs@6`, `@codemirror/legacy-modes@6`, language packages; commit the rebuilt `assess.js`.
- **Complexity**: standard
- **Validation**: test-first
- **Test scenarios**: `validateEvent` accepts each allowed keymap and rejects others; island tests (`src/*_test.ts`, run by `build.sh test`) cover the compartment swap preserving doc and history, and the localStorage round trip.
- **Checks**: `bash web/static/assess/build.sh`; `bash web/static/assess/build.sh test`; `make generate`; `make check`; `make test`
- **Notes**: N6
- **Interfaces**: I1, I4, I8
- **Escalate if**: a language has no CM6 mode at all.

### T06: Problem bank, authoring wizard, Try it, clone, quality review

- **Goal**: Recruiters browse, author (4 steps), clone, and try problems per spec screens 6–8.
- **Dependencies**: T01, T04, T05
- **Inputs**: `internal/web/problems/**`, `internal/service/problem.go`, `internal/domain/problemimport.go`, `internal/api/problems.go`, spec screens 6–8
- **Scope**: `db/**`, `internal/**`, `web/static/**`, `seed/**`, `cmd/**`, `templates/**`, `runner/**`, `./Makefile`, `README.md`, `.env.example`, `docker-compose.yml`, `.gitignore`, `sqlc.yaml`, `go.mod`, `go.sum`
- **Contract**:
  - Migration adds `problem.{recommended_minutes,guidelines,origin_problem_id,quality,proven_languages}` and `test_case.{name,class}` (`sample|edge|perf|core`).
  - `domain.ProblemQuality(p) int` scores per spec (≥6 cases, ≥1 public, ≥3 hidden, ≥1 tag, statement ≥200 chars, ≥2 proven languages) and is stored on save.
  - `AssessmentService` refuses to attach a problem scoring < 60 (`ErrProblemQuality`) because low-quality problems produce meaningless scores.
  - Save verifies every reference through the runner; failure returns per-language messages and stores nothing; draft-save stores `proven_languages = {}` and quality 0.
  - `Clone` copies a platform or org problem with `origin_problem_id`; platform problems are never edited.
  - I7 `Try` runs public cases only, for org users only; guidelines never reach candidate or client views.
- **Acceptance**: list filters by title/tag/difficulty/kind with proven-language badges; step 4 Verify shows per-case per-language results; a failing reference blocks save with the runner's output; a cloned seed problem is editable; Try it mounts the I8 island in `try` mode; the try endpoint appears in OpenAPI.
- **Non-goals**: seed content (T12); default set (T11).
- **Implementation notes**: one form, htmx-swapped step panels, hidden step field; drafts persist on Next; the quality panel is a fragment re-rendered per step.
- **Complexity**: advanced
- **Validation**: test-first
- **Test scenarios**: quality boundaries; `Try` uses public cases only and writes no submission; attach refused below 60; clone lineage and platform immutability; guidelines absent from `SessionProblem`; wizard round trip on a fake executor; try endpoint 403 for clients.
- **Checks**: `make generate`; `make check`; `make test`; `make test-integration`; `make openapi-lint`
- **Notes**: N6
- **Interfaces**: I1, I7, I8
- **Escalate if**: `test_case` needs a stable id exposed to the island.

### T07: Assessments with allowed languages, integrity settings, preview attempts

- **Goal**: An assessment carries allowed languages and integrity settings; recruiters can preview it as a candidate; preview attempts leave no trace.
- **Dependencies**: T05, T06
- **Inputs**: `internal/service/{assessment,attempt,scoring,signals,pool}.go`, `internal/web/assess/recruiter.{go,templ}`
- **Scope**: `db/**`, `internal/**`, `web/static/**`, `seed/**`, `cmd/**`, `templates/**`, `runner/**`, `./Makefile`, `README.md`, `.env.example`, `docker-compose.yml`, `.gitignore`, `sqlc.yaml`, `go.mod`, `go.sum`
- **Contract**:
  - Migration adds `assessment.{allowed_languages text[], integrity jsonb}` and `attempt.{preview bool, consent_at timestamptz, identity_blob_key text}`.
  - I3 validation: interval bounds; `photo_id` requires `webcam`.
  - Session languages = intersection of the assessment's allowed set (empty = any) with the problem's; an empty intersection fails at attach time because a candidate needs at least one language.
  - `AttemptService.Preview(ctx, p, assessmentID)` creates a `preview=true` attempt for the calling org user and returns a magic link; preview attempts are skipped by scoring stats, `PoolService.OnReviewPass`, signals, and medians.
  - Queue kind `attempt.purge_preview` runs hourly and deletes preview attempts older than 24h with their events, sources, submissions, and blobs.
- **Acceptance**: the assessment form shows language checkboxes with Any plus integrity toggles; preview opens a real session; after purge nothing references it; preview attempts never appear in pool, review queue, or signal jobs.
- **Non-goals**: candidate-side integrity UI (T09); recruiter Assessments screen (T10).
- **Implementation notes**: preview magic link reuses the assessment purpose with the org user's id as subject owner; purge handler deletes blobs before rows.
- **Complexity**: advanced
- **Validation**: test-first
- **Test scenarios**: I3 validation; language intersection; preview lifecycle and purge; exclusions listed above.
- **Checks**: `make generate`; `make check`; `make test`; `make test-integration`; `make openapi-lint`
- **Interfaces**: I1, I3, I4
- **Escalate if**: purging requires deleting `river_job` rows.

### T08: Snapshot storage, retention purge, new signals

- **Goal**: Webcam snapshots and the identity frame are stored, scoped, purged on schedule, and feed two new signals.
- **Dependencies**: T07
- **Inputs**: `internal/blob/blob.go`, `internal/service/signals.go`, `internal/domain/signals/*.go`, `internal/service/org_settings.go`, `internal/web/assess/assess.go`, `internal/api/attempts.go`
- **Scope**: `db/**`, `internal/**`, `web/static/**`, `seed/**`, `cmd/**`, `templates/**`, `runner/**`, `./Makefile`, `README.md`, `.env.example`, `docker-compose.yml`, `.gitignore`, `sqlc.yaml`, `go.mod`, `go.sum`
- **Contract**:
  - Table `attempt_snapshot(id, org_id, attempt_id, seq, taken_at, blob_key, bytes)` with RLS; identity frame is `attempt.identity_blob_key`.
  - `POST /assess/api/attempts/{id}/snapshots` (multipart, JPEG ≤ 120KB, candidate cookie) stores `snapshots/<attempt>/<seq>.jpg` and appends an I4 `snapshot` event; `POST .../identity` stores the ID frame once.
  - Org setting `snapshot_retention_days` (default 90, min 1); queue kind `snapshot.purge` daily deletes blobs then rows past retention; a failed blob delete leaves the row for retry.
  - Signals `fullscreen_exits` (count and seconds outside, normalised) and `snapshot_gaps` (missed ÷ expected intervals; 0 when webcam off) join `IntegritySignalNames` with default weights, computed in `internal/domain/signals`.
  - Snapshots are readable only by org users (recruiter/vetter/admin); client principals get 403.
- **Acceptance**: upload stores blob and row; oversized or non-JPEG rejected; purge removes past-retention snapshots and keeps newer; signals computed from a fixture stream with two fullscreen exits and 3 of 10 snapshots missing yield the expected values.
- **Non-goals**: capture UI (T09).
- **Implementation notes**: snapshot seq is client-assigned and unique per attempt; store `bytes` for retention accounting.
- **Complexity**: advanced
- **Validation**: test-first
- **Test scenarios**: as listed in Acceptance plus RLS scoping across two orgs and blob-delete failure leaving the row.
- **Checks**: `make generate`; `make check`; `make test`; `make test-integration`; `make openapi-lint`
- **Interfaces**: I3, I4
- **Escalate if**: blob client needs a list operation that MinIO's client wrapper does not expose.

### T09: Candidate integrity UI: consent, camera, photo ID, fullscreen, paste block

- **Goal**: Candidates experience the spec's pre-start and in-session integrity behaviour.
- **Dependencies**: T08
- **Inputs**: `web/static/assess/src/*.ts`, `internal/web/assess/**`, I3, I4, spec "Candidate session"
- **Scope**: `db/**`, `internal/**`, `web/static/**`, `seed/**`, `cmd/**`, `templates/**`, `runner/**`, `./Makefile`, `README.md`, `.env.example`, `docker-compose.yml`, `.gitignore`, `sqlc.yaml`, `go.mod`, `go.sum`
- **Contract**:
  - When any integrity flag is on, `/assess/` shows a consent page listing what is recorded; Start is disabled until consent and, when required, a camera preview is live and the ID frame captured.
  - Declining records nothing, keeps the attempt `invited`, and enqueues an `email.send` to the recruiter because the recruiter must know the invite stalled.
  - Snapshots every `webcam_interval_s` ± 10s jitter as JPEG ≤ 640px wide; failures record `snapshot {ok:false}`.
  - Fullscreen: request on start when required; exit shows a banner and records `fullscreen_exit`; re-entry records `fullscreen_enter`.
  - Paste block cancels the editor paste and records `paste {len:0, blocked:true}`.
- **Acceptance**: with all flags off the session behaves exactly as today; with flags on the consent page gates Start; event stream carries consent/fullscreen/snapshot events; a candidate with no camera and webcam required sees the "cannot start" message and the recruiter email lands in Mailpit.
- **Non-goals**: recruiter timeline (T10).
- **Implementation notes**: getUserMedia only inside the consent click handler; canvas → `toBlob('image/jpeg', 0.6)`.
- **Complexity**: standard
- **Validation**: test-first
- **Test scenarios**: service: consent required before Start when flags set; decline email; island: snapshot scheduler jitter bounds and failure event; paste block event shape.
- **Checks**: `bash web/static/assess/build.sh`; `bash web/static/assess/build.sh test`; `make generate`; `make check`; `make test`; `make test-integration`
- **Interfaces**: I3, I4
- **Escalate if**: the sealed cookie cannot carry the consent state without a schema change beyond `attempt.consent_at`.

### T10: Candidate detail, integrity timeline, Assessments screen

- **Goal**: Recruiters review an application on one screen with replay, timeline, code, tests, and score; the Assessments screen lists invites in flight.
- **Dependencies**: T04, T08
- **Inputs**: `internal/web/pipeline/**` (application page), `internal/web/reviews/**`, `web/static/replay/src/main.ts`, `internal/service/review.go`, `internal/service/review_replay.go`, `internal/web/assess/recruiter.go`, prototype Candidate detail and Assessments panels
- **Scope**: `db/**`, `internal/**`, `web/static/**`, `seed/**`, `cmd/**`, `templates/**`, `runner/**`, `./Makefile`, `README.md`, `.env.example`, `docker-compose.yml`, `.gitignore`, `sqlc.yaml`, `go.mod`, `go.sum`
- **Contract**:
  - Candidate detail = existing application page rebuilt: header with stage tag and Reject / Add to shortlist / Advance; replay viewer with speeds 1/1.5/2/4 and jump chips computed from submissions (first compile ok, first sample pass, all samples pass, timeout); integrity timeline under the scrubber with blur, paste, fullscreen, snapshot markers; final submission; test table; fit panel from the pool ranker terms + assessment score; résumé summary.
  - Snapshot markers open the image via a signed URL valid ≤ 5 minutes.
  - "No assessment yet" card with Send assessment when the application has no attempt.
  - Assessments screen lists invites with status, progress (problems submitted ÷ total), integrity flag count, expiry, and remind/review/revoke actions.
- **Acceptance**: page renders for applications with and without attempts; timeline markers match the event stream; client principals cannot load snapshot URLs; Assessments screen filters by status.
- **Non-goals**: shortlist builder (T13), work queue (T14).
- **Implementation notes**: timeline markers are derived server-side from the replay manifest so the client-side viewer stays a renderer.
- **Complexity**: standard
- **Validation**: test-first
- **Test scenarios**: jump chip derivation from submission outcomes; timeline marker list from a fixture stream; snapshot URL 403 for client users; assessments list progress math.
- **Checks**: `bash web/static/replay/build.sh`; `make generate`; `make check`; `make test`; `make test-integration`
- **Notes**: N6
- **Interfaces**: I4
- **Escalate if**: replay manifest must change shape for the client-side timeline.

### T11: Intake wizard and default-set picker

- **Goal**: The five-step intake creates client company, first client user, job with pipeline, and attached assessment atomically, with resumable drafts.
- **Dependencies**: T04, T06, T07
- **Inputs**: `internal/service/org.go`, `internal/service/job.go`, `internal/service/assessment.go`, `internal/web/jobs/**`, prototype Intake panel
- **Scope**: `db/**`, `internal/**`, `web/static/**`, `seed/**`, `cmd/**`, `templates/**`, `runner/**`, `./Makefile`, `README.md`, `.env.example`, `docker-compose.yml`, `.gitignore`, `sqlc.yaml`, `go.mod`, `go.sum`
- **Contract**:
  - `intake_draft(id, org_id, created_by, step, payload jsonb, updated_at)` with RLS; one open draft per user.
  - `domain.DefaultSet(problems, skills, seniority)` picks 1 easy + 1 medium for junior/mid and 1 medium + 1 hard for senior/staff/principal, preferring tag overlap with the ordered skills, excluding quality < 60, deterministic for equal inputs.
  - Step 4 offers Default set (swap per slot), Pick from bank, or Write your own (authoring with `?return=intake`).
  - `IntakeService.Create` runs one transaction: client company, client user (invite enqueued), job from template, assessment created and attached to the first `assessment` stage, draft deleted.
  - `client_company` gains `industry`, `shortlist_sla_days`, `brief`.
- **Acceptance**: drafts resume at the saved step; Create yields all four records plus an invite in Mailpit; a template without an assessment stage fails before Create; the default set changes with seniority.
- **Non-goals**: work-queue row for drafts (T14).
- **Implementation notes**: step panels are htmx partials posting to `/app/intake/{draft}/{step}`; Create is a separate confirm POST.
- **Complexity**: advanced
- **Validation**: test-first
- **Test scenarios**: default-set selection cases; draft save/resume; atomic create and rollback on assessment failure; validation errors per step.
- **Checks**: `make generate`; `make check`; `make test`; `make test-integration`
- **Interfaces**: I1, I3
- **Escalate if**: pipeline templates cannot be enumerated per org (only a default exists).

### T12: Seed bank of twelve verified problems

- **Goal**: `seed/problems` holds ≥ 12 original LeetCode-style problems that pass verification in every language they ship.
- **Dependencies**: T02, T06
- **Inputs**: `seed/problems/*.json`, `seed/problems/README.md`, `internal/service/problem_seed.go`
- **Scope**: `db/**`, `internal/**`, `web/static/**`, `seed/**`, `cmd/**`, `templates/**`, `runner/**`, `./Makefile`, `README.md`, `.env.example`, `docker-compose.yml`, `.gitignore`, `sqlc.yaml`, `go.mod`, `go.sum`
- **Contract**:
  - 3 easy, 5 medium, 2 hard code problems across arrays/strings, hashing, two pointers, intervals, BFS/graph, DP, simulation; 3 SQL problems.
  - Every code problem has `allowed_languages:["any"]`, reference solutions in at least python, javascript, go (rust and java where cheap), ≥ 6 named cases with classes, ≥ 2 public, `recommended_minutes`, tags, and quality ≥ 80.
  - Statements are original text in LeetCode form: description, examples with explanation, constraints.
- **Acceptance**: `admin seed-problems` against `make dev-runner` imports all with every reference passing; `make test-integration` includes a tagged test that runs each seed reference in its language when the image exists.
- **Non-goals**: more than 15 problems.
- **Implementation notes**: write each statement before its cases; hidden cases include one perf-class case near the constraint bound.
- **Complexity**: standard
- **Validation**: checks-only
- **Checks**: `make check`; `make test`; `make test-integration`
- **Notes**: N2
- **Interfaces**: I1
- **Escalate if**: a reference language image cannot pass under default limits.

### T13: Shortlist packets, builder, client packet view with replay

- **Goal**: Recruiters assemble and send a ranked shortlist; clients read it with read-only replay.
- **Dependencies**: T04, T10
- **Inputs**: `internal/service/{release,clientportal}.go`, `internal/web/{client,pipeline}/**`, `internal/api/mount.go`, spec screen 5
- **Scope**: `db/**`, `internal/**`, `web/static/**`, `seed/**`, `cmd/**`, `templates/**`, `runner/**`, `./Makefile`, `README.md`, `.env.example`, `docker-compose.yml`, `.gitignore`, `sqlc.yaml`, `go.mod`, `go.sum`
- **Contract**:
  - I5 tables and service; `Send` releases every pick with `ReleaseService` semantics in one transaction, sets `sent`, enqueues one `email.send` per client user, and refuses rejected picks by name.
  - A packet is immutable once `sent`; a new packet supersedes it.
  - Builder pool = applications on the job with a scored attempt, ranked by pool-ranker fit then score, filtered `score ≥ N` (default 70); ≤ 5 picks with reorder and remove, plus note and legend.
  - Client view `/client/jobs/{id}/shortlist` shows ranking, note, résumé, final code, tests, and read-only replay; signals, snapshots, guidelines, and other applicants are absent in both HTML and manifest.
  - API: `POST /shortlists`, `POST /shortlists/{id}/send`, `GET /shortlists/{id}`.
- **Acceptance**: sending a 3-pick packet releases all three, emails every client user, and the client page lists them in rank order; a client user of another company gets 404; replay manifest for clients omits event kinds other than `edit|run|submit|lang_change`.
- **Non-goals**: un-send.
- **Implementation notes**: client replay reuses the replay island with `readonly:true` and a filtered manifest endpoint under `/client/`.
- **Complexity**: advanced
- **Validation**: test-first
- **Test scenarios**: as Acceptance plus rejected-pick refusal, rank uniqueness, and immutability after send.
- **Checks**: `make generate`; `make check`; `make test`; `make test-integration`; `make openapi-lint`
- **Interfaces**: I4, I5
- **Escalate if**: the client replay needs events the manifest currently strips for privacy.

### T14: Work queue, Clients, Client detail

- **Goal**: Work queue, Clients, and Client detail exist with live sidebar counts.
- **Dependencies**: T04, T11, T13
- **Inputs**: `Board` in `internal/service/application.go`, `Assignments` in `internal/service/review.go`, `internal/service/schedule.go`, `internal/web/pipeline/**`, spec screens 1–3
- **Scope**: `db/**`, `internal/**`, `web/static/**`, `seed/**`, `cmd/**`, `templates/**`, `runner/**`, `./Makefile`, `README.md`, `.env.example`, `docker-compose.yml`, `.gitignore`, `sqlc.yaml`, `go.mod`, `go.sum`
- **Contract**:
  - I6 rules: `review` = `scored` attempt with no review; `expiring` = invite expiring < 24h, not started; `client_waiting` = request-info event with no later recruiter reply; `shortlist_draft` = packet `draft`; `scorecard_overdue` = slot ended > 24h ago with no scorecard. Preview attempts excluded.
  - `queue_snooze(org_id, user_id, kind, subject_id, until)` hides an item for that user only.
  - Clients table per spec: "Placed 90d" counts `hired` applications in 90 days; "Median days to shortlist" = median of first packet `sent_at` − job `created_at`.
  - Client detail tabs Jobs / Board / Shortlists; the board merges the client's jobs by stage name and kind (N5) behind a job selector.
  - Sidebar counts come from `WorkQueueService.Counts` plus simple counts, computed once per request in layout middleware.
- **Acceptance**: each rule produces and clears its row in a fixture; snooze hides for 24h for one user only; the client board merges two jobs into correct columns; counts match.
- **Non-goals**: SLA ranking; per-desk scoping.
- **Implementation notes**: queue rules are individual SQL queries unioned in Go, not one view, so each rule is testable alone.
- **Complexity**: standard
- **Validation**: test-first
- **Test scenarios**: one fixture per rule; snooze scoping; median computation; board merge.
- **Checks**: `make generate`; `make check`; `make test`; `make test-integration`
- **Notes**: N5
- **Interfaces**: I5, I6
- **Escalate if**: request-info replies leave no event to detect.

### T15: Browser end-to-end checks

- **Goal**: Playwright covers the editor and integrity flows the unit layer cannot.
- **Dependencies**: T03, T09, T12, T13
- **Inputs**: `README.md`, `./Makefile`, `web/static/assess/**`
- **Scope**: `web/e2e/**`, `./Makefile`, `README.md`, `.gitignore`
- **Contract**:
  - `make test-e2e` boots serve/worker/runner against the Compose stack with a fresh org and the seed bank, then runs Playwright headless.
  - Scenarios: Try it runs python and rust to a pass; vim `dd` and emacs `C-k` behave and the keymap event is recorded; consent with a mocked camera captures an ID frame and posts a snapshot; a fullscreen exit is recorded; a sent packet is visible to the client with replay and without signals.
  - The camera is mocked with Chromium's fake-device flags because no real hardware exists on the host.
- **Acceptance**: `make test-e2e` passes locally and README documents it.
- **Non-goals**: CI wiring.
- **Implementation notes**: `web/e2e/package.json` pins `@playwright/test`; Chromium flags `--use-fake-device-for-media-stream --use-fake-ui-for-media-stream`.
- **Complexity**: standard
- **Validation**: checks-only
- **Checks**: `make test-e2e`; `make check`
- **Escalate if**: Playwright cannot drive the fullscreen API headless (assert on the recorded event via a scripted `document.exitFullscreen` instead).
