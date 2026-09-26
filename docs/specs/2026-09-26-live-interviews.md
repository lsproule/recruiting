# Live Interviews, Screening Sprints, and Hiring Processes

## Outcome

Three things the platform could not do become part of the product:

1. **Video interviews in the browser.** An interview stage can be a phone
   call or a video room. A video room carries the interviewer's and the
   candidate's camera and microphone, lets either of them share a screen,
   and gives both a shared code editor that runs code through the sandboxed
   runner. What was written in the room is kept with the application.
2. **Screening sprints.** Speed-dating for recruiting: a recruiter picks a
   set of shortlisted candidates and a set of interviewers, and the platform
   schedules a round-robin of short video conversations (five minutes by
   default) so that every candidate meets every interviewer in one sitting.
   Six engineers screen sixty candidates in an hour. After each round the
   interviewer files a one-line rating; the sprint's summary ranks the
   candidates and the recruiter advances or rejects them from there.
3. **Hiring processes.** A pipeline is no longer one fixed default. An org
   keeps a library of processes (templates), ships with three that make
   sense, edits them, and picks one per job. The stages carry what the
   stage needs: an interview's format and length, a sprint's round length,
   a take-home's window. The user's own process is one of the defaults:
   *Applied → Recruiter call → Take-home → Screening sprint → Technical
   interview → HR interview → Offer → Hired / Rejected*.

Everything is server-rendered templ + htmx, the same as the rest of the
console, with two browser islands: the room (video, editor) and the sprint
console (rotation clock). Every flow has an integration test and a
Playwright browser test.

## Context

- The platform (`docs/specs/2026-08-29-recruiting-platform.md`, console v2
  `docs/specs/2026-08-30-recruiter-console-v2/spec.md`) has typed pipeline
  stages (`generic`, `interview`, `assessment`, `client_review`,
  `terminal`), booked phone screens with scorecards, proctored
  assessments, and per-org pipeline templates that are seeded once and
  never edited in the UI.
- "Video conferencing" was a v1 non-goal. It is now in scope, built in:
  WebRTC between browsers, with the app server as the signalling relay.
- Candidates have no accounts; every candidate page is behind a magic link.
- The e2e harness (`web/e2e/run.sh`) boots the real stack against Compose
  and runs Chromium with a synthetic camera.

## User Experience and Behavior

### Hiring processes

- **Processes screen** (`/app/processes`, recruiters and admins): the org's
  process library as cards — name, one-line description, the stage chain as
  chips, which is the default, how many jobs were built from it. Actions:
  *New process* (blank, or copied from the built-in library or from any
  existing process), *Make default*, *Delete* (never the default; never one
  a job was built from is fine, jobs keep their own stages).
- **Process editor** (`/app/processes/{id}`): the stage list with drag-free
  reordering (up/down), and a settings card per stage: name, kind, and the
  kind's own fields —
  - `interview`: format *Phone call* or *Video room*, length in minutes
    (default 30), default interviewer, rubric link once a job exists;
  - `sprint`: round length (default 5 min), break between rounds (default
    1 min);
  - `assessment`: the assessment's own format (`timed` or `take_home`) is
    set on the assessment, not the stage; the stage shows which it is;
  - `client_review`: unblind toggle;
  - `terminal`: closes as hired or rejected.
  The same editor, with the same look, is the per-job pipeline editor
  (`/app/jobs/{id}/pipeline`), which is where the current bare table of
  forms goes away.
- **Built-in library** (seeded for every new org; addable later from the
  Processes screen):
  1. *Agency standard* — the current default, unchanged, stays the org
     default so nothing existing moves.
  2. *Engineering loop* — Applied (generic) → Recruiter call (interview,
     phone, 20 min) → Take-home (assessment) → Screening sprint (sprint, 5
     min rounds, 1 min breaks) → Technical interview (interview, video, 60
     min) → HR interview (interview, video, 45 min) → Offer (generic) →
     Hired / Rejected.
  3. *Fast track* — Applied → Screening sprint → Technical interview (video,
     60 min) → Hired / Rejected.
