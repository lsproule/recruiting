// Package demo fills an org with a believable month of agency work: clients,
// roles, applicants with résumés, candidates at every step of every process,
// scored sittings, interviews, sprints, shortlists, and a talent network,
// so every screen has something to show and the work queue has something
// to do. It is what `admin seed-demo` runs.
//
// It writes rows directly, in one transaction on the schema-owner
// connection, rather than driving the services: a demo wants back-dated
// timestamps and no side effects (no invites to invented addresses, no jobs
// in the queue), and the shapes it writes are the ones the services read.
package demo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/runner/server"
	"recruiting/internal/service"
	"recruiting/internal/store"
)

// Options shape the demo. Every field has a default.
type Options struct {
	// OrgName names the agency; the slug is derived from it.
	OrgName string
	// AdminEmail is the first admin's sign-in. Every other user's address
	// is derived from the org.
	AdminEmail string
	// Password is what every demo account signs in with.
	Password string
	// Seed drives the random source, so the same seed gives the same demo.
	Seed int64
	// Now is the moment the demo is relative to; everything is dated
	// backwards from it.
	Now time.Time
	// Blob, when set, receives the résumé files so they can be downloaded;
	// without it the résumés exist as rows and text only.
	Blob service.BlobStore
	// Candidates is how many people apply or join the network; 48 fills
	// every screen without making any list unreadable.
	Candidates int
}

// Login is one account the demo created and its password.
type Login struct {
	Kind, Name, Email, Password string
	Roles                       []string
	Company                     string
}

// Report is what got written, for the command to print.
type Report struct {
	OrgID   uuid.UUID
	OrgSlug string
	Logins  []Login
	Counts  map[string]int
}

var (
	defaultOrgName  = "Northwind Talent"
	defaultPassword = "demo-password"
)

// ErrOrgExists means the slug is taken; the demo never writes into an org
// it did not create.
var ErrOrgExists = errors.New("demo: an org with that slug already exists")

// seeder carries the transaction and the ids as they are created.
type seeder struct {
	ctx  context.Context
	tx   *store.Tx
	o    Options
	r    *rand.Rand
	now  time.Time
	org  uuid.UUID
	slug string

	admin      uuid.UUID
	recruiters []user
	vetters    []user
	rubric     uuid.UUID
	problems   []problem
	templates  map[string]uuid.UUID // library key -> template id
	report     Report
}

type user struct {
	ID    uuid.UUID
	Name  string
	Email string
}

type problem struct {
	ID    uuid.UUID
	Title string
	Cases []testCase
	// Solution is a reference solution's source, for a passing submission.
	Solution string
}

type testCase struct {
	ID     uuid.UUID
	Weight float64
	Public bool
}

// Seed writes the demo and reports what it wrote. It must run on the
// schema-owner connection (system.WithSystemTx): it creates the org, which
// no tenant scope could.
func Seed(ctx context.Context, tx *store.Tx, o Options) (Report, error) {
	if o.OrgName == "" {
		o.OrgName = defaultOrgName
	}
	if o.Password == "" {
		o.Password = defaultPassword
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if o.Candidates <= 0 {
		o.Candidates = 48
	}
	if o.Seed == 0 {
		o.Seed = 20260926
	}
	s := &seeder{ctx: ctx, tx: tx, o: o, r: rand.New(rand.NewSource(o.Seed)), now: o.Now.UTC(), templates: map[string]uuid.UUID{}}
	s.report.Counts = map[string]int{}
	steps := []func() error{
		s.createOrg, s.createUsers, s.createRubric, s.loadProblems,
		s.createClientsAndJobs, s.createCandidates, s.createApplications,
		s.createShortlists, s.createTalentNetwork,
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return Report{}, err
		}
	}
	s.report.OrgID, s.report.OrgSlug = s.org, s.slug
	return s.report, nil
}

// exec runs one statement; every write in the seeder goes through it so a
// failure names the statement.
func (s *seeder) exec(sql string, args ...any) error {
	if _, err := s.tx.Exec(s.ctx, sql, args...); err != nil {
		return fmt.Errorf("demo: %s: %w", firstLine(sql), err)
	}
	return nil
}

func firstLine(sql string) string {
	sql = strings.TrimSpace(sql)
	if i := strings.IndexByte(sql, '\n'); i > 0 {
		sql = sql[:i]
	}
	if len(sql) > 60 {
		sql = sql[:60] + "…"
	}
	return sql
}

func (s *seeder) count(what string, n int) { s.report.Counts[what] += n }

func (s *seeder) ago(d time.Duration) time.Time { return s.now.Add(-d) }

func (s *seeder) days(n float64) time.Duration { return time.Duration(n * float64(24*time.Hour)) }

// ---------------------------------------------------------------- org and people

func (s *seeder) createOrg() error {
	slug := service.Slugify(s.o.OrgName)
	var taken bool
	if err := s.tx.QueryRow(s.ctx, `select exists (select 1 from org where slug = $1)`, slug).Scan(&taken); err != nil {
		return err
	}
	if taken {
		return fmt.Errorf("%w: %s", ErrOrgExists, slug)
	}
	email := s.o.AdminEmail
	if email == "" {
		email = "admin@" + slug + ".example"
	}
	res, err := service.BootstrapOrg(s.ctx, s.tx, service.NewOrg{Name: s.o.OrgName, Slug: slug, AdminEmail: email, AdminName: "Morgan Reyes"})
	if err != nil {
		return err
	}
	s.org, s.slug, s.admin = res.OrgID, slug, res.AdminUserID
	if err := s.password(`insert into org_user_credential (org_user_id, org_id, password_hash) values ($1, $2, $3)`, s.admin); err != nil {
		return err
	}
	s.report.Logins = append(s.report.Logins, Login{Kind: "org", Name: "Morgan Reyes", Email: email, Password: s.o.Password, Roles: []string{"admin"}})
	rows, err := s.tx.Query(s.ctx, `select id, library_key from pipeline_template where org_id = $1`, s.org)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var key *string
		if err := rows.Scan(&id, &key); err != nil {
			return err
		}
		if key != nil {
			s.templates[*key] = id
		}
	}
	return rows.Err()
}

func (s *seeder) password(insert string, id uuid.UUID) error {
	hash, err := service.HashPassword(s.o.Password)
	if err != nil {
		return err
	}
	return s.exec(insert, id, s.org, hash)
}

