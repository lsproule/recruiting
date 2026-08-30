# Notes

## N1: Repository is not a git work tree
No git repository exists; run state falls back into the plan folder. No branch is created by the plan. Initialising git is the user's call.

## N2: Queue tables are exempt from the RLS invariant
The Postgres-backed queue's own tables are infrastructure, not domain tables; they carry no `org_id` and are excluded from the "every domain table has RLS" constraint. Payloads carry `org_id` where a handler needs a tenant-scoped transaction.
