-- Shortlist packets: the recruiter's ranked recommendation to the client.
-- The pool and pick reads join the attempt, which no client policy admits,
-- so they run under an org scope; the client's own reads of the packet and
-- its picks are limited to sent packets of their company by RLS.

-- name: CreateShortlistPacket :one
insert into shortlist_packet (org_id, job_id, note, created_by)
values ($1, $2, $3, $4)
returning *;

-- name: GetShortlistPacket :one
select * from shortlist_packet where id = $1;

-- name: GetShortlistPacketForUpdate :one
select * from shortlist_packet where id = $1 for update;

-- name: UpdateShortlistPacketNote :one
-- Only a draft is writable; a sent packet is what the client already read.
update shortlist_packet set note = $2, updated_at = now()
where id = $1 and status = 'draft'
returning *;

-- name: MarkShortlistPacketSent :one
update shortlist_packet
set status = 'sent', sent_at = now(), sent_by = sqlc.arg(sent_by)::uuid, updated_at = now()
where id = sqlc.arg(id)::uuid and status = 'draft'
returning *;

-- name: ListShortlistPacketsByJob :many
select * from shortlist_packet where job_id = $1 order by created_at desc;

-- name: ListSentShortlistPacketsByCompany :many
select p.* from shortlist_packet p
join job j on j.id = p.job_id
where j.client_company_id = $1 and p.status = 'sent'
order by p.sent_at desc;

-- name: GetLatestSentShortlistPacketForJob :one
select * from shortlist_packet
where job_id = $1 and status = 'sent'
order by sent_at desc
limit 1;

-- name: DeleteShortlistPicks :exec
delete from shortlist_pick where packet_id = $1;

-- name: CreateShortlistPick :exec
insert into shortlist_pick (org_id, packet_id, application_id, rank)
values ($1, $2, $3, $4);

-- name: ListShortlistPicks :many
-- Rank order is the packet's meaning, so it is the only order this returns.
select k.application_id, k.rank, c.name as candidate_name, a.status as application_status,
    (select max(t.score) from attempt t
        where t.application_id = k.application_id and not t.preview and t.score is not null)::numeric as score
from shortlist_pick k
join application a on a.id = k.application_id
join candidate c on c.id = a.candidate_id
where k.packet_id = $1
order by k.rank;

-- name: GetShortlistPick :one
select k.application_id, k.rank, k.packet_id from shortlist_pick k
where k.packet_id = $1 and k.application_id = $2;

-- name: ListShortlistPool :many
-- The applications a shortlist may be built from: on the job, still open,
-- and carrying a scored sitting of their own. Preview sittings are the
-- recruiter's and never qualify anyone.
select a.id as application_id, a.candidate_id, a.status, c.name as candidate_name,
    s.name as stage_name,
    max(t.score)::numeric as score
from application a
join candidate c on c.id = a.candidate_id
join stage s on s.id = a.stage_id
join attempt t on t.application_id = a.id
where a.job_id = $1 and not t.preview and t.status in ('scored', 'reviewed') and t.score is not null
group by a.id, c.name, s.name
order by score desc, c.name;

-- name: GetScoredAttemptForApplication :one
-- The sitting a shortlisted candidate is read through: their best scored
-- one, which is the score the pool ranked them on.
select t.* from attempt t
where t.application_id = sqlc.arg(application_id)::uuid and not t.preview and t.score is not null
order by t.score desc, t.finished_at desc
limit 1;