- **Intake** step 2 keeps offering the library; a process without an
  assessment stage is still refused there, as today.
- Creating a job copies the process into the job's own stages, settings
  included. Later edits to a process never touch existing jobs.

### Video interview (interview stage, format `video`)

- Booking is unchanged: entering the stage emails the booking link, the
  candidate books a slot, both get confirmations. The confirmation for a
  video interview says the interview is by video and that the room opens
  from the booking page ten minutes before the start.
- **Candidate side**: the booking page shows *Join your interview* from ten
  minutes before the slot until two hours after it started; before that it
  shows when the room opens. The room is `/book/{token}/room`.
- **Interviewer side**: `/app/interviews` lists the signed-in user's
  upcoming interviews (booked slots where they are the interviewer, plus the
  sprints they are in) with *Join* and *Scorecard*. Recruiters and admins
  see every interview in the org there. The application page shows the
  booking and a *Join room* button while the room is open. The room is
  `/app/rooms/slot/{slotID}`.
- **The room**: a video grid (self, the others; each tile named), controls
  for microphone, camera, *Share screen*, *Leave*; a shared code editor
  (CodeMirror, language selector over the platform's languages, vim/emacs
  keymaps as elsewhere) with a *Run* button and an output pane; a note that
  the code is kept with the application. A shared screen appears as its own
  tile on every other participant's grid. Anyone may join a room more than
  once (a dropped connection just rejoins). Leaving does not end the
  interview for the others.
- After the call the interviewer files the stage's scorecard as today. The
  application page gains a *Written in the interview* panel showing the
  final editor contents and language.

### Screening sprint (stage kind `sprint`)

- **Setup** (`/app/sprints/new?job=&stage=`, recruiters): name, date and
  start time, round length and break (pre-filled from the stage), the
  interviewers (org users with the vetter or recruiter role, multi-select),
  the candidates (applications currently in the stage, all checked by
  default). The page shows the plan as it is configured: *12 candidates ×
  6 interviewers → 12 rounds of 5 min, 71 minutes, every candidate meets
  every interviewer.* Saving creates a draft with its pairings.
- **Schedule** sends every candidate a `sprint_invite` email with their
  link, their block (which rounds they are in, in their own time zone once
  they open the page), and what to expect; the sprint becomes `scheduled`.
  *Start now* moves the start to the current minute. *Cancel* voids links.
- **Rotation** is driven by the clock. Round *r* runs from
  `starts_at + r × (round + break)` for `round` seconds, then a break. The
  server is the authority; every page polls the sprint state and moves
  itself. Pairings: with *n* candidates and *k* interviewers, round *r* pairs
  small-side member *i* with big-side member *(r + i) mod max(n, k)*, so
  each candidate meets each interviewer exactly once, every candidate's
  meetings are contiguous, and there are `max(n, k)` rounds.
- **Interviewer console** (`/app/sprints/{id}/console`): the round clock,
  who is next, and the current room embedded. When a round ends the room
  swaps to the next pairing and a **rating card** for the candidate just
  seen slides in: score 1–5, *strong yes / yes / no / strong no*, a
  one-line note. Ratings can also be filed or changed later from the
  summary. Between rounds, a break screen.
- **Candidate lobby** (`/sprint/{token}`): the candidate's schedule, a
  countdown to their next conversation with the interviewer's name, a
  camera check, and the room itself when a round they are in is live.
  Between their rounds, a waiting screen; after their last, a thank-you.
- **Summary** (`/app/sprints/{id}`): candidates ranked by mean score, each
  interviewer's rating and note in a grid, rounds missed, and per candidate
  *Advance* / *Reject* (the ordinary move, so the pipeline's audit trail
  holds). A sprint stage requires at least one rating before an application
  leaves it, overridable with a reason like the other prerequisites.
- **Work queue** gains one rule: a round that ended more than five minutes
  ago whose interviewer has not rated the candidate.

### Take-home (assessment format `take_home`)

