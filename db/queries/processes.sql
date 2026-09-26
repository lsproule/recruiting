-- name: UpdatePipelineTemplate :one
update pipeline_template set name = $2, description = $3, updated_at = now() where id = $1 returning *;

-- name: UnsetDefaultPipelineTemplates :exec
update pipeline_template set is_default = false, updated_at = now() where org_id = $1 and is_default;

-- name: SetPipelineTemplateDefault :exec
update pipeline_template set is_default = true, updated_at = now() where id = $1;

-- name: DeletePipelineTemplate :execrows
delete from pipeline_template where id = $1 and not is_default;

-- name: CountJobsByTemplate :many
select template_id, count(*) as jobs from job where org_id = $1 and template_id is not null group by template_id;

-- name: CreateProcessStage :one
insert into pipeline_template_stage (org_id, template_id, position, name, kind, unblind, interview_format, duration_minutes, round_seconds, break_seconds, terminal_status,
    pass_score, auto_advance, auto_reject)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) returning *;

-- name: UpdateProcessStage :one
update pipeline_template_stage
set name = $2, kind = $3, unblind = $4, interview_format = $5, duration_minutes = $6, round_seconds = $7, break_seconds = $8, terminal_status = $9,
    pass_score = $10, auto_advance = $11, auto_reject = $12
where id = $1 returning *;

-- name: DeleteProcessStage :execrows
delete from pipeline_template_stage where id = $1;

-- name: SetProcessStagePosition :exec
update pipeline_template_stage set position = $2 where id = $1;

-- name: TouchPipelineTemplate :exec
update pipeline_template set updated_at = now() where id = $1;

-- name: SetJobTemplate :exec
update job set template_id = $2 where id = $1;
