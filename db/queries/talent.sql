-- The talent network: profiles people keep with the org, the requests
-- companies file against it, and the introductions between the two. Every
-- query runs org-scoped; a client's reads go through the service, which
-- anonymises before anything reaches the company.

-- name: UpsertTalentProfile :one
-- One profile per candidate. A returning member's submission replaces their
-- details, renews consent, and reverses a withdrawal.
insert into talent_profile (org_id, candidate_id, headline, skills, seniority, roles, location, remote_policy, salary_min, available_from)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
on conflict (org_id, candidate_id) do update
    set headline = excluded.headline, skills = excluded.skills, seniority = excluded.seniority,
        roles = excluded.roles, location = excluded.location, remote_policy = excluded.remote_policy,
        salary_min = excluded.salary_min, available_from = excluded.available_from,
        consent_at = now(), withdrawn_at = null, updated_at = now()
returning *;

-- name: UpdateTalentProfile :one
update talent_profile
set headline = $2, skills = $3, seniority = $4, roles = $5, location = $6, remote_policy = $7,
    salary_min = $8, available_from = $9, updated_at = now()
where id = $1
returning *;

-- name: WithdrawTalentProfile :execrows
update talent_profile set withdrawn_at = now(), updated_at = now() where id = $1 and withdrawn_at is null;

-- name: RejoinTalentProfile :execrows
update talent_profile set withdrawn_at = null, consent_at = now(), updated_at = now() where id = $1 and withdrawn_at is not null;

-- name: GetTalentProfile :one
select p.*, c.name as candidate_name, c.email as candidate_email, c.phone as candidate_phone, c.links as candidate_links,
    exists (select 1 from resume r where r.candidate_id = c.id) as has_resume
from talent_profile p
join candidate c on c.id = p.candidate_id
where p.id = $1;

-- name: GetTalentProfileByCandidate :one
select * from talent_profile where candidate_id = $1;

-- name: ListTalentProfiles :many
-- The recruiter's view of the network, newest first, narrowed by a search
-- over names, headlines, skills, roles, and résumé text.
select p.*, c.name as candidate_name, c.email as candidate_email
from talent_profile p
join candidate c on c.id = p.candidate_id
where p.org_id = $1
  and (sqlc.arg(include_withdrawn)::bool or p.withdrawn_at is null)
  and (sqlc.arg(query)::text = ''
    or c.name ilike '%' || sqlc.arg(query)::text || '%'
    or p.headline ilike '%' || sqlc.arg(query)::text || '%'
    or exists (select 1 from unnest(p.skills) tag where tag ilike '%' || sqlc.arg(query)::text || '%')
    or exists (select 1 from unnest(p.roles) tag where tag ilike '%' || sqlc.arg(query)::text || '%')
    or c.search @@ websearch_to_tsquery('english', sqlc.arg(query)::text))
order by p.updated_at desc
limit sqlc.arg(row_limit)::int;

-- name: ListTalentProfilesForMatching :many
-- Every consenting profile with the fields the matcher scores, plus how
-- well the résumé and name index answer the request's terms. The rank is
-- zero when the request has no terms or the text says nothing about them.
select p.id, p.candidate_id, p.headline, p.skills, p.seniority, p.roles, p.location, p.remote_policy,
    p.salary_min, p.available_from, p.updated_at,
    exists (select 1 from resume r where r.candidate_id = p.candidate_id) as has_resume,
    case when sqlc.arg(terms)::text = '' then 0::real
         else ts_rank(c.search, websearch_to_tsquery('english', sqlc.arg(terms)::text))::real end as text_rank
from talent_profile p
join candidate c on c.id = p.candidate_id
where p.org_id = $1 and p.withdrawn_at is null
limit sqlc.arg(row_limit)::int;

-- name: ListTalentPoolForMatching :many
-- The org's pool entries as the matcher sees them: the same terms, without
-- the network's preferences. Their people applied to the org before, so a
-- match here is an introduction the recruiter makes, not a promise made.
select e.id, e.candidate_id, e.skills, e.seniority, e.location, e.remote_ok, e.updated_at,
    exists (select 1 from resume r where r.candidate_id = e.candidate_id) as has_resume,
    case when sqlc.arg(terms)::text = '' then 0::real
         else ts_rank(c.search, websearch_to_tsquery('english', sqlc.arg(terms)::text))::real end as text_rank
