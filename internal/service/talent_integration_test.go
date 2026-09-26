//go:build integration

package service_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/mail"
	"recruiting/internal/queue"
	"recruiting/internal/service"
)

type talentFixture struct {
	*candidateFixture
	talent   *service.TalentService
	portal   *service.ClientPortalService
	tokens   *service.APITokenService
	clientID uuid.UUID
	job      service.Job
}

// newTalentFixture adds a client user at the job fixture's company and an
// open job with the skills a request is matched on.
func newTalentFixture(t *testing.T) *talentFixture {
	t.Helper()
	cf := newCandidateFixture(t)
	q, err := queue.New(cf.st.Pool(), queue.Config{})
	if err != nil {
		t.Fatal(err)
	}
	f := &talentFixture{
		candidateFixture: cf, clientID: uuid.New(),
		talent: service.NewTalentService(cf.st, cf.resumes, cf.links, q, "https://example.test/"),
		tokens: service.NewAPITokenService(cf.st),
	}
	apps := service.NewApplicationService(cf.st, q, "https://example.test/")
	f.portal = service.NewClientPortalService(cf.st, apps, cf.resumes, q, "https://example.test/")
	if _, err := cf.sys.Exec(cf.ctx, `insert into client_user (id, org_id, client_company_id, email, name) values ($1, $2, $3, $4, 'Carl Client')`,
		f.clientID, cf.orgID, cf.companyID, "client-"+cf.orgID.String()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	f.job = cf.newJob(t)
	return f
}

func (f *talentFixture) client() service.Principal {
	return service.Principal{Kind: service.PrincipalClientUser, OrgID: f.orgID, UserID: f.clientID, ClientCompanyID: f.companyID}
}

func (f *talentFixture) join(t *testing.T, email string, skills []string, resume []byte) service.TalentProfile {
	t.Helper()
	in := service.TalentProfileInput{
		Name: "Grace Hopper", Email: email, Skills: skills, Seniority: service.SenioritySenior, Location: "Berlin",
		RemotePolicy: service.RemoteRemote, Headline: "Compiler engineer", Roles: []string{"tech lead"}, Consent: true,
	}
	if resume != nil {
		in.Resume = &service.ResumeUpload{Filename: "grace.docx", Data: resume}
	}
	prof, err := f.talent.Join(f.ctx, f.orgSlug(), in)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	return prof
}

// link is the principal a magic link of purpose resolves to for a subject,
// found by the row the service issued.
func (f *talentFixture) link(t *testing.T, purpose string, subject uuid.UUID) service.Principal {
	t.Helper()
	var n int
	if err := f.sys.QueryRow(f.ctx, `select count(*) from magic_link where org_id = $1 and purpose = $2 and subject_id = $3 and revoked_at is null`,
		f.orgID, purpose, subject).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatalf("no %s link was issued for %s", purpose, subject)
	}
	return service.Principal{Kind: service.PrincipalMagicLink, OrgID: f.orgID, MagicPurpose: purpose, SubjectID: subject}
}

