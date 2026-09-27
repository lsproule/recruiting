# Problem import format and the seed bank

A problem import is one JSON file holding an **array of problems**. The same
format is what the recruiter's import screen accepts (`/app/problems/import`)
and what `POST /api/v1/problems` takes one entry of. The platform seed bank in
`_bank/` uses it too, with one twist described under *The seed bank* below.

Unknown fields are rejected, so a typo fails the import rather than being
silently dropped, and so is anything after the array. One import takes at most
50 problems and has a ten-minute deadline, since every reference solution in
the batch is executed before anything is stored.

## Three kinds of problem

| `kind` | What the candidate writes | How it is judged |
| ------ | ------------------------- | ---------------- |
| `function` | The body of one function whose entrypoint the problem declares (`signature`). The editor opens on a stub in the chosen language. | The harness calls the function once per test case with the case's `args` and compares what comes back with `returns`. |
| `code` | A whole program that reads stdin and writes stdout. | Each case's `input` is fed on stdin; stdout is compared with `expected` after trimming. |
| `sql` | One query. | The query runs against the schema and seed rows; the result set is compared with `expected`. |

`function` is the kind to reach for: a candidate never has to parse input, and
every language they may answer in gets the same typed call.

## Problem

| Field | Type | Required | Notes |
| ----- | ---- | -------- | ----- |
| `kind` | `"function"` \| `"code"` \| `"sql"` | no | Defaults to `function` when a `signature` is present |
| `title` | string | yes | Unique within the bank; re-importing a title replaces that problem |
| `statement` | string (Markdown) | yes | What the candidate reads |
| `difficulty` | `"easy"` \| `"medium"` \| `"hard"` | no | Defaults to `medium` |
| `tags` | string array | no | Lower-cased, de-duplicated, sorted; the bank is filtered by them |
| `allowed_languages` | string array | yes | Any of `python`, `javascript`, `ruby`, `php`, `go`, `java`, `csharp`, `cpp`, `c`, `rust`, `sql`, or `any` for every language the kind allows. Aliases (`js`, `py`, `node`, `golang`, `rb`, `rs`, `c++`, `c#`) are accepted and stored as the canonical id. A `sql` problem allows only `sql`; the other kinds must not allow `sql`. `any` on a `function` problem leaves out a language that cannot express the signature (C cannot return a matrix or a map) |
| `signature` | object | `function` only | The entrypoint; see below |
| `time_limit_ms` | integer | no | Per test case. Defaults to 2000, capped at 60000 |
| `memory_limit_kb` | integer | no | Defaults to 262144, capped at 2097152 |
| `sql_schema` | string | `sql` only | DDL creating the tables. Required for a `sql` problem, forbidden on the others |
| `sql_seed` | string | `sql` only | Rows inserted after the schema |
| `guidelines` | string | no | What an interviewer watches for. Internal: no candidate and no client ever sees it |
| `reference_solutions` | array | yes | At least one, in an allowed language, proving the problem solvable; see below |
| `test_cases` | array | yes | At least one, at most 100; see below |

Nothing in the format estimates how long a problem "should" take: an
assessment's duration is set by the recruiter who builds it.

## `signature`

```json
{"name": "receipt_total", "params": [{"name": "quantities", "type": "int[]"}, {"name": "prices", "type": "int[]"}], "returns": "long"}
```

`name` is a lower_snake_case identifier that is not a reserved word in any
supported language; it is used verbatim in every language, so a candidate reads
the same name whichever they pick. Up to eight `params`. The types:

| Type | Meaning | JSON in a case |
| ---- | ------- | -------------- |
| `int` | 32-bit signed integer | `42` |
| `long` | 64-bit signed integer | `5000000000` |
| `float` | double | `1.5` (compared to a relative tolerance of 1e-6) |
| `bool` | boolean | `true` |
| `string` | UTF-8 text | `"tea"` |
| `int[]`, `long[]`, `float[]`, `bool[]`, `string[]` | list | `[1, 2, 3]` |
| `int[][]` | list of int lists (rows may differ in length) | `[[1, 2], [3]]` |
| `string[][]` | list of string lists | `[["a", "b"]]` |
| `map<string,int>`, `map<string,string>` | string-keyed map | `{"tea": 3}` |

How each type is spelled per language is in the stub the editor shows and in
`runner/wire/langs.go`. C receives a list as a pointer and a length, a matrix
as rows with their lengths, a map as parallel key and value arrays, and returns
a list through an `out_len` parameter; a `char **` result is an array of
`malloc`ed strings.

## `reference_solutions[]`

