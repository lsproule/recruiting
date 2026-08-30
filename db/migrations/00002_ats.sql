-- +goose Up

create table candidate (
    id          uuid primary key default gen_random_uuid(),
    org_id      uuid not null references org(id) on delete cascade,
    email       text not null,
    name        text not null,
    phone       text,
    links       jsonb not null default '[]',
    created_at  timestamptz not null default now(),
    updated_at  timestamptz not null default now()
);
create unique index candidate_org_email_idx on candidate (org_id, lower(email));

create table resume (
    id                 uuid primary key default gen_random_uuid(),
    org_id             uuid not null references org(id) on delete cascade,
    candidate_id       uuid not null references candidate(id) on delete cascade,
    blob_key           text not null,
    filename           text not null,
    content_type       text not null,
    size_bytes         bigint not null,
    extracted_text     text,
    extraction_failed  boolean not null default false,
    created_at         timestamptz not null default now()
);
create index resume_candidate_idx on resume (candidate_id);
create index resume_text_idx on resume using gin (to_tsvector('english', coalesce(extracted_text, '')));

create table pipeline_template (
    id          uuid primary key default gen_random_uuid(),
    org_id      uuid not null references org(id) on delete cascade,
    name        text not null,
    is_default  boolean not null default false,
    created_at  timestamptz not null default now()
);
create unique index pipeline_template_default_idx on pipeline_template (org_id) where is_default;

create table pipeline_template_stage (
    id           uuid primary key default gen_random_uuid(),
    org_id       uuid not null references org(id) on delete cascade,
    template_id  uuid not null references pipeline_template(id) on delete cascade,
    position     integer not null,
    name         text not null,
    kind         text not null check (kind in ('generic', 'interview', 'assessment', 'client_review', 'terminal')),
    unblind      boolean not null default false,
    unique (template_id, position)
);

create table scorecard_rubric (
    id          uuid primary key default gen_random_uuid(),
    org_id      uuid not null references org(id) on delete cascade,
    name        text not null,
    criteria    jsonb not null default '[]',
    created_at  timestamptz not null default now()
);

create table job (
    id                 uuid primary key default gen_random_uuid(),
    org_id             uuid not null references org(id) on delete cascade,
    client_company_id  uuid not null references client_company(id) on delete restrict,
    title              text not null,
    description        text not null default '',
    skills             text[] not null default '{}',
    seniority          text,
    location           text,
    remote_policy      text,
    salary_min         integer,
    salary_max         integer,
    blind_mode         boolean not null default false,
    status             text not null default 'open' check (status in ('draft', 'open', 'closed')),
    created_by         uuid references org_user(id) on delete set null,
    created_at         timestamptz not null default now(),
    updated_at         timestamptz not null default now()
);
create index job_org_idx on job (org_id);
create index job_company_idx on job (client_company_id);

create table stage (
    id                   uuid primary key default gen_random_uuid(),
    org_id               uuid not null references org(id) on delete cascade,
    job_id               uuid not null references job(id) on delete cascade,
    position             integer not null,
    name                 text not null,
    kind                 text not null check (kind in ('generic', 'interview', 'assessment', 'client_review', 'terminal')),
    unblind              boolean not null default false,
    scorecard_rubric_id  uuid references scorecard_rubric(id) on delete set null,
    default_vetter_id    uuid references org_user(id) on delete set null,
    unique (job_id, position) deferrable initially immediate
);

create table application (
    id                 uuid primary key default gen_random_uuid(),
    org_id             uuid not null references org(id) on delete cascade,
    job_id             uuid not null references job(id) on delete cascade,
    candidate_id       uuid not null references candidate(id) on delete cascade,
    -- Denormalised from job so client RLS policies need no join.
    client_company_id  uuid not null references client_company(id) on delete restrict,
    stage_id           uuid not null references stage(id) on delete restrict,
    status             text not null default 'active' check (status in ('active', 'hired', 'rejected', 'withdrawn')),
    released_at        timestamptz,
    high_quality       boolean not null default false,
    recruiter_summary  text,
    screening_answers  jsonb not null default '{}',
    created_at         timestamptz not null default now(),
    updated_at         timestamptz not null default now(),
    unique (job_id, candidate_id)
);
create index application_candidate_idx on application (candidate_id);
create index application_stage_idx on application (stage_id);