func TestTalentNetworkFromJoinToAcceptedIntroduction(t *testing.T) {
	f := newTalentFixture(t)
	email := "grace@example.com"
	prof := f.join(t, email, []string{"go", "postgres", "rust"}, docxBytes(t, "Grace Hopper", "Kubernetes operator work at scale"))
	if prof.Withdrawn || !prof.HasResume || prof.ConsentAt.IsZero() || prof.Name != "Grace Hopper" {
		t.Fatalf("profile = %+v", prof)
	}
	if n := f.emails(t, mail.TemplateTalentWelcome, email); n != 1 {
		t.Fatalf("welcome emails = %d, want 1", n)
	}
	me := f.link(t, service.LinkProfile, prof.ID)
	if got, err := f.talent.Profile(f.ctx, me); err != nil || got.ID != prof.ID {
		t.Fatalf("profile by link = %+v, %v", got, err)
	}

	// The company describes who it wants and reads anonymised matches.
	client := f.client()
	req, err := f.talent.CreateRequest(f.ctx, client, service.TalentRequestInput{
		Title: "Platform engineer", Skills: []string{"Go", "postgres", "kubernetes"}, Seniority: "senior",
		RemotePolicy: "remote", JobID: f.job.ID, Note: "Someone who has run clusters.",
	})
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if req.JobTitle != f.job.Title || !req.Open() {
		t.Fatalf("request = %+v", req)
	}
	matches, err := f.talent.Matches(f.ctx, client, req.ID)
	if err != nil {
		t.Fatalf("matches: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %+v, want the one member", matches)
	}
	m := matches[0]
	if m.Source != domain.TalentSourceNetwork || m.ID != prof.ID || m.Score < domain.TalentThreshold {
		t.Fatalf("match = %+v", m)
	}
	if len(m.SharedSkills) != 2 || m.SharedSkills[0] != "go" || m.SharedSkills[1] != "postgres" {
		t.Fatalf("shared skills = %v", m.SharedSkills)
	}
	if m.Breakdown.Text != 1 || m.Breakdown.Seniority != 1 || m.Breakdown.Location != 1 {
		t.Fatalf("breakdown = %+v, want the résumé, seniority and location to fit", m.Breakdown)
	}
	if m.Headline != "Compiler engineer" || !m.HasResume || m.IntroStatus != "" {
		t.Fatalf("match card = %+v", m)
	}

	// Asking for the introduction names nobody and cannot be asked twice.
	intro, err := f.talent.Introduce(f.ctx, client, req.ID, m.ID)
	if err != nil {
		t.Fatalf("introduce: %v", err)
	}
	if intro.Status != service.IntroRequested || intro.Label == "Grace Hopper" || intro.ApplicationID != uuid.Nil {
		t.Fatalf("intro = %+v", intro)
	}
	if _, err := f.talent.Introduce(f.ctx, client, req.ID, m.ID); !errors.Is(err, service.ErrIntroRequested) {
		t.Fatalf("second introduce = %v, want ErrIntroRequested", err)
	}
	matches, _ = f.talent.Matches(f.ctx, client, req.ID)
	if len(matches) != 1 || matches[0].IntroStatus != service.IntroRequested {
		t.Fatalf("matches after asking = %+v", matches)
	}
	// The company reads the introduction it asked for before the person is
	// anyone it may name.
	if waiting, err := f.talent.Intros(f.ctx, client, req.ID); err != nil || len(waiting) != 1 || waiting[0].Status != service.IntroRequested || waiting[0].Label == "Grace Hopper" {
		t.Fatalf("company's intros while waiting = %+v, %v", waiting, err)
	}
	if again, _ := f.talent.Request(f.ctx, client, req.ID); again.IntroCount != 1 || again.WaitingCount != 1 {
		t.Fatalf("request counts = %+v", again)
	}
	if _, err := f.talent.Introduce(f.ctx, client, req.ID, uuid.New()); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("introduce to a made-up match = %v, want ErrNotFound", err)
	}

	// The recruiter sees the person and sends them the opportunity.
	rec := f.recruiter()
	d, err := f.talent.RequestForRecruiter(f.ctx, rec, req.ID)
	if err != nil {
		t.Fatalf("request for recruiter: %v", err)
	}
	if len(d.Intros) != 1 || d.Intros[0].CandidateName != "Grace Hopper" || len(d.Jobs) != 1 {
		t.Fatalf("recruiter view = %+v", d)
	}
	sent, err := f.talent.SendOpportunity(f.ctx, rec, intro.ID, uuid.Nil)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if sent.Status != service.IntroSent || sent.JobID != f.job.ID || sent.SentAt == nil {
		t.Fatalf("sent intro = %+v", sent)
	}
	if n := f.emails(t, mail.TemplateOpportunity, email); n != 1 {
		t.Fatalf("opportunity emails = %d, want 1", n)
	}
	if _, err := f.talent.SendOpportunity(f.ctx, rec, intro.ID, uuid.Nil); !errors.Is(err, service.ErrIntroNotWaiting) {
		t.Fatalf("second send = %v, want ErrIntroNotWaiting", err)
	}

	// The person reads the role and says yes: an application opens on the
	// job, released to the company, and the company can now name them.
	them := f.link(t, service.LinkOpportunity, intro.ID)
	op, err := f.talent.Opportunity(f.ctx, them)
	if err != nil {
		t.Fatalf("opportunity: %v", err)
	}
	if op.JobTitle != f.job.Title || op.ClientCompanyName != "Globex" || op.Status != service.IntroSent {
		t.Fatalf("opportunity = %+v", op)
	}
	op, err = f.talent.Answer(f.ctx, them, true)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if op.Status != service.IntroAccepted || op.ApplicationID == uuid.Nil {
		t.Fatalf("answered = %+v", op)
	}
	if _, err := f.talent.Answer(f.ctx, them, false); !errors.Is(err, service.ErrIntroNotSent) {
		t.Fatalf("answering twice = %v, want ErrIntroNotSent", err)
	}
	intros, err := f.talent.Intros(f.ctx, client, req.ID)
	if err != nil || len(intros) != 1 || intros[0].Label != "Grace Hopper" || intros[0].ApplicationID != op.ApplicationID {
		t.Fatalf("company's intros = %+v, %v", intros, err)
	}
	apps, err := f.portal.Applications(f.ctx, client, service.ClientApplicationFilter{JobID: f.job.ID})
	if err != nil || len(apps) != 1 || apps[0].ID != op.ApplicationID || apps[0].Candidate.Label != "Grace Hopper" {
		t.Fatalf("released applications = %+v, %v", apps, err)
	}
	events, err := f.portal.Events(f.ctx, client, 0, 10)
	if err != nil || len(events) == 0 || events[0].Kind != service.EventReleased || events[0].ApplicationID != op.ApplicationID {
		t.Fatalf("company's events = %+v, %v", events, err)
	}
	if more, _ := f.portal.Events(f.ctx, client, events[len(events)-1].Seq, 10); len(more) != 0 {
		t.Fatalf("events after the cursor = %+v, want none", more)
	}
	if n := f.emails(t, mail.TemplateClientReleaseNotice, "client-"+f.orgID.String()+"@example.com"); n != 1 {
		t.Fatalf("release notices = %d, want 1", n)
	}

	// Once in the company's pipeline the person is no longer a match for
	// its other requests.
	other, err := f.talent.CreateRequest(f.ctx, client, service.TalentRequestInput{Title: "Another", Skills: []string{"go"}})
	if err != nil {
		t.Fatal(err)
	}
	if matches, _ := f.talent.Matches(f.ctx, client, other.ID); len(matches) != 0 {
		t.Fatalf("a hired-in person still matches: %+v", matches)
	}
}

