package service

import (
	"context"
	"errors"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"recruiting/internal/domain"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// FitTerm is one part of a fit score as the candidate screen reads it: what
// was compared, what it is worth, and what it came out at.
type FitTerm struct {
	Key    string
	Label  string
	Note   string
	Weight float64
	// Value is the part before its weight, 0–1.
	Value float64
}

// ApplicationFit is how well one candidate matches the job they are on: the
// talent-pool ranker's own arithmetic, run for a single pair so the number a
// recruiter reads on the candidate is the number the suggestions panel would
// have given.
type ApplicationFit struct {
	Score     float64
	Breakdown domain.MatchBreakdown
	Terms     []FitTerm
	// AssessmentScore is the candidate's best assessment score, 0–100; zero
	// when they have never sat one.
	AssessmentScore float64
	// Profiled is whether a talent-pool entry backed the comparison. Without
	// one only the assessment part can say anything.
	Profiled bool
}

// Fit scores one application's candidate against its job.
func (s *PoolService) Fit(ctx context.Context, p Principal, applicationID uuid.UUID) (ApplicationFit, error) {
	if err := requireRecruiter(p); err != nil {
		return ApplicationFit{}, err
	}
	var out ApplicationFit
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		app, err := tx.Q.GetApplication(ctx, applicationID)
		if err != nil {
			return err
		}
		job, err := tx.Q.GetJob(ctx, app.JobID)
		if err != nil {
			return err
		}
		entry := domain.PoolEntry{CandidateID: app.CandidateID}
		row, err := tx.Q.GetTalentPoolEntryForCandidate(ctx, db.GetTalentPoolEntryForCandidateParams{OrgID: p.OrgID, CandidateID: app.CandidateID})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		default:
			e := toPoolEntry(row)
			entry.ID, entry.Skills, entry.Seniority = e.ID, e.Skills, e.Seniority
			entry.Location, entry.RemoteOK = e.Location, e.RemoteOK
			out.Profiled = true
		}
		// The scores come from the candidate's record either way, so an
		// entry that has not been refreshed cannot hold the fit back.
		scores, err := bestScores(ctx, tx, app.CandidateID)
		if err != nil {
			return err
		}
		out.AssessmentScore = PoolEntry{BestScores: scores}.BestAssessmentScore()
		entry.BestAssessmentScore = out.AssessmentScore
		out.Score, out.Breakdown = domain.ScorePoolMatch(domain.PoolJob{
			Skills: job.Skills, Seniority: deref(job.Seniority),
			Location: deref(job.Location), RemotePolicy: deref(job.RemotePolicy),
		}, entry)
		out.Terms = fitTerms(out.Breakdown, out.AssessmentScore)
		return nil
	})
	if err != nil {
		return ApplicationFit{}, wrapPool("application fit", err)
	}
	return out, nil
}

// fitTerms names the ranker's four parts in the recruiter's words, strongest
// weight first, so the panel reads in the order the score was built.
func fitTerms(b domain.MatchBreakdown, assessment float64) []FitTerm {
	w := domain.PoolWeights
	return []FitTerm{
		{Key: "skills", Label: "Skills overlap", Note: "tags shared with the role", Weight: w.Skills, Value: b.Skills},
		{Key: "seniority", Label: "Seniority", Note: "rung against the role's", Weight: w.Seniority, Value: b.Seniority},
		{Key: "location", Label: "Location", Note: "where the work can be done from", Weight: w.Location, Value: b.Location},
		{Key: "assessment", Label: "Assessment", Note: assessmentNote(assessment), Weight: w.Assessment, Value: b.Assessment},
	}
}

func assessmentNote(score float64) string {
	if score <= 0 {
		return "no assessment sat yet"
	}
	return "best score " + strconv.FormatFloat(score, 'f', -1, 64)
}
