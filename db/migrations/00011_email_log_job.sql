-- +goose Up

-- A retried email job must update its own log row rather than add another,
-- so the row is keyed by the queue job that owns it. Jobs enqueued before
-- this column existed, and any row written outside a job, leave it null.
alter table email_log add column job_id bigint;
create unique index email_log_job_idx on email_log (job_id) where job_id is not null;

-- +goose Down
drop index if exists email_log_job_idx;
alter table email_log drop column if exists job_id;