func TestTalentProfileIsEditedWithdrawnAndRejoined(t *testing.T) {
	f := newTalentFixture(t)
	prof := f.join(t, "ada@example.com", []string{"go"}, nil)
	me := f.link(t, service.LinkProfile, prof.ID)
	client := f.client()
	req, err := f.talent.CreateRequest(f.ctx, client, service.TalentRequestInput{Title: "Go", Skills: []string{"go"}})
	if err != nil {
		t.Fatal(err)
	}
	if matches, _ := f.talent.Matches(f.ctx, client, req.ID); len(matches) != 1 || matches[0].Breakdown.Text != 0 {
		t.Fatalf("matches without a résumé = %+v", matches)
	}

	from := time.Date(2027, time.January, 4, 0, 0, 0, 0, time.UTC)
	got, err := f.talent.UpdateProfile(f.ctx, me, service.TalentProfileEdit{
		Skills: []string{"go", "elixir"}, Location: "Lisbon", RemotePolicy: "hybrid", SalaryMin: 90000, AvailableFrom: &from,
		Phone: "+351 1", Links: []string{"https://ada.example"},
		Resume: &service.ResumeUpload{Filename: "ada.docx", Data: docxBytes(t, "Ada", "Elixir and Erlang")},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Location != "Lisbon" || got.SalaryMin != 90000 || got.AvailableFrom == nil || !got.AvailableFrom.Equal(from) || !got.HasResume || got.Phone != "+351 1" {
		t.Fatalf("updated profile = %+v", got)
	}
	if _, err := f.talent.UpdateProfile(f.ctx, me, service.TalentProfileEdit{Skills: []string{"go"}, Seniority: "wizard"}); !errors.Is(err, service.ErrInvalidJob) {
		t.Fatalf("bad seniority = %v, want ErrInvalidJob", err)
	}

	if err := f.talent.Withdraw(f.ctx, me); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if matches, _ := f.talent.Matches(f.ctx, client, req.ID); len(matches) != 0 {
		t.Fatalf("a withdrawn member still matches: %+v", matches)
	}
	if p, _ := f.talent.Profile(f.ctx, me); !p.Withdrawn {
		t.Fatal("profile does not read as withdrawn")
	}
	if err := f.talent.Rejoin(f.ctx, me); err != nil {
		t.Fatalf("rejoin: %v", err)
	}
	if matches, _ := f.talent.Matches(f.ctx, client, req.ID); len(matches) != 1 {
		t.Fatalf("a rejoined member does not match: %+v", matches)
	}

	// Joining again with the same address replaces the details and mails a
	// fresh link; the name on file is kept.
	again, err := f.talent.Join(f.ctx, f.orgSlug(), service.TalentProfileInput{
		Name: "Someone Else", Email: "ADA@example.com", Skills: []string{"rust"}, Consent: true,
	})
	if err != nil {
		t.Fatalf("join again: %v", err)
	}
	if again.ID != prof.ID || again.Name != "Grace Hopper" || len(again.Skills) != 1 || again.Skills[0] != "rust" {
		t.Fatalf("rejoined profile = %+v", again)
	}
	if n := f.emails(t, mail.TemplateTalentWelcome, "ada@example.com"); n != 2 {
		t.Fatalf("welcome emails = %d, want 2", n)
	}
	// The recruiter's list and detail read the member.
	list, err := f.talent.Profiles(f.ctx, f.recruiter(), "elixir", false)
	if err != nil || len(list) != 1 {
		t.Fatalf("search by résumé text = %+v, %v", list, err)
	}
	if _, err := f.talent.ProfileByID(f.ctx, f.client(), prof.ID); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("a client reading a profile = %v, want ErrForbidden", err)
	}
}

func TestTalentJoinNeedsConsentSkillsAndAKnownOrg(t *testing.T) {
	f := newTalentFixture(t)
	in := service.TalentProfileInput{Name: "N", Email: "n@example.com", Skills: []string{"go"}}
	if _, err := f.talent.Join(f.ctx, f.orgSlug(), in); !errors.Is(err, service.ErrTalentConsent) {
		t.Fatalf("no consent = %v", err)
	}
	in.Consent = true
	in.Skills = nil
	if _, err := f.talent.Join(f.ctx, f.orgSlug(), in); !errors.Is(err, service.ErrTalentSkills) {
		t.Fatalf("no skills = %v", err)
	}
	in.Skills = []string{"go"}
	if _, err := f.talent.Join(f.ctx, "no-such-org", in); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("unknown org = %v", err)
	}
	if _, err := f.talent.Org(f.ctx, ""); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("empty slug = %v", err)
	}
}

