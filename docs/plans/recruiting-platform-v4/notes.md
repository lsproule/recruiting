# Notes

## N1: Repository is not a git work tree
No git repository exists; run state falls back into the plan folder. No branch is created by the plan. Initialising git is the user's call.

## N2: Queue tables are exempt from the RLS invariant
The Postgres-backed queue's own tables are infrastructure, not domain tables; they carry no `org_id` and are excluded from the "every domain table has RLS" constraint. Payloads carry `org_id` where a handler needs a tenant-scoped transaction.

## N3: Mode wiring deferred
`cmd/recruiting/modes.go` stubs (`runServe`, `runAdmin`, `runWorker`, `runMigrate`) were not wired by T03/T04 because `modes.go` was outside their scope. `RunAdmin` exists in `cmd/recruiting/admin.go`; web packages expose `Mount` functions. Wiring the real server composition root and CLI dispatch must happen in the first task whose scope includes `cmd/recruiting/**` (T20 at the latest; T07 for the worker mode).

## N4: Public apply URL is org-scoped
Job slugs are unique per org only, so the public apply URL is `/apply/{org_slug}/{job_slug}` (not `/apply/{job_slug}` as the spec sketched). Any email template or link builder must use the org-scoped form.

## N5: Serve wiring owed by T07
T06 could not edit `cmd/recruiting/serve.go`. T07 (scope includes `cmd/recruiting/**`) must wire: `blob.New` from `cfg.Blob*` + `EnsureBucket`, `service.NewResumeService`, `service.NewCandidateService`, `apply.Mount` and `candidates.Mount` on the web mux after `auth.Mount`, `apply.MaxBody(apply.UploadBodyLimit)` before `auth.Mount`, and pass the logger.

## N6: Pipeline mount owed
`pipeline.Mount(web, pipeline.Deps{Applications: service.NewApplicationService(st, q, cfg.BaseURL), Org, Logger})` must be added to `cmd/recruiting/serve.go` after `auth.Mount` by the next task whose scope includes `cmd/recruiting/**` (T20 at the latest); T09–T12 should register their mounts the same way and list them in interfaces.

## N7: Wiring owed after T09
serve.go: `availability.Mount(web, availability.Deps{Schedule,Org,Logger})`, `book.Mount(web, book.Deps{Schedule,Links,Logger})`, `pipeline.Mount(...)` (N6). worker.go: `queue.KindInterviewRemind` → `service.RemindHandler(st,q,cfg.BaseURL)`. UI gaps: recruiter cannot yet pick a vetter per application (ScheduleService.Assign exists) nor edit `stage.default_vetter_id` in the stage editor — T10 or T11 (whichever touches internal/web/pipeline or jobs) should add both controls; otherwise T20.

## N8: UI embedding owed (T20 or earlier task touching these packages)
- `internal/web/pipeline` application page: embed `<div hx-get={ scorecards.SummaryPath(app.ID) } hx-trigger="load">`, add recruiter control to assign a vetter (`ScheduleService.Assign`), later also assessment summary/review link.
- `internal/web/jobs` stage editor: link `scorecards.RubricPath(jobID, stageID)` on interview stages; edit `stage.default_vetter_id`.
- `cmd/recruiting/serve.go`: mount pipeline, availability, book, scorecards (and later client portal, pool, problems, assess, reviews); worker.go: interview.remind handler.
- RLS `client_read` on `scorecard` exposes the `notes` column at DB level; the client portal (T11) must never select it for clients — consider a column-restricted view.

## N9: Release notice wiring
`internal/web/pipeline` release/unrelease handlers must call `service.ReleaseService` (not `ApplicationService.Release`) so client_release_notice emails are sent; serve.go must construct `NewReleaseService` and `NewClientPortalService` and `client.Mount`.

## N10: Integration suite shares one queue
`go test ./...` runs packages in parallel against one database; `internal/queue` tests start a real River worker on the default queue and drain `email.send` jobs enqueued by other packages' tests. Controller fix (test-only, 2026-08-30): the queue test recorder is scoped to its own recipient (`recorder.only`). Any future test that starts a worker must scope its assertions the same way or use a dedicated queue name.

