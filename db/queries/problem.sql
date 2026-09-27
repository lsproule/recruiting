-- name: CreateProblem :one
insert into problem (org_id, kind, title, statement, difficulty, tags, allowed_languages,
    time_limit_ms, memory_limit_kb, sql_schema, sql_seed, signature, guidelines,
    origin_problem_id, quality, proven_languages)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16) returning *;

-- name: UpdateProblem :one
update problem set kind = $3, title = $4, statement = $5, difficulty = $6, tags = $7,
    allowed_languages = $8, time_limit_ms = $9, memory_limit_kb = $10, sql_schema = $11,
    sql_seed = $12, signature = $13, guidelines = $14, quality = $15,
    proven_languages = $16, updated_at = now()
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
select problem.*, (select count(*) from test_case where test_case.problem_id = problem.id)::int as case_count
from problem
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
insert into test_case (org_id, problem_id, position, input, expected_output, visibility,
    weight, unordered, name, class)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) returning *;

-- name: ListTestCases :many
-- Every case with its payload. A perf case's input runs to megabytes, so
-- only a path that hands the cases to the runner reads this; everything
-- else reads ListTestCaseMeta.
select * from test_case where problem_id = $1 order by position;

-- name: ListTestCaseMeta :many
-- Everything about a problem's cases but the payloads: the byte size of
-- each stands in for it, which is all a poll, a review, or a score needs.
select id, position, name, class, visibility, weight, unordered,
    octet_length(input)::int as input_bytes, octet_length(expected_output)::int as expected_bytes
from test_case where problem_id = $1 order by position;

-- name: ListPublicTestCases :many
-- The cases a candidate may see, payloads included: the worked examples,
-- which are small by construction.
select * from test_case where problem_id = $1 and visibility = 'public' order by position;

-- name: ListTestCasesWithin :many
-- The cases whose payloads both fit in max_bytes, which is what a form can
-- inline; the larger ones are edited by reference.
select * from test_case where problem_id = $1
  and octet_length(input) <= sqlc.arg(max_bytes)::int
  and octet_length(expected_output) <= sqlc.arg(max_bytes)::int
order by position;

-- name: GetTestCase :one
select * from test_case where problem_id = $1 and id = $2;

-- name: DeleteTestCases :exec
delete from test_case where problem_id = $1;
