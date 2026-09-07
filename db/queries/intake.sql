-- The intake wizard's resumable draft. One row per recruiter, deleted the
-- moment the intake creates the client, the job, and the assessment.

-- name: CreateIntakeDraft :one
insert into intake_draft (org_id, created_by, step, payload) values ($1, $2, $3, $4) returning *;

-- name: GetIntakeDraft :one
select * from intake_draft where id = $1;

-- name: GetOpenIntakeDraft :one
select * from intake_draft where created_by = $1;

-- name: SaveIntakeDraft :one
update intake_draft set step = $2, payload = $3, updated_at = now() where id = $1 returning *;

-- name: DeleteIntakeDraft :execrows
delete from intake_draft where id = $1;