An assessment gains a format. `timed` is today's proctored sitting. A
`take_home` assessment defaults to a 72-hour window, integrity settings off,
and candidate copy that says *You have until Thursday 14:00* rather than a
countdown; the candidate may close the page and return through the same
link until the deadline. Scoring, replay, and review are unchanged.

## Scope

- Stage kind `sprint`; per-kind stage settings on job stages and template
  stages; process library, seeding, and CRUD; process editor; restyled
  per-job pipeline editor.
- Rooms: SSE signalling, WebRTC mesh in the browser, screen share, Yjs
  shared editor relayed by the server, code persistence, run through the
  runner; candidate and interviewer room pages; `/app/interviews`.
- Sprints: model, planner, scheduling and invites, rotation clock, console,
  lobby, rating, summary, work queue rule, pipeline prerequisite.
- Assessment format `take_home`.
- JSON API for processes, sprints, and room code; OpenAPI regenerated.
- Unit, integration, and Playwright coverage for each flow above.

## Non-goals

- Recording video or audio; transcripts.
- TURN relay hosting. ICE servers are configuration (`RTC_ICE_SERVERS`);
  a deployment behind symmetric NAT supplies its own TURN.
- More than one `serve` replica sharing a room: room state is in-memory
  in the serving process. Sticky sessions or a bus are a later change.
- Calendar invitations (.ics) and external calendar sync.
- Sprints spanning several jobs; sprints with a candidate meeting only a
  subset of interviewers.
- Client users joining rooms.

## Proposed Design

### Data

`db/migrations/00030_live_interviews.sql`:

- `stage` and `pipeline_template_stage` gain `interview_format text not
  null default 'call' check in ('call','video')`, `duration_minutes int`,
  `round_seconds int`, `break_seconds int`; the kind check gains `sprint`.
- `pipeline_template` gains `description text not null default ''`,
  `library_key text`, `updated_at`.
- `assessment` gains `format text not null default 'timed' check in
  ('timed','take_home')`.
- `magic_link.purpose` check gains `'sprint'`.
- `sprint` (id, org_id, job_id, stage_id, name, status
  `draft|scheduled|cancelled`, starts_at, round_seconds, break_seconds,
  created_by, created_at, updated_at). Live and done are derived from the
  clock, never stored.
- `sprint_interviewer` (sprint_id, org_id, user_id, position), unique per
  sprint.
- `sprint_candidate` (sprint_id, org_id, application_id, position), unique
  per sprint.
- `sprint_pairing` (id, sprint_id, org_id, round, interviewer_id,
  application_id), unique on (sprint, round, interviewer) and (sprint,
  round, application).
- `sprint_rating` (id, org_id, sprint_id, pairing_id, interviewer_id,
  application_id, score 1–5, recommendation `strong_yes|yes|no|strong_no`,
  note, created_at, updated_at), unique per pairing.
- `interview_room` (id, org_id, kind `slot|pairing`, subject_id, language,
  source, updated_at), unique on (kind, subject_id): the persisted editor.

All new tables carry `org_id` with the forced tenant policy. Candidate
principals (magic links) run in the org scope like today's booking page.

### Domain (`internal/domain`)

- `StageSprint` kind; `Stage` gains `InterviewFormat`, `DurationMinutes`,
  `RoundSeconds`, `BreakSeconds`; `ValidatePipeline` checks them per kind
  and `ValidateMove` treats a sprint stage like a generic one for who may
  leave it, with `Prereqs.HasRating` as its prerequisite.
- `processes.go`: the built-in library as `[]ProcessSpec{Key, Name,
  Description, Stages []StageSpec}`.
- `sprint.go`: `PlanRounds(candidates, interviewers []uuid.UUID)
  []Pairing`, `SprintClock{StartsAt, Rounds, RoundSeconds, BreakSeconds}`
  with `RoundAt(now)`, `RoundWindow(r)`, `EndsAt()`, `Phase(now)`
  (`before|round|break|after`).

### Services

- `ProcessService`: list, get, create (blank / from library / from process),
  update stages (add, update, delete, reorder), set default, delete.
  `copyTemplateStages` copies the settings into job stages.
