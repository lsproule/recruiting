-- +goose Up

-- A cancelled slot must not hold its time: only live rows contend for a
-- vetter's start time, so a cancelled booking can be taken again.
alter table interview_slot drop constraint interview_slot_vetter_id_starts_at_key;
create unique index interview_slot_vetter_start_live_idx on interview_slot (vetter_id, starts_at) where status <> 'cancelled';

-- +goose Down
drop index if exists interview_slot_vetter_start_live_idx;
alter table interview_slot add constraint interview_slot_vetter_id_starts_at_key unique (vetter_id, starts_at);