func TestTalentRequestRulesForTheCompany(t *testing.T) {
	f := newTalentFixture(t)
	client := f.client()
	if _, err := f.talent.CreateRequest(f.ctx, client, service.TalentRequestInput{Skills: []string{"go"}}); !errors.Is(err, service.ErrTalentTitle) {
		t.Fatalf("no title = %v", err)
	}
	if _, err := f.talent.CreateRequest(f.ctx, client, service.TalentRequestInput{Title: "x"}); !errors.Is(err, service.ErrTalentSkills) {
		t.Fatalf("no skills = %v", err)
	}
	if _, err := f.talent.CreateRequest(f.ctx, client, service.TalentRequestInput{Title: "x", Skills: []string{"go"}, JobID: uuid.New()}); !errors.Is(err, service.ErrIntroJob) {
		t.Fatalf("a stranger's job = %v", err)
	}
	req, err := f.talent.CreateRequest(f.ctx, client, service.TalentRequestInput{Title: "x", Skills: []string{"go"}})
	if err != nil {
		t.Fatal(err)
	}
	// Another company cannot read it.
	rivalID := uuid.New()
	if _, err := f.sys.Exec(f.ctx, `insert into client_company (id, org_id, name) values ($1, $2, 'Initech')`, rivalID, f.orgID); err != nil {
		t.Fatal(err)
	}
	rival := service.Principal{Kind: service.PrincipalClientUser, OrgID: f.orgID, UserID: uuid.New(), ClientCompanyID: rivalID}
	if _, err := f.talent.Request(f.ctx, rival, req.ID); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("rival reading the request = %v, want ErrNotFound", err)
	}
	if err := f.talent.CloseRequest(f.ctx, client, req.ID); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := f.talent.CloseRequest(f.ctx, client, req.ID); !errors.Is(err, service.ErrTalentRequestClosed) {
		t.Fatalf("closing twice = %v", err)
	}
	if _, err := f.talent.Introduce(f.ctx, client, req.ID, uuid.New()); !errors.Is(err, service.ErrTalentRequestClosed) {
		t.Fatalf("introduce on a closed request = %v", err)
	}
	// The pool is a second source: a past applicant the desk rates highly.
	prof := f.join(t, "pool@example.com", []string{"go"}, nil)
	if _, err := f.sys.Exec(f.ctx, `insert into talent_pool_entry (org_id, candidate_id, skills, seniority) values ($1, $2, '{go,postgres}', 'senior')`, f.orgID, prof.CandidateID); err != nil {
		t.Fatal(err)
	}
	open, err := f.talent.CreateRequest(f.ctx, client, service.TalentRequestInput{Title: "y", Skills: []string{"go", "postgres"}, Seniority: "senior"})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := f.talent.Matches(f.ctx, client, open.ID)
	if err != nil || len(matches) != 1 || matches[0].Source != domain.TalentSourcePool {
		t.Fatalf("matches = %+v, %v; want the person once, by their better pool entry", matches, err)
	}
	// Declining shares nothing and closes the introduction.
	intro, err := f.talent.Introduce(f.ctx, client, open.ID, matches[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.talent.SendOpportunity(f.ctx, f.recruiter(), intro.ID, uuid.Nil); !errors.Is(err, service.ErrTalentJobRequired) {
		t.Fatalf("send without a job = %v, want ErrTalentJobRequired", err)
	}
	if _, err := f.talent.SendOpportunity(f.ctx, f.recruiter(), intro.ID, f.job.ID); err != nil {
		t.Fatalf("send: %v", err)
	}
	op, err := f.talent.Answer(f.ctx, f.link(t, service.LinkOpportunity, intro.ID), false)
	if err != nil || op.Status != service.IntroDeclined || op.ApplicationID != uuid.Nil {
		t.Fatalf("declined = %+v, %v", op, err)
	}
	if apps, _ := f.portal.Applications(f.ctx, client, service.ClientApplicationFilter{}); len(apps) != 0 {
		t.Fatalf("a decline opened an application: %+v", apps)
	}
	if err := f.talent.DismissIntro(f.ctx, f.recruiter(), intro.ID); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("dismissing an answered intro = %v, want ErrNotFound", err)
	}
}