- `JobService.StageInput` gains the settings; `AddStage`/`UpdateStage`
  validate them; `Stages` returns them.
- `SprintService`: `Create`, `Update` (draft only), `Schedule`, `StartNow`,
  `Cancel`, `Get` (with pairings and ratings), `State(now)`, `Console(p,
  id)` (the interviewer's own pairings), `Lobby(p magic)`, `Rate`,
  `Summary`. Scheduling issues one `sprint` magic link per candidate and
  queues `email.send` with template `sprint_invite`. The stage prerequisite
  loader reports `HasRating` for sprint stages.
- `RoomService`: `Open(ctx, p, key RoomKey) (Room, error)` authorises and
  describes a room (participants allowed, editor language list, open
  window); an in-process `Hub` keeps per-room members (SSE subscribers), a
  bounded Yjs update log, and the roster. `Signal` relays one message,
  `Doc` appends and relays an update (or replaces the log with a
  snapshot), `SaveCode` persists language + source, `Run` executes source
  with a stdin through the runner client and returns stdout/stderr/status.
  Rooms open ten minutes before a slot and close two hours after its start;
  a pairing's room is open during its round and its following break.
- `AssessmentInput.Format`; the candidate session copy and the island's
  timer read it. `ScheduleService.confirm` adds the join note for video.
- `WorkQueueService` gains `QueueSprintRating`.

### HTTP

- `internal/web/processes`: `/app/processes`, `/{id}`, stage routes
  mirroring the job pipeline editor's.
- `internal/web/jobs`: pipeline editor restyled to the shared stage editor
  component and the new fields.
- `internal/web/interviews`: `/app/interviews`.
- `internal/web/room`: the page and the room API under a prefix:
  `GET {prefix}/events` (SSE), `POST {prefix}/signal`, `POST
  {prefix}/doc`, `POST {prefix}/code`, `POST {prefix}/run`. Mounted at
  `/app/rooms/{kind}/{id}` for org users, `/book/{token}/room` for the
  booked candidate, and `/sprint/{token}/room/{pairing}` for a sprint
  candidate. The SSE stream's first event is `hello`: the client's
  participant id, the roster, the persisted code, and the update log.
- `internal/web/sprints`: `/app/sprints/new`, `/app/sprints/{id}`
  (summary), `/console`, `/state` (JSON for the clock), `/rate`,
  `/schedule`, `/start`, `/cancel`; `/sprint/{token}` lobby and
  `/sprint/{token}/state`.
- API (`/api/v1`): `pipeline-templates` (list, get, create, update-stage,
  create-stage, delete-stage, reorder, set-default, delete), `sprints`
  (list, get, create, schedule, start, cancel, rate, summary), `rooms`
  (`get-room-code`, `run-room-code`). Every new service sentinel is mapped
  in `statusErrors`.

### Browser

`web/static/room/` (esbuild, committed bundle `room.js`):

- `signal.ts`: `EventSource` for the stream, `fetch` for posts, CSRF
  header from the page, reconnect with backoff.
- `rtc.ts`: full-mesh `RTCPeerConnection` per remote participant, perfect
  negotiation (the lexically smaller participant id is polite), tracks
  added from the local stream, screen share as an extra track with a
  `screen` content hint sent through signalling so the far side labels it.
- `doc.ts`: a Yjs document bound to CodeMirror through `y-codemirror.next`,
  a provider that posts each local update to `/doc` and applies what the
  stream delivers; the log is replayed on `hello`; the first client to
  find an empty log seeds the document from the persisted source; every
  change also debounces a `/code` save.
- `main.ts`: mounts the grid, the controls, the editor, the run pane, and,
  for sprint rooms, nothing else; the sprint console page owns the clock
  and mounts a fresh room island per pairing.
- `ICE servers` come from the page's config JSON (`RTC_ICE_SERVERS` env,
  JSON array; default the public Google STUN entry).

### Email

Templates `sprint_invite` (subject, text, html). `booking_confirmation`
gains `JoinNote`.

## Failure and Edge Cases

