-- name: TouchSession :exec
update session set expires_at = $2 where id = $1;

-- name: DeleteSessionByID :exec
delete from session where id = $1;

-- name: GetClientUser :one
select * from client_user where id = $1;

-- name: RevokeMagicLink :exec
update magic_link set revoked_at = now() where id = $1 and revoked_at is null;

-- name: CreatePasswordReset :one
insert into password_reset (org_id, org_user_id, client_user_id, token_hash, expires_at)
values ($1, $2, $3, $4, $5) returning *;

-- name: LookupPasswordReset :one
select * from password_reset where token_hash = $1;

-- name: MarkPasswordResetUsed :execrows
update password_reset set used_at = now() where id = $1 and used_at is null and expires_at > now();

-- name: DeleteSessionsForOrgUser :exec
delete from session where org_user_id = $1;

-- name: DeleteSessionsForClientUser :exec
delete from session where client_user_id = $1;

-- name: MarkPasswordResetsUsedForOrgUser :exec
update password_reset set used_at = now() where org_user_id = $1 and used_at is null;

-- name: MarkPasswordResetsUsedForClientUser :exec
update password_reset set used_at = now() where client_user_id = $1 and used_at is null;

-- name: CreateAPIToken :one
insert into api_token (org_id, org_user_id, client_user_id, name, prefix, token_hash, expires_at)
values ($1, $2, $3, $4, $5, $6, $7) returning *;

-- name: ListAPITokens :many
select sqlc.embed(t), coalesce(o.email, c.email, '')::text as user_email
from api_token t
left join org_user o on o.id = t.org_user_id
left join client_user c on c.id = t.client_user_id
where t.org_id = $1
  and t.revoked_at is null
  and (t.expires_at is null or t.expires_at > now())
order by t.created_at desc;

-- name: RevokeAPIToken :execrows
update api_token set revoked_at = now() where id = $1 and revoked_at is null;

-- name: ListAPITokensByClientUser :many
-- A client user's own live tokens, for the portal's developer page.
select * from api_token
where client_user_id = $1 and revoked_at is null
  and (expires_at is null or expires_at > now())
order by created_at desc;

-- name: RevokeAPITokenOfClientUser :execrows
update api_token set revoked_at = now()
where id = $1 and client_user_id = $2 and revoked_at is null;