from talent_pool_entry e
join candidate c on c.id = e.candidate_id
where e.org_id = $1 and e.removed_at is null
limit sqlc.arg(row_limit)::int;

-- name: CreateTalentRequest :one
insert into talent_request (org_id, client_company_id, client_user_id, job_id, title, skills, seniority, location, remote_policy, note)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
returning *;

-- name: GetTalentRequest :one
select r.*, cc.name as client_company_name, j.title as job_title
from talent_request r
join client_company cc on cc.id = r.client_company_id
left join job j on j.id = r.job_id
where r.id = $1;

-- name: ListTalentRequestsByCompany :many
select r.*, cc.name as client_company_name, j.title as job_title,
    (select count(*) from talent_intro i where i.request_id = r.id)::int as intro_count
from talent_request r
join client_company cc on cc.id = r.client_company_id
left join job j on j.id = r.job_id
where r.client_company_id = $1
order by r.created_at desc;

-- name: ListTalentRequests :many
-- The recruiter's list: every company's requests, open ones first, with
-- how many introductions are waiting to be sent on each.
select r.*, cc.name as client_company_name, j.title as job_title,
    (select count(*) from talent_intro i where i.request_id = r.id)::int as intro_count,
    (select count(*) from talent_intro i where i.request_id = r.id and i.status = 'requested')::int as waiting_count
from talent_request r
join client_company cc on cc.id = r.client_company_id
left join job j on j.id = r.job_id
where r.org_id = $1
order by (r.status = 'open') desc, r.created_at desc;

-- name: CloseTalentRequest :execrows
update talent_request set status = 'closed', updated_at = now() where id = $1 and status = 'open';

-- name: CreateTalentIntro :one
insert into talent_intro (org_id, request_id, candidate_id, source, score)
values ($1, $2, $3, $4, $5)
returning *;

-- name: GetTalentIntro :one
select i.*, c.name as candidate_name, c.email as candidate_email,
    r.title as request_title, r.client_company_id, cc.name as client_company_name, r.job_id as request_job_id
from talent_intro i
join candidate c on c.id = i.candidate_id
join talent_request r on r.id = i.request_id
join client_company cc on cc.id = r.client_company_id
where i.id = $1;

-- name: ListTalentIntrosByRequest :many
select i.*, c.name as candidate_name, c.email as candidate_email
from talent_intro i
join candidate c on c.id = i.candidate_id
where i.request_id = $1
order by i.requested_at, i.id;

-- name: ListTalentIntrosWaiting :many
-- Introductions a company asked for that no recruiter has sent yet: the
-- work-queue rule.
select i.id, i.request_id, i.requested_at, c.name as candidate_name,
    r.title as request_title, cc.name as client_name
from talent_intro i
join candidate c on c.id = i.candidate_id
join talent_request r on r.id = i.request_id
join client_company cc on cc.id = r.client_company_id
where i.status = 'requested'
order by i.requested_at, i.id;

-- name: MarkTalentIntroSent :execrows
update talent_intro set status = 'sent', job_id = $2, sent_by = $3, sent_at = now()
where id = $1 and status = 'requested';

-- name: AnswerTalentIntro :execrows
update talent_intro set status = $2, application_id = $3, answered_at = now()
where id = $1 and status = 'sent';

-- name: DismissTalentIntro :execrows
update talent_intro set status = 'dismissed', answered_at = now()
where id = $1 and status in ('requested', 'sent');

-- name: CountTalentIntroWaiting :one
select count(*) from talent_intro where org_id = $1 and status = 'requested';

-- name: ListCandidateIDsAtCompany :many
-- People already in a company's pipeline, on any of its jobs: a match the
-- company has, or has had, is no introduction.
select distinct a.candidate_id from application a where a.client_company_id = $1;

-- name: ListOpenJobsByCompany :many
select * from job where client_company_id = $1 and status = 'open' order by created_at desc;
