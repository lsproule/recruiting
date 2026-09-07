-- name: GetStage :one
select * from stage where id = $1;

-- name: ListJobApplicationCards :many
-- One row per application on a job, with what a board card or list row shows.
select a.id, a.job_id, a.candidate_id, a.stage_id, a.status, a.released_at, a.high_quality, a.created_at, a.updated_at,
    c.name as candidate_name, c.email as candidate_email,
    s.name as stage_name, s.position as stage_position, s.kind as stage_kind
from application a
join candidate c on c.id = a.candidate_id
join stage s on s.id = a.stage_id
where a.job_id = $1
order by s.position, a.created_at, a.id;

-- name: GetApplicationCard :one
select a.id, a.job_id, a.candidate_id, a.stage_id, a.status, a.released_at, a.high_quality, a.created_at, a.updated_at,
    c.name as candidate_name, c.email as candidate_email,
    s.name as stage_name, s.position as stage_position, s.kind as stage_kind,
    j.title as job_title
from application a
join candidate c on c.id = a.candidate_id
join stage s on s.id = a.stage_id
join job j on j.id = a.job_id
where a.id = $1;

-- name: GetApplicationForUpdate :one
-- Locks the row for the rest of the transaction, so concurrent moves of one
-- application serialise and the second sees the first's result.
select * from application where id = $1 for update;

-- name: CloseApplication :one
update application set status = $2, updated_at = now() where id = $1 and status = 'active' returning *;

-- name: RevokeBookLinks :exec
update magic_link set revoked_at = now() where purpose = 'book' and subject_id = $1 and revoked_at is null;

-- name: FilterJobApplicationCards :many
-- The list screen: stage and status narrow in SQL, and the search runs on the
-- candidate's maintained tsvector so it matches resume text as well as names.
select a.id, a.job_id, a.candidate_id, a.stage_id, a.status, a.released_at, a.high_quality, a.created_at, a.updated_at,
    c.name as candidate_name, c.email as candidate_email,
    s.name as stage_name, s.position as stage_position, s.kind as stage_kind
from application a
join candidate c on c.id = a.candidate_id
join stage s on s.id = a.stage_id
where a.job_id = $1
  and (sqlc.narg(stage_id)::uuid is null or a.stage_id = sqlc.narg(stage_id)::uuid)
  and (sqlc.arg(status)::text = '' or a.status = sqlc.arg(status)::text)
  and (sqlc.arg(query)::text = '' or c.search @@ websearch_to_tsquery('english', sqlc.arg(query)::text)
       or lower(c.email) like '%' || lower(sqlc.arg(query)::text) || '%')
order by s.position, a.created_at, a.id
limit sqlc.arg(row_limit)::int;

-- name: HasScorecardForStage :one
select exists (select 1 from scorecard where application_id = $1 and stage_id = $2);

-- name: HasVerdictForStage :one
select exists (
    select 1 from review r join attempt t on t.id = r.attempt_id
    where t.application_id = sqlc.arg(application_id)::uuid and t.stage_id = sqlc.arg(stage_id)::uuid
);
