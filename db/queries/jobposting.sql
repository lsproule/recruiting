-- name: CreateJobPosting :one
insert into job_posting (org_id, job_id, board, title, body, apply_url, created_by)
values ($1, $2, $3, $4, $5, $6, $7) returning *;

-- name: GetJobPosting :one
select * from job_posting where id = $1;

-- name: GetJobPostingForUpdate :one
select * from job_posting where id = $1 for update;

-- name: ListJobPostings :many
select * from job_posting where job_id = $1 order by created_at desc;

-- name: ListJobPostingsForOrg :many
-- Every posting of the org, newest first, for the sourcing overview.
select p.*, j.title as job_title from job_posting p join job j on j.id = p.job_id
where p.org_id = $1 order by p.created_at desc limit sqlc.arg(row_limit)::int;

-- name: StartJobPosting :execrows
-- Claims a queued or failed posting for one publish attempt.
update job_posting set status = 'posting', attempts = attempts + 1, updated_at = now()
where id = $1 and status in ('queued', 'posting', 'failed');

-- name: FinishJobPosting :exec
update job_posting set status = 'posted', external_url = $2, external_id = $3, error = null, posted_at = now(), updated_at = now()
where id = $1;

-- name: FailJobPosting :exec
update job_posting set status = 'failed', error = $2, updated_at = now() where id = $1;

-- name: RemoveJobPosting :execrows
update job_posting set status = 'removed', updated_at = now() where id = $1 and org_id = $2;
