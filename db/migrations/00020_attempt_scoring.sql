-- +goose Up

-- Finalization writes the whole score here: the attempt's mean, and the
-- per-problem breakdown the portal and the review screen read without
-- re-deriving anything from submissions.
alter table attempt add column problem_scores jsonb not null default '[]';

-- How many submissions of the attempt the runner never answered for. The
-- candidate may resubmit within the window; the vetter sees the count so a
-- low score against a broken runner is not read as a weak candidate.
alter table attempt add column error_count integer not null default 0;

-- +goose Down
alter table attempt drop column if exists error_count;
alter table attempt drop column if exists problem_scores;
