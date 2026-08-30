package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"recruiting/internal/domain"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// What put a candidate in the pool. Each is a way an application earns an
// entry; the entry keeps the most recent one.
const (
	PoolSourceFlag      = "recruiter_flag"
	PoolSourceScorecard = "scorecard_strong_yes"
	PoolSourceReview    = "assessment_review"
)

// PoolListLimit caps a pool listing; like the candidate list it is a working
// view, not an export.
const PoolListLimit = 200

// PoolRankLimit bounds the ranking pass. Ranking reads the whole pool rather
// than a page of it, so a good match is not lost to a listing cut-off; the
// cap is a guard against an unbounded read, not a page size.
const PoolRankLimit = 10000

// PoolEntry is one person kept in the org's talent pool, aggregated from the
// applications that earned them a place in it.
type PoolEntry struct {
	ID             uuid.UUID
	CandidateID    uuid.UUID
	CandidateName  string
	CandidateEmail string
	Skills         []string
	Seniority      string
	Location       string
	RemoteOK       bool
	// BestScores is the candidate's best assessment score per problem tag.
	BestScores       map[string]float64
	ScorecardSummary string
	Notes            string
	SourceJobIDs     []uuid.UUID
	Source           string
	UpdatedAt        time.Time
}

// BestAssessmentScore is the entry's best score across every tag, which is
// what the ranker scores the assessment part on.
func (e PoolEntry) BestAssessmentScore() float64 {
	var best float64
	for _, v := range e.BestScores {
		if v > best {
			best = v
		}
	}
	return best
}

// PoolEdit is the recruiter's edit of an entry: the tags it is found by,
// where the person will work, and the recruiter's own notes. Everything else
// is aggregated, not typed.
type PoolEdit struct {
	Skills   []string
	Location string
	RemoteOK bool
	Notes    string
}

// PoolSuggestion is one pool entry ranked against a job, with the breakdown
// that explains the score.
type PoolSuggestion struct {
	Entry     PoolEntry
	Score     float64
	Breakdown domain.MatchBreakdown
}

var (
	// ErrPoolAlreadyOnJob is the pool's own voice for a duplicate: the panel
	// is read by a recruiter, not by the candidate.
	ErrPoolAlreadyOnJob = errors.New("service: that candidate already has an application for this job")
	// ErrJobNotOpen refuses an add to a draft or closed role.
	ErrJobNotOpen = errors.New("service: that job is not open to applications")
)

// PoolService owns the talent pool: the entries applications earn, the
// recruiter's edits, and the suggestions a job draws from them.
type PoolService struct{ st *store.Store }

func NewPoolService(st *store.Store) *PoolService { return &PoolService{st: st} }

// List is the pool browse screen. An empty query lists everything; otherwise
// the candidate's name, address, and tags are matched.
func (s *PoolService) List(ctx context.Context, p Principal, query string) ([]PoolEntry, error) {
	if err := requireRecruiter(p); err != nil {
		return nil, err
	}
	var out []PoolEntry
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.SearchTalentPoolEntriesWithCandidate(ctx, db.SearchTalentPoolEntriesWithCandidateParams{
			OrgID: p.OrgID, Query: strings.TrimSpace(query), RowLimit: PoolListLimit,
		})
		if err != nil {
			return err
		}
		out = make([]PoolEntry, 0, len(rows))
		for _, r := range rows {
			out = append(out, toPoolEntryFromSearch(r))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list pool: %w", err)
	}
	return out, nil
}

// Entry loads one live pool entry.
func (s *PoolService) Entry(ctx context.Context, p Principal, id uuid.UUID) (PoolEntry, error) {
	if err := requireRecruiter(p); err != nil {
		return PoolEntry{}, err
	}
	var out PoolEntry
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetTalentPoolEntry(ctx, id)
		if err != nil {
			return err
		}
		out = toPoolEntryFromGet(row)
		return nil
	})
	if err != nil {
		return PoolEntry{}, wrapPool("pool entry", err)
	}
	return out, nil
}

