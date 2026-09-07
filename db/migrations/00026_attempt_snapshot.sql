-- +goose Up

-- The webcam frames a sitting produced. The bytes live in object storage;
-- the row is what the retention sweep and the reviewer's gallery read. seq
-- is the beat the session assigned, so a frame uploaded twice is one row.
create table attempt_snapshot (
    id         uuid primary key default gen_random_uuid(),
    org_id     uuid not null references org(id) on delete cascade,
    attempt_id uuid not null references attempt(id) on delete cascade,
    seq        integer not null,
    taken_at   timestamptz not null default now(),
    blob_key   text not null,
    bytes      integer not null,
    unique (attempt_id, seq)
);

alter table attempt_snapshot enable row level security;
alter table attempt_snapshot force row level security;
create policy tenant on attempt_snapshot for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
grant select, insert, update, delete on attempt_snapshot to app_rw;

-- The sweep walks the oldest frames first, within an org and across orgs.
create index attempt_snapshot_taken_at_idx on attempt_snapshot (taken_at);

-- Retention is per org, so the sweep has to enter each org under its own
-- scope; app_rw sees no row without one set. This returns only the org ids
-- holding frames older than the shortest retention anyone may configure.
-- +goose StatementBegin
create function snapshot_orgs(before timestamptz) returns setof uuid
language sql security definer set search_path = public as $$
    select distinct org_id from attempt_snapshot where taken_at < before
$$;
-- +goose StatementEnd
grant execute on function snapshot_orgs(timestamptz) to app_rw;

-- +goose Down
drop function if exists snapshot_orgs(timestamptz);
drop table if exists attempt_snapshot;
