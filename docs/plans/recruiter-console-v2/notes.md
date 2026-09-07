# Notes

## N1: Two database roles
`serve`/`worker` refuse the schema-owner URL (`store: connection role owns the schema; RLS would be bypassed`) and must use `postgres://app_rw:app_rw@localhost:5433/recruiting?sslmode=disable`; `migrate`, `admin`, and `make migrate` need the owner URL from `.env.example`. `.env.example` currently documents only the owner URL.

## N2: Local runner
gVisor is not installed on the dev host. `runner` starts only with `RUNNER_ALLOW_INSECURE_RUNTIME=1`; images are built by `runner/images/build.sh` (`recruiting-runner-<lang>`), none exist locally yet. `admin seed-problems` executes every reference solution through the runner, so seeding needs `runner` up. SQL problems need `RUNNER_SQL_URL` with CREATEDB/CREATEROLE (the Compose `recruiting` superuser URL works).

## N3: Language ids
`domain.ProblemLanguages` and `server.Languages` are the two closed language sets; `node` is the current id for JavaScript. The harness (`runner/harness/main.go`) switches on the id in `prepare`; the docker layer picks the image by `<prefix><language>`. Compiled languages compile once in `prepare`, then each test runs the produced command.

## N4: Static assets are JS-only
`web/static/embed.go` embeds `*.js assess/*.js replay/*.js` and serves everything as `text/javascript`; `layout.MountStatic` exposes them under `/static/`. Serving CSS needs a content-type keyed by extension and a new glob.

## N5: Board is per job
`ApplicationService.Board(ctx, p, jobID)` returns one job's columns. There is no cross-job board; a client-scoped board must fan out over the client's jobs and merge columns by stage name/kind.

## N6: Script islands
templ emits `<script>` bodies literally; boot JSON for islands goes through `configScript(id, body)` (`internal/web/reviews/view.go`) as a whole raw element. Reuse that pattern for every new island.

## N7: TypeScript is interpreted, not compiled
`node --check` parses a `.ts` file as JavaScript, so any type annotation is a SyntaxError (verified on node 22.19 and 24.20). T02 removed typescript's `Compile` step in `runner/harness/main.go`; typescript runs like lua, through `node --experimental-strip-types`. Contract-preserving: the language set and I2 shape are unchanged. Tasks touching editor language modes (T05) or seed references (T12) should not expect a typescript compile diagnostic.

## N8: Kotlin and C# image specifics
Kotlin's stock `kotlinc` launcher is OOM-killed at 256MB; the image wraps `K2JVMCompiler` directly plus an AppCDS archive (~4.4s hello-world). C# needs a pre-restored template and `NUGET_PACKAGES=/opt/nuget` (~6.4s). Neither needed the default limits raised.

## N9: Flaky queue test
`internal/queue/TestFailingJobIsRetriedThenSucceeds` flaked in 2 of 4 full `make test-integration` runs on this host (30s budget) and passes in isolation; it is unrelated to console work. Re-run before treating it as a regression.

## N10: Try-mode execute path must be reconciled in T06
T05's island calls `executePath(mode, problemID, kind)` from `web/static/assess/src/mode.ts`; in `try` mode it currently posts under the attempt-scoped API base. I7 specifies `POST /api/v1/problems/{id}/try` with no attempt. T06 owns I7 and the Try it page: implement I7 as specified and update `mode.ts` (and its test) so try mode posts there. The candidate `attempt` path is unchanged.

## N11: esbuild tree-shakes the editor keymaps
`@replit/codemirror-vim` and `-emacs` register bindings in top-level calls their ESM marks `/* @__PURE__ */`; without `--ignore-annotations` esbuild drops the emacs keymap silently. `web/static/assess/build.sh` passes it for both the bundle and the test build — keep it.

## N12: Quality floor reshaped the shared test fixtures
T06's attach refusal below quality 60 invalidated ~40 pre-existing integration fixtures built from two-case toy problems. The shared fixtures (`codeProblemJSON`, `doubleImport`, `sumImport`, the assess web fixture) now clear the floor and scoring assertions derive weights from the fixture. A SQL problem can never score the "two proven languages" points, so it must satisfy the other five rules to be attachable — T12's SQL seeds must be authored accordingly.

## N13: jsonForScript replacer is a no-op
`internal/web/assess/view.go`'s `jsonForScript` replaces "<" with "<" (intent was `<`). Harmless today because `json.Marshal` escapes it already, but the comment overstates the code. `internal/web/problems/island.go` has the correct version; fold the fix in when a task next touches that file.

## N14: Snapshot upload contract for the candidate island
The snapshot endpoint appends the I4 `snapshot` event server-side using the attempt's next event seq, so the recorder must resync its counter from `last_seq` (returned by `GET /attempts/{id}` and the events endpoint) after each upload; snapshot events carry no `problem_id`. Uploads require the attempt to be `started` and send CSRF as a header — the middleware checks the header before touching the multipart body. Default integrity weights now sum to 115; `Risk` still saturates at 100 and existing org weights were left alone deliberately.

## N15: Attempt Run/Submit double-prefixed their URL (fixed)
T15's browser suite found that `web/static/assess/src/main.ts` called `api.call` with a path from `executePath`, which already carries `/attempts/{id}`; `Api.call` prefixed it again, so Run and Submit in a real sitting POSTed `/assess/api/attempts/{id}/attempts/{id}/...` and 404'd. No candidate could run or submit code. Try mode was unaffected (it used `callBase`). Fixed at controller level by switching that branch to `api.callBase` and rebuilding the committed bundle; `mode.ts` and its unit test were already correct. Integration tests missed it because they call the service layer directly — only the browser exercises the island's URL construction.
