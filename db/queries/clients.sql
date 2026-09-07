-- The recruiter's book of accounts: one client company per row with the
-- numbers the desk is judged on. Every count is a correlated subquery so a
-- company with no jobs still reports zeroes rather than dropping out.

-- name: ListClientAccounts :many
select cc.id, cc.name, cc.industry, cc.shortlist_sla_days, cc.brief,
    (select count(*) from job j
        where j.client_company_id = cc.id and j.status = 'open')::int as open_jobs,
    (select count(*) from application a
        join job j on j.id = a.job_id
        where j.client_company_id = cc.id and a.status = 'active')::int as in_pipeline,
    -- Awaiting client: sitting in a client-review stage, waiting on them.
    (select count(*) from application a
        join job j on j.id = a.job_id
        join stage s on s.id = a.stage_id
        where j.client_company_id = cc.id and a.status = 'active'
            and s.kind = 'client_review')::int as awaiting_client,
    -- Placed in the last 90 days, dated by the move into a hired stage and
    -- falling back to the application's last change when no event recorded it.
    (select count(*) from application a
        join job j on j.id = a.job_id
        where j.client_company_id = cc.id and a.status = 'hired'
            and coalesce((select max(e.created_at) from application_event e
                join stage hs on hs.id = e.to_stage_id
                where e.application_id = a.id and hs.terminal_status = 'hired'),
                a.updated_at) >= now() - interval '90 days')::int as placed_90d,
    -- The desk that opened the account: whoever created its oldest job.
    coalesce((select u.name from job j
        join org_user u on u.id = j.created_by
        where j.client_company_id = cc.id
        order by j.created_at, j.id limit 1), '')::text as owner_name,
    -- Days from a job opening to its first shortlist going out, median over
    -- the jobs that have had one.
    (select percentile_cont(0.5) within group (
            order by extract(epoch from (first_sent.sent_at - j.created_at)) / 86400)
        from job j
        join lateral (
            select min(p.sent_at) as sent_at from shortlist_packet p
            where p.job_id = j.id and p.status = 'sent') first_sent on true
        where j.client_company_id = cc.id and first_sent.sent_at is not null
        )::numeric as median_days_to_shortlist
from client_company cc
where cc.org_id = $1
    and (sqlc.narg(id)::uuid is null or cc.id = sqlc.narg(id)::uuid)
order by cc.name, cc.id;

-- name: ListClientAccountJobs :many
select j.id, j.title, j.seniority, j.location, j.status, j.created_at,
    (select count(*) from application a where a.job_id = j.id)::int as applicants,
    (select count(distinct a.id) from application a
        join attempt t on t.application_id = a.id
        where a.job_id = j.id and not t.preview and t.score is not null)::int as scored,
    (select count(distinct k.application_id) from shortlist_pick k
        join shortlist_packet p on p.id = k.packet_id
        where p.job_id = j.id)::int as shortlisted,
    coalesce((select string_agg(distinct asm.name, ', ') from stage s
        join assessment asm on asm.id = s.assessment_id
        where s.job_id = j.id), '')::text as assessment_names
from job j
where j.client_company_id = $1
order by j.created_at desc, j.id;

-- name: ListShortlistPacketsByCompany :many
-- Every packet of the company's jobs, drafts included: this is the
-- recruiter's own view of the account, not the client's.
select p.*, j.title as job_title,
    (select count(*) from shortlist_pick k where k.packet_id = p.id)::int as picks
from shortlist_packet p
join job j on j.id = p.job_id
where j.client_company_id = $1
order by p.created_at desc, p.id;
