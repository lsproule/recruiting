-- +goose Up

-- Tenant scoping is enforced with RLS. Every tenant table has org_id and a
-- policy comparing it to the app.org_id session setting, which the store sets
-- per transaction. Client users additionally set app.client_company_id.
--
-- Migrations run as the table owner (RLS does not apply to owners), while
-- serve/worker connect as app_rw, which owns nothing and cannot bypass RLS.
-- Point DATABASE_URL for those processes at app_rw, e.g.
--   postgres://app_rw:app_rw@localhost:5433/recruiting?sslmode=disable
-- and change the password with `alter role app_rw password '...'` outside dev.

-- +goose StatementBegin
do $$
begin
    if not exists (select 1 from pg_roles where rolname = 'app_rw') then
        create role app_rw login password 'app_rw' nobypassrls;
    end if;
end
$$;
-- +goose StatementEnd

grant usage on schema public to app_rw;
alter default privileges in schema public grant select, insert, update, delete on tables to app_rw;
alter default privileges in schema public grant usage, select on sequences to app_rw;
alter default privileges in schema public grant execute on functions to app_rw;

-- Null when the setting is absent or cleared, so a policy never matches.
create function app_org_id() returns uuid
    language sql stable parallel safe
    as $$ select nullif(current_setting('app.org_id', true), '')::uuid $$;

create function app_client_company_id() returns uuid
    language sql stable parallel safe
    as $$ select nullif(current_setting('app.client_company_id', true), '')::uuid $$;

create function app_is_client() returns boolean
    language sql stable parallel safe
    as $$ select app_client_company_id() is not null $$;

-- Credential lookup before a principal is known (login, session, magic link).
create function app_lookup_token() returns text
    language sql stable parallel safe
    as $$ select nullif(current_setting('app.lookup_token', true), '') $$;

create function app_lookup_email() returns text
    language sql stable parallel safe
    as $$ select lower(nullif(current_setting('app.lookup_email', true), '')) $$;

-- Owner of seed problems every tenant may read.
create function platform_org_id() returns uuid
    language sql immutable parallel safe
    as $$ select '00000000-0000-0000-0000-000000000001'::uuid $$;

create table org (
    id          uuid primary key default gen_random_uuid(),
    name        text not null,
    slug        text not null unique,
    created_at  timestamptz not null default now()
);
alter table org enable row level security;
alter table org force row level security;
create policy tenant on org for all
    using (id = app_org_id() and not app_is_client())
    with check (id = app_org_id() and not app_is_client());
create policy client_read on org for select
    using (id = app_org_id() and app_is_client());

insert into org (id, name, slug) values (platform_org_id(), 'Platform', 'platform');

create table org_setting (
    org_id      uuid not null references org(id) on delete cascade,
    key         text not null,
    value       jsonb not null,
    updated_at  timestamptz not null default now(),
    primary key (org_id, key)
);

create table org_user (
    id             uuid primary key default gen_random_uuid(),
    org_id         uuid not null references org(id) on delete cascade,
    email          text not null,
    name           text not null,
    timezone       text not null default 'UTC',
    created_at     timestamptz not null default now()
);
create unique index org_user_org_email_idx on org_user (org_id, lower(email));

create table org_user_role (
    org_user_id  uuid not null references org_user(id) on delete cascade,
    org_id       uuid not null references org(id) on delete cascade,
    role         text not null check (role in ('admin', 'recruiter', 'vetter')),
    primary key (org_user_id, role)
);
create index org_user_role_org_idx on org_user_role (org_id);

create table client_company (
    id          uuid primary key default gen_random_uuid(),
    org_id      uuid not null references org(id) on delete cascade,
    name        text not null,
    created_at  timestamptz not null default now()
);
create index client_company_org_idx on client_company (org_id);

create table client_user (
    id                 uuid primary key default gen_random_uuid(),
    org_id             uuid not null references org(id) on delete cascade,
    client_company_id  uuid not null references client_company(id) on delete cascade,
    email              text not null,
    name               text not null,
    timezone           text not null default 'UTC',
    created_at         timestamptz not null default now()
);
create unique index client_user_org_email_idx on client_user (org_id, lower(email));
create index client_user_company_idx on client_user (client_company_id);

-- Secrets live apart from the profile rows so client users can read names
-- for scorecards without ever seeing a hash.
create table org_user_credential (
    org_user_id    uuid primary key references org_user(id) on delete cascade,
    org_id         uuid not null references org(id) on delete cascade,
    password_hash  text not null,
    disabled_at    timestamptz,
    updated_at     timestamptz not null default now()
);

create table client_user_credential (
    client_user_id  uuid primary key references client_user(id) on delete cascade,
    org_id          uuid not null references org(id) on delete cascade,
    password_hash   text not null,
    disabled_at     timestamptz,
    updated_at      timestamptz not null default now()
);

create table session (
    id              uuid primary key default gen_random_uuid(),
    org_id          uuid not null references org(id) on delete cascade,
    org_user_id     uuid references org_user(id) on delete cascade,
    client_user_id  uuid references client_user(id) on delete cascade,
    token_hash      text not null unique,
    expires_at      timestamptz not null,
    created_at      timestamptz not null default now(),
    check ((org_user_id is null) <> (client_user_id is null))
);

