-- +goose Up

-- What last put the candidate in the pool: a recruiter's flag, a strong-yes
-- scorecard, or a passing assessment review. Recruiters read it to know why
-- an entry is there.
alter table talent_pool_entry add column source text not null default 'recruiter_flag'
    check (source in ('recruiter_flag', 'scorecard_strong_yes', 'assessment_review'));

-- Browsing and searching the pool both walk an org's live entries, newest
-- touched first.
create index talent_pool_entry_org_idx on talent_pool_entry (org_id, updated_at desc) where removed_at is null;

-- +goose Down
drop index if exists talent_pool_entry_org_idx;
alter table talent_pool_entry drop column if exists source;
