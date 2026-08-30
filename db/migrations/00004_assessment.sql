-- +goose Up

create table problem (
    id                  uuid primary key default gen_random_uuid(),
    org_id              uuid not null references org(id) on delete cascade,
    kind                text not null check (kind in ('code', 'sql')),
    title               text not null,
    statement           text not null,
    difficulty          text not null default 'medium' check (difficulty in ('easy', 'medium', 'hard')),
    tags                text[] not null default '{}',
    allowed_languages   text[] not null default '{}',
    time_limit_ms       integer not null default 2000,
    memory_limit_kb     integer not null default 262144,
    reference_language  text,
    reference_solution  text,
    sql_schema          text,
    sql_seed            text,
    created_at          timestamptz not null default now(),
    updated_at          timestamptz not null default now()
);
create index problem_org_idx on problem (org_id);

create table test_case (
    id               uuid primary key default gen_random_uuid(),
    org_id           uuid not null references org(id) on delete cascade,
    problem_id       uuid not null references problem(id) on delete cascade,
    position         integer not null,
    input            text not null default '',
    expected_output  text not null,
    visibility       text not null check (visibility in ('public', 'hidden')),
    weight           numeric not null default 1,
    unique (problem_id, position)
);

create table assessment (
    id                  uuid primary key default gen_random_uuid(),
    org_id              uuid not null references org(id) on delete cascade,
    name                text not null,
    duration_minutes    integer not null,
    language_override   text,
    invite_window_days  integer not null default 7,
    created_at          timestamptz not null default now()
);

create table assessment_problem (
    assessment_id  uuid not null references assessment(id) on delete cascade,
    org_id         uuid not null references org(id) on delete cascade,
    problem_id     uuid not null references problem(id) on delete restrict,
    position       integer not null,
    primary key (assessment_id, problem_id),
    unique (assessment_id, position)
);

alter table stage add column assessment_id uuid references assessment(id) on delete set null;

create table attempt (
    id                   uuid primary key default gen_random_uuid(),
    org_id               uuid not null references org(id) on delete cascade,
    application_id       uuid not null references application(id) on delete cascade,
    assessment_id        uuid not null references assessment(id) on delete restrict,
    stage_id             uuid not null references stage(id) on delete cascade,
    status               text not null default 'invited' check (status in ('invited', 'started', 'submitted', 'expired', 'scored', 'reviewed')),
    invited_at           timestamptz not null default now(),
    invite_expires_at    timestamptz,
    started_at           timestamptz,
    expires_at           timestamptz,
    finished_at          timestamptz,
    score                numeric,
    risk_score           numeric,
    recording_status     text not null default 'pending' check (recording_status in ('pending', 'complete', 'incomplete')),
    recording_blob_key   text,
    created_at           timestamptz not null default now(),
    updated_at           timestamptz not null default now()
);
create index attempt_application_idx on attempt (application_id);

create table submission (
    id          uuid primary key default gen_random_uuid(),
    org_id      uuid not null references org(id) on delete cascade,
    attempt_id  uuid not null references attempt(id) on delete cascade,
    problem_id  uuid not null references problem(id) on delete restrict,
    kind        text not null check (kind in ('run', 'submit')),
    language    text not null,
    source      text not null,
    status      text not null default 'queued' check (status in ('queued', 'running', 'done', 'error')),
    result      jsonb,
    score       numeric,
    created_at  timestamptz not null default now(),
    updated_at  timestamptz not null default now()
);
create index submission_attempt_idx on submission (attempt_id, created_at);

create table attempt_event (
    id          uuid primary key default gen_random_uuid(),
    org_id      uuid not null references org(id) on delete cascade,
    attempt_id  uuid not null references attempt(id) on delete cascade,
    seq         bigint not null,
    kind        text not null check (kind in ('edit', 'paste', 'focus', 'blur', 'run', 'submit', 'lang_change')),
    payload     jsonb not null,
    client_ts   timestamptz,
    server_ts   timestamptz not null default now(),
    unique (attempt_id, seq)
);

create table integrity_signal (
    id          uuid primary key default gen_random_uuid(),
    org_id      uuid not null references org(id) on delete cascade,
    attempt_id  uuid not null references attempt(id) on delete cascade,
    name        text not null,
    value       numeric not null,
    weight      numeric not null,
    confidence  text not null default 'normal' check (confidence in ('low', 'normal')),
    evidence    jsonb not null default '[]',
    created_at  timestamptz not null default now(),
    unique (attempt_id, name)
);

create table review (
    id          uuid primary key default gen_random_uuid(),
    org_id      uuid not null references org(id) on delete cascade,
    attempt_id  uuid not null references attempt(id) on delete cascade,
    vetter_id   uuid not null references org_user(id) on delete restrict,
    verdict     text not null check (verdict in ('pass', 'borderline', 'fail')),
    notes       text,
    created_at  timestamptz not null default now(),
    unique (attempt_id)
);

-- Problem bank: platform seed rows readable by every org, writable by none.
alter table problem enable row level security;
alter table problem force row level security;
create policy tenant on problem for all
    using (not app_is_client() and org_id = app_org_id())
    with check (not app_is_client() and org_id = app_org_id());
create policy platform_read on problem for select
    using (not app_is_client() and org_id = platform_org_id());

alter table test_case enable row level security;
alter table test_case force row level security;
create policy tenant on test_case for all
    using (not app_is_client() and org_id = app_org_id())
    with check (not app_is_client() and org_id = app_org_id());
create policy platform_read on test_case for select
    using (not app_is_client() and org_id = platform_org_id());

-- Org-only: raw submissions, recordings, and signals never reach clients.
-- +goose StatementBegin
do $$
declare t text;
begin
    foreach t in array array['assessment', 'assessment_problem', 'submission', 'attempt_event', 'integrity_signal'] loop
        execute format('alter table %I enable row level security', t);
        execute format('alter table %I force row level security', t);
        execute format('create policy tenant on %I for all using (org_id = app_org_id() and not app_is_client()) with check (org_id = app_org_id() and not app_is_client())', t);
    end loop;
end
$$;
-- +goose StatementEnd

-- Clients see the attempt score and the vetter verdict of released applications.
alter table attempt enable row level security;
alter table attempt force row level security;
create policy tenant on attempt for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
create policy client_read on attempt for select
    using (org_id = app_org_id() and app_is_client() and exists (select 1 from application a where a.id = attempt.application_id));

alter table review enable row level security;
alter table review force row level security;
create policy tenant on review for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
create policy client_read on review for select
    using (org_id = app_org_id() and app_is_client() and exists (select 1 from attempt t where t.id = review.attempt_id));

grant select, insert, update, delete on all tables in schema public to app_rw;

-- +goose Down
drop table if exists review, integrity_signal, attempt_event, submission, attempt cascade;
alter table stage drop column if exists assessment_id;
drop table if exists assessment_problem, assessment, test_case, problem cascade;
