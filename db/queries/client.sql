-- Client portal reads. Every query here runs under a client-scoped
-- transaction, so RLS already limits rows to released applications of the
-- caller's company; the predicates repeat that so the queries read correctly
-- under an org scope too. None of them selects a notes column.

-- name: ListClientUsersByCompany :many
select * from client_user where client_company_id = $1 order by name;

-- name: ListOrgUsersWithRole :many
select u.* from org_user u
join org_user_role r on r.org_user_id = u.id
where u.org_id = $1 and r.role = $2
order by u.name;

-- name: ListClientJobs :many
-- The company's jobs with how many applications are released on each.
select j.*, (select count(*) from application a where a.job_id = j.id and a.released_at is not null)::int as released_count
from job j
where j.client_company_id = $1 and j.status <> 'draft'
order by j.created_at desc;

-- name: ListReleasedApplications :many
select a.id, a.job_id, a.stage_id, a.status, a.released_at, a.recruiter_summary, a.created_at, a.updated_at,
    c.id as candidate_id, c.name as candidate_name, c.email as candidate_email, c.phone as candidate_phone, c.links as candidate_links,
    s.name as stage_name, s.position as stage_position, s.kind as stage_kind, s.unblind as stage_unblind,
    j.title as job_title, j.blind_mode as job_blind_mode, j.client_company_id
from application a
join candidate c on c.id = a.candidate_id
join stage s on s.id = a.stage_id
join job j on j.id = a.job_id
where a.job_id = $1 and a.released_at is not null
order by s.position, a.created_at, a.id;

-- name: GetReleasedApplication :one
select a.id, a.job_id, a.stage_id, a.status, a.released_at, a.recruiter_summary, a.created_at, a.updated_at,
    c.id as candidate_id, c.name as candidate_name, c.email as candidate_email, c.phone as candidate_phone, c.links as candidate_links,
    s.name as stage_name, s.position as stage_position, s.kind as stage_kind, s.unblind as stage_unblind,
    j.title as job_title, j.blind_mode as job_blind_mode, j.client_company_id
from application a
join candidate c on c.id = a.candidate_id
join stage s on s.id = a.stage_id
join job j on j.id = a.job_id
where a.id = $1 and a.released_at is not null;

-- name: ListClientScorecards :many
-- The interviewer's notes column is deliberately absent: it never reaches a client.
select s.id, s.stage_id, s.scores, s.overall, s.created_at,
    u.name as vetter_name, st.name as stage_name
from scorecard s
join org_user u on u.id = s.vetter_id
join stage st on st.id = s.stage_id
where s.application_id = $1
order by st.position, s.created_at;

-- name: GetLatestAssessmentOutcome :one
-- Score and verdict only; the reviewer's notes stay internal.
select t.score, r.verdict
from attempt t
left join review r on r.attempt_id = t.id
where t.application_id = sqlc.arg(application_id)::uuid
order by t.created_at desc
limit 1;

-- name: ListReleasedApplicationsByCompany :many
-- Every application released to the company, across its jobs, for the API's
-- collection read. Filters are optional; a zero uuid or empty string is "any".
select a.id, a.job_id, a.stage_id, a.status, a.released_at, a.recruiter_summary, a.created_at, a.updated_at,
    c.id as candidate_id, c.name as candidate_name, c.email as candidate_email, c.phone as candidate_phone, c.links as candidate_links,
    s.name as stage_name, s.position as stage_position, s.kind as stage_kind, s.unblind as stage_unblind,
    j.title as job_title, j.blind_mode as job_blind_mode, j.client_company_id
from application a
join candidate c on c.id = a.candidate_id
join stage s on s.id = a.stage_id
join job j on j.id = a.job_id
where a.client_company_id = sqlc.arg(client_company_id)::uuid and a.released_at is not null
  and (sqlc.arg(job_id)::uuid = '00000000-0000-0000-0000-000000000000'::uuid or a.job_id = sqlc.arg(job_id)::uuid)
  and (sqlc.arg(status)::text = '' or a.status = sqlc.arg(status)::text)
  and (sqlc.arg(updated_since)::timestamptz is null or a.updated_at >= sqlc.arg(updated_since)::timestamptz)
order by a.updated_at desc, a.id
limit sqlc.arg(row_limit)::int offset sqlc.arg(row_offset)::int;

-- name: ListClientEvents :many
-- The company's change feed: every event on a released application from
-- the moment it was released, in sequence order, after a cursor. The
-- sequence is global and monotonic, so a caller that remembers the last
-- seq it saw reads exactly what happened since.
select e.seq, e.id, e.application_id, e.kind, e.actor_kind, e.reason, e.created_at,
    a.job_id, j.title as job_title,
    f.name as from_stage, t.name as to_stage
from application_event e
join application a on a.id = e.application_id
join job j on j.id = a.job_id
left join stage f on f.id = e.from_stage_id
left join stage t on t.id = e.to_stage_id
where a.client_company_id = sqlc.arg(client_company_id)::uuid
  and a.released_at is not null
  -- The release event is written in the transaction that sets released_at
  -- from the application clock, so it can predate the stamp by a moment
  -- and is named rather than compared.
  and (e.kind = 'released' or e.created_at >= a.released_at)
  and e.seq > sqlc.arg(after_seq)::bigint
order by e.seq
limit sqlc.arg(row_limit)::int;
