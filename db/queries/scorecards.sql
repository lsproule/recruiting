-- name: GetScorecardRubric :one
select * from scorecard_rubric where id = $1;

-- name: CreateScorecardRubric :one
insert into scorecard_rubric (org_id, name, criteria) values ($1, $2, $3) returning *;

-- name: UpdateScorecardRubric :one
update scorecard_rubric set name = $2, criteria = $3 where id = $1 returning *;

-- name: SetStageRubric :exec
update stage set scorecard_rubric_id = $2 where id = $1;

-- name: GetScorecard :one
select * from scorecard where id = $1;

-- name: GetScorecardForVetter :one
select * from scorecard where application_id = $1 and stage_id = $2 and vetter_id = $3;

-- name: UpsertScorecard :one
-- One card per interviewer per stage: a card filed twice at once updates the
-- first rather than failing on the key. The guard restates the authorship the
-- conflict target already carries, and the snapshot of what the interviewer
-- was asked stays as it was filed.
insert into scorecard (org_id, application_id, stage_id, vetter_id, rubric_id, criteria, scores, overall, notes)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9)
on conflict (application_id, stage_id, vetter_id) do update
    set scores = excluded.scores, overall = excluded.overall, notes = excluded.notes, updated_at = now()
    where scorecard.vetter_id = excluded.vetter_id
returning *;

-- name: UpdateScorecardByAuthor :one
-- The vetter is part of the key so a card can only be rewritten by the
-- interviewer who filed it, whatever the caller claims.
update scorecard set scores = $3, overall = $4, notes = $5, updated_at = now()
where id = $1 and vetter_id = $2 returning *;

-- name: ListScorecardsForApplication :many
select s.id, s.application_id, s.stage_id, s.vetter_id, s.rubric_id, s.criteria, s.scores,
    s.overall, s.notes, s.created_at, s.updated_at,
    u.name as vetter_name, st.name as stage_name, st.position as stage_position
from scorecard s
join org_user u on u.id = s.vetter_id
join stage st on st.id = s.stage_id
where s.application_id = $1
order by st.position, s.created_at;

-- name: ListVetterAssignments :many
-- The interviews waiting on the signed-in vetter, with their own card if they
-- have already filed one.
select a.id as application_id, a.stage_id, c.name as candidate_name, c.email as candidate_email,
    j.title as job_title, st.name as stage_name, sc.id as scorecard_id, sc.overall as overall
from application a
join candidate c on c.id = a.candidate_id
join job j on j.id = a.job_id
join stage st on st.id = a.stage_id
left join scorecard sc on sc.application_id = a.id and sc.stage_id = a.stage_id and sc.vetter_id = $1
where a.vetter_id = $1 and a.status = 'active' and st.kind = 'interview'
order by a.updated_at desc, a.id;
