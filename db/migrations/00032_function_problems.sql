-- +goose Up

-- A function problem asks the candidate for one function of a declared
-- signature rather than a whole program: every test case calls it with
-- typed arguments and compares what comes back. The signature is stored as
-- the JSON the harness reads. A code problem (a program over stdin) and a
-- SQL problem are unchanged.
alter table problem drop constraint problem_kind_check;
alter table problem add constraint problem_kind_check check (kind in ('function', 'code', 'sql'));
alter table problem add column signature jsonb;

-- How long a problem "should" take was a guess nobody could make; the
-- recruiter sets an assessment's length directly.
alter table problem drop column recommended_minutes;

-- +goose Down
alter table problem add column recommended_minutes integer not null default 45;
alter table problem drop column signature;
-- A function problem cannot exist below this version; everything that points
-- at one goes with it.
delete from assessment_problem where problem_id in (select id from problem where kind = 'function');
delete from submission where problem_id in (select id from problem where kind = 'function');
delete from problem where kind = 'function';
alter table problem drop constraint problem_kind_check;
alter table problem add constraint problem_kind_check check (kind in ('code', 'sql'));
