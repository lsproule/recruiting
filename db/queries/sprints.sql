-- name: CreateSprint :one
insert into sprint (org_id, job_id, stage_id, name, starts_at, round_seconds, break_seconds, created_by)
values ($1, $2, $3, $4, $5, $6, $7, $8) returning *;

-- name: GetSprint :one
select * from sprint where id = $1;

-- name: GetSprintForUpdate :one
select * from sprint where id = $1 for update;

-- name: UpdateSprintDraft :one
update sprint set name = $2, starts_at = $3, round_seconds = $4, break_seconds = $5, updated_at = now()
where id = $1 and status = 'draft' returning *;

-- name: SetSprintStatus :one
update sprint set status = $2, updated_at = now() where id = $1 returning *;

-- name: SetSprintStartsAt :one
update sprint set starts_at = $2, updated_at = now() where id = $1 returning *;

-- name: ListSprints :many
-- Every sprint of the org, newest start first, with where it belongs.
select s.*, j.title as job_title, st.name as stage_name,
    (select count(*) from sprint_candidate c where c.sprint_id = s.id) as candidates,
    (select count(*) from sprint_interviewer i where i.sprint_id = s.id) as interviewers
from sprint s
join job j on j.id = s.job_id
join stage st on st.id = s.stage_id
where s.org_id = $1
order by s.starts_at desc, s.id;

-- name: ListSprintsForStage :many
select * from sprint where stage_id = $1 and status <> 'cancelled' order by starts_at desc;

-- name: ListSprintsForInterviewer :many
-- The sprints one user interviews in, soonest first.
select s.*, j.title as job_title
from sprint s
join sprint_interviewer i on i.sprint_id = s.id
join job j on j.id = s.job_id
where i.user_id = $1 and s.status = 'scheduled'
order by s.starts_at, s.id;

-- name: DeleteSprintInterviewers :exec
delete from sprint_interviewer where sprint_id = $1;

-- name: AddSprintInterviewer :exec
insert into sprint_interviewer (sprint_id, org_id, user_id, position) values ($1, $2, $3, $4);

-- name: ListSprintInterviewers :many
select i.user_id, i.position, u.name, u.email
from sprint_interviewer i
join org_user u on u.id = i.user_id
where i.sprint_id = $1
order by i.position;

-- name: DeleteSprintCandidates :exec
delete from sprint_candidate where sprint_id = $1;

-- name: AddSprintCandidate :one
insert into sprint_candidate (sprint_id, org_id, application_id, position) values ($1, $2, $3, $4) returning *;

-- name: ListSprintCandidates :many
select sc.id, sc.application_id, sc.position, c.name as candidate_name, c.email as candidate_email,
    a.status as application_status, a.stage_id
from sprint_candidate sc
join application a on a.id = sc.application_id
join candidate c on c.id = a.candidate_id
where sc.sprint_id = $1
order by sc.position;

-- name: GetSprintCandidate :one
select sc.*, c.name as candidate_name, c.email as candidate_email
from sprint_candidate sc
join application a on a.id = sc.application_id
join candidate c on c.id = a.candidate_id
where sc.id = $1;

-- name: DeleteSprintPairings :exec
delete from sprint_pairing where sprint_id = $1;

-- name: CreateSprintPairing :one
insert into sprint_pairing (sprint_id, org_id, round, interviewer_id, application_id)
values ($1, $2, $3, $4, $5) returning *;

-- name: ListSprintPairings :many
-- Every conversation of a sprint with both names and the rating if filed.
select p.id, p.round, p.interviewer_id, p.application_id,
    u.name as interviewer_name, c.name as candidate_name, c.email as candidate_email,
    r.score, r.recommendation, r.note, r.updated_at as rated_at
from sprint_pairing p
join org_user u on u.id = p.interviewer_id
join application a on a.id = p.application_id
join candidate c on c.id = a.candidate_id
left join sprint_rating r on r.pairing_id = p.id
where p.sprint_id = $1
order by p.round, u.name, p.id;

-- name: GetSprintPairing :one
select * from sprint_pairing where id = $1;

-- name: UpsertSprintRating :one
insert into sprint_rating (org_id, sprint_id, pairing_id, interviewer_id, application_id, score, recommendation, note)
values ($1, $2, $3, $4, $5, $6, $7, $8)
on conflict (pairing_id) do update
    set score = excluded.score, recommendation = excluded.recommendation, note = excluded.note, updated_at = now()
    where sprint_rating.interviewer_id = excluded.interviewer_id
returning *;

-- name: HasSprintRatingForStage :one
-- Whether any interviewer rated the application in a sprint of this stage.
select exists (
    select 1 from sprint_rating r join sprint s on s.id = r.sprint_id
    where r.application_id = sqlc.arg(application_id)::uuid and s.stage_id = sqlc.arg(stage_id)::uuid
);

-- name: ListSprintRatingsForApplication :many
select r.*, s.name as sprint_name, u.name as interviewer_name
from sprint_rating r
join sprint s on s.id = r.sprint_id
join org_user u on u.id = r.interviewer_id
where r.application_id = $1
order by r.created_at;

-- name: RevokeSprintLinks :exec
update magic_link set revoked_at = now()
where purpose = 'sprint' and revoked_at is null
  and subject_id in (select id from sprint_candidate where sprint_id = $1);

-- name: ListQueueSprintRatingsMissing :many
-- Conversations that ended more than five minutes ago and were never rated,
-- on sprints that still stand.
select p.id as pairing_id, s.id as sprint_id, s.name as sprint_name,
    u.name as interviewer_name, c.name as candidate_name, j.title as job_title, cc.name as client_name,
    s.starts_at + make_interval(secs => (p.round + 1) * (s.round_seconds + s.break_seconds) - s.break_seconds) as ended_at
from sprint_pairing p
join sprint s on s.id = p.sprint_id
join org_user u on u.id = p.interviewer_id
join application a on a.id = p.application_id
join candidate c on c.id = a.candidate_id
join job j on j.id = s.job_id
join client_company cc on cc.id = j.client_company_id
left join sprint_rating r on r.pairing_id = p.id
where s.status = 'scheduled' and r.id is null
  and s.starts_at + make_interval(secs => (p.round + 1) * (s.round_seconds + s.break_seconds) - s.break_seconds) < now() - interval '5 minutes'
order by ended_at;
