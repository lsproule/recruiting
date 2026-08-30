-- name: CreateProblem :one
insert into problem (org_id, kind, title, statement, difficulty, tags, allowed_languages, time_limit_ms, memory_limit_kb, sql_schema, sql_seed)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) returning *;

-- name: UpdateProblem :one
update problem set kind = $3, title = $4, statement = $5, difficulty = $6, tags = $7,
    allowed_languages = $8, time_limit_ms = $9, memory_limit_kb = $10, sql_schema = $11,
    sql_seed = $12, updated_at = now()
where id = $1 and org_id = $2 returning *;

-- name: GetProblem :one
select * from problem where id = $1;

-- name: GetProblemByTitle :one
select * from problem where org_id = $1 and lower(title) = lower($2);

-- name: DeleteProblem :execrows
delete from problem where id = $1 and org_id = $2;

-- name: FilterProblems :many
-- RLS shows the caller's own problems and the platform seed; the filters are
-- all optional, an empty string meaning "any".
select * from problem
where (sqlc.arg(kind)::text = '' or kind = sqlc.arg(kind)::text)
  and (sqlc.arg(difficulty)::text = '' or difficulty = sqlc.arg(difficulty)::text)
  and (sqlc.arg(tag)::text = '' or sqlc.arg(tag)::text = any(tags))
  and (sqlc.arg(query)::text = '' or title ilike '%' || sqlc.arg(query)::text || '%')
order by title
limit sqlc.arg(row_limit)::int;

-- name: CreateProblemReference :one
insert into problem_reference (org_id, problem_id, language, source)
values ($1, $2, $3, $4) returning *;

-- name: ListProblemReferences :many
select * from problem_reference where problem_id = $1 order by language;

-- name: DeleteProblemReferences :exec
delete from problem_reference where problem_id = $1;

-- name: CreateTestCase :one
insert into test_case (org_id, problem_id, position, input, expected_output, visibility, weight, unordered)
values ($1, $2, $3, $4, $5, $6, $7, $8) returning *;

-- name: ListTestCases :many
select * from test_case where problem_id = $1 order by position;

-- name: DeleteTestCases :exec
delete from test_case where problem_id = $1;

-- name: CreateAssessment :one
insert into assessment (org_id, name, duration_minutes, language_override, invite_window_days)
values ($1, $2, $3, $4, $5) returning *;

-- name: UpdateAssessment :one
update assessment set name = $3, duration_minutes = $4, language_override = $5, invite_window_days = $6, updated_at = now()
where id = $1 and org_id = $2 returning *;

-- name: GetAssessment :one
select * from assessment where id = $1;

-- name: ListAssessments :many
select * from assessment where org_id = $1 order by name;

-- name: DeleteAssessment :execrows
delete from assessment where id = $1 and org_id = $2;

-- name: CreateAssessmentProblem :exec
insert into assessment_problem (assessment_id, org_id, problem_id, position) values ($1, $2, $3, $4);

-- name: DeleteAssessmentProblems :exec
delete from assessment_problem where assessment_id = $1;

-- name: ListAssessmentProblems :many
select p.* from assessment_problem ap join problem p on p.id = ap.problem_id
where ap.assessment_id = $1 order by ap.position;

-- name: CountAssessmentProblems :many
select assessment_id, count(*)::int as n from assessment_problem where org_id = $1 group by assessment_id;

-- name: SetStageAssessment :execrows
update stage set assessment_id = $3 where id = $1 and job_id = $2;

-- name: CountAttemptsForAssessment :one
select count(*) from attempt where assessment_id = $1;

-- name: CreateAttempt :one
insert into attempt (org_id, application_id, assessment_id, stage_id, invite_expires_at)
values ($1, $2, $3, $4, $5) returning *;

-- name: GetAttempt :one
select * from attempt where id = $1;

-- name: GetAttemptForUpdate :one
select * from attempt where id = $1 for update;

-- name: GetAttemptForApplicationStage :one
select * from attempt where application_id = $1 and stage_id = $2 order by created_at desc limit 1;

-- name: StartAttempt :one
update attempt set status = 'started', started_at = $2, expires_at = $3, updated_at = now()
where id = $1 and status = 'invited' returning *;

-- name: CloseAttempt :one
update attempt set status = $2, finished_at = $3, updated_at = now()
where id = $1 and status = 'started' returning *;

-- name: SetAttemptLastEventSeq :exec
update attempt set last_event_seq = $2, updated_at = now() where id = $1;

-- name: AppendAttemptEvent :exec
insert into attempt_event (org_id, attempt_id, seq, kind, payload, client_ts, problem_id, server_ts)
values ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: ListAttemptEvents :many
select * from attempt_event where attempt_id = $1 order by seq;

-- name: UpsertAttemptSource :exec
insert into attempt_source (attempt_id, org_id, problem_id, language, source, updated_at)
values ($1, $2, $3, $4, $5, $6)
on conflict (attempt_id, problem_id) do update set language = excluded.language, source = excluded.source, updated_at = excluded.updated_at;

-- name: ListAttemptSources :many
select * from attempt_source where attempt_id = $1 order by problem_id;

-- name: CreateSubmission :one
insert into submission (org_id, attempt_id, problem_id, kind, language, source)
values ($1, $2, $3, $4, $5, $6) returning *;

-- name: GetSubmission :one
select * from submission where id = $1 and attempt_id = $2;

