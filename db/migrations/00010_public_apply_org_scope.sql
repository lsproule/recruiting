-- +goose Up

-- A job slug is unique within an org, not across the platform, so a slug
-- alone is not an address: two orgs recruiting a "senior-go-engineer" would
-- both answer the same public URL. The apply URL therefore names the org
-- first, and the policy matches on both halves.
create function app_public_org_slug() returns text
    language sql stable parallel safe
    as $$ select nullif(current_setting('app.public_org_slug', true), '') $$;

-- The apply page has to resolve the org slug it was given; only that one row
-- becomes visible, and only when a slug is set.
create policy public_apply on org for select
    using (slug = app_public_org_slug());

drop policy if exists public_apply on job;
create policy public_apply on job for select
    using (
        status = 'open'
        and slug = app_public_job_slug()
        and org_id = (select o.id from org o where o.slug = app_public_org_slug())
    );

-- to_tsvector rejects a document past a megabyte, and a resume that long is
-- an attack rather than a career. Truncating here keeps a candidate whose
-- text overflows searchable by everything before the cut, instead of failing
-- every write to their row.
-- +goose StatementBegin
create or replace function candidate_search_vector(cid uuid, cname text) returns tsvector
    language sql stable as $$
    select setweight(to_tsvector('english', left(coalesce(cname, ''), 100000)), 'A')
        || setweight(to_tsvector('english', left(coalesce(string_agg(r.extracted_text, ' '), ''), 500000)), 'B')
    from resume r where r.candidate_id = cid
$$;
-- +goose StatementEnd

grant select, insert, update, delete on all tables in schema public to app_rw;

-- +goose Down
-- +goose StatementBegin
create or replace function candidate_search_vector(cid uuid, cname text) returns tsvector
    language sql stable as $$
    select setweight(to_tsvector('english', coalesce(cname, '')), 'A')
        || setweight(to_tsvector('english', coalesce(string_agg(r.extracted_text, ' '), '')), 'B')
    from resume r where r.candidate_id = cid
$$;
-- +goose StatementEnd
drop policy if exists public_apply on job;
create policy public_apply on job for select
    using (status = 'open' and slug = app_public_job_slug());
drop policy if exists public_apply on org;
drop function if exists app_public_org_slug();
