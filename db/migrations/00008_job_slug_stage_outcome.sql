-- +goose Up

-- Jobs are reached by slug on the public apply URL, so the slug has to be
-- stable and unique within the org.
alter table job add column slug text;
update job set slug = id::text where slug is null;
alter table job alter column slug set not null;
create unique index job_org_slug_idx on job (org_id, slug);

-- A terminal stage closes an application, and which outcome it closes with is
-- part of the pipeline rather than the stage's name: a recruiter may rename
-- "Hired" freely without changing what reaching it means.
alter table stage add column terminal_status text
    check (terminal_status in ('hired', 'rejected'));
alter table stage add constraint stage_terminal_status_matches_kind
    check ((kind = 'terminal') = (terminal_status is not null));

grant select, insert, update, delete on all tables in schema public to app_rw;

-- +goose Down
alter table stage drop constraint if exists stage_terminal_status_matches_kind;
alter table stage drop column if exists terminal_status;
drop index if exists job_org_slug_idx;
alter table job drop column if exists slug;
