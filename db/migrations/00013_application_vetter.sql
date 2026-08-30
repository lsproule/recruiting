-- +goose Up

-- The interviewer for an application's phone screen. Set by a recruiter, or
-- copied from the stage's default when the candidate first opens the booking
-- page; the booking page has no one to offer slots for without it.
alter table application add column vetter_id uuid references org_user(id) on delete set null;

-- +goose Down
alter table application drop column if exists vetter_id;