| Field | Type | Notes |
| ----- | ---- | ----- |
| `language` | string | Must be one of the problem's `allowed_languages`; at most one solution per language |
| `source` | string | For `function`, the filled-in stub: the same file a candidate submits. For `code`, a complete program. For `sql`, a single query |

Every reference solution is executed against every test case when the import
runs. A solution that fails any case rejects the **whole batch**, and the
import reports what went wrong per problem — nothing is stored until all of it
passes.

## `test_cases[]`

| Field | Type | Notes |
| ----- | ---- | ----- |
| `name` | string | What the case is called, up to 80 characters. A result table names the case rather than numbering it |
| `class` | `"sample"` \| `"edge"` \| `"perf"` \| `"core"` | What the case is for. Defaults to `sample` for a public case and `core` for a hidden one |
| `args` | JSON array | `function` only: one value per parameter, each of its declared type |
| `returns` | JSON value | `function` only: the expected return value |
| `input` | string | `code`: fed to the program on stdin. `sql`: extra SQL run after the schema and seed, so a case can add or change rows. `function`: the canonical JSON form of `args`, which is what the API returns; a document may write either |
| `expected` | string | `code`: compared after trimming surrounding whitespace. `sql`: the rows. `function`: the canonical JSON form of `returns` |
| `visibility` | `"public"` \| `"hidden"` | Defaults to `public`; at least one case must be public so the candidate sees an example |
| `weight` | number | Defaults to 1; must be positive. Scoring is weighted by it |
| `unordered` | boolean | `sql` only: compares result rows as a multiset, for queries whose row order is not part of the answer |

A `sql` result set is compared as text: one line per row, columns separated by
tabs, `NULL` for a null cell, `t`/`f` for booleans. A `function` result is
compared value by value: a float within tolerance, a map by key, a list in
order.

## Quality review

Every stored problem is scored against six rules: at least six test cases, at
least one public case, at least three hidden ones, at least one tag, a
statement of at least 200 characters, and reference solutions proving at least
two languages. A problem that misses too many of them is stored and editable
but cannot be attached to an assessment, because the scores it would produce
say little about a candidate. A draft saved from the authoring wizard has
proven nothing until it is saved for real and its solutions pass.

## Example

```json
[
  {
    "title": "Sum Two Numbers",
    "statement": "Return the sum of `a` and `b`.",
    "difficulty": "easy",
    "tags": ["arithmetic"],
    "signature": {"name": "add", "params": [{"name": "a", "type": "int"}, {"name": "b", "type": "int"}], "returns": "int"},
    "guidelines": "A smoke test of the editor more than of the candidate.",
    "allowed_languages": ["any"],
    "reference_solutions": [
      {"language": "python", "source": "def add(a: int, b: int) -> int:\n    return a + b\n"},
      {"language": "go", "source": "package main\n\nfunc add(a int, b int) int {\n\treturn a + b\n}\n"}
    ],
    "test_cases": [
      {"name": "worked example", "class": "sample", "args": [1, 2], "returns": 3, "visibility": "public"},
      {"name": "signs cancel", "class": "edge", "args": [-1, 1], "returns": 0, "visibility": "hidden"}
    ]
  }
]
```

## The seed bank

`_bank/` holds the platform's built-in problems, one directory each:

```
_bank/010-receipt-total/
  problem.json      the import entry, without reference_solutions
  solution.py       the python reference solution
  solution.js       javascript
  solution.rb       ruby
  solution.php      php
  solution.go       go
  Solution.java     java
  Solution.cs       csharp
  solution.cpp      cpp
  solution.c        c
  solution.rs       rust
  solution.sql      sql (sql problems only)
```

Solutions live as source files rather than JSON strings so they can be read,
diffed, and run like code. `service.SeedProblem` attaches them by file name
(`seed/problems/embed.go` is the map); any other file in a problem directory is
an error, so a typo cannot quietly drop a language.

A `problem.json` is indented like any other JSON document except inside
`test_cases`, where each case is written as one compact line: a performance
case carries a hundred-thousand-element array, and pretty-printing one
element per line made the bank several times larger than its data. Keep a
rewritten or added case compact the same way (the top level stays readable;
a case is `json.dumps(case, separators=(",", ":"))` on its own line). The
platform seed load reads one directory at a time for the same reason, so
the bank's size is never held in memory all at once. Every function problem
ships a solution in all ten languages, C excepted where the signature returns a
map, and the bank is proven end to end by
`TestSeedReferencesSolveTheirCases` (the runner alone) and
`TestPlatformSeedRunsOnTheRunner` (through the import path and the database)
under `-tags integration`.

The directory name starts with an underscore so the go tool never reads the
solution files as Go or C packages.

## Loading the seed bank

The bank is embedded in the binary and loaded under the platform org by
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
