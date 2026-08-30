-- name: GetTalentPoolEntry :one
select e.*, c.name as candidate_name, c.email as candidate_email
from talent_pool_entry e
join candidate c on c.id = e.candidate_id
where e.id = $1 and e.removed_at is null;

-- name: GetTalentPoolEntryForCandidate :one
select * from talent_pool_entry where org_id = $1 and candidate_id = $2;

-- name: SearchTalentPoolEntriesWithCandidate :many
-- The pool list is a working view: a name, an address, or a tag narrows it.
select e.*, c.name as candidate_name, c.email as candidate_email
from talent_pool_entry e
join candidate c on c.id = e.candidate_id
where e.org_id = $1 and e.removed_at is null
  and (sqlc.arg(query)::text = ''
    or c.name ilike '%' || sqlc.arg(query)::text || '%'
    or c.email ilike '%' || sqlc.arg(query)::text || '%'
    or exists (select 1 from unnest(e.skills) tag where tag ilike '%' || sqlc.arg(query)::text || '%'))
order by e.updated_at desc
limit sqlc.arg(row_limit)::int;

-- name: ListTalentPoolEntriesForRanking :many
-- Every live entry the ranker scores, with only the fields the formula and
-- the panel read. The browse list is paged; ranking must see the whole pool.
select e.id, e.candidate_id, e.skills, e.seniority, e.location, e.remote_ok,
    e.best_scores, c.name as candidate_name, c.email as candidate_email
from talent_pool_entry e
join candidate c on c.id = e.candidate_id
where e.org_id = $1 and e.removed_at is null
limit sqlc.arg(row_limit)::int;

-- name: UpsertTalentPoolEntry :one
-- One entry per candidate: a second pool-worthy event updates the aggregate
-- the caller merged rather than filing another entry.
insert into talent_pool_entry (
    org_id, candidate_id, skills, seniority, location, remote_ok,
    best_scores, scorecard_summary, source_job_ids, source)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
on conflict (org_id, candidate_id) do update set
    skills = excluded.skills,
    seniority = coalesce(excluded.seniority, talent_pool_entry.seniority),
    location = excluded.location,
    remote_ok = excluded.remote_ok,
    best_scores = excluded.best_scores,
    scorecard_summary = coalesce(excluded.scorecard_summary, talent_pool_entry.scorecard_summary),
    source_job_ids = excluded.source_job_ids,
    source = excluded.source,
    updated_at = now()
returning *;

-- name: RestoreTalentPoolEntry :exec
-- A removal stands until a recruiter asks for the person back; the automatic
-- sources refresh the aggregates of a removed entry without reviving it.
update talent_pool_entry set removed_at = null, updated_at = now() where id = $1;

-- name: UpdateTalentPoolEntry :one
update talent_pool_entry set skills = $2, notes = $3, location = $4, remote_ok = $5, updated_at = now()
where id = $1 and removed_at is null returning *;

-- name: RemoveTalentPoolEntry :execrows
update talent_pool_entry set removed_at = now(), updated_at = now()
where id = $1 and removed_at is null;

-- name: SetApplicationHighQuality :one
update application set high_quality = $2, updated_at = now() where id = $1 returning *;

-- name: BestAssessmentScoresForCandidate :many
-- The candidate's best assessment score per problem tag, across every
-- application they have. Derived, so re-running an upsert cannot drift.
select tag::text as tag, max(a.score)::numeric as best_score
from attempt a
join application app on app.id = a.application_id
join assessment_problem ap on ap.assessment_id = a.assessment_id
join problem p on p.id = ap.problem_id
cross join lateral unnest(p.tags) as tag
where app.candidate_id = $1 and a.score is not null
group by tag;

-- name: LatestScorecardForCandidate :one
select s.overall, s.notes
from scorecard s
join application a on a.id = s.application_id
where a.candidate_id = $1
order by s.created_at desc
limit 1;

-- name: ListJobCandidateIDs :many
select candidate_id from application where job_id = $1;

-- name: ListClientRejections :many
-- When each candidate was last rejected by one client company, read off the
-- move that closed the application rather than its row timestamp, which any
-- later edit would push forward. The ranker drops the recent ones.
select a.candidate_id, max(e.created_at)::timestamptz as rejected_at
from application a
join application_event e on e.application_id = a.id
join stage s on s.id = e.to_stage_id
where a.client_company_id = $1 and a.status = 'rejected' and s.terminal_status = 'rejected'
group by a.candidate_id;
