-- +goose Up

-- What a candidate's recording has used so far, kept on the attempt so
-- ingest can refuse a batch past the per-attempt ceiling under the row lock
-- without counting the event rows on every call. Events the server writes
-- itself (webcam beats, consent) are bounded by the session's own schedule
-- and are not counted here.
alter table attempt add column recording_events bigint not null default 0;
alter table attempt add column recording_bytes bigint not null default 0;

-- Set once a batch was refused for the ceiling: the replay ends where the
-- candidate's session did not, and a reviewer must know that.
alter table attempt add column recording_truncated boolean not null default false;

-- +goose Down
alter table attempt drop column if exists recording_truncated;
alter table attempt drop column if exists recording_bytes;
alter table attempt drop column if exists recording_events;
