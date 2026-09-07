-- name: CreateOrg :one
insert into org (name, slug) values ($1, $2) returning *;

-- name: GetOrg :one
select * from org where id = $1;

-- name: GetOrgBySlug :one
select * from org where slug = $1;

-- name: CreateOrgUser :one
insert into org_user (org_id, email, name, timezone)
values ($1, $2, $3, $4) returning *;

-- name: UpsertOrgUserCredential :exec
insert into org_user_credential (org_user_id, org_id, password_hash) values ($1, $2, $3)
on conflict (org_user_id) do update set password_hash = excluded.password_hash, updated_at = now();

-- name: GetOrgUserCredential :one
select * from org_user_credential where org_user_id = $1;

-- name: GetOrgUser :one
select * from org_user where id = $1 and org_id = $2;

-- name: GetOrgUserByEmail :one
select * from org_user where org_id = $1 and lower(email) = lower($2);

-- name: LookupOrgUsersByEmail :many
select sqlc.embed(u), c.password_hash
from org_user u join org_user_credential c on c.org_user_id = u.id
where lower(u.email) = lower($1) and c.disabled_at is null;

-- name: ListOrgUsers :many
select * from org_user where org_id = $1 order by name;

-- name: AddOrgUserRole :exec
insert into org_user_role (org_user_id, org_id, role) values ($1, $2, $3) on conflict do nothing;

-- name: ListOrgUserRoles :many
select role from org_user_role where org_user_id = $1 order by role;

-- name: CreateClientCompany :one
insert into client_company (org_id, name, industry, shortlist_sla_days, brief)
values ($1, $2, $3, $4, $5) returning *;

-- name: ListClientCompanies :many
select * from client_company where org_id = $1 order by name;

-- name: CreateClientUser :one
insert into client_user (org_id, client_company_id, email, name, timezone)
values ($1, $2, $3, $4, $5) returning *;

-- name: UpsertClientUserCredential :exec
insert into client_user_credential (client_user_id, org_id, password_hash) values ($1, $2, $3)
on conflict (client_user_id) do update set password_hash = excluded.password_hash, updated_at = now();

-- name: LookupClientUsersByEmail :many
select sqlc.embed(u), c.password_hash
from client_user u join client_user_credential c on c.client_user_id = u.id
where lower(u.email) = lower($1) and c.disabled_at is null;

-- name: CreateSession :one
insert into session (org_id, org_user_id, client_user_id, token_hash, expires_at)
values ($1, $2, $3, $4, $5) returning *;

-- name: LookupSession :one
select * from session where token_hash = $1 and expires_at > now();

-- name: DeleteSession :exec
delete from session where token_hash = $1;

-- name: CreateMagicLink :one
insert into magic_link (org_id, token_hash, purpose, subject_id, expires_at)
values ($1, $2, $3, $4, $5) returning *;

-- name: LookupMagicLink :one
select * from magic_link where token_hash = $1;

-- name: MarkMagicLinkUsed :execrows
update magic_link set used_at = now()
where id = $1 and used_at is null and revoked_at is null and expires_at > now();

-- name: LookupAPIToken :one
select * from api_token
where token_hash = $1 and revoked_at is null
  and (expires_at is null or expires_at > now());

-- name: UpsertOrgSetting :exec
insert into org_setting (org_id, key, value) values ($1, $2, $3)
on conflict (org_id, key) do update set value = excluded.value, updated_at = now();

-- name: GetOrgSetting :one
select value from org_setting where org_id = $1 and key = $2;

-- name: ListOrgSettings :many
select * from org_setting where org_id = $1 order by key;

-- name: CreatePipelineTemplate :one
insert into pipeline_template (org_id, name, is_default) values ($1, $2, $3) returning *;

-- name: GetDefaultPipelineTemplate :one
select * from pipeline_template where org_id = $1 and is_default;

-- name: ListPipelineTemplates :many
-- The default first, so a picker that takes the head takes the org's default.
select * from pipeline_template where org_id = $1 order by is_default desc, name;

-- name: GetPipelineTemplate :one
select * from pipeline_template where id = $1 and org_id = $2;

-- name: CreatePipelineTemplateStage :one
insert into pipeline_template_stage (org_id, template_id, position, name, kind, unblind)
values ($1, $2, $3, $4, $5, $6) returning *;

-- name: ListPipelineTemplateStages :many
select * from pipeline_template_stage where template_id = $1 order by position;

-- name: GetClientCompany :one
select * from client_company where id = $1 and org_id = $2;

-- name: ListClientUsers :many
select * from client_user where org_id = $1 order by name;

-- name: DeleteOrgUserRoles :exec
delete from org_user_role where org_user_id = $1;

-- name: ListOrgUserRolesForOrg :many
select org_user_id, role from org_user_role where org_id = $1 order by org_user_id, role;