func (s *seeder) createUsers() error {
	people := []struct {
		name, local string
		roles       []string
		tz          string
	}{
		{"Priya Raman", "priya", []string{"recruiter"}, "Europe/London"},
		{"Tomás Álvarez", "tomas", []string{"recruiter", "vetter"}, "Europe/Madrid"},
		{"Chen Wei", "chen", []string{"vetter"}, "Europe/Berlin"},
		{"Amara Okoye", "amara", []string{"vetter"}, "Europe/Amsterdam"},
		{"Jonas Lindgren", "jonas", []string{"vetter"}, "Europe/Stockholm"},
	}
	for _, p := range people {
		id := uuid.New()
		email := p.local + "@" + s.slug + ".example"
		if err := s.exec(`insert into org_user (id, org_id, email, name, timezone) values ($1, $2, $3, $4, $5)`, id, s.org, email, p.name, p.tz); err != nil {
			return err
		}
		for _, role := range p.roles {
			if err := s.exec(`insert into org_user_role (org_user_id, org_id, role) values ($1, $2, $3)`, id, s.org, role); err != nil {
				return err
			}
		}
		if err := s.password(`insert into org_user_credential (org_user_id, org_id, password_hash) values ($1, $2, $3)`, id); err != nil {
			return err
		}
		u := user{ID: id, Name: p.name, Email: email}
		for _, role := range p.roles {
			switch role {
			case "recruiter":
				s.recruiters = append(s.recruiters, u)
			case "vetter":
				s.vetters = append(s.vetters, u)
				// Weekday office hours, so the booking screen has slots.
				for wd := 1; wd <= 5; wd++ {
					if err := s.exec(`insert into availability_rule (org_id, vetter_id, weekday, start_time, end_time, timezone, buffer_minutes, slot_minutes)
						values ($1, $2, $3, '09:00', '17:00', $4, 10, 30)`, s.org, id, wd, p.tz); err != nil {
						return err
					}
				}
			}
		}
		s.report.Logins = append(s.report.Logins, Login{Kind: "org", Name: p.name, Email: email, Password: s.o.Password, Roles: p.roles})
	}
	s.count("users", len(people)+1)
	return nil
}

func (s *seeder) createRubric() error {
	s.rubric = uuid.New()
	criteria, _ := json.Marshal([]service.Criterion{
		{Name: "Problem solving", Description: "Breaks the problem down, checks assumptions, recovers from wrong turns"},
		{Name: "Code quality", Description: "Names things well, handles the edges, tests what matters"},
		{Name: "Communication", Description: "Explains trade-offs, listens, asks before guessing"},
		{Name: "Ownership", Description: "Talks about what they shipped and what went wrong afterwards"},
	})
	if err := s.exec(`insert into scorecard_rubric (id, org_id, name, criteria) values ($1, $2, 'Engineering interview', $3)`, s.rubric, s.org, criteria); err != nil {
		return err
	}
	return nil
}

