-- +goose Up

-- Every interviewer on a stage files their own card, so the key carries the
-- vetter as well as the application and the stage.
alter table scorecard drop constraint scorecard_application_id_stage_id_key;
alter table scorecard add constraint scorecard_application_stage_vetter_key
    unique (application_id, stage_id, vetter_id);

-- The criteria as they read when the card was filed. Later rubric edits must
-- not rewrite what an interviewer was asked.
alter table scorecard add column criteria jsonb not null default '[]';
alter table scorecard add column updated_at timestamptz not null default now();

-- +goose Down
alter table scorecard drop column if exists updated_at;
alter table scorecard drop column if exists criteria;
alter table scorecard drop constraint if exists scorecard_application_stage_vetter_key;
alter table scorecard add constraint scorecard_application_id_stage_id_key unique (application_id, stage_id);