## N11: Pool wiring owed
`ScorecardService.Save` must call `PoolService.OnStrongYes(ctx, tx, orgID, applicationID)` after writing the strong-yes event; `internal/web/jobs/pages.templ` must embed `@pool.SuggestionsFragment(job.ID)` on the saved job page; `internal/web/pipeline` application page must embed `@pool.FlagButton(app.ID)`; serve.go must construct `service.NewPoolService(st)` and call `pool.Mount(web, pool.Deps{Pool,Org,Logger})` after `jobs.Mount`.

## N12: Runner runtime and wiring
gVisor is not installed on the dev host; runner defaults to `RUNNER_RUNTIME=runsc` and refuses to start without it unless `RUNNER_ALLOW_INSECURE_RUNTIME=1` (dev only). `cmd/recruiting/modes.go` `runRunner` must call `server.Run(ctx, logger, server.ConfigFromEnv(cfg.RunnerSecret))`. Production needs a runner-dedicated Postgres via `RUNNER_SQL_URL`.

## N13: Problem bank wiring owed
serve.go: `service.NewProblemService(st, service.NewHTTPExecutor(cfg.RunnerURL, cfg.RunnerSecret))` + `problems.Mount`. Admin CLI: `seed-problems` calling `ProblemService.ImportPlatformSeed` inside `system.WithSystemTx`. modes.go: `runRunner` → `server.Run`.

## N14: Assessment wiring owed
serve.go: `api.MountAttempts(r.API, api.AttemptsDeps{Attempts, Resolve})`, `assess.Mount(web, ...)` (candidate, behind auth's Assessment middleware), `assess.MountRecruiter(web, ...)`. worker.go: `queue.KindAssessmentInvite` → `service.AssessmentInviteHandler(st,q,cfg.BaseURL)`. Jobs stage editor: link assessment stages to `/app/assessments/attach?job=&stage=`. Invite TTL comes from the assessment's own `invite_window_days` (org setting seeds the default).

## N15: Assessment API passthrough and expiry sweep
The sealed assessment cookie is scoped to `/assess/`; the island therefore calls `/assess/api/*`, which `assess.Mount` forwards to the Huma handler passed in `assess.Deps.API` (serve.go passes the API mux). The worker must call `AttemptService.ExpireDue(ctx)` periodically (e.g. every minute) so abandoned attempts auto-submit and finalize.

## N16: Execution/finalize wiring owed
worker.go: `queue.KindRunnerExecute` → `service.RunnerExecuteHandler(st, client.New(cfg.RunnerURL, cfg.RunnerSecret), logger)`; `queue.KindAttemptFinalize` → `service.AttemptFinalizeHandler(st, q, blob, logger)`. `service.HTTPExecutor` duplicates `internal/runner/client`; retire it in T19/T20 by making ProblemService take `client.New(...)`.

## N17: Signals wiring owed
worker.go: `queue.KindSignalsCompute` → `service.SignalsComputeHandler(st, blob, logger)`.

## N18: Review wiring owed + assess config defect
serve.go: `api.MountReplay(r.API, api.ReplayDeps{Reviews, Resolve})`, `reviews.Mount(web, reviews.Deps{Reviews,Org,Logger})`; pipeline application page embeds `@reviews.SummaryFragment(...)`.
DEFECT (T14 file, fix in the acceptance-gate phase): `internal/web/assess/pages.templ` uses `@templ.Raw(jsonForScript(cfgJSON))` inside a `<script>`; templ emits script bodies literally, so the candidate island never receives its config. Fix by writing the whole `<script>` element from a Go helper (see `configScript` in `internal/web/reviews`).

## N19: API wiring owed
serve.go: `api.MountAll(r, api.Deps{...all services, Sessions, Tokens})` replaces the individual MountAttempts/MountReplay calls; admin UI: a handler for the API-token screen embedding `api.TokensFragment`. Signed *upload* URLs for resumes are not implemented (upload is inline on create-candidate) — acceptable v1 deviation.
