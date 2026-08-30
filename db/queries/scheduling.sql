-- name: ListAvailabilityRules :many
select * from availability_rule where vetter_id = $1 order by weekday, start_time;

-- name: ListAvailabilityExceptions :many
select * from availability_exception where vetter_id = $1 and ends_at > $2 and starts_at < $3;

-- name: ListInterviewSlots :many
select * from interview_slot where vetter_id = $1 and status <> 'cancelled' and ends_at > $2 and starts_at < $3;

-- name: CreateInterviewSlot :one
insert into interview_slot (org_id, vetter_id, application_id, stage_id, candidate_timezone, starts_at, ends_at)
values ($1, $2, $3, $4, $5, $6, $7) returning *;

-- name: UpdateInterviewSlotStatus :one
update interview_slot set status = $2, updated_at = now() where id = $1 returning *;

-- name: DeleteAvailabilityRules :exec
delete from availability_rule where vetter_id = $1;

-- name: CreateAvailabilityRule :one
insert into availability_rule (org_id, vetter_id, weekday, start_time, end_time, timezone, buffer_minutes, slot_minutes, valid_from, valid_to)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) returning *;

-- name: CreateAvailabilityException :one
insert into availability_exception (org_id, vetter_id, starts_at, ends_at, reason)
values ($1, $2, $3, $4, $5) returning *;

-- name: DeleteAvailabilityException :execrows
delete from availability_exception where id = $1 and vetter_id = $2;

-- name: GetInterviewSlot :one
select * from interview_slot where id = $1;

-- name: GetBookedSlotForApplication :one
select * from interview_slot where application_id = $1 and status = 'booked' order by starts_at desc limit 1;

-- name: SetInterviewSlotOutcome :one
-- Only the slot's own vetter records how it went.
update interview_slot set status = $3, updated_at = now()
where id = $1 and vetter_id = $2 and status in ('booked', 'completed', 'no_show') returning *;

-- name: ListVetterSlots :many
-- A vetter's calendar: booked and finished interviews with who they are with.
select s.*, c.name as candidate_name, c.email as candidate_email, j.title as job_title
from interview_slot s
join application a on a.id = s.application_id
join candidate c on c.id = a.candidate_id
join job j on j.id = a.job_id
where s.vetter_id = $1 and s.status <> 'cancelled' and s.ends_at > $2
order by s.starts_at;

-- name: SetApplicationVetter :one
update application set vetter_id = $2, updated_at = now() where id = $1 returning *;

-- name: SetOrgUserTimezone :exec
update org_user set timezone = $3 where id = $1 and org_id = $2;
