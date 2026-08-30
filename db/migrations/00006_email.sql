-- +goose Up

create table email_log (
    id          uuid primary key default gen_random_uuid(),
    org_id      uuid not null references org(id) on delete cascade,
    template    text not null,
    to_email    text not null,
    subject     text not null,
    status      text not null default 'queued' check (status in ('queued', 'sent', 'failed')),
    attempts    integer not null default 0,
    last_error  text,
    created_at  timestamptz not null default now(),
    sent_at     timestamptz
);
create index email_log_org_idx on email_log (org_id, created_at);

alter table email_log enable row level security;
alter table email_log force row level security;
create policy tenant on email_log for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());

grant select, insert, update, delete on all tables in schema public to app_rw;

-- +goose Down
drop table if exists email_log cascade;
