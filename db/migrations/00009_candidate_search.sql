-- +goose Up

-- Extraction runs inside the apply request and is allowed to fail or time
-- out; the application still lands, so the resume records what became of its
-- text rather than only whether it failed.
alter table resume add column text_status text not null default 'pending'
    check (text_status in ('pending', 'extracted', 'failed'));
update resume set text_status = case
    when extraction_failed then 'failed'
    when extracted_text is not null then 'extracted'
    else 'pending' end;
alter table resume drop column extraction_failed;

-- Search spans a candidate's name and every resume they have sent, so the
-- vector lives on candidate and both tables refresh it by trigger.
alter table candidate add column search tsvector;
create index candidate_search_idx on candidate using gin (search);
drop index if exists resume_text_idx;

-- +goose StatementBegin
create function candidate_search_vector(cid uuid, cname text) returns tsvector
    language sql stable as $$
    select setweight(to_tsvector('english', coalesce(cname, '')), 'A')
        || setweight(to_tsvector('english', coalesce(string_agg(r.extracted_text, ' '), '')), 'B')
    from resume r where r.candidate_id = cid
$$;
-- +goose StatementEnd

-- +goose StatementBegin
create function candidate_search_refresh() returns trigger
    language plpgsql as $$
begin
    new.search := candidate_search_vector(new.id, new.name);
    return new;
end
$$;
-- +goose StatementEnd

create trigger candidate_search before insert or update on candidate
    for each row execute function candidate_search_refresh();

-- A resume's text is not on the candidate row, so touching the candidate is
-- what re-runs the vector above.
-- +goose StatementBegin
create function resume_refresh_candidate_search() returns trigger
    language plpgsql as $$
declare
    changed uuid;
begin
    if tg_op = 'DELETE' then
        changed := old.candidate_id;
    else
        changed := new.candidate_id;
        if tg_op = 'UPDATE' and old.candidate_id <> new.candidate_id then
            update candidate set updated_at = now() where id = old.candidate_id;
        end if;
    end if;
    update candidate set updated_at = now() where id = changed;
    return null;
end
$$;
-- +goose StatementEnd

create trigger resume_search after insert or update or delete on resume
    for each row execute function resume_refresh_candidate_search();

-- The public apply page has no session and therefore no org: it names a job
-- slug instead, and only an open job answering to that slug becomes visible.
create function app_public_job_slug() returns text
    language sql stable parallel safe
    as $$ select nullif(current_setting('app.public_job_slug', true), '') $$;

create policy public_apply on job for select
    using (status = 'open' and slug = app_public_job_slug());

grant select, insert, update, delete on all tables in schema public to app_rw;

-- +goose Down
drop policy if exists public_apply on job;
drop function if exists app_public_job_slug();
drop trigger if exists resume_search on resume;
drop function if exists resume_refresh_candidate_search();
drop trigger if exists candidate_search on candidate;
drop function if exists candidate_search_refresh();
drop function if exists candidate_search_vector(uuid, text);
drop index if exists candidate_search_idx;
alter table candidate drop column if exists search;
create index if not exists resume_text_idx on resume using gin (to_tsvector('english', coalesce(extracted_text, '')));
alter table resume add column extraction_failed boolean not null default false;
update resume set extraction_failed = (text_status = 'failed');
alter table resume drop column text_status;
