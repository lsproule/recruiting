-- +goose Up

-- The river job ids of the slot's pending reminders, so cancelling or
-- rescheduling the booking can cancel the reminders in the same transaction.
alter table interview_slot add column remind_job_ids bigint[] not null default '{}';

-- +goose Down
alter table interview_slot drop column remind_job_ids;
