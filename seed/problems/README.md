# Problem import format

A problem import is one JSON file holding an **array of problems**. The same
format is what the recruiter's import screen accepts (`/app/problems/import`)
and what every file in this directory carries; the files here are the platform
seed bank, which every org reads and none may edit.

Unknown fields are rejected, so a typo fails the import rather than being
silently dropped, and so is anything after the array. One import takes at most
50 problems and has a ten-minute deadline, since every reference solution in
the batch is executed before anything is stored.

## Problem

| Field | Type | Required | Notes |
| ----- | ---- | -------- | ----- |
| `kind` | `"code"` \| `"sql"` | yes | `code` runs a program against stdin/stdout; `sql` runs one query against a seeded database |
| `title` | string | yes | Unique within the bank; re-importing a title replaces that problem |
| `statement` | string (Markdown) | yes | What the candidate reads |
| `difficulty` | `"easy"` \| `"medium"` \| `"hard"` | no | Defaults to `medium` |
| `tags` | string array | no | Lower-cased, de-duplicated, sorted; the bank is filtered by them |
| `allowed_languages` | string array | yes | Any of `python`, `node`, `go`, `java`, `sql`. A `sql` problem allows only `sql`; a `code` problem must not allow `sql` |
| `time_limit_ms` | integer | no | Per test case. Defaults to 2000, capped at 60000 |
| `memory_limit_kb` | integer | no | Defaults to 262144, capped at 2097152 |
| `sql_schema` | string | `sql` only | DDL creating the tables. Required for a `sql` problem, forbidden on a `code` one |
| `sql_seed` | string | `sql` only | Rows inserted after the schema |
| `reference_solutions` | array | yes | One per allowed language — every language a candidate may pick must be proven solvable; see below |
| `test_cases` | array | yes | At least one, at most 100; see below |

## `reference_solutions[]`

| Field | Type | Notes |
| ----- | ---- | ----- |
| `language` | string | Must be one of the problem's `allowed_languages`; exactly one solution per allowed language |
| `source` | string | A complete program (or, for `sql`, a single query) |

Every reference solution is executed against every test case when the import
runs. A solution that fails any case rejects the **whole batch**, and the
import reports what went wrong per problem — nothing is stored until all of it
passes.

## `test_cases[]`

| Field | Type | Notes |
| ----- | ---- | ----- |
| `input` | string | Fed to the program on stdin. For a `sql` problem it is extra SQL run after the schema and seed, so a case can add or change rows |
| `expected` | string | Compared after trimming surrounding whitespace. May be empty: a query returning no rows, or a program printing nothing |
| `visibility` | `"public"` \| `"hidden"` | Defaults to `public`; at least one case must be public so the candidate sees an example |
| `weight` | number | Defaults to 1; must be positive. Scoring is weighted by it |
| `unordered` | boolean | `sql` only: compares result rows as a multiset, for queries whose row order is not part of the answer |

A `sql` result set is compared as text: one line per row, columns separated by
tabs, `NULL` for a null cell, `t`/`f` for booleans.

## Example

```json
[
  {
    "kind": "code",
    "title": "Sum Two Numbers",
    "statement": "Read two integers and print their sum.",
    "difficulty": "easy",
    "tags": ["arithmetic"],
    "allowed_languages": ["python"],
    "reference_solutions": [
      {"language": "python", "source": "print(sum(map(int, input().split())))\n"}
    ],
    "test_cases": [
      {"input": "1 2\n", "expected": "3", "visibility": "public", "weight": 1},
      {"input": "-1 1\n", "expected": "0", "visibility": "hidden", "weight": 1}
    ]
  }
]
```

## Loading the seed bank

The files here are embedded in the binary and loaded under the platform org by
`service.ProblemService.ImportPlatformSeed`, which takes an RLS-bypassing
system transaction because no tenant may write the platform org. Loading runs
every reference solution through the runner first, so the process needs
`RUNNER_URL` and `RUNNER_SECRET` and a reachable runner. A second load
refreshes each problem by title rather than duplicating it.

Solutions go to the runner a few at a time — `ProblemService.Concurrency`,
which defaults to the runner's own default of 2. Raise it to match a runner
configured with more slots (`RUNNER_MAX_CONCURRENT`); a saturated runner
answers `503` with `Retry-After`, which the executor waits out rather than
failing the batch.
