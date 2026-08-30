-- +goose Up

create table talent_pool_entry (
    id                 uuid primary key default gen_random_uuid(),
    org_id             uuid not null references org(id) on delete cascade,
    candidate_id       uuid not null references candidate(id) on delete cascade,
    skills             text[] not null default '{}',
    seniority          text,
    location           text,
    remote_ok          boolean not null default false,
    best_scores        jsonb not null default '{}',
    scorecard_summary  text,
    notes              text,
    source_job_ids     uuid[] not null default '{}',
    created_at         timestamptz not null default now(),
    updated_at         timestamptz not null default now(),
    removed_at         timestamptz,
    unique (org_id, candidate_id)
);

alter table talent_pool_entry enable row level security;
alter table talent_pool_entry force row level security;
create policy tenant on talent_pool_entry for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());

grant select, insert, update, delete on all tables in schema public to app_rw;

-- +goose Down
drop table if exists talent_pool_entry cascade;
