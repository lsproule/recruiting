-- +goose Up

-- The prefix is the leading characters of the secret, kept so an admin can
-- tell one live token from another; the rest of the secret exists only as the
-- hash, and is shown once when the token is issued.
alter table api_token add column prefix text not null default '';

-- Expiry is optional: a token without one lives until it is revoked.
alter table api_token add column expires_at timestamptz;

-- A token belongs to one user, org or client. A client user's token reaches
-- only the portal reads, which the API enforces from the resolved principal.
alter table api_token add column client_user_id uuid references client_user(id) on delete cascade;
alter table api_token alter column org_user_id drop not null;
alter table api_token add constraint api_token_one_subject
    check (num_nonnulls(org_user_id, client_user_id) = 1);

-- +goose Down
alter table api_token drop constraint if exists api_token_one_subject;
delete from api_token where org_user_id is null;
alter table api_token alter column org_user_id set not null;
alter table api_token drop column if exists client_user_id;
alter table api_token drop column if exists expires_at;
alter table api_token drop column if exists prefix;
