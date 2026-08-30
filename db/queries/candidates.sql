-- name: FindPublicJobBySlug :one
-- A job slug is unique only within an org, so the public URL names both. The
-- policy on job enforces the same pair; the join makes it explicit here.
select j.* from job j
join org o on o.id = j.org_id
where o.slug = sqlc.arg(org_slug) and j.slug = sqlc.arg(job_slug) and j.status = 'open';

-- name: UpsertCandidateFromApply :one
-- The public form must not let a stranger rename an existing candidate or
-- drop the links already on file: the name is kept and the links merge.
insert into candidate (org_id, email, name, phone, links)
values ($1, $2, $3, $4, $5)
on conflict (org_id, lower(email)) do update
    set name = coalesce(nullif(candidate.name, ''), excluded.name),
        phone = coalesce(candidate.phone, excluded.phone),
        links = (select coalesce(jsonb_agg(distinct l), '[]'::jsonb)
            from jsonb_array_elements(candidate.links || excluded.links) l),
        updated_at = now()
returning id, org_id, email, name, phone, links, created_at, updated_at;

-- name: GetCandidateResume :one
-- Both ids are matched so a resume id from one candidate cannot be read
-- through another candidate's URL.
select * from resume where id = $1 and candidate_id = $2;

-- name: FirstStage :one
select * from stage where job_id = $1 order by position limit 1;

-- name: CountCandidateApplications :one
select count(*) from application where job_id = $1 and candidate_id = $2;

-- name: ListCandidateApplications :many
select a.*, j.title as job_title, j.slug as job_slug, s.name as stage_name
from application a
join job j on j.id = a.job_id
join stage s on s.id = a.stage_id
where a.candidate_id = $1
order by a.created_at desc;

-- name: SearchCandidates :many
select c.id, c.org_id, c.email, c.name, c.phone, c.links, c.created_at, c.updated_at, count(a.id) as application_count
from candidate c
left join application a on a.candidate_id = c.id
where c.org_id = $1
  and (sqlc.arg(query)::text = '' or c.search @@ websearch_to_tsquery('english', sqlc.arg(query)::text))
group by c.id
order by
    case when sqlc.arg(query)::text = '' then 0
         else ts_rank(c.search, websearch_to_tsquery('english', sqlc.arg(query)::text)) end desc,
    c.created_at desc
limit sqlc.arg(row_limit)::int;

-- name: CreateResume :one
insert into resume (org_id, candidate_id, blob_key, filename, content_type, size_bytes, extracted_text, text_status)
values ($1, $2, $3, $4, $5, $6, $7, $8) returning *;

-- name: GetResume :one
select * from resume where id = $1;

-- name: ListResumesByCandidate :many
select * from resume where candidate_id = $1 order by created_at desc;
