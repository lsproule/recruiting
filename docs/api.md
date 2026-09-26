# The Recruiting API

Everything the platform does is served as JSON under `/api/v1`. The served
OpenAPI 3.1 document is at `/api/v1/openapi.json`, and `/api/v1/docs` renders
it interactively; every operation there carries a summary, a description of
what it does and refuses, and typed request and response bodies. This guide
is the narrative: how to authenticate, which surface you are on, how to page
and poll, and the two flows a company integrates most: consuming its
candidates, and searching the talent network.

## Two surfaces, one credential each

The API has two surfaces, and a credential reaches exactly one of them.

| Surface | Who | Paths | Credential |
| ------- | --- | ----- | ---------- |
| Org | The recruiting org's recruiters, admins, and interviewers | everything except `/portal/*` | a token an **org admin** issues for an org user (`POST /api-tokens`, or `/app/admin/api-tokens`) |
| Company | A client company of the org | `/portal/*` | a token a **client user** issues for themselves (`POST /portal/tokens`, or the portal's *Developer* page) |

A company token acts as the client user who issued it, so it sees exactly
what that user sees in the portal: the company's own jobs, the applications
the recruiter has *released* to it, the recruiter's shortlist packets, and
its own talent requests. It never sees an unreleased application, a blind
candidate's identity, an interviewer's notes, or an assessment's integrity
signals. Presenting a company token on an org operation, or the reverse, is
a `403`.

Send the token as a bearer credential:

```sh
curl https://recruiting.example/api/v1/portal/me \
  -H "Authorization: Bearer $TOKEN"
```

```json
{"id":"…","client_company_id":"…","company_name":"Acme","email":"jo@acme.example","name":"Jo"}
```

### Getting a first token

A client user signs in to the portal and opens **Developer**. The page
issues a token, shows its secret once, and lists the live ones by prefix.
From then on tokens can be managed by API:

| Operation | Method and path |
| --------- | --------------- |
| List your live tokens | `GET /portal/tokens` |
| Issue a token (secret returned once) | `POST /portal/tokens` `{"name":"ats sync","expires_at":"2027-01-01T00:00:00Z"}` |
| Revoke one | `DELETE /portal/tokens/{token_id}` |

A user keeps at most ten live tokens. An org admin can also issue a token
for a client user from the admin screen; it behaves the same.

## Conventions

- **Replies** are JSON objects. Collections are wrapped: `{"jobs": [...]}`,
  `{"applications": [...]}`, `{"events": [...]}`.
- **Refusals** are RFC 9457 problem details, `application/problem+json`,
  with `status` and `detail`:

  ```json
  {"title":"Conflict","status":409,"detail":"an introduction to that person was already requested"}
  ```

  `401` is a missing or dead token; `403` the wrong surface or an action the
  caller may not take; `404` a thing that does not exist *for this caller*
  (another company's job answers 404, not 403); `409` a state the action
  cannot apply to; `422` a body the rules refuse, with the rule in `detail`.
- **Ids** are UUIDs. **Times** are RFC 3339 in UTC.
- **Paging** on collection reads is `limit` (default 50, at most 200) and
  `offset`. The change feed pages by cursor instead (below).
- **Enumerations**: seniority is `junior|mid|senior|staff`; remote policy
  `remote|hybrid|onsite`; application status `active|hired|rejected|withdrawn`.

## Consuming your candidates

The company surface mirrors the portal, plus what an integration needs and
a page does not.

| Read | Path |
| ---- | ---- |
| Your jobs, with how many applications are released on each | `GET /portal/jobs` |
| One job and its released applications, in pipeline order | `GET /portal/jobs/{job_id}` |
| The shortlist packet the recruiter sent for a job (404 until one is sent) | `GET /portal/jobs/{job_id}/shortlist` |
| Every released application across your jobs, most recently changed first | `GET /portal/applications?job_id=&status=&updated_since=&limit=&offset=` |
| One application: scores, assessment outcome, whether a résumé is on file | `GET /portal/applications/{application_id}` |
| A signed, short-lived résumé download URL | `GET /portal/applications/{application_id}/resume` |

An application looks like this:

```json
{
  "id": "…", "job_id": "…", "job_title": "Senior Go Engineer",
  "stage_id": "…", "stage_name": "Client review", "status": "active",
  "candidate": {"blind": false, "label": "Ada Lovelace", "email": "ada@example.com", "phone": "", "links": ["https://github.com/ada"]},
  "recruiter_summary": "Strong systems background.",
  "released_at": "2026-09-26T10:04:11Z"
}
```

While a blind-mode job keeps a candidate anonymous, `candidate.blind` is
`true`, `label` is an anonymous handle, and the contact fields and the
résumé are withheld; they appear once the application reaches a stage the
recruiter marked as unblinding.

`GET /portal/applications/{id}` adds `scorecards` (each interviewer's
`overall` verdict and per-criterion `scores`, never their notes),
`assessment` (`score` and `verdict`, never the integrity signals), and
`has_resume`.

### Acting

The three actions the portal offers are the same three operations:

| Action | Path and body |
| ------ | ------------- |
| Advance to a later client-review stage | `POST /portal/applications/{id}/advance` `{"to_stage_id":"…"}` |
| Reject, with a reason the recruiter reads | `POST /portal/applications/{id}/reject` `{"reason":"…"}` |
| Ask the recruiter a question | `POST /portal/applications/{id}/request-info` `{"message":"…"}` |

All three answer `204`. Only a later client-review stage may be advanced
to; `get-portal-application` lists the stages allowed.

### Syncing: the change feed

Rather than re-reading every application, poll the feed:

```sh
curl "https://recruiting.example/api/v1/portal/events?since=0" \
  -H "Authorization: Bearer $TOKEN"
```

```json
{
  "events": [
    {"seq": 8123, "id": "…", "application_id": "…", "job_id": "…", "job_title": "Senior Go Engineer",
     "kind": "released", "actor_kind": "org_user", "created_at": "2026-09-26T10:04:11Z"},
    {"seq": 8130, "id": "…", "application_id": "…", "job_id": "…", "job_title": "Senior Go Engineer",
     "kind": "moved", "actor_kind": "client_user", "from_stage": "Client review", "to_stage": "Final round",
     "created_at": "2026-09-26T11:20:03Z"}
  ],
  "next_since": 8130
}
```

`seq` is a cursor that only grows. Remember `next_since` and pass it back
as `since` on the next poll; an empty `events` list means nothing happened.
Every minute is a sensible cadence. Kinds are `released`, `unreleased`
(the application is hidden again; drop it), `moved`, `withdrawn`,
`client_request_info`, and `scorecard_strong_yes`; `reason` is carried only
for events your own users wrote. The feed starts at the moment an
application was released, so nothing the recruiter did before deciding to
show it to you is in it.

`updated_since` on `GET /portal/applications` is the cruder alternative for
a nightly full sync.

## Searching the talent network

The org keeps a talent network: people who joined it from the public page
(`/talent/{org}`) and consented to be approached about roles that fit, and
past applicants the recruiters rate highly. A company describes who it
wants, reads anonymised matches, and asks to be introduced; a recruiter
approaches the person; the person decides. Until they say yes, the company
sees what they do and want, never who they are.

| Step | Path |
| ---- | ---- |
| Describe who you want | `POST /portal/talent-requests` |
| Read anonymised matches, best first | `GET /portal/talent-requests/{request_id}/matches` |
| Ask to be introduced to one | `POST /portal/talent-requests/{request_id}/matches/{match_id}/introduce` |
| Read the request and where each introduction stands | `GET /portal/talent-requests/{request_id}` |
| Close a request | `POST /portal/talent-requests/{request_id}/close` |

```sh
curl https://recruiting.example/api/v1/portal/talent-requests \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"title":"Senior Go engineer","skills":["go","postgres","kubernetes"],
       "seniority":"senior","remote_policy":"remote","job_id":"…",
       "note":"Someone who has run clusters in production."}'
```

`skills` is required and is what the match is mostly scored on; `job_id`
names one of your open jobs, and is where an accepted introduction opens
an application (the recruiter can pick another when sending). A match:

```json
{
  "id": "…", "source": "network", "score": 0.82,
  "breakdown": {"skills": 0.67, "seniority": 1, "location": 1, "text": 0.9},
  "shared_skills": ["go", "postgres"], "skills": ["go", "postgres", "rust"],
  "seniority": "senior", "location": "Berlin", "remote_policy": "remote",
  "headline": "Backend engineer, ten years of Go", "available_from": "2027-01-04T00:00:00Z",
  "has_resume": true
}
```

The score is a weighted sum of four parts: skills in common (Jaccard),
seniority fit, location and remote fit, and how well the résumé answers
the request's terms relative to the best match in the batch. `source` is
`network` for someone who joined, `pool` for a past applicant. People
already in your pipeline are not offered. Once you ask for an introduction
the match carries `intro_status`, and the request's `introductions` list
shows each one as `requested` (waiting on a recruiter), `sent` (the person
is reading it), `accepted`, `declined`, or `dismissed`. On `accepted` the
introduction carries the candidate's name and the `application_id` of the
released application, which appears in the change feed as `released` like
any other; from there the person is an ordinary candidate of yours.

## The org surface, briefly

The org surface is the whole console: jobs and their pipelines, candidates
and résumés, applications and moves, scorecards, availability and
bookings, the problem bank, assessments and attempts, reviews, the talent
pool, shortlists, hiring processes, screening sprints, interview rooms,
and, for the talent network, `GET /talent-requests`,
`POST /talent-requests/{id}/introductions/{intro_id}/send`, and
`GET /talent-profiles`. The served document groups them by tag; each
operation names the role it needs in its description where one is needed.

## Versioning

The document's `info.version` is the API's. Additions (new fields, new
operations) do not change it; a removal or a change in meaning would, under
a new path prefix.
