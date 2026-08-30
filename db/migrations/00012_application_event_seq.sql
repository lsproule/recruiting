-- +goose Up
-- Two events written in the same transaction share created_at to the
-- microsecond and ids are random, so the timeline needs its own order.
alter table application_event add column seq bigserial;
create index application_event_seq_idx on application_event (application_id, seq);

-- +goose Down
drop index if exists application_event_seq_idx;
alter table application_event drop column if exists seq;