// Update replaces the tags an entry is found by and the recruiter's notes.
func (s *PoolService) Update(ctx context.Context, p Principal, id uuid.UUID, in PoolEdit) (PoolEntry, error) {
	if err := requireRecruiter(p); err != nil {
		return PoolEntry{}, err
	}
	var out PoolEntry
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		if _, err := tx.Q.UpdateTalentPoolEntry(ctx, db.UpdateTalentPoolEntryParams{
			ID: id, Skills: cleanTags(in.Skills), Notes: nullable(strings.TrimSpace(in.Notes)),
			Location: nullable(strings.TrimSpace(in.Location)), RemoteOk: in.RemoteOK,
		}); err != nil {
			return err
		}
		row, err := tx.Q.GetTalentPoolEntry(ctx, id)
		if err != nil {
			return err
		}
		out = toPoolEntryFromGet(row)
		return nil
	})
	if err != nil {
		return PoolEntry{}, wrapPool("update pool entry", err)
	}
	return out, nil
}

// Remove takes an entry out of the pool. The row stays for the audit trail;
// nothing reads it again.
func (s *PoolService) Remove(ctx context.Context, p Principal, id uuid.UUID) error {
	if err := requireRecruiter(p); err != nil {
		return err
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		n, err := tx.Q.RemoveTalentPoolEntry(ctx, id)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return wrapPool("remove pool entry", err)
	}
	return nil
}

// Flag is the recruiter marking an application high quality, which files its
// candidate in the pool.
func (s *PoolService) Flag(ctx context.Context, p Principal, applicationID uuid.UUID) (PoolEntry, error) {
	if err := requireRecruiter(p); err != nil {
		return PoolEntry{}, err
	}
	var out PoolEntry
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		if _, err := tx.Q.SetApplicationHighQuality(ctx, db.SetApplicationHighQualityParams{
			ID: applicationID, HighQuality: true,
		}); err != nil {
			return err
		}
		entry, err := s.UpsertFromApplication(ctx, tx, p.OrgID, applicationID, PoolSourceFlag)
		if err != nil {
			return err
		}
		// Flagging is the recruiter asking for this person back, so it is the
		// one source that undoes a removal.
		if err := tx.Q.RestoreTalentPoolEntry(ctx, entry.ID); err != nil {
			return err
		}
		out = entry
		return nil
	})
	if err != nil {
		return PoolEntry{}, wrapPool("flag application", err)
	}
	return out, nil
}

// OnStrongYes is the hook ScorecardService.Save must call, in its own
// transaction, when a scorecard crosses into a strong yes.
func (s *PoolService) OnStrongYes(ctx context.Context, tx *store.Tx, orgID, applicationID uuid.UUID) error {
	_, err := s.UpsertFromApplication(ctx, tx, orgID, applicationID, PoolSourceScorecard)
	return err
}

// OnReviewPass is the hook the assessment review service must call, in its
// own transaction, when a vetter passes an attempt. A score below the org's
// pool threshold earns no entry; the caller supplies the threshold because it
// already holds the org's settings.
func (s *PoolService) OnReviewPass(ctx context.Context, tx *store.Tx, orgID, applicationID uuid.UUID, score float64, threshold int) error {
	if score < float64(threshold) {
		return nil
	}
	_, err := s.UpsertFromApplication(ctx, tx, orgID, applicationID, PoolSourceReview)
	return err
}

// RetractReviewPass is the hook the review service calls, in its own
// transaction, when a vetter amends a pass down to a borderline or a fail.
// It withdraws only an entry that review pass is the latest source of: one
// the recruiter flagged, or whose application they marked high quality, is
// theirs and stays.
func (s *PoolService) RetractReviewPass(ctx context.Context, tx *store.Tx, orgID, applicationID uuid.UUID) error {
	_, err := tx.Q.RetractTalentPoolEntryFromReview(ctx, db.RetractTalentPoolEntryFromReviewParams{
		ApplicationID: applicationID, OrgID: orgID,
	})
	return err
}