-- name: ListSubmissions :many
select * from submission where attempt_id = $1 order by created_at, id;

-- name: ListDueAttemptOrgs :many
select org_id::uuid from due_attempt_orgs(sqlc.arg(at)::timestamptz) as t(org_id);

-- name: ListDueAttempts :many
select * from attempt where status = 'started' and expires_at <= $1 order by expires_at for update skip locked;

-- name: CountPendingSubmissions :one
select count(*) from submission where attempt_id = $1 and problem_id = $2 and status in ('queued', 'running');

-- name: ExtendMagicLinkExpiry :exec
-- Only ever extends: a link is never cut short by the attempt it opens.
update magic_link set expires_at = greatest(expires_at, $3)
where purpose = $1 and subject_id = $2 and revoked_at is null;

-- name: GetSubmissionForUpdate :one
select * from submission where id = $1 for update;

-- name: StartSubmission :exec
update submission set status = 'running', updated_at = now() where id = $1;

-- name: FinishSubmission :exec
update submission set status = $2, result = $3, score = $4, updated_at = now() where id = $1;

-- name: CountAttemptEvents :one
select count(*) from attempt_event where attempt_id = $1;

-- name: CountAttemptSubmissionErrors :one
select count(*) from submission where attempt_id = $1 and status = 'error';

-- name: ScoreAttempt :one
-- Only a closed attempt is scored, and only once: concurrent deliveries of
-- attempt.finalize race here and the losers match no row.
update attempt set status = 'scored', score = $2, problem_scores = $3, error_count = $4,
    recording_status = $5, recording_blob_key = $6, updated_at = now()
where id = $1 and status in ('submitted', 'expired') returning *;

-- name: DeleteIntegritySignals :exec
delete from integrity_signal where attempt_id = $1;

-- name: CreateIntegritySignal :exec
insert into integrity_signal (org_id, attempt_id, name, value, weight, confidence, evidence)
values ($1, $2, $3, $4, $5, $6, $7);

-- name: ListIntegritySignals :many
select * from integrity_signal where attempt_id = $1 order by name;

-- name: SetAttemptRiskScore :exec
update attempt set risk_score = $2, updated_at = now() where id = $1;

-- name: ListOtherProblemSubmits :many
-- The latest submit of the problem, in the language, by each of the 200
-- most recent other attempts RLS lets the caller see (every attempt of
-- the org).
select attempt_id, source from (
    select distinct on (attempt_id) attempt_id, source, created_at from submission
    where problem_id = $1 and attempt_id <> $2 and kind = 'submit' and language = $3
    order by attempt_id, created_at desc
) latest order by created_at desc limit 200;

-- name: ListVetterAttemptReviews :many
-- The closed attempts waiting on the signed-in vetter, with their verdict if
-- one is filed. The application's own vetter decides; until one is set the
-- stage's default stands in, the same rule the scorecard queue uses.
select t.id as attempt_id, t.application_id, t.stage_id, t.status, t.score, t.risk_score,
    t.error_count, t.recording_status, t.finished_at,
    c.name as candidate_name, c.email as candidate_email,
    j.title as job_title, st.name as stage_name, r.verdict as verdict
from attempt t
join application a on a.id = t.application_id
join candidate c on c.id = a.candidate_id
join job j on j.id = a.job_id
join stage st on st.id = t.stage_id
left join review r on r.attempt_id = t.id
where t.status in ('scored', 'reviewed')
  and coalesce(a.vetter_id, st.default_vetter_id) = $1
order by t.finished_at desc nulls last, t.id;

-- name: GetReviewForAttempt :one
select r.id, r.org_id, r.attempt_id, r.vetter_id, r.verdict, r.notes, r.created_at,
    u.name as vetter_name
from review r join org_user u on u.id = r.vetter_id
where r.attempt_id = $1;

-- name: UpsertReviewByAuthor :one
-- One review per attempt. A second reviewer matches no row rather than
-- overwriting the verdict the first one filed.
insert into review (org_id, attempt_id, vetter_id, verdict, notes)
values ($1, $2, $3, $4, $5)
on conflict (attempt_id) do update set verdict = excluded.verdict, notes = excluded.notes
where review.vetter_id = excluded.vetter_id
returning *;

-- name: SetAttemptReviewed :exec
update attempt set status = 'reviewed', updated_at = now() where id = $1 and status in ('scored', 'reviewed');

-- name: ListAttemptSummariesForApplication :many
-- The score and verdict of every attempt on an application, for the
-- recruiter's summary of it.
select t.id as attempt_id, t.stage_id, t.status, t.score, t.risk_score, t.error_count, t.finished_at,
    st.name as stage_name, r.verdict as verdict, r.notes as review_notes,
    r.created_at as reviewed_at, u.name as vetter_name
from attempt t
join stage st on st.id = t.stage_id
left join review r on r.attempt_id = t.id
left join org_user u on u.id = r.vetter_id
where t.application_id = $1
order by t.created_at;

-- name: ListAttemptEventsAfter :many
-- One page of the recording, in seq order: the replay viewer pages through
-- the stream rather than loading a whole sitting at once.
select * from attempt_event where attempt_id = $1 and seq > $2 order by seq limit sqlc.arg(row_limit)::int;

-- name: ListEditedProblems :many
-- The problems the recording holds an edit for. The rest never changed, so
-- the text still synced for them is the text they started from.
select distinct problem_id from attempt_event where attempt_id = $1 and kind = 'edit' and problem_id is not null;
