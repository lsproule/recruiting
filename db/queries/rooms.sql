-- name: GetInterviewRoom :one
select * from interview_room where kind = $1 and subject_id = $2;

-- name: UpsertInterviewRoom :one
insert into interview_room (org_id, kind, subject_id, language, source)
values ($1, $2, $3, $4, $5)
on conflict (kind, subject_id) do update
    set language = excluded.language, source = excluded.source, updated_at = now()
returning *;

-- name: GetInterviewSlotRoom :one
-- A booked interview with everyone and everything a room needs to know.
select s.id, s.org_id, s.vetter_id, s.application_id, s.stage_id, s.starts_at, s.ends_at, s.status,
    u.name as vetter_name, c.name as candidate_name, j.title as job_title, j.id as job_id,
    st.name as stage_name, st.interview_format
from interview_slot s
join org_user u on u.id = s.vetter_id
join application a on a.id = s.application_id
join candidate c on c.id = a.candidate_id
join job j on j.id = a.job_id
join stage st on st.id = a.stage_id
where s.id = $1;

-- name: ListInterviewSlotsForOrg :many
-- Every booked or finished interview from a moment on, soonest first, for
-- the interviews screen. A vetter's own list filters by vetter_id.
select s.id, s.vetter_id, s.application_id, s.stage_id, s.starts_at, s.ends_at, s.status,
    u.name as vetter_name, c.name as candidate_name, c.email as candidate_email, j.title as job_title,
    st.name as stage_name, st.interview_format,
    exists (select 1 from scorecard sc where sc.application_id = s.application_id and sc.stage_id = s.stage_id and sc.vetter_id = s.vetter_id) as has_scorecard
from interview_slot s
join org_user u on u.id = s.vetter_id
join application a on a.id = s.application_id
join candidate c on c.id = a.candidate_id
join job j on j.id = a.job_id
join stage st on st.id = coalesce(s.stage_id, a.stage_id)
where s.status <> 'cancelled' and s.ends_at > sqlc.arg(ends_after)::timestamptz
  and (sqlc.narg(vetter_id)::uuid is null or s.vetter_id = sqlc.narg(vetter_id)::uuid)
order by s.starts_at, s.id;
