-- +goose Up

create table availability_rule (
    id              uuid primary key default gen_random_uuid(),
    org_id          uuid not null references org(id) on delete cascade,
    vetter_id       uuid not null references org_user(id) on delete cascade,
    weekday         smallint not null check (weekday between 0 and 6),
    start_time      time not null,
    end_time        time not null,
    timezone        text not null,
    buffer_minutes  integer not null default 0,
    slot_minutes    integer not null default 30,
    valid_from      date,
    valid_to        date,
    created_at      timestamptz not null default now(),
    check (end_time > start_time)
);
create index availability_rule_vetter_idx on availability_rule (vetter_id);

create table availability_exception (
    id          uuid primary key default gen_random_uuid(),
    org_id      uuid not null references org(id) on delete cascade,
    vetter_id   uuid not null references org_user(id) on delete cascade,
    starts_at   timestamptz not null,
    ends_at     timestamptz not null,
    reason      text,
    check (ends_at > starts_at)
);
create index availability_exception_vetter_idx on availability_exception (vetter_id, starts_at);

create table interview_slot (
    id                  uuid primary key default gen_random_uuid(),
    org_id              uuid not null references org(id) on delete cascade,
    vetter_id           uuid not null references org_user(id) on delete restrict,
    application_id      uuid references application(id) on delete set null,
    stage_id            uuid references stage(id) on delete set null,
    candidate_timezone  text,
    starts_at           timestamptz not null,
    ends_at             timestamptz not null,
    status              text not null default 'booked' check (status in ('booked', 'cancelled', 'completed', 'no_show')),
    created_at          timestamptz not null default now(),
    updated_at          timestamptz not null default now(),
    unique (vetter_id, starts_at),
    check (ends_at > starts_at)
);
create index interview_slot_application_idx on interview_slot (application_id);

-- +goose StatementBegin
do $$
declare t text;
begin
    foreach t in array array['availability_rule', 'availability_exception', 'interview_slot'] loop
        execute format('alter table %I enable row level security', t);
        execute format('alter table %I force row level security', t);
        execute format('create policy tenant on %I for all using (org_id = app_org_id() and not app_is_client()) with check (org_id = app_org_id() and not app_is_client())', t);
    end loop;
end
$$;
-- +goose StatementEnd

grant select, insert, update, delete on all tables in schema public to app_rw;

-- +goose Down
drop table if exists interview_slot, availability_exception, availability_rule cascade;
