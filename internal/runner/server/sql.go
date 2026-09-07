package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// SQLExecutor provisions a throwaway database and role per request, seeds it
// from the schema, runs the candidate query per test, and drops everything.
// AdminURL should point at a Postgres dedicated to the runner in production.
type SQLExecutor struct {
	AdminURL string
}

func (s *SQLExecutor) Execute(ctx context.Context, req *Request) *Response {
	limits := req.Limits.normalized()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second+time.Duration(len(req.Tests))*time.Duration(limits.WallMs)*time.Millisecond)
	defer cancel()

	admin, err := pgx.Connect(ctx, s.AdminURL)
	if err != nil {
		return &Response{ID: req.ID, Status: StatusError, CompileOutput: "sql runner: " + err.Error(), Results: errored(req.Tests, "runner error")}
	}
	defer admin.Close(context.Background())

	suffix := strings.ToLower(hex.EncodeToString(shortHash(req.ID)))
	dbName, role := "runner_"+suffix, "runner_"+suffix
	pw := randomPassword()
	// Statements set the ownership chain so the candidate role sees only its db.
	setup := []string{
		fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, pgx.Identifier{dbName}.Sanitize()),
		fmt.Sprintf(`DROP ROLE IF EXISTS %s`, pgx.Identifier{role}.Sanitize()),
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT CONNECTION LIMIT 2`, pgx.Identifier{role}.Sanitize(), pw),
		fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, pgx.Identifier{dbName}.Sanitize(), pgx.Identifier{role}.Sanitize()),
		fmt.Sprintf(`ALTER ROLE %s SET statement_timeout = %d`, pgx.Identifier{role}.Sanitize(), limits.WallMs),
		fmt.Sprintf(`ALTER ROLE %s SET temp_file_limit = '64MB'`, pgx.Identifier{role}.Sanitize()),
	}
	for _, q := range setup {
		if _, err := admin.Exec(ctx, q); err != nil {
			return &Response{ID: req.ID, Status: StatusError, CompileOutput: "sql runner: provision: " + err.Error(), Results: errored(req.Tests, "runner error")}
		}
	}
	defer func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		_, _ = admin.Exec(cctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, pgx.Identifier{dbName}.Sanitize()))
		_, _ = admin.Exec(cctx, fmt.Sprintf(`DROP ROLE IF EXISTS %s`, pgx.Identifier{role}.Sanitize()))
	}()

	u, err := url.Parse(s.AdminURL)
	if err != nil {
		return &Response{ID: req.ID, Status: StatusError, CompileOutput: err.Error(), Results: errored(req.Tests, "runner error")}
	}
	u.User = url.UserPassword(role, pw)
	u.Path = "/" + dbName
	candidateURL := u.String()

	results := make([]TestResult, len(req.Tests))
	overall := StatusOK
	for i, t := range req.Tests {
		results[i] = s.runTest(ctx, candidateURL, req, t, limits)
		if results[i].Status == TestError && strings.HasPrefix(results[i].StderrTail, "schema:") {
			overall = StatusRuntimeError
		}
	}
	return &Response{ID: req.ID, Status: overall, Results: results}
}

// runTest uses a fresh database state per test: the seed runs inside a
// transaction that is rolled back, so tests cannot leak rows into each other.
func (s *SQLExecutor) runTest(ctx context.Context, dsn string, req *Request, t Test, limits Limits) TestResult {
	res := TestResult{TestID: t.ID, Status: TestError}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		res.StderrTail = "connect: " + err.Error()
		return res
	}
	defer conn.Close(context.Background())
	tx, err := conn.Begin(ctx)
	if err != nil {
		res.StderrTail = err.Error()
		return res
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	for _, seed := range []string{req.SQLSchema, t.Input} {
		if strings.TrimSpace(seed) == "" {
			continue
		}
		if _, err := tx.Exec(ctx, seed); err != nil {
			res.StderrTail = "schema: " + tail(err.Error())
			return res
		}
	}

	start := time.Now()
	rows, err := tx.Query(ctx, req.Source)
	if err != nil {
		return classify(res, err, start)
	}
	var lines []string
	var size int
	maxBytes := limits.OutputKB * 1024
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			rows.Close()
			return classify(res, err, start)
		}
		cells := make([]string, len(vals))
		for j, v := range vals {
			cells[j] = formatCell(v)
		}
		line := strings.Join(cells, "\t")
		if size+len(line) > maxBytes {
			rows.Close()
			res.StderrTail = "result set exceeds output limit"
			res.TimeMs = time.Since(start).Milliseconds()
			return res
		}
		size += len(line) + 1
		lines = append(lines, line)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return classify(res, err, start)
	}
	res.TimeMs = time.Since(start).Milliseconds()

	got, want := lines, strings.Split(strings.TrimSpace(t.Expected), "\n")
	if strings.TrimSpace(t.Expected) == "" {
		want = nil
	}
	if t.Unordered {
		got, want = append([]string(nil), got...), append([]string(nil), want...)
		sort.Strings(got)
		sort.Strings(want)
	}
	stdout := strings.TrimSpace(strings.Join(lines, "\n"))
	h := sha256.Sum256([]byte(stdout))
	res.StdoutHash = hex.EncodeToString(h[:])
	res.StdoutTail = stdout
	res.Status = TestFail
	if equalLines(got, want) {
		res.Status = TestPass
	}
	return res
}

func classify(res TestResult, err error, start time.Time) TestResult {
	res.TimeMs = time.Since(start).Milliseconds()
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "57014" { // query_canceled: statement_timeout fired
		res.Status = TestTimeout
		res.StderrTail = "statement timeout"
		return res
	}
	res.Status = TestError
	res.StderrTail = tail(err.Error())
	return res
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.TrimRight(a[i], " \t\r") != strings.TrimRight(b[i], " \t\r") {
			return false
		}
	}
	return true
}

func formatCell(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case []byte:
		return string(x)
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	case bool:
		if x {
			return "t"
		}
		return "f"
	default:
		return fmt.Sprint(x)
	}
}

func tail(s string) string {
	if len(s) > 1024 {
		return s[len(s)-1024:]
	}
	return s
}

func shortHash(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:12]
}

func randomPassword() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
