-- +goose Up

-- An assessment stage can decide for itself: a pass mark out of 100 and,
-- optionally, what the score does on its own. With auto_advance a sitting at
-- or above the mark moves the application to the next stage; with
-- auto_reject one below it closes the application as rejected, which sends
-- the candidate the org's rejection email. A stage with neither leaves the
-- verdict to a person, as before. The same settings live on template stages
-- so a process carries them into every job built from it.
alter table stage
    add column pass_score integer check (pass_score between 0 and 100),
    add column auto_advance boolean not null default false,
    add column auto_reject boolean not null default false;
alter table pipeline_template_stage
    add column pass_score integer check (pass_score between 0 and 100),
    add column auto_advance boolean not null default false,
    add column auto_reject boolean not null default false;

-- +goose Down
alter table pipeline_template_stage
    drop column if exists pass_score,
    drop column if exists auto_advance,
    drop column if exists auto_reject;
alter table stage
    drop column if exists pass_score,
    drop column if exists auto_advance,
    drop column if exists auto_reject;