// loadProblems reads the platform bank, which seed-problems fills; a demo
// without it gets three unverified problems of its own so the assessments
// still have something to attach.
func (s *seeder) loadProblems() error {
	rows, err := s.tx.Query(s.ctx, `select p.id, p.title, coalesce((select r.source from problem_reference r where r.problem_id = p.id and r.language = 'python' limit 1), '')
		from problem p where p.org_id = $1 and p.kind in ('function', 'code') order by p.difficulty, p.title`, service.PlatformOrgID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var p problem
		if err := rows.Scan(&p.ID, &p.Title, &p.Solution); err != nil {
			rows.Close()
			return err
		}
		s.problems = append(s.problems, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(s.problems) == 0 {
		if err := s.ownProblems(); err != nil {
			return err
		}
	}
	for i := range s.problems {
		cases, err := s.tx.Query(s.ctx, `select id, weight, visibility from test_case where problem_id = $1 order by position`, s.problems[i].ID)
		if err != nil {
			return err
		}
		for cases.Next() {
			var tc testCase
			var visibility string
			if err := cases.Scan(&tc.ID, &tc.Weight, &visibility); err != nil {
				cases.Close()
				return err
			}
			tc.Public = visibility == "public"
			s.problems[i].Cases = append(s.problems[i].Cases, tc)
		}
		cases.Close()
		if err := cases.Err(); err != nil {
			return err
		}
	}
	return nil
}

// ownProblems is the fallback bank: three function problems the org owns,
// unproven, with enough cases to be attachable.
func (s *seeder) ownProblems() error {
	specs := []struct {
		title, name, returns, statement string
		params                          []domain.Param
		cases                           [][2]string
	}{
		{"Receipt Total", "receipt_total", "long", "Add up a receipt: each quantity times its price, in cents.",
			[]domain.Param{{Name: "quantities", Type: "int[]"}, {Name: "prices", Type: "int[]"}},
			[][2]string{{`[[2,1],[250,310]]`, `810`}, {`[[1],[0]]`, `0`}, {`[[],[]]`, `0`}, {`[[3],[7]]`, `21`}, {`[[1000],[100000]]`, `100000000`}, {`[[1,1,1],[1,2,3]]`, `6`}}},
		{"Badge Palindrome", "is_badge_palindrome", "bool", "Is the badge a palindrome, ignoring anything that is not a letter or digit?",
			[]domain.Param{{Name: "badge", Type: "string"}},
			[][2]string{{`["A man, a plan, a canal: Panama"]`, `true`}, {`["badge-42"]`, `false`}, {`[""]`, `true`}, {`["0P"]`, `false`}, {`["No lemon, no melon!"]`, `true`}, {`["abc"]`, `false`}}},
		{"Meeting Room Crunch", "rooms_needed", "int", "How many rooms do these meetings need?",
			[]domain.Param{{Name: "starts", Type: "int[]"}, {Name: "ends", Type: "int[]"}},
			[][2]string{{`[[0,5,15],[30,10,20]]`, `2`}, {`[[7,10],[10,12]]`, `1`}, {`[[],[]]`, `0`}, {`[[0,0],[1,1]]`, `2`}, {`[[0,10],[100,90]]`, `2`}, {`[[1],[2]]`, `1`}}},
	}
	for _, sp := range specs {
		id := uuid.New()
		sig, _ := json.Marshal(domain.Signature{Name: sp.name, Params: sp.params, Returns: sp.returns})
		if err := s.exec(`insert into problem (id, org_id, kind, title, statement, difficulty, tags, allowed_languages, signature, quality, proven_languages)
			values ($1, $2, 'function', $3, $4, 'easy', '{demo}', '{python,javascript,go}', $5, 60, '{}')`, id, s.org, sp.title, sp.statement, sig); err != nil {
			return err
		}
		for i, c := range sp.cases {
			visibility := "hidden"
			if i < 2 {
				visibility = "public"
			}
			if err := s.exec(`insert into test_case (org_id, problem_id, position, input, expected_output, visibility, weight, name, class)
				values ($1, $2, $3, $4, $5, $6, 1, $7, $8)`, s.org, id, i+1, c[0], c[1], visibility, fmt.Sprintf("case %d", i+1), map[bool]string{true: "sample", false: "core"}[i < 2]); err != nil {
				return err
			}
		}
		s.problems = append(s.problems, problem{ID: id, Title: sp.title, Solution: "def " + sp.name + "(*args):\n    pass\n"})
	}
	s.count("problems", len(specs))
	return nil
}

// ---------------------------------------------------------------- clients and jobs

// job is one role with its stages, as the seeder needs them.
type job struct {
	ID        uuid.UUID
	Title     string
	Spec      jobSpec
	Client    clientRow
	Stages    []domain.Stage
	Assess    uuid.UUID // the assessment attached to the assessment stage, if any
	CreatedAt time.Time
}

type clientRow struct {
	ID      uuid.UUID
	Name    string
	User    user
	Contact string
}

func (j job) stage(kind domain.StageKind, nth int) (domain.Stage, bool) {
	for _, st := range j.Stages {
		if st.Kind == kind {
			if nth == 0 {
				return st, true
			}
			nth--
		}
	}
	return domain.Stage{}, false
}

func (j job) terminal(status domain.ApplicationStatus) domain.Stage {
	for _, st := range j.Stages {
		if st.Kind == domain.StageTerminal && st.Terminal == status {
			return st
		}
	}
	return domain.Stage{}
}

func (s *seeder) createClientsAndJobs() error {
	for _, c := range clients {
		companyID := uuid.New()
		if err := s.exec(`insert into client_company (id, org_id, name, created_at) values ($1, $2, $3, $4)`, companyID, s.org, c.Name, s.ago(s.days(40))); err != nil {
			return err
		}
		userID := uuid.New()
		local := strings.ToLower(strings.Fields(strings.NewReplacer("Dr. ", "").Replace(c.Contact))[0])
		email := local + "@" + c.Domain
		if err := s.exec(`insert into client_user (id, org_id, client_company_id, email, name, timezone) values ($1, $2, $3, $4, $5, 'Europe/London')`, userID, s.org, companyID, email, c.Contact); err != nil {
			return err
		}
		if err := s.password(`insert into client_user_credential (client_user_id, org_id, password_hash) values ($1, $2, $3)`, userID); err != nil {
			return err
		}
		s.report.Logins = append(s.report.Logins, Login{Kind: "client", Name: c.Contact, Email: email, Password: s.o.Password, Company: c.Name})
		row := clientRow{ID: companyID, Name: c.Name, User: user{ID: userID, Name: c.Contact, Email: email}, Contact: c.Contact}
		for i, js := range c.Jobs {
			if err := s.createJob(row, js, s.ago(s.days(float64(30-4*i)))); err != nil {
				return err
			}
		}
		s.count("clients", 1)
	}
	return nil
}

var jobs []job

func (s *seeder) createJob(c clientRow, js jobSpec, createdAt time.Time) error {
	tmpl, ok := s.templates[js.Process]
	if !ok {
		return fmt.Errorf("demo: no template for process %q", js.Process)
	}
	id := uuid.New()
	slug := service.Slugify(js.Title) + "-" + strings.ToLower(strings.Fields(c.Name)[0])
	recruiter := s.recruiters[s.r.Intn(len(s.recruiters))]
	if err := s.exec(`insert into job (id, org_id, client_company_id, title, description, skills, seniority, location, remote_policy, salary_min, salary_max, blind_mode, status, slug, template_id, created_by, created_at, updated_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 'open', $13, $14, $15, $16, $16)`,
		id, s.org, c.ID, js.Title, jobDescription(clientSpec(c), js), js.Skills, js.Seniority, c.Name+", "+jobCity(c), js.Remote, js.SalaryMin, js.SalaryMax, js.Blind, slug, tmpl, recruiter.ID, createdAt); err != nil {
		return err
	}
	j := job{ID: id, Title: js.Title, Spec: js, Client: c, CreatedAt: createdAt}
	// The stages come from the template, as a job built on the screen
	// would get them.
	rows, err := s.tx.Query(s.ctx, `select id, position, name, kind, unblind, interview_format, duration_minutes, round_seconds, break_seconds, terminal_status, pass_score, auto_advance, auto_reject
		from pipeline_template_stage where template_id = $1 order by position`, tmpl)
	if err != nil {
		return err
	}
	var stages []domain.Stage
	for rows.Next() {
		var st domain.Stage
		var tid uuid.UUID
		var dur, round, brk, pass *int32
		var terminal *string
		if err := rows.Scan(&tid, &st.Position, &st.Name, &st.Kind, &st.Unblind, &st.InterviewFormat, &dur, &round, &brk, &terminal, &pass, &st.AutoAdvance, &st.AutoReject); err != nil {
			rows.Close()
			return err
		}
		st.DurationMinutes, st.RoundSeconds, st.BreakSeconds, st.PassScore = deref(dur), deref(round), deref(brk), deref(pass)
		if st.Kind == domain.StageTerminal {
			st.Terminal = domain.TerminalOutcomeFor(st.Name)
			if terminal != nil {
				st.Terminal = domain.ApplicationStatus(*terminal)
			}
		}
		stages = append(stages, domain.NormalizeStage(st))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range stages {
		st := &stages[i]
		st.ID = uuid.New()
		if st.Kind == domain.StageAssessment && js.Automate {
			st.PassScore, st.AutoReject = 60, true
		}
		if st.Kind == domain.StageInterview {
			st.DefaultVetterID = s.vetters[s.r.Intn(len(s.vetters))].ID
		}
		var assessment uuid.NullUUID
		if st.Kind == domain.StageAssessment {
			a, err := s.createAssessment(j.Title)
			if err != nil {
				return err
			}
			assessment = uuid.NullUUID{UUID: a, Valid: true}
			j.Assess = a
		}
		var rubric uuid.NullUUID
		if st.Kind == domain.StageInterview {
			rubric = uuid.NullUUID{UUID: s.rubric, Valid: true}
		}
		if err := s.exec(`insert into stage (id, org_id, job_id, position, name, kind, terminal_status, unblind, scorecard_rubric_id, default_vetter_id, assessment_id,
			interview_format, duration_minutes, round_seconds, break_seconds, pass_score, auto_advance, auto_reject)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`,
			st.ID, s.org, id, st.Position, st.Name, string(st.Kind), nullString(string(st.Terminal)), st.Unblind, rubric, nullUUID(st.DefaultVetterID), assessment,
			formatOr(st.InterviewFormat), nullInt(st.DurationMinutes), nullInt(st.RoundSeconds), nullInt(st.BreakSeconds), nullInt(st.PassScore), st.AutoAdvance, st.AutoReject); err != nil {
			return err
		}
	}
	j.Stages = stages
	jobs = append(jobs, j)
	s.count("jobs", 1)
	return nil
}

func jobCity(c clientRow) string {
	for _, cl := range clients {
		if cl.Name == c.Name {
			return cl.City
		}
	}
	return ""
}

// createAssessment attaches two or three of the bank's problems.
func (s *seeder) createAssessment(title string) (uuid.UUID, error) {
	id := uuid.New()
	if err := s.exec(`insert into assessment (id, org_id, name, duration_minutes, invite_window_days) values ($1, $2, $3, 90, 7)`, id, s.org, title+" screen"); err != nil {
		return uuid.Nil, err
	}
	n := 2 + s.r.Intn(2)
	if n > len(s.problems) {
		n = len(s.problems)
	}
	perm := s.r.Perm(len(s.problems))
	for i := 0; i < n; i++ {
		if err := s.exec(`insert into assessment_problem (assessment_id, org_id, problem_id, position) values ($1, $2, $3, $4)`, id, s.org, s.problems[perm[i]].ID, i+1); err != nil {
			return uuid.Nil, err
		}
	}
	s.count("assessments", 1)
	return id, nil
}

// ---------------------------------------------------------------- candidates

type seededCandidate struct {
	ID uuid.UUID
	candidate
	// Applied is how many roles they are on; the network is for the rest.
	Applied int
}

var candidates []seededCandidate

func (s *seeder) createCandidates() error {
	candidates = nil
	for _, c := range people(s.r, s.o.Candidates, s.slug) {
		id := uuid.New()
		links, _ := json.Marshal(c.Links)
		created := s.ago(s.days(float64(2 + s.r.Intn(30))))
		if err := s.exec(`insert into candidate (id, org_id, email, name, phone, links, created_at, updated_at) values ($1, $2, $3, $4, $5, $6, $7, $7)`,
			id, s.org, c.Email, c.Name, c.Phone, links, created); err != nil {
			return err
		}
		body := docx(c.Resume)
		key := fmt.Sprintf("resumes/%s/%s/%s.docx", s.org, id, uuid.New())
		if s.o.Blob != nil {
			if err := s.o.Blob.Put(s.ctx, key, strings.NewReader(string(body)), int64(len(body)), domain.ResumeDOCX); err != nil {
				return fmt.Errorf("demo: store résumé: %w", err)
			}
		}
		filename := strings.ReplaceAll(c.Name, " ", "-") + "-CV.docx"
		if err := s.exec(`insert into resume (org_id, candidate_id, blob_key, filename, content_type, size_bytes, extracted_text, text_status, created_at)
			values ($1, $2, $3, $4, $5, $6, $7, 'extracted', $8)`, s.org, id, key, filename, domain.ResumeDOCX, len(body), strings.Join(c.Resume, "\n"), created); err != nil {
			return err
		}
		candidates = append(candidates, seededCandidate{ID: id, candidate: c})
	}
	s.count("candidates", len(candidates))
	return nil
}

// ---------------------------------------------------------------- applications

// step is where an application is parked and what it has collected there.
type step string

const (
	stepApplied        step = "applied"        // first stage, untouched
	stepScreened       step = "screened"       // second generic stage, waiting to move on
	stepCallUnbooked   step = "call_unbooked"  // interview stage, no slot
	stepCallBooked     step = "call_booked"    // interview stage, slot tomorrow
	stepFeedbackDue    step = "feedback_due"   // slot yesterday, no scorecard
	stepFeedbackIn     step = "feedback_in"    // slot done, scorecard filed
	stepExamSent       step = "exam_sent"      // attempt invited
	stepExamLapsed     step = "exam_lapsed"    // invite expired unopened
	stepExamOpen       step = "exam_open"      // attempt started
	stepExamScored     step = "exam_scored"    // scored, no review
	stepExamPassed     step = "exam_passed"    // reviewed pass, waiting to move
	stepExamFailed     step = "exam_failed"    // reviewed fail, waiting to move
	stepSprintWaiting  step = "sprint_waiting" // sprint stage, no sprint
	stepSprintRated    step = "sprint_rated"   // in a sprint that ran, rated
	stepSprintUnrated  step = "sprint_unrated" // in a sprint that ran, a rating missing
	stepClientPending  step = "client_pending" // client stage, not released
	stepClientSilent   step = "client_silent"  // released days ago, nothing back
	stepClientAsked    step = "client_asked"   // released, client asked a question
	stepClientEngaged  step = "client_engaged" // released, client advanced them
	stepHired          step = "hired"
	stepRejectedResume step = "rejected_resume" // rejected at the résumé
	stepRejectedExam   step = "rejected_exam"   // rejected on the exam score, by the stage itself
	stepRejectedClient step = "rejected_client" // rejected by the client
)

// plan is how many applications each job gets at each step. Every step
// appears somewhere, so the queue has one of everything; the counts lean
// towards the top of the funnel the way a real pipeline does.
var plan = []struct {
	step  step
	count int
}{
	{stepApplied, 9}, {stepScreened, 3}, {stepCallUnbooked, 3}, {stepCallBooked, 3}, {stepFeedbackDue, 2}, {stepFeedbackIn, 2},
	{stepExamSent, 3}, {stepExamLapsed, 1}, {stepExamOpen, 2}, {stepExamScored, 3}, {stepExamPassed, 2}, {stepExamFailed, 1},
	{stepSprintWaiting, 3}, {stepSprintRated, 4}, {stepSprintUnrated, 2},
	{stepClientPending, 2}, {stepClientSilent, 2}, {stepClientAsked, 1}, {stepClientEngaged, 3},
	{stepHired, 2}, {stepRejectedResume, 5}, {stepRejectedExam, 2}, {stepRejectedClient, 2},
}

// sprintRun is a sprint that already happened, remembered so its pairings
// and ratings can be written once every candidate in it is known.
type sprintRun struct {
	ID        uuid.UUID
	Job       job
	Stage     domain.Stage
	StartsAt  time.Time
	Members   []uuid.UUID // application ids
	Unrated   map[uuid.UUID]bool
	Interview []user
}

func (s *seeder) createApplications() error {
	if len(candidates) == 0 || len(jobs) == 0 {
		return errors.New("demo: nothing to apply with")
	}
	sprints := map[uuid.UUID]*sprintRun{}    // by job
	on := map[uuid.UUID]map[uuid.UUID]bool{} // candidate -> jobs applied to
	next := 0
	pick := func(j job) *seededCandidate {
		// Round-robin over the candidates; a candidate on two roles is a
		// normal thing, so the list wraps, but never onto the same role
		// twice.
		for tries := 0; tries < len(candidates); tries++ {
			c := &candidates[next%len(candidates)]
			next++
			if on[c.ID][j.ID] {
				continue
			}
			if on[c.ID] == nil {
				on[c.ID] = map[uuid.UUID]bool{}
			}
			on[c.ID][j.ID] = true
			c.Applied++
			return c
		}
		return nil
	}
	for _, entry := range plan {
		for i := 0; i < entry.count; i++ {
			j := s.jobFor(entry.step, i)
			if j == nil {
				continue
			}
			c := pick(*j)
			if c == nil {
				continue
			}
			if err := s.application(*j, entry.step, c, sprints); err != nil {
				return fmt.Errorf("%s: %w", entry.step, err)
			}
		}
	}
	for _, run := range sprints {
		if err := s.writeSprint(run); err != nil {
			return err
		}
	}
	return nil
}

// jobFor picks a job whose process has the stage the step needs, spreading
// the steps over the jobs.
func (s *seeder) jobFor(st step, i int) *job {
	need := map[step]domain.StageKind{
		stepScreened: domain.StageGeneric, stepCallUnbooked: domain.StageInterview, stepCallBooked: domain.StageInterview,
		stepFeedbackDue: domain.StageInterview, stepFeedbackIn: domain.StageInterview,
		stepExamSent: domain.StageAssessment, stepExamLapsed: domain.StageAssessment, stepExamOpen: domain.StageAssessment,
		stepExamScored: domain.StageAssessment, stepExamPassed: domain.StageAssessment, stepExamFailed: domain.StageAssessment,
		stepRejectedExam:  domain.StageAssessment,
		stepSprintWaiting: domain.StageSprint, stepSprintRated: domain.StageSprint, stepSprintUnrated: domain.StageSprint,
		stepClientPending: domain.StageClientReview, stepClientSilent: domain.StageClientReview, stepClientAsked: domain.StageClientReview,
		stepClientEngaged: domain.StageClientReview, stepRejectedClient: domain.StageClientReview,
	}[st]
	var fit []*job
	for k := range jobs {
		j := &jobs[k]
		switch st {
		case stepScreened:
			if _, ok := j.stage(domain.StageGeneric, 1); ok && j.stage2IsOpen() {
				fit = append(fit, j)
			}
		case stepRejectedExam:
			if j.Spec.Automate {
				fit = append(fit, j)
			}
		default:
			if need == "" {
				fit = append(fit, j)
			} else if _, ok := j.stage(need, 0); ok {
				fit = append(fit, j)
			}
		}
	}
	if len(fit) == 0 {
		return nil
	}
	return fit[i%len(fit)]
}

// stage2IsOpen reports whether the job's second generic stage comes before
// the terminals (an "Offer" stage at the end is generic too, but a screened
// candidate is not an offered one).
func (j job) stage2IsOpen() bool {
	st, ok := j.stage(domain.StageGeneric, 1)
	return ok && st.Position == 2
}

// application writes one candidate on one job at one step, with the events
// that got them there and whatever the step has collected.
func (s *seeder) application(j job, st step, c *seededCandidate, sprints map[uuid.UUID]*sprintRun) error {
	applied := s.ago(s.days(float64(3 + s.r.Intn(18))))
	if applied.Before(j.CreatedAt) {
		applied = j.CreatedAt.Add(time.Hour)
	}
	first, _ := j.stage(domain.StageGeneric, 0)
	appID := uuid.New()
	recruiter := s.recruiters[s.r.Intn(len(s.recruiters))]
	vetter := s.vetters[s.r.Intn(len(s.vetters))]

	// Where the application ends up, and the path there in stage kinds.
	var target domain.Stage
	var path []domain.Stage
	status := domain.StatusActive
	interview, hasInterview := j.stage(domain.StageInterview, 0)
	assess, hasAssess := j.stage(domain.StageAssessment, 0)
	sprint, hasSprint := j.stage(domain.StageSprint, 0)
	client, hasClient := j.stage(domain.StageClientReview, 0)
	screened, hasScreened := j.stage(domain.StageGeneric, 1)
	switch st {
	case stepApplied:
		target = first
	case stepScreened:
		target = screened
	case stepCallUnbooked, stepCallBooked, stepFeedbackDue, stepFeedbackIn:
		target = interview
	case stepExamSent, stepExamLapsed, stepExamOpen, stepExamScored, stepExamPassed, stepExamFailed, stepRejectedExam:
		target = assess
	case stepSprintWaiting, stepSprintRated, stepSprintUnrated:
		target = sprint
	case stepClientPending, stepClientSilent, stepClientAsked, stepClientEngaged, stepRejectedClient:
		target = client
	case stepHired:
		target = j.terminal(domain.StatusHired)
		status = domain.StatusHired
	case stepRejectedResume:
		target = j.terminal(domain.StatusRejected)
		status = domain.StatusRejected
	}
	if target.ID == uuid.Nil {
		return nil
	}
	// The path: every non-terminal stage before the target, in order, so
	// the timeline reads like a real one.
	for _, stg := range j.Stages {
		if stg.Kind == domain.StageTerminal || stg.Position >= target.Position {
			continue
		}
		path = append(path, stg)
	}
	_ = hasScreened
	_ = hasInterview
	_ = hasAssess
	_ = hasSprint
	_ = hasClient

	if err := s.exec(`insert into application (id, org_id, job_id, candidate_id, client_company_id, stage_id, status, vetter_id, created_at, updated_at, high_quality)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9, $10)`, appID, s.org, j.ID, c.ID, j.Client.ID, target.ID, string(status), nullUUID(vetterFor(j, target, vetter)), applied, s.r.Intn(4) == 0); err != nil {
		return err
	}
	s.count("applications", 1)

	// Walk the path with a moved event per hop, a day or so apart.
	at := applied
	hop := func(from, to domain.Stage, actor string, actorID uuid.UUID, reason string, override bool) error {
		at = at.Add(time.Duration(12+s.r.Intn(36)) * time.Hour)
		if at.After(s.now) {
			at = s.now.Add(-time.Hour)
		}
		payload := fmt.Sprintf(`{"override":%t}`, override)
		return s.exec(`insert into application_event (org_id, application_id, actor_kind, actor_id, kind, from_stage_id, to_stage_id, reason, payload, created_at)
			values ($1, $2, $3, $4, 'moved', $5, $6, $7, $8, $9)`, s.org, appID, actor, nullUUID(actorID), from.ID, to.ID, nullString(reason), payload, at)
	}
	prev := first
	for i, stg := range path {
		if i == 0 {
			continue // the first stage is where they applied
		}
		if err := hop(prev, stg, "org_user", recruiter.ID, "", false); err != nil {
			return err
		}
		if err := s.artifactsFor(j, stg, appID, vetter, at, true); err != nil {
			return err
		}
		prev = stg
	}
	entered := at
	if target.ID != first.ID {
		actor, actorID, reason, override := "org_user", recruiter.ID, "", false
		switch st {
		case stepRejectedResume:
			reason = []string{"Not enough depth on " + j.Spec.Skills[0], "Looking for a different level", "Salary expectations out of range"}[s.r.Intn(3)]
		case stepHired:
			reason = "Offer accepted"
		}
		if err := hop(prev, target, actor, actorID, reason, override); err != nil {
			return err
		}
		entered = at
	}
	// The row's created_at doubles as the entered time on the first stage;
	// later stages read the last move.
	_ = entered

	// What the target stage has collected.
	switch st {
	case stepCallUnbooked:
		// nothing: the booking link is out and unanswered
	case stepCallBooked:
		return s.slot(appID, interview, vetter, s.now.Add(s.days(1)+2*time.Hour), interview.DurationMinutes, "booked")
	case stepFeedbackDue:
		return s.slot(appID, interview, vetter, s.ago(20*time.Hour), interview.DurationMinutes, "completed")
	case stepFeedbackIn:
		if err := s.slot(appID, interview, vetter, s.ago(s.days(2)), interview.DurationMinutes, "completed"); err != nil {
			return err
		}
		return s.scorecard(appID, interview, vetter, s.ago(s.days(1.5)), "yes")
	case stepExamSent:
		return s.attempt(j, appID, assess, "invited", entered, 0, nil)
	case stepExamLapsed:
		return s.attempt(j, appID, assess, "lapsed", entered, 0, nil)
	case stepExamOpen:
		return s.attempt(j, appID, assess, "started", entered, 0, nil)
	case stepExamScored:
		return s.attempt(j, appID, assess, "scored", entered, 40+s.r.Intn(55), nil)
	case stepExamPassed:
		v := "pass"
		return s.attempt(j, appID, assess, "scored", entered, 70+s.r.Intn(30), &v)
	case stepExamFailed:
		v := "fail"
		return s.attempt(j, appID, assess, "scored", entered, 10+s.r.Intn(35), &v)
	case stepRejectedExam:
		// The stage decided: scored below its mark, closed by the system.
		score := 15 + s.r.Intn(40)
		if err := s.attempt(j, appID, assess, "scored", entered, score, nil); err != nil {
			return err
		}
		rejected := j.terminal(domain.StatusRejected)
		reason := fmt.Sprintf("Automatic: scored %d, below the pass mark of %d", score, assess.PassScore)
		if err := hop(assess, rejected, "system", uuid.Nil, reason, true); err != nil {
			return err
		}
		return s.exec(`update application set stage_id = $2, status = 'rejected', updated_at = $3 where id = $1`, appID, rejected.ID, at)
	case stepSprintWaiting:
		// in the stage, in no sprint: the recruiter has to plan one
	case stepSprintRated, stepSprintUnrated:
		run := sprints[j.ID]
		if run == nil {
			run = &sprintRun{ID: uuid.New(), Job: j, Stage: sprint, StartsAt: s.ago(s.days(1)), Unrated: map[uuid.UUID]bool{}, Interview: s.vetters[:2]}
			sprints[j.ID] = run
		}
		run.Members = append(run.Members, appID)
		if st == stepSprintUnrated {
			run.Unrated[appID] = true
		}
	case stepClientPending:
		// reached the client stage; the recruiter has not forwarded them
	case stepClientSilent:
		return s.release(appID, recruiter, s.ago(s.days(4)))
	case stepClientAsked:
		if err := s.release(appID, recruiter, s.ago(s.days(2))); err != nil {
			return err
		}
		question := []string{"Could we see the take-home code before the interview?", "Are they open to relocating to " + jobCity(j.Client) + "?", "What notice period are they on?"}[s.r.Intn(3)]
		// Asked after the last thing the recruiter did, so it is still
		// unanswered.
		return s.exec(`insert into application_event (org_id, application_id, actor_kind, actor_id, kind, reason, payload, created_at)
			values ($1, $2, 'client_user', $3, $4, $5, '{}', $6)`, s.org, appID, j.Client.User.ID, service.EventRequestInfo, question, s.ago(30*time.Minute))
	case stepClientEngaged:
		if err := s.release(appID, recruiter, s.ago(s.days(5))); err != nil {
			return err
		}
		// The client moved them on to their own next stage.
		if later, ok := j.stage(domain.StageClientReview, 1); ok {
			at = s.ago(s.days(2))
			if err := hop(client, later, "client_user", j.Client.User.ID, "", false); err != nil {
				return err
			}
			return s.exec(`update application set stage_id = $2 where id = $1`, appID, later.ID)
		}
	case stepRejectedClient:
		if err := s.release(appID, recruiter, s.ago(s.days(6))); err != nil {
			return err
		}
		rejected := j.terminal(domain.StatusRejected)
		at = s.ago(s.days(3))
		if err := hop(client, rejected, "client_user", j.Client.User.ID, "Went with an internal candidate", false); err != nil {
			return err
		}
		return s.exec(`update application set stage_id = $2, status = 'rejected' where id = $1`, appID, rejected.ID)
	case stepHired:
		return s.release(appID, recruiter, s.ago(s.days(9)))
	}
	return nil
}

// vetterFor names the interviewer on an application in an interview stage.
func vetterFor(j job, target domain.Stage, v user) uuid.UUID {
	if target.Kind == domain.StageInterview {
		return v.ID
	}
	return uuid.Nil
}

// artifactsFor writes what a stage the candidate has already left would
// have collected: a scorecard for an interview, a reviewed sitting for an
// assessment, a released flag for a client stage.
func (s *seeder) artifactsFor(j job, stg domain.Stage, appID uuid.UUID, vetter user, at time.Time, passed bool) error {
	switch stg.Kind {
	case domain.StageInterview:
		if err := s.slot(appID, stg, vetter, at.Add(-6*time.Hour), stg.DurationMinutes, "completed"); err != nil {
			return err
		}
		return s.scorecard(appID, stg, vetter, at.Add(-2*time.Hour), []string{"yes", "strong_yes"}[s.r.Intn(2)])
	case domain.StageAssessment:
		v := "pass"
		return s.attempt(j, appID, stg, "scored", at.Add(-s.days(2)), 72+s.r.Intn(28), &v)
	case domain.StageClientReview:
		return s.release(appID, s.recruiters[0], at.Add(-time.Hour))
	}
	return nil
}

func (s *seeder) slot(appID uuid.UUID, stg domain.Stage, vetter user, start time.Time, minutes int, status string) error {
	if minutes == 0 {
		minutes = 30
	}
	start = start.Truncate(30 * time.Minute)
	// Vetter slots are unique by start; nudge until free.
	for {
		var taken bool
		if err := s.tx.QueryRow(s.ctx, `select exists (select 1 from interview_slot where vetter_id = $1 and starts_at = $2)`, vetter.ID, start).Scan(&taken); err != nil {
			return err
		}
		if !taken {
			break
		}
		start = start.Add(30 * time.Minute)
	}
	if err := s.exec(`insert into interview_slot (org_id, vetter_id, application_id, stage_id, candidate_timezone, starts_at, ends_at, status, created_at)
		values ($1, $2, $3, $4, 'Europe/London', $5, $6, $7, $5)`, s.org, vetter.ID, appID, stg.ID, start, start.Add(time.Duration(minutes)*time.Minute), status); err != nil {
		return err
	}
	s.count("interviews", 1)
	return nil
}

func (s *seeder) scorecard(appID uuid.UUID, stg domain.Stage, vetter user, at time.Time, overall string) error {
	scores, _ := json.Marshal([]service.CriterionScore{
		{Name: "Problem solving", Score: 3 + s.r.Intn(2), Notes: "Worked the example before generalising"},
		{Name: "Code quality", Score: 2 + s.r.Intn(3)},
		{Name: "Communication", Score: 3 + s.r.Intn(2), Notes: "Asked about the edge cases unprompted"},
		{Name: "Ownership", Score: 2 + s.r.Intn(3)},
	})
	if err := s.exec(`insert into scorecard (org_id, application_id, stage_id, vetter_id, rubric_id, scores, overall, notes, created_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, s.org, appID, stg.ID, vetter.ID, s.rubric, scores, overall,
		[]string{"Solid on fundamentals, would want to see them on a bigger system.", "Clear thinker; slightly slow to start coding.", "Strong communicator, a little light on testing."}[s.r.Intn(3)], at); err != nil {
		return err
	}
	s.count("scorecards", 1)
	return nil
}

func (s *seeder) release(appID uuid.UUID, recruiter user, at time.Time) error {
	if err := s.exec(`update application set released_at = $2 where id = $1`, appID, at); err != nil {
		return err
	}
	return s.exec(`insert into application_event (org_id, application_id, actor_kind, actor_id, kind, payload, created_at) values ($1, $2, 'org_user', $3, 'released', '{}', $4)`,
		s.org, appID, recruiter.ID, at)
}

// attempt writes one sitting on the job's assessment: invited, lapsed,
// started, or scored (with submissions, per-problem scores, integrity
// signals, and a verdict when one is given).
func (s *seeder) attempt(j job, appID uuid.UUID, stg domain.Stage, state string, entered time.Time, score int, verdict *string) error {
	if j.Assess == uuid.Nil {
		return nil
	}
	id := uuid.New()
	invited := entered
	expires := invited.Add(s.days(7))
	if state == "lapsed" {
		invited = s.ago(s.days(9))
		expires = s.ago(s.days(2))
		state = "invited"
	}
	if err := s.exec(`insert into attempt (id, org_id, application_id, assessment_id, stage_id, status, invited_at, invite_expires_at, created_at, updated_at)
		values ($1, $2, $3, $4, $5, 'invited', $6, $7, $6, $6)`, id, s.org, appID, j.Assess, stg.ID, invited, expires); err != nil {
		return err
	}
	s.count("attempts", 1)
	switch state {
	case "invited":
		return nil
	case "started":
		started := s.ago(35 * time.Minute)
		return s.exec(`update attempt set status = 'started', started_at = $2, expires_at = $3 where id = $1`, id, started, started.Add(90*time.Minute))
	}
	// scored
	started := invited.Add(s.days(1))
	finished := started.Add(time.Duration(40+s.r.Intn(45)) * time.Minute)
	problems, err := s.assessmentProblems(j.Assess)
	if err != nil {
		return err
	}
	var scores []service.ProblemScore
	total := 0.0
	for i, p := range problems {
		// Spread the target score over the problems: the first is solved
		// best, the last worst.
		share := float64(score)/100 + float64(len(problems)-1-2*i)*0.12
		if share > 1 {
			share = 1
		}
		if share < 0 {
			share = 0
		}
		ps, err := s.submission(id, p, share, finished.Add(-time.Duration(len(problems)-i)*8*time.Minute))
		if err != nil {
			return err
		}
		scores = append(scores, ps)
		total += ps.Score
	}
	mean := 0.0
	if len(scores) > 0 {
		mean = 100 * total / float64(len(scores))
	}
	breakdown, _ := json.Marshal(scores)
	risk := s.r.Float64() * 25
	if err := s.exec(`update attempt set status = 'scored', started_at = $2, expires_at = $3, finished_at = $4, score = $5, problem_scores = $6, risk_score = $7, recording_status = 'incomplete', updated_at = $4 where id = $1`,
		id, started, started.Add(90*time.Minute), finished, fmt.Sprintf("%.2f", mean), breakdown, fmt.Sprintf("%.1f", risk)); err != nil {
		return err
	}
	for _, sig := range []struct {
		name   string
		value  float64
		weight float64
	}{
		{"paste_ratio", s.r.Float64() * 0.3, 25}, {"paste_then_pass", 0, 25}, {"burst_typing", s.r.Float64() * 0.2, 10},
		{"edit_ratio", 0.4 + s.r.Float64()*0.4, 10}, {"blur_then_solution", 0, 15}, {"speed_vs_difficulty", s.r.Float64() * 0.5, 5},
		{"reference_similarity", s.r.Float64() * 0.15, 10}, {"fullscreen_exits", float64(s.r.Intn(2)), 10}, {"snapshot_gaps", 0, 5},
	} {
		if err := s.exec(`insert into integrity_signal (org_id, attempt_id, name, value, weight, confidence, evidence) values ($1, $2, $3, $4, $5, 'normal', '[]')`,
			s.org, id, sig.name, fmt.Sprintf("%.3f", sig.value), fmt.Sprint(sig.weight)); err != nil {
			return err
		}
	}
	if verdict != nil {
		vetter := s.vetters[s.r.Intn(len(s.vetters))]
		if err := s.exec(`insert into review (org_id, attempt_id, vetter_id, verdict, notes, created_at) values ($1, $2, $3, $4, $5, $6)`,
			s.org, id, vetter.ID, *verdict, map[string]string{"pass": "Clean solutions, sensible names, handled the edges.", "fail": "Two of three unsolved; the one that passed was brute force.", "borderline": "Right ideas, ran out of time."}[*verdict], finished.Add(s.days(1))); err != nil {
			return err
		}
		if err := s.exec(`update attempt set status = 'reviewed' where id = $1`, id); err != nil {
			return err
		}
		s.count("reviews", 1)
	}
	return nil
}

func (s *seeder) assessmentProblems(assessmentID uuid.UUID) ([]problem, error) {
	rows, err := s.tx.Query(s.ctx, `select problem_id from assessment_problem where assessment_id = $1 order by position`, assessmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []problem
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		for _, p := range s.problems {
			if p.ID == id {
				out = append(out, p)
			}
		}
	}
	return out, rows.Err()
}

// submission writes the scored submit of one problem: the cases pass in
// order until the share of weight is spent, the way a partial solution
// usually passes the easy cases and fails the hard ones.
func (s *seeder) submission(attemptID uuid.UUID, p problem, share float64, at time.Time) (service.ProblemScore, error) {
	id := uuid.New()
	total, passed := 0.0, 0.0
	for _, c := range p.Cases {
		total += c.Weight
	}
	res := server.Response{ID: id.String(), Status: server.StatusOK}
	budget := share * total
	for _, c := range p.Cases {
		status := "fail"
		if passed+c.Weight <= budget+1e-9 {
			status = "pass"
			passed += c.Weight
		}
		res.Results = append(res.Results, server.TestResult{TestID: c.ID.String(), Status: status, TimeMs: int64(5 + s.r.Intn(120)), MemKB: int64(20000 + s.r.Intn(30000))})
	}
	source := p.Solution
	if share < 0.99 {
		source = "# partial solution\n" + strings.Replace(source, "return", "return  # TODO: handle every case\n        return", 1)
	}
	raw, _ := json.Marshal(res)
	scoreFrac := 0.0
	if total > 0 {
		scoreFrac = passed / total
	}
	if err := s.exec(`insert into submission (id, org_id, attempt_id, problem_id, kind, language, source, status, result, score, created_at, updated_at)
		values ($1, $2, $3, $4, 'submit', 'python', $5, 'done', $6, $7, $8, $8)`, id, s.org, attemptID, p.ID, source, raw, fmt.Sprintf("%.4f", scoreFrac), at); err != nil {
		return service.ProblemScore{}, err
	}
	return service.ProblemScore{ProblemID: p.ID, SubmissionID: id, Score: scoreFrac, PassedWeight: passed, TotalWeight: total, Status: server.StatusOK}, nil
}

// writeSprint writes a sprint that ran yesterday: its interviewers, its
// candidates, every pairing of the round-robin, and a rating for each pairing
// except the ones marked as owed.
func (s *seeder) writeSprint(run *sprintRun) error {
	if err := s.exec(`insert into sprint (id, org_id, job_id, stage_id, name, status, starts_at, round_seconds, break_seconds, created_by, created_at, updated_at)
		values ($1, $2, $3, $4, $5, 'scheduled', $6, $7, $8, $9, $10, $10)`,
		run.ID, s.org, run.Job.ID, run.Stage.ID, run.Job.Title+" screening sprint", run.StartsAt, run.Stage.RoundSeconds, run.Stage.BreakSeconds, s.recruiters[0].ID, run.StartsAt.Add(-s.days(2))); err != nil {
		return err
	}
	for i, u := range run.Interview {
		if err := s.exec(`insert into sprint_interviewer (sprint_id, org_id, user_id, position) values ($1, $2, $3, $4)`, run.ID, s.org, u.ID, i); err != nil {
			return err
		}
	}
	for i, appID := range run.Members {
		if err := s.exec(`insert into sprint_candidate (sprint_id, org_id, application_id, position) values ($1, $2, $3, $4)`, run.ID, s.org, appID, i); err != nil {
			return err
		}
	}
	// The rounds are the ones the sprint console would have run, from the
	// domain's own planner, so the summary screen reads them back exactly.
	interviewers := make([]uuid.UUID, 0, len(run.Interview))
	for _, u := range run.Interview {
		interviewers = append(interviewers, u.ID)
	}
	for _, pr := range domain.PlanRounds(run.Members, interviewers) {
		pairing := uuid.New()
		if err := s.exec(`insert into sprint_pairing (id, sprint_id, org_id, round, interviewer_id, application_id) values ($1, $2, $3, $4, $5, $6)`,
			pairing, run.ID, s.org, pr.Round, pr.InterviewerID, pr.ApplicationID); err != nil {
			return err
		}
		if run.Unrated[pr.ApplicationID] && pr.InterviewerID == interviewers[0] {
			continue // the first interviewer never got round to it
		}
		score := 2 + s.r.Intn(4)
		rec := map[int]string{2: "no", 3: "yes", 4: "yes", 5: "strong_yes"}[score]
		if err := s.exec(`insert into sprint_rating (org_id, sprint_id, pairing_id, interviewer_id, application_id, score, recommendation, note, created_at)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, s.org, run.ID, pairing, pr.InterviewerID, pr.ApplicationID, score, rec,
			[]string{"Quick to the point.", "Good questions about the data model.", "Needed the prompt read twice.", "Would take them to the next round."}[s.r.Intn(4)],
			run.StartsAt.Add(time.Duration(pr.Round+1)*time.Duration(run.Stage.RoundSeconds+run.Stage.BreakSeconds)*time.Second)); err != nil {
			return err
		}
	}
	s.count("sprints", 1)
	return nil
}

// ---------------------------------------------------------------- shortlists and network

func (s *seeder) createShortlists() error {
	// One sent packet and one draft, on jobs that have released candidates.
	rows, err := s.tx.Query(s.ctx, `select job_id, array_agg(id order by created_at) from application where org_id = $1 and released_at is not null and status = 'active' group by job_id order by count(*) desc limit 2`, s.org)
	if err != nil {
		return err
	}
	type packet struct {
		job  uuid.UUID
		apps []uuid.UUID
	}
	var packets []packet
	for rows.Next() {
		var p packet
		if err := rows.Scan(&p.job, &p.apps); err != nil {
			rows.Close()
			return err
		}
		packets = append(packets, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i, p := range packets {
		id := uuid.New()
		status, sentAt, sentBy := "draft", (*time.Time)(nil), uuid.NullUUID{}
		if i == 0 {
			status = "sent"
			t := s.ago(s.days(3))
			sentAt, sentBy = &t, uuid.NullUUID{UUID: s.recruiters[0].ID, Valid: true}
		}
		if err := s.exec(`insert into shortlist_packet (id, org_id, job_id, status, note, sent_at, sent_by, created_by, created_at, updated_at)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)`, id, s.org, p.job, status,
			"Three strong profiles; the first two cleared the take-home with time to spare.", sentAt, sentBy, s.recruiters[0].ID, s.ago(s.days(4))); err != nil {
			return err
		}
		for rank, appID := range p.apps {
			if rank >= 3 {
				break
			}
			if err := s.exec(`insert into shortlist_pick (org_id, packet_id, application_id, rank) values ($1, $2, $3, $4)`, s.org, id, appID, rank+1); err != nil {
				return err
			}
		}
		s.count("shortlists", 1)
	}
	return nil
}

func (s *seeder) createTalentNetwork() error {
	// Everyone who applied nowhere joined the network, plus a few who did.
	var joined []seededCandidate
	for _, c := range candidates {
		if c.Applied == 0 || s.r.Intn(4) == 0 {
			joined = append(joined, c)
		}
	}
	for _, c := range joined {
		roles := []string{strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(c.Headline, "Junior "), "Senior "), "Staff ")}
		remote := []string{"remote", "hybrid", "onsite"}[s.r.Intn(3)]
		if err := s.exec(`insert into talent_profile (org_id, candidate_id, headline, skills, seniority, roles, location, remote_policy, salary_min, available_from, consent_at, created_at, updated_at)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11, $11)`, s.org, c.ID, c.Headline, c.Skills, c.Seniority, roles, c.City, remote,
			40000+10000*s.r.Intn(9), s.now.Add(s.days(float64(7*s.r.Intn(8)))).Format("2006-01-02"), s.ago(s.days(float64(1+s.r.Intn(20))))); err != nil {
			return err
		}
		s.count("talent profiles", 1)
	}
	// Two companies asked the network for people; one has an introduction
	// waiting on a recruiter, one already sent.
	requests := []struct {
		client  int
		title   string
		skills  []string
		sen     string
		remote  string
		note    string
		intros  int
		sentOne bool
	}{
		{0, "Go engineer for the routing team", []string{"go", "postgres", "kubernetes"}, "senior", "hybrid", "Someone who has run a service on-call, not just written one.", 3, true},
		{1, "React engineer, contract to perm", []string{"typescript", "react", "graphql"}, "mid", "remote", "Merchant dashboard work; strong on forms and tables.", 2, false},
	}
	var companyIDs []uuid.UUID
	crow, err := s.tx.Query(s.ctx, `select id from client_company where org_id = $1 order by created_at, name`, s.org)
	if err != nil {
		return err
	}
	for crow.Next() {
		var id uuid.UUID
		if err := crow.Scan(&id); err != nil {
			crow.Close()
			return err
		}
		companyIDs = append(companyIDs, id)
	}
	crow.Close()
	for _, rq := range requests {
		if rq.client >= len(companyIDs) {
			continue
		}
		id := uuid.New()
		if err := s.exec(`insert into talent_request (id, org_id, client_company_id, title, skills, seniority, remote_policy, note, created_at, updated_at)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)`, id, s.org, companyIDs[rq.client], rq.title, rq.skills, rq.sen, rq.remote, rq.note, s.ago(s.days(5))); err != nil {
			return err
		}
		s.count("talent requests", 1)
		// Introductions to the best-matching joined profiles.
		matched := 0
		for _, c := range joined {
			if matched >= rq.intros || !overlaps(c.Skills, rq.skills) {
				continue
			}
			status, sentAt, sentBy := "requested", (*time.Time)(nil), uuid.NullUUID{}
			if rq.sentOne && matched == 0 {
				status = "sent"
				t := s.ago(s.days(1))
				sentAt, sentBy = &t, uuid.NullUUID{UUID: s.recruiters[0].ID, Valid: true}
			}
			if err := s.exec(`insert into talent_intro (org_id, request_id, candidate_id, source, score, status, sent_by, requested_at, sent_at)
				values ($1, $2, $3, 'network', $4, $5, $6, $7, $8)`, s.org, id, c.ID, 0.55+s.r.Float64()*0.4, status, sentBy, s.ago(s.days(3)), sentAt); err != nil {
				return err
			}
			matched++
			s.count("introductions", 1)
		}
	}
	return nil
}

func overlaps(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------- small helpers

func deref(n *int32) int {
	if n == nil {
		return 0
	}
	return int(*n)
}

func nullInt(n int) *int32 {
	if n == 0 {
		return nil
	}
	v := int32(n)
	return &v
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}

func formatOr(f string) string {
	if f == "" {
		return domain.FormatCall
	}
	return f
}
