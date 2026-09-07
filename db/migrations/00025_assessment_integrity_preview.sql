-- +goose Up

-- An assessment narrows the languages its problems offer and states the
-- integrity measures the session runs under. An empty language set defers to
-- each problem; the integrity object is the settings block the candidate
-- session boots from.
alter table assessment add column allowed_languages text[] not null default '{}';
alter table assessment add column integrity jsonb not null default '{}';

-- A recruiter can sit the assessment themselves before a candidate does. A
-- preview belongs to the org user who opened it rather than to an
-- application, so it carries no candidate, moves no card, and is swept away
-- a day later.
alter table attempt add column preview boolean not null default false;
alter table attempt add column preview_user_id uuid references org_user(id) on delete cascade;
alter table attempt alter column application_id drop not null;
alter table attempt alter column stage_id drop not null;
alter table attempt add constraint attempt_preview_shape check (
    case when preview
        then application_id is null and stage_id is null and preview_user_id is not null
        else application_id is not null and stage_id is not null and preview_user_id is null
    end
);
create index attempt_preview_idx on attempt (created_at) where preview;

-- The webcam frame and the photo of an ID are the candidate's own; the
-- consent they gave and where the frames live are recorded on the attempt.
alter table attempt add column consent_at timestamptz;
alter table attempt add column identity_blob_key text;

-- The recording now carries the keymap the candidate chose and the
-- integrity beats the session emits.
alter table attempt_event drop constraint attempt_event_kind_check;
alter table attempt_event add constraint attempt_event_kind_check check (kind in (
    'edit', 'paste', 'focus', 'blur', 'run', 'submit', 'lang_change',
    'keymap', 'fullscreen_enter', 'fullscreen_exit', 'snapshot', 'consent'));

-- The purge sweeps every org, but app_rw sees no row without an org set.
-- This returns only the org ids holding expired previews, so the sweep can
-- enter each one under its own scope; nothing else escapes.
-- +goose StatementBegin
create function stale_preview_orgs(before timestamptz) returns setof uuid
language sql security definer set search_path = public as $$
    select distinct org_id from attempt where preview and created_at < before
$$;
-- +goose StatementEnd
grant execute on function stale_preview_orgs(timestamptz) to app_rw;

-- +goose Down
drop function if exists stale_preview_orgs(timestamptz);
alter table attempt_event drop constraint attempt_event_kind_check;
alter table attempt_event add constraint attempt_event_kind_check check (kind in (
    'edit', 'paste', 'focus', 'blur', 'run', 'submit', 'lang_change'));
alter table attempt drop column if exists identity_blob_key;
alter table attempt drop column if exists consent_at;
drop index if exists attempt_preview_idx;
alter table attempt drop constraint if exists attempt_preview_shape;
delete from attempt where preview;
alter table attempt alter column stage_id set not null;
alter table attempt alter column application_id set not null;
alter table attempt drop column if exists preview_user_id;
alter table attempt drop column if exists preview;
alter table assessment drop column if exists integrity;
alter table assessment drop column if exists allowed_languages;
