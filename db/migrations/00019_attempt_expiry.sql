-- +goose Up

-- One live attempt per application and stage: a redelivered invite cannot
-- create a second even under concurrent workers.
create unique index attempt_live_idx on attempt (application_id, stage_id) where status in ('invited', 'started');

-- Replay and signals read one problem's events at a time.
create index attempt_event_problem_idx on attempt_event (problem_id);

-- The worker sweeps overdue attempts across every org, but app_rw sees no
-- row without an org set. This returns only the org ids that have work, so
-- the sweep can enter each one under its own scope; nothing else escapes.
-- +goose StatementBegin
create function due_attempt_orgs(at timestamptz) returns setof uuid
language sql security definer set search_path = public as $$
    select distinct org_id from attempt where status = 'started' and expires_at <= at
$$;
-- +goose StatementEnd
grant execute on function due_attempt_orgs(timestamptz) to app_rw;

-- +goose Down
drop function if exists due_attempt_orgs(timestamptz);
drop index if exists attempt_event_problem_idx;
drop index if exists attempt_live_idx;
