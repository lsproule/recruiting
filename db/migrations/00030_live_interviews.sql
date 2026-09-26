-- +goose Up

-- A stage carries what its kind needs: an interview is a phone call or a
-- video room of a given length, a sprint rotates on a round clock. The same
-- settings live on template stages so a process can be defined once and
-- copied into every job built from it.
alter table stage drop constraint stage_kind_check;
alter table stage add constraint stage_kind_check
    check (kind in ('generic', 'interview', 'assessment', 'client_review', 'sprint', 'terminal'));
alter table stage
    add column interview_format text not null default 'call' check (interview_format in ('call', 'video')),
    add column duration_minutes integer,
    add column round_seconds integer,
    add column break_seconds integer;

alter table pipeline_template_stage drop constraint pipeline_template_stage_kind_check;
alter table pipeline_template_stage add constraint pipeline_template_stage_kind_check
    check (kind in ('generic', 'interview', 'assessment', 'client_review', 'sprint', 'terminal'));
alter table pipeline_template_stage
    add column interview_format text not null default 'call' check (interview_format in ('call', 'video')),
    add column duration_minutes integer,
    add column round_seconds integer,
    add column break_seconds integer,
    -- Which outcome a terminal template stage closes with; rows seeded before
    -- this column read it off their name, as jobs copied from them did.
    add column terminal_status text check (terminal_status in ('hired', 'rejected'));
-- Reordering rewrites every position in one transaction, so the key has to
-- tolerate the intermediate collisions the way the job stage key does.
alter table pipeline_template_stage drop constraint pipeline_template_stage_template_id_position_key;
alter table pipeline_template_stage add constraint pipeline_template_stage_template_id_position_key
    unique (template_id, position) deferrable initially immediate;

-- A process is a template with a face: what it is for, and which library
-- entry it was copied from, so the screen can say so.
alter table pipeline_template
    add column description text not null default '',
    add column library_key text,
    add column updated_at timestamptz not null default now();

-- Which process a job was built from. Informational only: the job owns its
-- stages, and a process deleted later leaves the job alone.
alter table job add column template_id uuid references pipeline_template(id) on delete set null;
create index job_template_idx on job (template_id);

-- A timed assessment is today's proctored sitting; a take-home is the same
-- set with days rather than minutes and no proctoring, which the candidate
-- may leave and return to.
alter table assessment add column format text not null default 'timed' check (format in ('timed', 'take_home'));

-- Sprint candidates reach their lobby through a link of their own.
alter table magic_link drop constraint magic_link_purpose_check;
alter table magic_link add constraint magic_link_purpose_check
    check (purpose in ('apply', 'book', 'assessment', 'sprint'));

-- A screening sprint: a set of candidates meets a set of interviewers in
-- short rounds on a clock. Live and done are read off the clock, never
-- stored; only the recruiter's decisions are.
create table sprint (
    id             uuid primary key default gen_random_uuid(),
    org_id         uuid not null references org(id) on delete cascade,
    job_id         uuid not null references job(id) on delete cascade,
    stage_id       uuid not null references stage(id) on delete cascade,
    name           text not null,
    status         text not null default 'draft' check (status in ('draft', 'scheduled', 'cancelled')),
    starts_at      timestamptz not null,
    round_seconds  integer not null check (round_seconds between 15 and 3600),
    break_seconds  integer not null check (break_seconds between 0 and 3600),
    created_by     uuid references org_user(id) on delete set null,
    created_at     timestamptz not null default now(),
    updated_at     timestamptz not null default now()
);
create index sprint_job_idx on sprint (job_id);
create index sprint_stage_idx on sprint (stage_id);

create table sprint_interviewer (
    sprint_id  uuid not null references sprint(id) on delete cascade,
    org_id     uuid not null references org(id) on delete cascade,
    user_id    uuid not null references org_user(id) on delete cascade,
    position   integer not null,
    primary key (sprint_id, user_id)
);

create table sprint_candidate (
    id              uuid primary key default gen_random_uuid(),
    sprint_id       uuid not null references sprint(id) on delete cascade,
    org_id          uuid not null references org(id) on delete cascade,
    application_id  uuid not null references application(id) on delete cascade,
    position        integer not null,
    unique (sprint_id, application_id)
);

