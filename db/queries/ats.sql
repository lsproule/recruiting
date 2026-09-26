-- name: UpsertCandidate :one
insert into candidate (org_id, email, name, phone, links)
values ($1, $2, $3, $4, $5)
on conflict (org_id, lower(email)) do update
    set name = excluded.name, phone = coalesce(excluded.phone, candidate.phone),
        links = (select coalesce(jsonb_agg(distinct l), '[]'::jsonb)
            from jsonb_array_elements(candidate.links || excluded.links) l), updated_at = now()
-- search is a derived tsvector maintained by trigger; it is never read back.
returning id, org_id, email, name, phone, links, created_at, updated_at;

-- name: GetCandidate :one
select id, org_id, email, name, phone, links, created_at, updated_at from candidate where id = $1;

-- name: CreateJob :one
insert into job (org_id, client_company_id, title, slug, description, skills, seniority, location, remote_policy, salary_min, salary_max, blind_mode, status, created_by)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) returning *;

-- name: UpdateJob :one
update job set client_company_id = $2, title = $3, slug = $4, description = $5, skills = $6,
    seniority = $7, location = $8, remote_policy = $9, salary_min = $10, salary_max = $11,
    blind_mode = $12, status = $13, updated_at = now()
where id = $1 returning *;

-- name: GetJob :one
select * from job where id = $1;

-- name: GetJobBySlug :one
select * from job where org_id = $1 and slug = $2;

-- name: ListJobs :many
select * from job where org_id = $1 order by created_at desc;

-- name: CreateStage :one
insert into stage (org_id, job_id, position, name, kind, terminal_status, unblind, scorecard_rubric_id, default_vetter_id, assessment_id,
    interview_format, duration_minutes, round_seconds, break_seconds)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) returning *;

-- name: UpdateStage :one
update stage set name = $2, kind = $3, terminal_status = $4, unblind = $5, default_vetter_id = $6,
    interview_format = $7, duration_minutes = $8, round_seconds = $9, break_seconds = $10
where id = $1 returning *;

-- name: SetStagePosition :exec
update stage set position = $2 where id = $1;

-- name: DeleteStage :execrows
delete from stage where id = $1;

-- name: ListStages :many
select * from stage where job_id = $1 order by position;

-- name: CountApplicationsInStage :one
select count(*) from application where stage_id = $1;

-- name: CreateApplication :one
insert into application (org_id, job_id, candidate_id, client_company_id, stage_id, screening_answers)
values ($1, $2, $3, $4, $5, $6) returning *;

-- name: GetApplication :one
select * from application where id = $1;

-- name: ListApplicationsByJob :many
select * from application where job_id = $1 order by created_at;

-- name: MoveApplication :one
-- Conditional on the stage the mover saw, so a move decided on a stale read
-- updates nothing rather than overwriting a concurrent move.
update application set stage_id = $2, status = $3, updated_at = now()
where id = $1 and stage_id = sqlc.arg(from_stage_id) and status = 'active' returning *;

-- name: SetApplicationReleased :one
update application set released_at = $2, updated_at = now() where id = $1 returning *;

-- name: CreateApplicationEvent :one
insert into application_event (org_id, application_id, actor_kind, actor_id, kind, from_stage_id, to_stage_id, reason, payload)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9) returning *;

-- name: ListApplicationEvents :many
select * from application_event where application_id = $1 order by seq;
