# jobpost: browser automation for job boards

`jobpost` takes a posting (title, body, apply URL) and places it on a job
board by driving a real browser through the board's own posting flow, the
way a recruiter would by hand, then prints where it landed. The platform's
worker runs it for every posting a recruiter queues from a job's *Postings*
panel; it can also be run by hand.

```sh
cd tools/jobpost && npm ci
echo '{"board":"demo","title":"Backend Engineer","body":"...","apply_url":"https://app.example/apply/acme/backend"}' \
  | node cli.js post
# {"url":"http://localhost:8765/jobs/3","id":"3"}
```

The request comes in on stdin as JSON and the result goes out on stdout as
JSON; anything else the tool has to say goes to stderr, so the worker can
read the answer whatever the browser logged. A non-zero exit with a JSON
`{"error": "..."}` on stdout is a failed posting the platform records
verbatim.

## Boards

| `board` | Adapter | Credentials (environment) |
| ------- | ------- | ------------------------- |
| `linkedin` | `boards/linkedin.js` — signs in, opens the free job post flow, fills the form, submits | `LINKEDIN_EMAIL`, `LINKEDIN_PASSWORD` |
| `indeed` | `boards/indeed.js` — signs in to the employer account, walks the post-a-job wizard | `INDEED_EMAIL`, `INDEED_PASSWORD` |
| `glassdoor` | `boards/glassdoor.js` — Glassdoor's employer posting is Indeed's; the adapter posts through Indeed and reports both | `INDEED_EMAIL`, `INDEED_PASSWORD` |
| `demo` | `boards/demo.js` — a local board this tool ships (`fake-board.js`), driven through the browser exactly like the real ones | `JOBPOST_DEMO_BOARD_URL` (default `http://localhost:8765`) |

Every adapter implements one function, `post(page, posting) -> {url, id}`,
and the boards' selectors live in the adapter alone, so a board changing its
markup is one file to fix. Adding a board is adding a file to `boards/` and a
row to `internal/domain/jobpost.go`.

Real boards change their pages, rate-limit automation, and ask for
verification codes. The adapters stop and report a clear error rather than
guess, and `JOBPOST_HEADFUL=1` runs the browser visibly so a person can
watch or step in. Set `JOBPOST_DRY_RUN=1` to fill every form and stop
before the final submit, which is how the adapters are checked against a
live board without posting anything.

## The demo board

`node fake-board.js --port 8765` serves a tiny job board: a posting form at
`/post`, a listing at `/jobs`, and each posting at `/jobs/{id}` with the
apply link pointing back at the platform. The e2e suite starts it, has the
worker post to it through this tool, and checks the posting appears, so the
automation path is proven end to end on every run.
