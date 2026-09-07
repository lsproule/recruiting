-- +goose Up

-- What the intake asks about a client beyond its name: the sector the brief
-- has to speak to, how long the client waits for a shortlist, and the
-- client's own words, which the brief is written from.
alter table client_company
    add column industry text not null default '',
    add column shortlist_sla_days integer,
    add column brief text not null default '';

-- An intake in progress. The wizard writes one row per recruiter and reads
-- it back on the step they left off at, so closing the tab costs nothing;
-- the row is deleted the moment the intake creates its four records.
create table intake_draft (
    id         uuid primary key default gen_random_uuid(),
    org_id     uuid not null references org(id) on delete cascade,
    created_by uuid not null references org_user(id) on delete cascade,
    step       integer not null default 1,
    payload    jsonb not null default '{}'::jsonb,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

-- One open draft per recruiter: two half-finished intakes in the same head
-- is how a client company gets created twice.
create unique index intake_draft_creator_idx on intake_draft (created_by);

alter table intake_draft enable row level security;
alter table intake_draft force row level security;
create policy tenant on intake_draft for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
grant select, insert, update, delete on intake_draft to app_rw;

-- +goose Down
drop table if exists intake_draft;
alter table client_company
    drop column if exists industry,
    drop column if exists shortlist_sla_days,
    drop column if exists brief;
