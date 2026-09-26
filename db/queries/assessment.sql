-- name: CreateAssessment :one
insert into assessment (org_id, name, duration_minutes, language_override, invite_window_days, allowed_languages, integrity, format)
values ($1, $2, $3, $4, $5, $6, $7, $8) returning *;

-- name: UpdateAssessment :one
update assessment set name = $3, duration_minutes = $4, language_override = $5, invite_window_days = $6,
    allowed_languages = $7, integrity = $8, format = $9, updated_at = now()
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
-- A preview is the recruiter's own sitting and never stands in the way of
-- deleting the assessment it previewed.
select count(*) from attempt where assessment_id = $1 and not preview;

-- name: CreateAttempt :one
insert into attempt (org_id, application_id, assessment_id, stage_id, invite_expires_at)
values (sqlc.arg(org_id), sqlc.arg(application_id)::uuid, sqlc.arg(assessment_id), sqlc.arg(stage_id)::uuid,
        sqlc.arg(invite_expires_at)) returning *;

-- name: CreatePreviewAttempt :one
-- A recruiter sitting their own assessment: no application, no stage, and
-- nothing downstream reads it.
insert into attempt (org_id, assessment_id, preview, preview_user_id, invite_expires_at)
values ($1, $2, true, $3, $4) returning *;

-- name: ListStalePreviewOrgs :many
select org_id::uuid from stale_preview_orgs(sqlc.arg(before)::timestamptz) as t(org_id);

-- name: ListStalePreviewAttempts :many
select * from attempt where preview and created_at < $1 order by created_at for update skip locked;

-- name: DeleteAttempt :exec
-- Events, sources, and submissions cascade from the attempt row.
delete from attempt where id = $1 and preview;

-- name: DeleteMagicLinksForSubject :exec
delete from magic_link where purpose = $1 and subject_id = $2;

-- name: GetAttempt :one
select * from attempt where id = $1;

-- name: GetAttemptForUpdate :one
select * from attempt where id = $1 for update;

-- name: GetAttemptForApplicationStage :one
select * from attempt where application_id = sqlc.arg(application_id)::uuid and stage_id = sqlc.arg(stage_id)::uuid
order by created_at desc limit 1;

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
    select distinct on (s.attempt_id) s.attempt_id, s.source, s.created_at from submission s
    join attempt t on t.id = s.attempt_id and not t.preview
    where s.problem_id = $1 and s.attempt_id <> $2 and s.kind = 'submit' and s.language = $3
    order by s.attempt_id, s.created_at desc
) latest order by created_at desc limit 200;

-- name: ListVetterAttemptReviews :many
-- The closed attempts waiting on the signed-in vetter, with their verdict if
-- one is filed. The application's own vetter decides; until one is set the
-- stage's default stands in, the same rule the scorecard queue uses.
select t.id as attempt_id, t.application_id::uuid as application_id, t.stage_id::uuid as stage_id,
    t.status, t.score, t.risk_score,
    t.error_count, t.recording_status, t.finished_at,
    c.name as candidate_name, c.email as candidate_email,
    j.title as job_title, st.name as stage_name, r.verdict as verdict
from attempt t
join application a on a.id = t.application_id
join candidate c on c.id = a.candidate_id
join job j on j.id = a.job_id
join stage st on st.id = t.stage_id
left join review r on r.attempt_id = t.id
where t.status in ('scored', 'reviewed') and not t.preview
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
select t.id as attempt_id, t.stage_id::uuid as stage_id, t.status, t.score, t.risk_score, t.error_count, t.finished_at,
    st.name as stage_name, r.verdict as verdict, r.notes as review_notes,
    r.created_at as reviewed_at, u.name as vetter_name
from attempt t
join stage st on st.id = t.stage_id
left join review r on r.attempt_id = t.id
left join org_user u on u.id = r.vetter_id
where t.application_id = sqlc.arg(application_id)::uuid
order by t.created_at;

-- name: ListAttemptEventsAfter :many
-- One page of the recording, in seq order: the replay viewer pages through
-- the stream rather than loading a whole sitting at once.
select * from attempt_event where attempt_id = $1 and seq > $2 order by seq limit sqlc.arg(row_limit)::int;

-- name: ListEditedProblems :many
-- The problems the recording holds an edit for. The rest never changed, so
-- the text still synced for them is the text they started from.
select distinct problem_id from attempt_event where attempt_id = $1 and kind = 'edit' and problem_id is not null;

-- name: UpsertAttemptSnapshot :one
-- The beat is the session's own counter, so a frame uploaded twice replaces
-- the first rather than doubling the row and orphaning its object.
insert into attempt_snapshot (org_id, attempt_id, seq, taken_at, blob_key, bytes)
values ($1, $2, $3, $4, $5, $6)
on conflict (attempt_id, seq) do update
    set taken_at = excluded.taken_at, blob_key = excluded.blob_key, bytes = excluded.bytes
returning *;

-- name: ListAttemptSnapshots :many
select * from attempt_snapshot where attempt_id = $1 order by seq;

-- name: SetAttemptConsentAt :one
-- Consent is given before the timer starts; a session already under way has
-- nothing left to agree to.
update attempt set consent_at = $2, updated_at = now()
where id = $1 and status = 'invited' returning *;

-- name: SetAttemptIdentityBlobKey :one
-- The frame is taken once: an attempt that already has one matches no row.
update attempt set identity_blob_key = $2, updated_at = now()
where id = $1 and identity_blob_key is null returning *;

-- name: ListSnapshotOrgs :many
select org_id::uuid from snapshot_orgs(sqlc.arg(before)::timestamptz) as t(org_id);

-- name: ListSnapshotsTakenBefore :many
select * from attempt_snapshot where taken_at < $1 order by taken_at for update skip locked;

-- name: DeleteAttemptSnapshot :exec
delete from attempt_snapshot where id = $1;

-- name: ListAttemptInvites :many
-- The org's assessment sittings for the recruiter's Assessments screen: what
-- each candidate was sent, how much of the set they have submitted, and how
-- many events the recording flagged. A null status asks for all of them.
select t.id as attempt_id, t.application_id::uuid as application_id, t.stage_id::uuid as stage_id,
    t.status, t.invited_at, t.invite_expires_at, t.expires_at, t.finished_at, t.score, t.risk_score,
    c.name as candidate_name, c.email as candidate_email,
    j.title as job_title, cc.name as client_company_name, a.name as assessment_name,
    (select count(*) from assessment_problem ap where ap.assessment_id = t.assessment_id)::int as problems_total,
    (select count(distinct s.problem_id) from submission s
        where s.attempt_id = t.id and s.kind = 'submit')::int as problems_submitted,
    (select count(*) from attempt_event e
        where e.attempt_id = t.id and e.kind in ('blur', 'paste', 'fullscreen_exit'))::int as integrity_flags
from attempt t
join application app on app.id = t.application_id
join candidate c on c.id = app.candidate_id
join job j on j.id = app.job_id
join client_company cc on cc.id = j.client_company_id
join assessment a on a.id = t.assessment_id
where not t.preview
  and (sqlc.narg(status)::text is null or t.status = sqlc.narg(status)::text)
order by t.invited_at desc, t.id;

-- name: ExpireInvitedAttempt :one
-- A revoked invite the candidate never opened: there is no work to close
-- over, so it is marked expired where it stands.
update attempt set status = 'expired', finished_at = $2, updated_at = now()
where id = $1 and status = 'invited' returning *;