create table magic_link (
    id          uuid primary key default gen_random_uuid(),
    org_id      uuid not null references org(id) on delete cascade,
    token_hash  text not null unique,
    purpose     text not null check (purpose in ('apply', 'book', 'assessment')),
    subject_id  uuid not null,
    expires_at  timestamptz not null,
    used_at     timestamptz,
    revoked_at  timestamptz,
    created_at  timestamptz not null default now()
);

create table api_token (
    id           uuid primary key default gen_random_uuid(),
    org_id       uuid not null references org(id) on delete cascade,
    org_user_id  uuid not null references org_user(id) on delete cascade,
    name         text not null,
    token_hash   text not null unique,
    created_at   timestamptz not null default now(),
    revoked_at   timestamptz
);

-- Org-internal tables: never visible to client users.
-- +goose StatementBegin
do $$
declare t text;
begin
    foreach t in array array['org_setting', 'org_user_role', 'api_token'] loop
        execute format('alter table %I enable row level security', t);
        execute format('alter table %I force row level security', t);
        execute format('create policy tenant on %I for all using (org_id = app_org_id() and not app_is_client()) with check (org_id = app_org_id() and not app_is_client())', t);
    end loop;
end
$$;
-- +goose StatementEnd

-- Client users may read their company and the org's users (names on scorecards).
alter table client_company enable row level security;
alter table client_company force row level security;
create policy tenant on client_company for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
create policy client_read on client_company for select
    using (org_id = app_org_id() and app_is_client() and id = app_client_company_id());

alter table org_user enable row level security;
alter table org_user force row level security;
create policy tenant on org_user for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
create policy client_read on org_user for select
    using (org_id = app_org_id() and app_is_client());
create policy lookup on org_user for select
    using (lower(email) = app_lookup_email());

alter table org_user_credential enable row level security;
alter table org_user_credential force row level security;
create policy tenant on org_user_credential for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
create policy lookup on org_user_credential for select
    using (exists (select 1 from org_user u where u.id = org_user_credential.org_user_id and lower(u.email) = app_lookup_email()));

alter table client_user enable row level security;
alter table client_user force row level security;
create policy tenant on client_user for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
create policy client_read on client_user for select
    using (org_id = app_org_id() and app_is_client() and client_company_id = app_client_company_id());
create policy lookup on client_user for select
    using (lower(email) = app_lookup_email());

alter table client_user_credential enable row level security;
alter table client_user_credential force row level security;
create policy tenant on client_user_credential for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
create policy lookup on client_user_credential for select
    using (exists (select 1 from client_user u where u.id = client_user_credential.client_user_id and lower(u.email) = app_lookup_email()));

-- Token tables: tenant reads plus lookup by token hash before the org is known.
-- Clients may only create sessions for client users of their own company.
alter table session enable row level security;
alter table session force row level security;
create policy tenant on session for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
-- Clients manage sessions of their own company's users only (login/logout).
create policy client_own on session for all
    using (org_id = app_org_id() and app_is_client() and client_user_id is not null
        and exists (select 1 from client_user u where u.id = session.client_user_id and u.client_company_id = app_client_company_id()))
    with check (org_id = app_org_id() and app_is_client() and client_user_id is not null
        and exists (select 1 from client_user u where u.id = session.client_user_id and u.client_company_id = app_client_company_id()));
create policy lookup on session for select using (token_hash = app_lookup_token());

alter table magic_link enable row level security;
alter table magic_link force row level security;
create policy tenant on magic_link for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
create policy lookup on magic_link for select using (token_hash = app_lookup_token());
create policy lookup on api_token for select using (token_hash = app_lookup_token());

grant select, insert, update, delete on all tables in schema public to app_rw;
grant execute on all functions in schema public to app_rw;

-- +goose Down
drop table if exists api_token, magic_link, session, client_user_credential, org_user_credential, client_user, client_company, org_user_role, org_user, org_setting, org cascade;
drop function if exists platform_org_id, app_lookup_email, app_lookup_token, app_is_client, app_client_company_id, app_org_id;
alter default privileges in schema public revoke select, insert, update, delete on tables from app_rw;
alter default privileges in schema public revoke usage, select on sequences from app_rw;
alter default privileges in schema public revoke execute on functions from app_rw;
revoke usage on schema public from app_rw;
-- +goose StatementBegin
do $$
begin
    if exists (select 1 from pg_roles where rolname = 'app_rw') then
        -- Objects owned by or granted to app_rw in THIS database go first.
        execute 'drop owned by app_rw';
        -- The role itself is cluster-wide, so another database on the same
        -- cluster (a developer's alongside the test suite's, say) may still
        -- carry grants for it. Dropping it then fails, and that is not this
        -- database's migration to force: leave the role in place and let the
        -- last database holding it clean it up.
        begin
            execute 'drop role app_rw';
        exception when dependent_objects_still_exist then
            raise notice 'app_rw kept: another database still grants to it';
        end;
    end if;
end
$$;
-- +goose StatementEnd
