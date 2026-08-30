-- +goose Up

-- Password reset tokens are looked up by hash before the org is known, like
-- sessions and magic links; consuming one is done under the org's scope.
create table password_reset (
    id              uuid primary key default gen_random_uuid(),
    org_id          uuid not null references org(id) on delete cascade,
    org_user_id     uuid references org_user(id) on delete cascade,
    client_user_id  uuid references client_user(id) on delete cascade,
    token_hash      text not null unique,
    expires_at      timestamptz not null,
    used_at         timestamptz,
    created_at      timestamptz not null default now(),
    check ((org_user_id is null) <> (client_user_id is null))
);
alter table password_reset enable row level security;
alter table password_reset force row level security;
create policy tenant on password_reset for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
create policy lookup on password_reset for select using (token_hash = app_lookup_token());

grant select, insert, update, delete on password_reset to app_rw;

-- +goose Down
drop table if exists password_reset;
