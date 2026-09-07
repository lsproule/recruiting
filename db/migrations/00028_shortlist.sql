-- +goose Up

-- The recruiter's recommendation to the client: a ranked handful of
-- applications and the note that explains them. A packet is a draft until it
-- is sent; sending releases its picks and freezes the packet, so a change of
-- mind is a new packet rather than an edit the client never sees.
create table shortlist_packet (
    id         uuid primary key default gen_random_uuid(),
    org_id     uuid not null references org(id) on delete cascade,
    job_id     uuid not null references job(id) on delete cascade,
    status     text not null default 'draft' check (status in ('draft', 'sent')),
    note       text not null default '',
    sent_at    timestamptz,
    sent_by    uuid references org_user(id) on delete set null,
    created_by uuid not null references org_user(id) on delete restrict,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);
create index shortlist_packet_job_idx on shortlist_packet (job_id, created_at desc);

-- One pick of a packet. Rank is what the client reads first, so it is unique
-- within the packet: two candidates cannot both be the recommendation.
create table shortlist_pick (
    id             uuid primary key default gen_random_uuid(),
    org_id         uuid not null references org(id) on delete cascade,
    packet_id      uuid not null references shortlist_packet(id) on delete cascade,
    application_id uuid not null references application(id) on delete cascade,
    rank           integer not null check (rank >= 1),
    unique (packet_id, application_id),
    unique (packet_id, rank)
);

alter table shortlist_packet enable row level security;
alter table shortlist_packet force row level security;
create policy tenant on shortlist_packet for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
-- A client reads sent packets of their own company and nothing else: a draft
-- is the recruiter's working copy.
create policy client_read on shortlist_packet for select
    using (org_id = app_org_id() and app_is_client() and status = 'sent'
        and exists (select 1 from job j where j.id = shortlist_packet.job_id
            and j.client_company_id = app_client_company_id()));
grant select, insert, update, delete on shortlist_packet to app_rw;

alter table shortlist_pick enable row level security;
alter table shortlist_pick force row level security;
create policy tenant on shortlist_pick for all
    using (org_id = app_org_id() and not app_is_client())
    with check (org_id = app_org_id() and not app_is_client());
-- The packet subquery is itself RLS-filtered, so a pick follows its packet.
create policy client_read on shortlist_pick for select
    using (org_id = app_org_id() and app_is_client()
        and exists (select 1 from shortlist_packet p where p.id = shortlist_pick.packet_id));
grant select, insert, update, delete on shortlist_pick to app_rw;

-- +goose Down
drop table if exists shortlist_pick;
drop table if exists shortlist_packet;
