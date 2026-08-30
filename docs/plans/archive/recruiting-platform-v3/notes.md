# Notes

## N1: Repository is not a git work tree
No git repository exists; run state falls back into the plan folder. No branch is created by the plan. Initialising git is the user's call.

## N2: Queue tables are exempt from the RLS invariant
The Postgres-backed queue's own tables are infrastructure, not domain tables; they carry no `org_id` and are excluded from the "every domain table has RLS" constraint. Payloads carry `org_id` where a handler needs a tenant-scoped transaction.

## N3: Mode wiring deferred
`cmd/recruiting/modes.go` stubs (`runServe`, `runAdmin`, `runWorker`, `runMigrate`) were not wired by T03/T04 because `modes.go` was outside their scope. `RunAdmin` exists in `cmd/recruiting/admin.go`; web packages expose `Mount` functions. Wiring the real server composition root and CLI dispatch must happen in the first task whose scope includes `cmd/recruiting/**` (T20 at the latest; T07 for the worker mode).
