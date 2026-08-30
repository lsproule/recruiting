-- +goose Up

-- The candidate's editor syncs its text as they work so an attempt that runs
-- out of time can be submitted with what they had, not with nothing.
create table attempt_source (
    attempt_id  uuid not null references attempt(id) on delete cascade,
    org_id      uuid not null references org(id) on delete cascade,
    problem_id  uuid not null references problem(id) on delete restrict,
    language    text not null,
    source      text not null,
    updated_at  timestamptz not null default now(),
    primary key (attempt_id, problem_id)
);

alter table attempt_source enable row level security;
alter table attempt_source force row level security;
create policy tenant on attempt_source for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
grant select, insert, update, delete on attempt_source to app_rw;

-- The highest event seq accepted so far: ingest refuses anything at or below
-- it under the attempt's row lock, and finalize compares it to the stored
-- count to spot gaps.
alter table attempt add column last_event_seq bigint not null default 0;

-- Which problem an event belongs to is queried by replay and signals.
alter table attempt_event add column problem_id uuid references problem(id) on delete restrict;

alter table assessment add column updated_at timestamptz not null default now();

-- +goose Down
alter table assessment drop column if exists updated_at;
alter table attempt_event drop column if exists problem_id;
alter table attempt drop column if exists last_event_seq;
drop table if exists attempt_source;