func TestClientUserIssuesTheirOwnTokens(t *testing.T) {
	f := newTalentFixture(t)
	client := f.client()
	tok, secret, err := f.tokens.IssueOwn(f.ctx, client, "sync", nil)
	if err != nil {
		t.Fatalf("issue own: %v", err)
	}
	if !tok.IsClient || tok.UserID != f.clientID || secret == "" {
		t.Fatalf("token = %+v", tok)
	}
	p, err := f.tokens.ResolveToken(f.ctx, secret)
	if err != nil || p.Kind != service.PrincipalClientUser || p.ClientCompanyID != f.companyID {
		t.Fatalf("resolved = %+v, %v", p, err)
	}
	list, err := f.tokens.ListOwn(f.ctx, client)
	if err != nil || len(list) != 1 || list[0].ID != tok.ID {
		t.Fatalf("own tokens = %+v, %v", list, err)
	}
	if _, _, err := f.tokens.IssueOwn(f.ctx, client, " ", nil); !errors.Is(err, service.ErrTokenName) {
		t.Fatalf("nameless = %v", err)
	}
	other := service.Principal{Kind: service.PrincipalClientUser, OrgID: f.orgID, UserID: uuid.New(), ClientCompanyID: f.companyID}
	if err := f.tokens.RevokeOwn(f.ctx, other, tok.ID); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("another user revoking = %v, want ErrNotFound", err)
	}
	if err := f.tokens.RevokeOwn(f.ctx, client, tok.ID); err != nil {
		t.Fatalf("revoke own: %v", err)
	}
	if _, err := f.tokens.ResolveToken(f.ctx, secret); !errors.Is(err, service.ErrNoSession) {
		t.Fatalf("revoked token resolves: %v", err)
	}
	if _, _, err := f.tokens.IssueOwn(f.ctx, f.recruiter(), "x", nil); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("an org user issuing an own token = %v", err)
	}
	for i := 0; i < service.ClientTokenLimit; i++ {
		if _, _, err := f.tokens.IssueOwn(f.ctx, client, "n", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := f.tokens.IssueOwn(f.ctx, client, "one more", nil); !errors.Is(err, service.ErrTokenLimit) {
		t.Fatalf("over the limit = %v", err)
	}
}