// UpsertFromApplication files or refreshes the candidate's single pool entry
// from one application, inside the caller's transaction so the entry commits
// with whatever earned it. The profile comes from the job — its skills,
// seniority, and location are what the candidate was judged against — and is
// merged into what the entry already holds, so a recruiter's own tags and
// notes survive. Assessment scores and the scorecard summary are derived
// from the candidate's record, so repeating the call cannot drift.
//
// Where the person works follows the latest source job rather than
// accumulating: only a fully remote job makes an entry remote-ok, so a
// hybrid role leaves the city to match on. A recruiter's own correction
// stands until the next pool-worthy event on that candidate. An entry a
// recruiter removed stays removed; only Flag brings it back.
func (s *PoolService) UpsertFromApplication(ctx context.Context, tx *store.Tx, orgID, applicationID uuid.UUID, source string) (PoolEntry, error) {
	app, err := tx.Q.GetApplication(ctx, applicationID)
	if err != nil {
		return PoolEntry{}, err
	}
	job, err := tx.Q.GetJob(ctx, app.JobID)
	if err != nil {
		return PoolEntry{}, err
	}
	params := db.UpsertTalentPoolEntryParams{
		OrgID: orgID, CandidateID: app.CandidateID, Source: source,
		Skills: cleanTags(job.Skills), Seniority: job.Seniority, Location: job.Location,
		RemoteOk:     deref(job.RemotePolicy) == RemoteRemote,
		SourceJobIds: []uuid.UUID{job.ID},
	}
	existing, err := tx.Q.GetTalentPoolEntryForCandidate(ctx, db.GetTalentPoolEntryForCandidateParams{
		OrgID: orgID, CandidateID: app.CandidateID,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return PoolEntry{}, err
	default:
		params.Skills = mergeTags(existing.Skills, params.Skills)
		params.SourceJobIds = mergeIDs(existing.SourceJobIds, params.SourceJobIds)
	}
	scores, err := bestScores(ctx, tx, app.CandidateID)
	if err != nil {
		return PoolEntry{}, err
	}
	if params.BestScores, err = json.Marshal(scores); err != nil {
		return PoolEntry{}, err
	}
	if params.ScorecardSummary, err = latestScorecardSummary(ctx, tx, app.CandidateID); err != nil {
		return PoolEntry{}, err
	}
	row, err := tx.Q.UpsertTalentPoolEntry(ctx, params)
	if err != nil {
		return PoolEntry{}, err
	}
	cand, err := tx.Q.GetCandidate(ctx, app.CandidateID)
	if err != nil {
		return PoolEntry{}, err
	}
	entry := toPoolEntry(row)
	entry.CandidateName, entry.CandidateEmail = cand.Name, cand.Email
	return entry, nil
}

// Suggestions ranks the org's pool against one job: the panel a recruiter
// sees when they save it.
func (s *PoolService) Suggestions(ctx context.Context, p Principal, jobID uuid.UUID) ([]PoolSuggestion, error) {
	if err := requireRecruiter(p); err != nil {
		return nil, err
	}
	var out []PoolSuggestion
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		job, err := tx.Q.GetJob(ctx, jobID)
		if err != nil {
			return err
		}
		rows, err := tx.Q.ListTalentPoolEntriesForRanking(ctx, db.ListTalentPoolEntriesForRankingParams{
			OrgID: p.OrgID, RowLimit: PoolRankLimit,
		})
		if err != nil {
			return err
		}
		entries := make([]domain.PoolEntry, 0, len(rows))
		byID := make(map[uuid.UUID]PoolEntry, len(rows))
		for _, r := range rows {
			e := toPoolEntryForRanking(r)
			byID[e.ID] = e
			entries = append(entries, domain.PoolEntry{
				ID: e.ID, CandidateID: e.CandidateID, Skills: e.Skills,
				Seniority: e.Seniority, Location: e.Location, RemoteOK: e.RemoteOK,
				BestAssessmentScore: e.BestAssessmentScore(),
			})
		}
		ex, err := poolExclusions(ctx, tx, job)
		if err != nil {
			return err
		}
		ranked := domain.RankPool(domain.PoolJob{
			Skills: job.Skills, Seniority: deref(job.Seniority),
			Location: deref(job.Location), RemotePolicy: deref(job.RemotePolicy),
		}, entries, ex, time.Now())
		out = make([]PoolSuggestion, 0, len(ranked))
		for _, r := range ranked {
			out = append(out, PoolSuggestion{Entry: byID[r.Entry.ID], Score: r.Score, Breakdown: r.Breakdown})
		}
		return nil
	})
	if err != nil {
		return nil, wrapPool("pool suggestions", err)
	}
	return out, nil
}

