-- The work queue is a read model: one query per rule, unioned in Go, so a
-- rule can be read and tested on its own. Preview sittings are the
-- recruiter's rehearsal and never raise work.

-- name: ListQueueReview :many
-- A sitting has been scored and nobody has reviewed it yet.
select t.id as attempt_id, a.id as application_id, c.name as candidate_name,
    t.score, t.finished_at, j.title as job_title, cc.name as client_name
from attempt t
join application a on a.id = t.application_id
join candidate c on c.id = a.candidate_id
join job j on j.id = a.job_id
join client_company cc on cc.id = j.client_company_id
left join review r on r.attempt_id = t.id
where t.status = 'scored' and not t.preview and r.id is null
order by t.finished_at, t.id;

-- name: ListQueueExpiring :many
-- An invite runs out within the day and the candidate has not started. An
-- invite already past its window still counts: nothing sweeps it, and the
-- recruiter is the one who has to re-issue it.
select t.id as attempt_id, a.id as application_id, c.name as candidate_name,
    t.invite_expires_at, j.title as job_title, cc.name as client_name
from attempt t
join application a on a.id = t.application_id
join candidate c on c.id = a.candidate_id
join job j on j.id = a.job_id
join client_company cc on cc.id = j.client_company_id
where t.status = 'invited' and not t.preview
    and t.invite_expires_at is not null
    and t.invite_expires_at < now() + interval '24 hours'
order by t.invite_expires_at, t.id;

-- name: ListQueueClientWaiting :many
-- The client asked a question and no org user has touched the application
-- since: every recruiter reply lands on the timeline, so a later org_user
-- event is the answer. The oldest unanswered question per application wins,
-- because that is how long the client has been waiting.
select distinct on (a.id)
    a.id as application_id, e.created_at, e.reason as message,
    c.name as candidate_name, j.title as job_title, cc.name as client_name
from application_event e
join application a on a.id = e.application_id
join candidate c on c.id = a.candidate_id
join job j on j.id = a.job_id
join client_company cc on cc.id = j.client_company_id
where e.kind = sqlc.arg(request_kind)::text
    and not exists (
        select 1 from application_event later
        where later.application_id = e.application_id
            and later.actor_kind = 'org_user'
            and later.created_at > e.created_at)
order by a.id, e.created_at;

-- name: ListQueueShortlistDrafts :many
-- A packet was built and never sent; the client is waiting on a decision
-- that has already been made.
select p.id as packet_id, p.job_id, p.updated_at,
    j.title as job_title, cc.name as client_name,
    (select count(*) from shortlist_pick k where k.packet_id = p.id)::int as picks
from shortlist_packet p
join job j on j.id = p.job_id
join client_company cc on cc.id = j.client_company_id
where p.status = 'draft'
order by p.updated_at, p.id;

-- name: ListQueueScorecardsOverdue :many
-- The interview finished more than a day ago and its scorecard is missing.
select s.id as slot_id, s.application_id, s.stage_id, s.ends_at,
    c.name as candidate_name, u.name as vetter_name, st.name as stage_name,
    j.title as job_title, cc.name as client_name
from interview_slot s
join application a on a.id = s.application_id
join stage st on st.id = s.stage_id
join candidate c on c.id = a.candidate_id
join org_user u on u.id = s.vetter_id
join job j on j.id = a.job_id
join client_company cc on cc.id = j.client_company_id
where s.status in ('booked', 'completed')
    and s.ends_at < now() - interval '24 hours'
    and not exists (
        select 1 from scorecard sc
        where sc.application_id = s.application_id and sc.stage_id = s.stage_id)
order by s.ends_at, s.id;

-- name: UpsertQueueSnooze :exec
insert into queue_snooze (org_id, user_id, kind, subject_id, until)
values ($1, $2, $3, $4, $5)
on conflict (user_id, kind, subject_id) do update set until = excluded.until;

-- name: ListActiveQueueSnoozes :many
-- A snooze hides the item from the one user who set it, until it lapses.
select kind, subject_id from queue_snooze where user_id = $1 and until > now();

-- name: CountNavSubjects :one
-- The sidebar's non-queue counts, in one round trip.
select
    (select count(*) from client_company cc where cc.org_id = $1)::int as clients,
    (select count(*) from candidate c where c.org_id = $1)::int as candidates,
    (select count(*) from problem pr where pr.org_id = $1)::int as problems,
    (select count(*) from assessment asm where asm.org_id = $1)::int as assessments;