- A participant's browser refuses the camera: they join with audio only or
  nothing; the tile shows their name; the editor still works.
- WebRTC never connects (no TURN): the editor, the chat of code, and the
  run pane still work, because they go through the server, and the tile
  says *connecting…* rather than failing the page.
- The serve process restarts: rooms lose their roster and update log;
  clients reconnect the stream, receive `hello` with the persisted code,
  and re-seed. Nothing typed more than a couple of seconds before is lost.
- A candidate opens the sprint link early: the lobby with a countdown and a
  camera check. Late: the lobby joins the round in progress. After the
  sprint: a thank-you page.
- An interviewer misses a round: the candidate sees *your interviewer has
  not joined yet*; the rating card still appears and can be left unrated,
  which the work queue then reports.
- Scheduling a sprint with fewer candidates than interviewers: each round
  leaves interviewers idle, which the plan says up front.
- Moving an application out of a sprint stage with no rating: refused
  unless overridden with a reason.
- Deleting the default process: refused. Deleting a process a job used:
  allowed; jobs own their stages.
- Take-home link reused after the deadline: the existing expired-link page.

## Validation

- Unit: `PlanRounds` (every pair once, contiguity, n<k, n=1, k=1),
  `SprintClock` phases across boundaries, process library validity,
  `ValidatePipeline` with kind settings, `ValidateMove` for sprint stages.
- Integration: process CRUD and copy into a job with settings; sprint
  lifecycle (create → schedule issues links and mails → state → rate →
  summary → prerequisite); room authorisation (booked candidate only their
  slot, sprint candidate only their pairing, another org nothing); room
  code persistence; work-queue rule; take-home defaults; RLS on every new
  table.
- Browser (Playwright, `web/e2e/tests`): `process.spec.ts` (library, new
  process, edit settings, make default, job from it); `interview-room.spec.ts`
  (candidate books a video interview, both join from their own pages, each
  sees the other's video, a shared screen appears, typing on one side lands
  on the other, *Run* prints python output, the interviewer files the
  scorecard and the application shows the code); `sprint.spec.ts` (2
  interviewers × 3 candidates with 20-second rounds through the API:
  lobbies count down, rooms rotate, each candidate meets each interviewer,
  ratings filed, summary ranks, top candidate advanced);
  `take-home.spec.ts` (deadline copy, leave and return).
- `make check test test-integration openapi-lint test-e2e` green.

## Acceptance Criteria

1. A new org has three processes; the Processes screen edits one, adds a
   sprint stage with a 4-minute round, makes it the default, and a job
   created afterwards carries those stages and settings.
2. A candidate on a video interview stage books, gets a confirmation that
   says the interview is by video, joins from the booking page, and the
   interviewer joins from `/app/interviews`; both see and hear each other,
   a shared screen shows on the other side, both edit one document, and a
   python run prints on both sides.
3. A sprint of 3 candidates and 2 interviewers runs three rounds on the
   clock; every candidate meets every interviewer; the summary ranks them by
   the ratings filed from the console; the recruiter advances one.
4. An application cannot leave a sprint stage unrated without an override
   reason, and the work queue lists an unrated round.
5. A take-home assessment shows the deadline rather than a countdown and
   survives closing the tab.
6. Every new table refuses cross-org reads at the RLS level.

## Resolved Decisions

- Signalling over SSE + POST rather than WebSockets: no new Go dependency,
  works through the existing CSRF and session middleware, and the message
  volume is tiny.
- Editor sync through the server (Yjs updates relayed) rather than over
  the data channel, so the editor works even when media cannot connect.
- Sprint rotation is clock-driven, not button-driven: the whole point is
  that sixty conversations run on rails.
- `sprint` is a stage kind, not a property of `interview`: it has its own
  prerequisite, its own screens, and its own on-entry behaviour (none; the
  recruiter schedules it deliberately).
- The current default process stays the default. Existing tests and orgs
  keep their pipeline; the new process is one click away.
- "Vetter" remains the interviewer role; recruiters who interview hold both
  roles. Sprint interviewers may be either.