// AddToJob is the panel's one-click add: the pool entry's candidate enters
// the job's first stage, with the same applied event a manual add writes.
func (s *PoolService) AddToJob(ctx context.Context, p Principal, jobID, entryID uuid.UUID) (uuid.UUID, error) {
	if err := requireRecruiter(p); err != nil {
		return uuid.Nil, err
	}
	var applicationID uuid.UUID
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		entry, err := tx.Q.GetTalentPoolEntry(ctx, entryID)
		if err != nil {
			return err
		}
		job, err := tx.Q.GetJob(ctx, jobID)
		if err != nil {
			return err
		}
		if job.Status != JobOpen {
			return ErrJobNotOpen
		}
		stage, err := tx.Q.FirstStage(ctx, job.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoStages
		}
		if err != nil {
			return err
		}
		n, err := tx.Q.CountCandidateApplications(ctx, db.CountCandidateApplicationsParams{
			JobID: job.ID, CandidateID: entry.CandidateID,
		})
		if err != nil {
			return err
		}
		if n > 0 {
			return ErrPoolAlreadyOnJob
		}
		app, err := tx.Q.CreateApplication(ctx, db.CreateApplicationParams{
			OrgID: p.OrgID, JobID: job.ID, CandidateID: entry.CandidateID,
			ClientCompanyID: job.ClientCompanyID, StageID: stage.ID, ScreeningAnswers: []byte("{}"),
		})
		if err != nil {
			return err
		}
		if _, err := tx.Q.CreateApplicationEvent(ctx, db.CreateApplicationEventParams{
			OrgID: p.OrgID, ApplicationID: app.ID, ActorKind: actorKind(p),
			ActorID: uuid.NullUUID{UUID: p.UserID, Valid: p.UserID != uuid.Nil},
			Kind:    eventApplied, ToStageID: uuid.NullUUID{UUID: stage.ID, Valid: true},
			Payload: []byte(`{"from_pool":true}`),
		}); err != nil {
			return err
		}
		applicationID = app.ID
		return nil
	})
	if err != nil {
		return uuid.Nil, wrapPool("add to job", err)
	}
	return applicationID, nil
}

// poolExclusions names the candidates this job must not be offered: those
// already on it, and those its client company has rejected. How recent a
// rejection has to be to count is the ranker's rule.
func poolExclusions(ctx context.Context, tx *store.Tx, job db.Job) (domain.PoolExclusions, error) {
	ex := domain.PoolExclusions{InJob: map[uuid.UUID]bool{}, RejectedAt: map[uuid.UUID]time.Time{}}
	inJob, err := tx.Q.ListJobCandidateIDs(ctx, job.ID)
	if err != nil {
		return ex, err
	}
	for _, id := range inJob {
		ex.InJob[id] = true
	}
	rejected, err := tx.Q.ListClientRejections(ctx, job.ClientCompanyID)
	if err != nil {
		return ex, err
	}
	for _, r := range rejected {
		ex.RejectedAt[r.CandidateID] = r.RejectedAt.Time
	}
	return ex, nil
}

