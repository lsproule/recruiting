-- +goose Up

-- Two more candidate-facing links: a talent-network member's own profile
-- page, and the one-click answer to an opportunity a recruiter sends.
alter table magic_link drop constraint magic_link_purpose_check;
alter table magic_link add constraint magic_link_purpose_check
    check (purpose in ('apply', 'book', 'assessment', 'sprint', 'profile', 'opportunity'));

-- A person who joined the org's talent network: what they do, what they
-- want, and their standing consent to be approached about roles that fit.
-- One profile per candidate; the résumé lives on the candidate like any
-- applicant's. Withdrawing keeps the row (so a returning member finds their
-- details) but takes it out of every match.
create table talent_profile (
    id             uuid primary key default gen_random_uuid(),
    org_id         uuid not null references org(id) on delete cascade,
    candidate_id   uuid not null references candidate(id) on delete cascade,
    headline       text not null default '',
    skills         text[] not null default '{}',
    seniority      text check (seniority in ('junior', 'mid', 'senior', 'staff')),
    roles          text[] not null default '{}',
    location       text,
    remote_policy  text check (remote_policy in ('remote', 'hybrid', 'onsite')),
    salary_min     integer check (salary_min is null or salary_min >= 0),
    available_from date,
    consent_at     timestamptz not null default now(),
    withdrawn_at   timestamptz,
    created_at     timestamptz not null default now(),
    updated_at     timestamptz not null default now(),
    unique (org_id, candidate_id)
);
create index talent_profile_live_idx on talent_profile (org_id) where withdrawn_at is null;

alter table talent_profile enable row level security;
alter table talent_profile force row level security;
create policy tenant on talent_profile for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());

-- What a client company is looking for, outside any job it has opened. The
-- company reads anonymised matches against it and asks for introductions;
-- the recruiter makes them. job_id, when set, is the job an accepted
-- introduction opens an application on.
create table talent_request (
    id                 uuid primary key default gen_random_uuid(),
    org_id             uuid not null references org(id) on delete cascade,
    client_company_id  uuid not null references client_company(id) on delete cascade,
    client_user_id     uuid references client_user(id) on delete set null,
    job_id             uuid references job(id) on delete set null,
    title              text not null,
    skills             text[] not null default '{}',
    seniority          text check (seniority in ('junior', 'mid', 'senior', 'staff')),
    location           text,
    remote_policy      text check (remote_policy in ('remote', 'hybrid', 'onsite')),
    note               text not null default '',
    status             text not null default 'open' check (status in ('open', 'closed')),
    created_at         timestamptz not null default now(),
    updated_at         timestamptz not null default now()
);
create index talent_request_company_idx on talent_request (client_company_id, created_at desc);

alter table talent_request enable row level security;
alter table talent_request force row level security;
create policy tenant on talent_request for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
create policy client_own on talent_request for all
    using (org_id = app_org_id() and app_is_client() and client_company_id = app_client_company_id())
    with check (org_id = app_org_id() and app_is_client() and client_company_id = app_client_company_id());

-- One introduction: the company asked to meet a match, the recruiter sent
-- the person the opportunity, and the person answered. The candidate stays
-- unnamed to the company until they accept, at which point the application
-- the acceptance opens is released to them like any other.
create table talent_intro (
    id              uuid primary key default gen_random_uuid(),
    org_id          uuid not null references org(id) on delete cascade,
    request_id      uuid not null references talent_request(id) on delete cascade,
    candidate_id    uuid not null references candidate(id) on delete cascade,
    source          text not null check (source in ('network', 'pool')),
    score           real not null default 0,
    status          text not null default 'requested'
                    check (status in ('requested', 'sent', 'accepted', 'declined', 'dismissed')),
    job_id          uuid references job(id) on delete set null,
    application_id  uuid references application(id) on delete set null,
    sent_by         uuid references org_user(id) on delete set null,
    requested_at    timestamptz not null default now(),
    sent_at         timestamptz,
    answered_at     timestamptz,
    unique (request_id, candidate_id)
);
create index talent_intro_request_idx on talent_intro (request_id, requested_at);
create index talent_intro_waiting_idx on talent_intro (org_id) where status = 'requested';

alter table talent_intro enable row level security;
alter table talent_intro force row level security;
create policy tenant on talent_intro for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
-- A company reads its own introductions; it never writes one directly (the
-- service does, after checking the match is one it was shown).
create policy client_read on talent_intro for select
    using (org_id = app_org_id() and app_is_client()
        and exists (select 1 from talent_request r where r.id = talent_intro.request_id));

grant select, insert, update, delete on all tables in schema public to app_rw;

-- +goose Down
drop table if exists talent_intro;
drop table if exists talent_request;
drop table if exists talent_profile;
alter table magic_link drop constraint magic_link_purpose_check;
alter table magic_link add constraint magic_link_purpose_check
    check (purpose in ('apply', 'book', 'assessment', 'sprint'));
