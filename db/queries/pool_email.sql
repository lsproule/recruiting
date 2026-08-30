-- name: ListTalentPoolEntries :many
select * from talent_pool_entry where org_id = $1 and removed_at is null order by updated_at desc;

-- name: CreateEmailLog :one
insert into email_log (org_id, template, to_email, subject) values ($1, $2, $3, $4) returning *;

-- name: UpdateEmailLogStatus :exec
update email_log set status = $2, attempts = attempts + 1, last_error = $3, sent_at = case when $2 = 'sent' then now() else sent_at end where id = $1;

-- name: UpsertEmailLogForJob :one
insert into email_log (org_id, template, to_email, subject, job_id)
values ($1, $2, $3, $4, $5)
on conflict (job_id) where job_id is not null
do update set subject = excluded.subject
returning *;

-- name: GetEmailLogByJob :one
select * from email_log where job_id = $1;