// bestScores is the candidate's best assessment score per problem tag.
func bestScores(ctx context.Context, tx *store.Tx, candidateID uuid.UUID) (map[string]float64, error) {
	rows, err := tx.Q.BestAssessmentScoresForCandidate(ctx, candidateID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(rows))
	for _, r := range rows {
		v, err := r.BestScore.Float64Value()
		if err != nil || !v.Valid {
			continue
		}
		out[r.Tag] = v.Float64
	}
	return out, nil
}

// latestScorecardSummary is the verdict and notes of the last card filed on
// the candidate, or nil when nobody has interviewed them.
func latestScorecardSummary(ctx context.Context, tx *store.Tx, candidateID uuid.UUID) (*string, error) {
	row, err := tx.Q.LatestScorecardForCandidate(ctx, candidateID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	summary := row.Overall
	if notes := strings.TrimSpace(deref(row.Notes)); notes != "" {
		summary += ": " + notes
	}
	return &summary, nil
}

func mergeTags(existing, incoming []string) []string {
	return cleanTags(append(append([]string{}, existing...), incoming...))
}

func mergeIDs(existing, incoming []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(existing)+len(incoming))
	out := make([]uuid.UUID, 0, len(existing)+len(incoming))
	for _, id := range append(append([]uuid.UUID{}, existing...), incoming...) {
		if id == uuid.Nil || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

func decodeBestScores(raw []byte) map[string]float64 {
	out := map[string]float64{}
	if len(raw) == 0 {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

func toPoolEntry(row db.TalentPoolEntry) PoolEntry {
	return PoolEntry{
		ID: row.ID, CandidateID: row.CandidateID, Skills: row.Skills,
		Seniority: deref(row.Seniority), Location: deref(row.Location), RemoteOK: row.RemoteOk,
		BestScores: decodeBestScores(row.BestScores), ScorecardSummary: deref(row.ScorecardSummary),
		Notes: deref(row.Notes), SourceJobIDs: row.SourceJobIds, Source: row.Source,
		UpdatedAt: row.UpdatedAt.Time,
	}
}

func toPoolEntryFromGet(row db.GetTalentPoolEntryRow) PoolEntry {
	out := toPoolEntry(db.TalentPoolEntry{
		ID: row.ID, OrgID: row.OrgID, CandidateID: row.CandidateID, Skills: row.Skills,
		Seniority: row.Seniority, Location: row.Location, RemoteOk: row.RemoteOk,
		BestScores: row.BestScores, ScorecardSummary: row.ScorecardSummary, Notes: row.Notes,
		SourceJobIds: row.SourceJobIds, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		RemovedAt: row.RemovedAt, Source: row.Source,
	})
	out.CandidateName, out.CandidateEmail = row.CandidateName, row.CandidateEmail
	return out
}

func toPoolEntryFromSearch(row db.SearchTalentPoolEntriesWithCandidateRow) PoolEntry {
	return toPoolEntryFromGet(db.GetTalentPoolEntryRow(row))
}

// toPoolEntryForRanking fills what the ranker and its panel read; the fields
// the ranking query leaves out are not shown alongside a suggestion.
func toPoolEntryForRanking(row db.ListTalentPoolEntriesForRankingRow) PoolEntry {
	return PoolEntry{
		ID: row.ID, CandidateID: row.CandidateID,
		CandidateName: row.CandidateName, CandidateEmail: row.CandidateEmail,
		Skills: row.Skills, Seniority: deref(row.Seniority), Location: deref(row.Location),
		RemoteOK: row.RemoteOk, BestScores: decodeBestScores(row.BestScores),
	}
}

func wrapPool(what string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrForbidden),
		errors.Is(err, ErrPoolAlreadyOnJob), errors.Is(err, ErrJobNotOpen), errors.Is(err, ErrNoStages):
		return err
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation &&
		strings.HasPrefix(pgErr.ConstraintName, "application_job_id_candidate_id") {
		return ErrPoolAlreadyOnJob
	}
	return fmt.Errorf("%s: %w", what, err)
}