create table application_event (
    id              uuid primary key default gen_random_uuid(),
    org_id          uuid not null references org(id) on delete cascade,
    application_id  uuid not null references application(id) on delete cascade,
    actor_kind      text not null check (actor_kind in ('org_user', 'client_user', 'candidate', 'system')),
    actor_id        uuid,
    kind            text not null,
    from_stage_id   uuid references stage(id) on delete set null,
    to_stage_id     uuid references stage(id) on delete set null,
    reason          text,
    payload         jsonb not null default '{}',
    created_at      timestamptz not null default now()
);
create index application_event_application_idx on application_event (application_id, created_at);

create table scorecard (
    id              uuid primary key default gen_random_uuid(),
    org_id          uuid not null references org(id) on delete cascade,
    application_id  uuid not null references application(id) on delete cascade,
    stage_id        uuid not null references stage(id) on delete cascade,
    vetter_id       uuid not null references org_user(id) on delete restrict,
    rubric_id       uuid references scorecard_rubric(id) on delete set null,
    scores          jsonb not null default '[]',
    overall         text not null check (overall in ('strong_yes', 'yes', 'no', 'strong_no')),
    notes           text,
    no_show         boolean not null default false,
    created_at      timestamptz not null default now(),
    unique (application_id, stage_id)
);

-- Org-only tables.
-- +goose StatementBegin
do $$
declare t text;
begin
    foreach t in array array['pipeline_template', 'pipeline_template_stage', 'scorecard_rubric'] loop
        execute format('alter table %I enable row level security', t);
        execute format('alter table %I force row level security', t);
        execute format('create policy tenant on %I for all using (org_id = app_org_id() and not app_is_client()) with check (org_id = app_org_id() and not app_is_client())', t);
    end loop;
end
$$;
-- +goose StatementEnd

-- Client users: jobs of their company; applications released to them; and
-- rows hanging off a visible application (the subquery is itself RLS-filtered).
alter table job enable row level security;
alter table job force row level security;
create policy tenant on job for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
create policy client_read on job for select
    using (org_id = app_org_id() and app_is_client() and client_company_id = app_client_company_id());

alter table stage enable row level security;
alter table stage force row level security;
create policy tenant on stage for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
create policy client_read on stage for select
    using (org_id = app_org_id() and app_is_client() and exists (select 1 from job j where j.id = stage.job_id));

alter table application enable row level security;
alter table application force row level security;
create policy tenant on application for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
-- Clients read released rows only; moves happen in service code as the org.
create policy client_read on application for select
    using (org_id = app_org_id() and app_is_client() and client_company_id = app_client_company_id() and released_at is not null);

alter table candidate enable row level security;
alter table candidate force row level security;
create policy tenant on candidate for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
create policy client_read on candidate for select
    using (org_id = app_org_id() and app_is_client() and exists (select 1 from application a where a.candidate_id = candidate.id));

alter table resume enable row level security;
alter table resume force row level security;
create policy tenant on resume for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
create policy client_read on resume for select
    using (org_id = app_org_id() and app_is_client() and exists (select 1 from application a where a.candidate_id = resume.candidate_id));

alter table application_event enable row level security;
alter table application_event force row level security;
create policy tenant on application_event for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
create policy client_read on application_event for select
    using (org_id = app_org_id() and app_is_client() and exists (select 1 from application a where a.id = application_event.application_id));
create policy client_append on application_event for insert
    with check (org_id = app_org_id() and app_is_client() and actor_kind = 'client_user' and exists (select 1 from application a where a.id = application_event.application_id));

alter table scorecard enable row level security;
alter table scorecard force row level security;
create policy tenant on scorecard for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
create policy client_read on scorecard for select
    using (org_id = app_org_id() and app_is_client() and exists (select 1 from application a where a.id = scorecard.application_id));

grant select, insert, update, delete on all tables in schema public to app_rw;

-- +goose Down
drop table if exists scorecard, application_event, application, stage, job, scorecard_rubric, pipeline_template_stage, pipeline_template, resume, candidate cascade;
