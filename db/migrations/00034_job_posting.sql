-- +goose Up

-- A job posting is one copy of a job placed on an external board (LinkedIn,
-- Indeed, or the local demo board) by the browser automation, so applicants
-- land on the platform's own apply page and their résumés in the org's
-- candidate database. The row is the record: what was posted where, with
-- which copy, and what the board answered.
create table job_posting (
    id            uuid primary key default gen_random_uuid(),
    org_id        uuid not null references org(id) on delete cascade,
    job_id        uuid not null references job(id) on delete cascade,
    board         text not null,
    status        text not null default 'queued' check (status in ('queued', 'posting', 'posted', 'failed', 'removed')),
    title         text not null,
    body          text not null,
    apply_url     text not null,
    external_url  text,
    external_id   text,
    error         text,
    attempts      integer not null default 0,
    posted_at     timestamptz,
    created_by    uuid references org_user(id) on delete set null,
    created_at    timestamptz not null default now(),
    updated_at    timestamptz not null default now()
);
create index job_posting_job_idx on job_posting (job_id, created_at desc);

alter table job_posting enable row level security;
alter table job_posting force row level security;
create policy job_posting_org on job_posting
    using (org_id = current_setting('app.org_id', true)::uuid)
    with check (org_id = current_setting('app.org_id', true)::uuid);
grant select, insert, update, delete on job_posting to app_rw;

-- +goose Down
drop table if exists job_posting;
