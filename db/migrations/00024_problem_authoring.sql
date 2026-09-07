-- +goose Up

-- The authoring wizard records what a recruiter needs to run the problem in
-- an interview and what the bank needs to judge it: how long it should take,
-- what an interviewer watches for (never shown to a candidate or a client),
-- where a clone came from, and the quality score with the languages a
-- reference solution has actually proven.
alter table problem add column recommended_minutes integer not null default 45
    check (recommended_minutes > 0 and recommended_minutes <= 480);
alter table problem add column guidelines text not null default '';
alter table problem add column origin_problem_id uuid references problem(id) on delete set null;
alter table problem add column quality integer not null default 0
    check (quality between 0 and 100);
alter table problem add column proven_languages text[] not null default '{}';

-- A case is named and classed so the reference run and the reviewer's result
-- table say which case failed rather than which row number.
alter table test_case add column name text not null default '';
alter table test_case add column class text not null default 'core'
    check (class in ('sample', 'edge', 'perf', 'core'));

-- Existing rows predate verification; every stored problem was proven against
-- the runner at write time, so its allowed languages with a reference are its
-- proven set.
-- +goose StatementBegin
do $$
begin
    update problem p set proven_languages = coalesce(r.langs, '{}')
    from (select problem_id, array_agg(language order by language) as langs
          from problem_reference group by problem_id) r
    where r.problem_id = p.id;
end
$$;
-- +goose StatementEnd

-- A sample case is the one the candidate sees, so public cases carry that
-- class rather than the 'core' default.
update test_case set class = 'sample' where visibility = 'public';

-- +goose Down
alter table test_case drop column if exists class;
alter table test_case drop column if exists name;
alter table problem drop column if exists proven_languages;
alter table problem drop column if exists quality;
alter table problem drop column if exists origin_problem_id;
alter table problem drop column if exists guidelines;
alter table problem drop column if exists recommended_minutes;
