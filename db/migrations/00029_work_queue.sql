-- +goose Up

-- The work queue itself is a read model over attempts, events, packets and
-- slots — there is no queue table. What has to be stored is the recruiter's
-- "not now": a snooze hides one item from one user until it lapses, and
-- everyone else still sees the work.
create table queue_snooze (
    id         uuid primary key default gen_random_uuid(),
    org_id     uuid not null references org(id) on delete cascade,
    user_id    uuid not null references org_user(id) on delete cascade,
    kind       text not null,
    subject_id uuid not null,
    until      timestamptz not null,
    created_at timestamptz not null default now(),
    unique (user_id, kind, subject_id)
);
create index queue_snooze_user_idx on queue_snooze (user_id, until);

alter table queue_snooze enable row level security;
alter table queue_snooze force row level security;
create policy tenant on queue_snooze for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
grant select, insert, update, delete on queue_snooze to app_rw;

-- +goose Down
drop table if exists queue_snooze;