-- One conversation: which interviewer meets which candidate in which round.
create table sprint_pairing (
    id              uuid primary key default gen_random_uuid(),
    sprint_id       uuid not null references sprint(id) on delete cascade,
    org_id          uuid not null references org(id) on delete cascade,
    round           integer not null check (round >= 0),
    interviewer_id  uuid not null references org_user(id) on delete cascade,
    application_id  uuid not null references application(id) on delete cascade,
    unique (sprint_id, round, interviewer_id),
    unique (sprint_id, round, application_id)
);
create index sprint_pairing_interviewer_idx on sprint_pairing (interviewer_id);
create index sprint_pairing_application_idx on sprint_pairing (application_id);

-- What the interviewer thought, filed once per conversation and editable
-- by them alone.
create table sprint_rating (
    id              uuid primary key default gen_random_uuid(),
    org_id          uuid not null references org(id) on delete cascade,
    sprint_id       uuid not null references sprint(id) on delete cascade,
    pairing_id      uuid not null references sprint_pairing(id) on delete cascade,
    interviewer_id  uuid not null references org_user(id) on delete cascade,
    application_id  uuid not null references application(id) on delete cascade,
    score           smallint not null check (score between 1 and 5),
    recommendation  text not null check (recommendation in ('strong_yes', 'yes', 'no', 'strong_no')),
    note            text not null default '',
    created_at      timestamptz not null default now(),
    updated_at      timestamptz not null default now(),
    unique (pairing_id)
);
create index sprint_rating_application_idx on sprint_rating (application_id);

-- The shared editor of a room, as it last stood. The live document is
-- relayed in memory; this row is what survives a restart and what the
-- application page shows afterwards.
create table interview_room (
    id          uuid primary key default gen_random_uuid(),
    org_id      uuid not null references org(id) on delete cascade,
    kind        text not null check (kind in ('slot', 'pairing')),
    subject_id  uuid not null,
    language    text not null default 'python',
    source      text not null default '',
    updated_at  timestamptz not null default now(),
    unique (kind, subject_id)
);

-- +goose StatementBegin
do $$
declare t text;
begin
    foreach t in array array['sprint', 'sprint_interviewer', 'sprint_candidate', 'sprint_pairing', 'sprint_rating', 'interview_room'] loop
        execute format('alter table %I enable row level security', t);
        execute format('alter table %I force row level security', t);
        execute format('create policy tenant on %I for all using (org_id = app_org_id() and not app_is_client()) with check (org_id = app_org_id() and not app_is_client())', t);
    end loop;
end
$$;
-- +goose StatementEnd

grant select, insert, update, delete on sprint, sprint_interviewer, sprint_candidate, sprint_pairing, sprint_rating, interview_room to app_rw;

-- +goose Down
drop table if exists interview_room, sprint_rating, sprint_pairing, sprint_candidate, sprint_interviewer, sprint cascade;
alter table magic_link drop constraint magic_link_purpose_check;
alter table magic_link add constraint magic_link_purpose_check
    check (purpose in ('apply', 'book', 'assessment'));
alter table assessment drop column if exists format;
drop index if exists job_template_idx;
alter table job drop column if exists template_id;
alter table pipeline_template
    drop column if exists description,
    drop column if exists library_key,
    drop column if exists updated_at;
alter table pipeline_template_stage drop constraint pipeline_template_stage_template_id_position_key;
alter table pipeline_template_stage add constraint pipeline_template_stage_template_id_position_key unique (template_id, position);
alter table pipeline_template_stage
    drop column if exists interview_format,
    drop column if exists duration_minutes,
    drop column if exists round_seconds,
    drop column if exists break_seconds,
    drop column if exists terminal_status;
delete from pipeline_template_stage where kind = 'sprint';
alter table pipeline_template_stage drop constraint pipeline_template_stage_kind_check;
alter table pipeline_template_stage add constraint pipeline_template_stage_kind_check
    check (kind in ('generic', 'interview', 'assessment', 'client_review', 'terminal'));
alter table stage
    drop column if exists interview_format,
    drop column if exists duration_minutes,
    drop column if exists round_seconds,
    drop column if exists break_seconds;
delete from stage where kind = 'sprint';
alter table stage drop constraint stage_kind_check;
alter table stage add constraint stage_kind_check
    check (kind in ('generic', 'interview', 'assessment', 'client_review', 'terminal'));
