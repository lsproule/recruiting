-- +goose Up

-- A problem offers a reference solution per language it allows, not one
-- overall: an import proves the problem is solvable in every language a
-- candidate may pick.
create table problem_reference (
    id          uuid primary key default gen_random_uuid(),
    org_id      uuid not null references org(id) on delete cascade,
    problem_id  uuid not null references problem(id) on delete cascade,
    language    text not null,
    source      text not null,
    unique (problem_id, language)
);

-- +goose StatementBegin
do $$
declare r record;
begin
    for r in select id, org_id, reference_language, reference_solution from problem
             where reference_language is not null and reference_solution is not null loop
        insert into problem_reference (org_id, problem_id, language, source)
        values (r.org_id, r.id, r.reference_language, r.reference_solution);
    end loop;
end
$$;
-- +goose StatementEnd

alter table problem drop column reference_language;
alter table problem drop column reference_solution;

-- SQL result sets are compared as a multiset when row order is not part of
-- the answer.
alter table test_case add column unordered boolean not null default false;

-- The bank is browsed by kind, difficulty, and tag, over the org's own rows
-- and the platform seed alike.
create index problem_bank_idx on problem (org_id, kind, difficulty);
create index problem_tags_idx on problem using gin (tags);

-- A title names a problem in the bank; the platform seed re-imports by title.
create unique index problem_org_title_idx on problem (org_id, lower(title));

alter table problem_reference enable row level security;
alter table problem_reference force row level security;
create policy tenant on problem_reference for all
    using (not app_is_client() and org_id = app_org_id())
    with check (not app_is_client() and org_id = app_org_id());
create policy platform_read on problem_reference for select
    using (not app_is_client() and org_id = platform_org_id());

grant select, insert, update, delete on problem_reference to app_rw;

-- +goose Down
drop index if exists problem_org_title_idx;
drop index if exists problem_tags_idx;
drop index if exists problem_bank_idx;
alter table test_case drop column if exists unordered;

-- The old columns hold one solution, so rolling back keeps the first language
-- of each problem alphabetically and loses the rest.
alter table problem add column reference_language text;
alter table problem add column reference_solution text;
update problem p set reference_language = r.language, reference_solution = r.source
from (select distinct on (problem_id) problem_id, language, source
      from problem_reference order by problem_id, language) r
where r.problem_id = p.id;

drop table if exists problem_reference;
