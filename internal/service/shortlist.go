package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"recruiting/internal/domain"
	"recruiting/internal/mail"
	"recruiting/internal/queue"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// Packet statuses. A draft is the recruiter's working copy; a sent packet is
// what the client has already read and is never edited again.
const (
	ShortlistDraft = "draft"
	ShortlistSent  = "sent"
)

// MaxShortlistPicks is how many candidates a shortlist carries. A client
// reads a recommendation, not a longlist.
const MaxShortlistPicks = 5

// DefaultShortlistMinScore is the assessment score a candidate must reach
// before the builder offers them.
const DefaultShortlistMinScore = 70

// shortlistPath is where a sent packet lands on the client surface.
const shortlistPath = "/shortlist"

var (
	ErrPacketSent    = errors.New("service: this shortlist has already been sent")
	ErrTooManyPicks  = fmt.Errorf("service: a shortlist carries at most %d candidates", MaxShortlistPicks)
	ErrDuplicatePick = errors.New("service: that candidate is already on this shortlist")
	ErrNoPicks       = errors.New("service: a shortlist needs at least one candidate")
	ErrPickRejected  = errors.New("service: a rejected candidate cannot be sent to the client")
)

// ShortlistPick is one candidate of a packet at their rank.
type ShortlistPick struct {
	ApplicationID uuid.UUID
	Rank          int
	CandidateName string
	Score         *float64
}

// ShortlistPacket is the recruiter's ranked recommendation for one job.
type ShortlistPacket struct {
	ID, OrgID, JobID uuid.UUID
	Status           string
	Note             string
	Picks            []ShortlistPick
	SentAt           *time.Time
	SentBy           uuid.UUID
}

// Sent reports whether the client has already received the packet.
func (p ShortlistPacket) Sent() bool { return p.Status == ShortlistSent }

// ShortlistInput is a saved draft: the note and the picks, whose order is
// their rank.
type ShortlistInput struct {
	JobID          uuid.UUID
	Note           string
	ApplicationIDs []uuid.UUID
}

// ShortlistCandidate is one row of the builder's qualified pool: an
// application on the job whose sitting was scored, with the talent-pool
// ranker's fit beside the score.
type ShortlistCandidate struct {
	ApplicationID uuid.UUID
	CandidateName string
	StageName     string
	Score         float64
	Fit           float64
}

// ClientShortlistPick is one pick as the client reads it. What the recruiter
// weighed — fit, integrity, the other applicants — is not part of it.
type ClientShortlistPick struct {
	ApplicationID uuid.UUID
	Rank          int
	Blind         bool
	Label         string
	Score         string
}

// ClientShortlist is the sent packet on the client's own page: the ranking
// and the recruiter's note, and nothing about how either was arrived at.
type ClientShortlist struct {
	PacketID uuid.UUID
	JobID    uuid.UUID
	JobTitle string
	Note     string
	SentAt   time.Time
	Picks    []ClientShortlistPick
}

// ClientTest is one case of a scored submit as the client sees it go by. The
// inputs and expected outputs stay server-side, as they do for the reviewer.
type ClientTest struct {
	Position   int
	Name       string
	Visibility string
	Status     string
}

// ClientCode is one problem of the sitting: the code it was scored on and
// how each case went.
type ClientCode struct {
	Title       string
	Language    string
	FinalSource string
	Tests       []ClientTest
}

// ClientShortlistDetail is one pick's page: who they are, their résumé, the
// code they wrote, how it tested, and the recording to replay. It carries no
// integrity signal, no webcam frame, no interviewer guideline and no
// internal note, because none of those is a field of it.
type ClientShortlistDetail struct {
	Packet    ClientShortlist
	Pick      ClientShortlistPick
	AttemptID uuid.UUID
	HasResume bool
	Problems  []ClientCode
}

// clientReplayKinds is every event kind a client's replay may carry: the
// code being written, run and submitted, and the language it was written
// in. Focus, paste, fullscreen, snapshot and consent events are the org's
// integrity record and are dropped before the manifest is built.
var clientReplayKinds = map[string]bool{
	"edit": true, "run": true, "submit": true, "lang_change": true,
}

// ShortlistService assembles and sends shortlist packets, and serves the
// sent ones to the client company they were sent to. Sending is one
// transaction: every pick is released, the packet is frozen, and the notice
// is queued together, so a client is never told about a shortlist whose
// candidates they cannot open.
type ShortlistService struct {
	st       *store.Store
	releases *ReleaseService
	reviews  *ReviewService
	pool     *PoolService
	q        Enqueuer
	baseURL  string
}

// NewShortlistService wires the store, the release switch the picks go
// through, the review service the client's replay is read with, the pool
// ranker the builder orders by, and the queue the notice goes to. A nil
// queue drops the notice, which suits tests of the visibility alone.
func NewShortlistService(st *store.Store, releases *ReleaseService, reviews *ReviewService, pool *PoolService, q Enqueuer, baseURL string) *ShortlistService {
	return &ShortlistService{st: st, releases: releases, reviews: reviews, pool: pool, q: q, baseURL: strings.TrimRight(baseURL, "/")}
}

// Save writes a draft packet. A nil id starts a new one; an id amends the
// draft it names. The picks replace whatever the draft held, in the order
// given: that order is the ranking the client reads.
func (s *ShortlistService) Save(ctx context.Context, p Principal, id *uuid.UUID, in ShortlistInput) (ShortlistPacket, error) {
	if err := requireRecruiter(p); err != nil {
		return ShortlistPacket{}, err
	}
	if len(in.ApplicationIDs) > MaxShortlistPicks {
		return ShortlistPacket{}, ErrTooManyPicks
	}
	seen := make(map[uuid.UUID]bool, len(in.ApplicationIDs))
	for _, appID := range in.ApplicationIDs {
		if seen[appID] {
			return ShortlistPacket{}, ErrDuplicatePick
		}
		seen[appID] = true
	}
	var out ShortlistPacket
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		var packet db.ShortlistPacket
		var err error
		if id == nil {
			packet, err = tx.Q.CreateShortlistPacket(ctx, db.CreateShortlistPacketParams{
				OrgID: p.OrgID, JobID: in.JobID, Note: strings.TrimSpace(in.Note), CreatedBy: p.UserID,
			})
		} else {
			packet, err = tx.Q.GetShortlistPacketForUpdate(ctx, *id)
			if err == nil && packet.Status == ShortlistSent {
				return ErrPacketSent
			}
			if err == nil {
				packet, err = tx.Q.UpdateShortlistPacketNote(ctx, db.UpdateShortlistPacketNoteParams{ID: *id, Note: strings.TrimSpace(in.Note)})
			}
		}
		if err != nil {
			return err
		}
		if err := tx.Q.DeleteShortlistPicks(ctx, packet.ID); err != nil {
			return err
		}
		for i, appID := range in.ApplicationIDs {
			app, err := tx.Q.GetApplication(ctx, appID)
			if err != nil {
				return err
			}
			if app.JobID != packet.JobID {
				// A pick from another job would rank a stranger.
				return ErrNotFound
			}
			if err := tx.Q.CreateShortlistPick(ctx, db.CreateShortlistPickParams{
				OrgID: p.OrgID, PacketID: packet.ID, ApplicationID: appID, Rank: int32(i + 1),
			}); err != nil {
				return err
			}
		}
		out, err = packetWithPicks(ctx, tx, packet)
		return err
	})
	if err != nil {
		return ShortlistPacket{}, wrapShortlist("save shortlist", err)
	}
	return out, nil
}

// Send releases every pick and tells the client, in one transaction: a
// notice that cannot be queued leaves the packet a draft and its candidates
// hidden. A rejected pick is refused by name before anything is released,
// since sending one would introduce the client to a closed application.
func (s *ShortlistService) Send(ctx context.Context, p Principal, id uuid.UUID) (ShortlistPacket, error) {
	if err := requireRecruiter(p); err != nil {
		return ShortlistPacket{}, err
	}
	var out ShortlistPacket
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		packet, err := tx.Q.GetShortlistPacketForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if packet.Status == ShortlistSent {
			return ErrPacketSent
		}
		picks, err := tx.Q.ListShortlistPicks(ctx, packet.ID)
		if err != nil {
			return err
		}
		if len(picks) == 0 {
			return ErrNoPicks
		}
		var rejected []string
		for _, pick := range picks {
			if pick.ApplicationStatus != string(domain.StatusActive) {
				rejected = append(rejected, pick.CandidateName)
			}
		}
		if len(rejected) > 0 {
			return fmt.Errorf("%w: %s", ErrPickRejected, strings.Join(rejected, ", "))
		}
		for _, pick := range picks {
			if _, _, _, err := s.releases.apply(ctx, tx, p, pick.ApplicationID, true); err != nil {
				return err
			}
		}
		packet, err = tx.Q.MarkShortlistPacketSent(ctx, db.MarkShortlistPacketSentParams{ID: id, SentBy: p.UserID})
		if err != nil {
			return err
		}
		if out, err = packetWithPicks(ctx, tx, packet); err != nil {
			return err
		}
		if s.q == nil {
			return nil
		}
		job, err := tx.Q.GetJob(ctx, packet.JobID)
		if err != nil {
			return err
		}
		return s.notify(ctx, tx, p.OrgID, job, len(picks))
	})
	if err != nil {
		return ShortlistPacket{}, wrapShortlist("send shortlist", err)
	}
	return out, nil
}

// notify queues one notice per client user of the company, inside the send's
// own transaction.
func (s *ShortlistService) notify(ctx context.Context, tx *store.Tx, orgID uuid.UUID, job db.Job, picks int) error {
	users, err := tx.Q.ListClientUsersByCompany(ctx, job.ClientCompanyID)
	if err != nil {
		return err
	}
	for _, u := range users {
		_, err := s.q.Enqueue(ctx, tx, queue.KindEmailSend, queue.EmailPayload{
			Template: mail.TemplateClientShortlist, To: u.Email, OrgID: orgID,
			Data: map[string]any{
				"ContactName":    u.Name,
				"JobTitle":       job.Title,
				"CandidateCount": picks,
				"PortalURL":      s.baseURL + clientJobPath + job.ID.String() + shortlistPath,
			},
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Get is one packet with its picks, for the recruiter who owns it.
func (s *ShortlistService) Get(ctx context.Context, p Principal, id uuid.UUID) (ShortlistPacket, error) {
	if err := requireRecruiter(p); err != nil {
		return ShortlistPacket{}, err
	}
	var out ShortlistPacket
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		packet, err := tx.Q.GetShortlistPacket(ctx, id)
		if err != nil {
			return err
		}
		out, err = packetWithPicks(ctx, tx, packet)
		return err
	})
	if err != nil {
		return ShortlistPacket{}, wrapShortlist("shortlist", err)
	}
	return out, nil
}

// ListByJob is every packet built for a job, newest first.
func (s *ShortlistService) ListByJob(ctx context.Context, p Principal, jobID uuid.UUID) ([]ShortlistPacket, error) {
	if err := requireRecruiter(p); err != nil {
		return nil, err
	}
	var out []ShortlistPacket
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.ListShortlistPacketsByJob(ctx, jobID)
		if err != nil {
			return err
		}
		out = make([]ShortlistPacket, 0, len(rows))
		for _, row := range rows {
			packet, err := packetWithPicks(ctx, tx, row)
			if err != nil {
				return err
			}
			out = append(out, packet)
		}
		return nil
	})
	if err != nil {
		return nil, wrapShortlist("shortlists of job", err)
	}
	return out, nil
}

// ListByClient is every packet a client company has been sent. It reads as
// the client, so RLS refuses another company's packets and every draft.
func (s *ShortlistService) ListByClient(ctx context.Context, p Principal, companyID uuid.UUID) ([]ShortlistPacket, error) {
	if err := requireClient(p); err != nil {
		return nil, err
	}
	var rows []db.ShortlistPacket
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		var err error
		rows, err = tx.Q.ListSentShortlistPacketsByCompany(ctx, companyID)
		return err
	})
	if err != nil {
		return nil, wrapShortlist("client shortlists", err)
	}
	// The picks carry the assessment score, which no client policy admits,
	// so they are read back as the org once the packets are known to be the
	// client's own.
	out := make([]ShortlistPacket, 0, len(rows))
	err = s.st.WithTx(ctx, orgScoped(p.OrgID), func(ctx context.Context, tx *store.Tx) error {
		for _, row := range rows {
			packet, err := packetWithPicks(ctx, tx, row)
			if err != nil {
				return err
			}
			out = append(out, packet)
		}
		return nil
	})
	if err != nil {
		return nil, wrapShortlist("client shortlists", err)
	}
	return out, nil
}

// Pool is what the builder offers: the applications on the job with a scored
// sitting at or above minScore, ranked by the talent-pool ranker's fit and
// then by that score. A minScore of zero or less takes the default.
func (s *ShortlistService) Pool(ctx context.Context, p Principal, jobID uuid.UUID, minScore float64) ([]ShortlistCandidate, error) {
	if err := requireRecruiter(p); err != nil {
		return nil, err
	}
	if minScore <= 0 {
		minScore = DefaultShortlistMinScore
	}
	var out []ShortlistCandidate
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.ListShortlistPool(ctx, jobID)
		if err != nil {
			return err
		}
		out = make([]ShortlistCandidate, 0, len(rows))
		for _, row := range rows {
			score := numericPtr(row.Score)
			if score == nil || *score < minScore {
				continue
			}
			out = append(out, ShortlistCandidate{
				ApplicationID: row.ApplicationID, CandidateName: row.CandidateName,
				StageName: row.StageName, Score: *score,
			})
		}
		return nil
	})
	if err != nil {
		return nil, wrapShortlist("shortlist pool", err)
	}
	if s.pool != nil {
		for i := range out {
			// A candidate the ranker cannot place still belongs in the pool;
			// they simply sort on their score alone.
			if fit, err := s.pool.Fit(ctx, p, out[i].ApplicationID); err == nil {
				out[i].Fit = fit.Score
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Fit != out[j].Fit {
			return out[i].Fit > out[j].Fit
		}
		return out[i].Score > out[j].Score
	})
	return out, nil
}

// ClientShortlist is the newest packet sent to the client for a job. The
// packet is found as the client, so another company's job is not found at
// all; what hangs off it is then read as the org, trimmed to the fields a
// client may see.
func (s *ShortlistService) ClientShortlist(ctx context.Context, p Principal, jobID uuid.UUID) (ClientShortlist, error) {
	packet, job, err := s.clientPacket(ctx, p, jobID)
	if err != nil {
		return ClientShortlist{}, err
	}
	var out ClientShortlist
	err = s.st.WithTx(ctx, orgScoped(p.OrgID), func(ctx context.Context, tx *store.Tx) error {
		out, err = clientShortlist(ctx, tx, packet, job)
		return err
	})
	if err != nil {
		return ClientShortlist{}, wrapShortlist("client shortlist", err)
	}
	return out, nil
}

// ClientPick is one pick's page: the code, the cases, and the recording's
// attempt. A candidate not on the packet is not found, so the client cannot
// walk from a packet they were sent to an applicant they were not.
func (s *ShortlistService) ClientPick(ctx context.Context, p Principal, jobID, applicationID uuid.UUID) (ClientShortlistDetail, error) {
	packet, job, err := s.clientPacket(ctx, p, jobID)
	if err != nil {
		return ClientShortlistDetail{}, err
	}
	var out ClientShortlistDetail
	err = s.st.WithTx(ctx, orgScoped(p.OrgID), func(ctx context.Context, tx *store.Tx) error {
		list, err := clientShortlist(ctx, tx, packet, job)
		if err != nil {
			return err
		}
		out.Packet = list
		found := false
		for _, pick := range list.Picks {
			if pick.ApplicationID == applicationID {
				out.Pick, found = pick, true
			}
		}
		if !found {
			return ErrNotFound
		}
		app, err := tx.Q.GetApplication(ctx, applicationID)
		if err != nil {
			return err
		}
		resumes, err := tx.Q.ListResumesByCandidate(ctx, app.CandidateID)
		if err != nil {
			return err
		}
		out.HasResume = len(resumes) > 0 && !out.Pick.Blind
		att, err := tx.Q.GetScoredAttemptForApplication(ctx, applicationID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil
		case err != nil:
			return err
		}
		out.AttemptID = att.ID
		out.Problems, err = clientCode(ctx, tx, att)
		return err
	})
	if err != nil {
		return ClientShortlistDetail{}, wrapShortlist("client shortlist pick", err)
	}
	return out, nil
}

// ClientReplay is one page of a pick's recording with every event kind the
// client may not see already gone, so neither the stream nor the markers
// derived from it can carry an integrity signal to the page.
func (s *ShortlistService) ClientReplay(ctx context.Context, p Principal, jobID, applicationID uuid.UUID, q ReplayQuery) (Replay, error) {
	packet, _, err := s.clientPacket(ctx, p, jobID)
	if err != nil {
		return Replay{}, err
	}
	var out Replay
	err = s.st.WithTx(ctx, orgScoped(p.OrgID), func(ctx context.Context, tx *store.Tx) error {
		if _, err := tx.Q.GetShortlistPick(ctx, db.GetShortlistPickParams{PacketID: packet.ID, ApplicationID: applicationID}); err != nil {
			return err
		}
		att, err := tx.Q.GetScoredAttemptForApplication(ctx, applicationID)
		if err != nil {
			return err
		}
		out, err = s.reviews.replay(ctx, tx, att, q, clientReplayKinds)
		return err
	})
	if err != nil {
		return Replay{}, wrapShortlist("client shortlist replay", err)
	}
	return out, nil
}

// clientPacket finds the newest sent packet for the job as the signed-in
// client. RLS is what limits it to their own company's sent packets, so a
// rival's job answers as if it did not exist.
func (s *ShortlistService) clientPacket(ctx context.Context, p Principal, jobID uuid.UUID) (db.ShortlistPacket, db.Job, error) {
	if err := requireClient(p); err != nil {
		return db.ShortlistPacket{}, db.Job{}, err
	}
	var packet db.ShortlistPacket
	var job db.Job
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		var err error
		if job, err = tx.Q.GetJob(ctx, jobID); err != nil {
			return err
		}
		packet, err = tx.Q.GetLatestSentShortlistPacketForJob(ctx, jobID)
		return err
	})
	if err != nil {
		return db.ShortlistPacket{}, db.Job{}, wrapShortlist("client shortlist", err)
	}
	return packet, job, nil
}

// clientShortlist trims a packet to what a client reads. A blind job hides
// the name behind the same label the rest of the portal uses.
func clientShortlist(ctx context.Context, tx *store.Tx, packet db.ShortlistPacket, job db.Job) (ClientShortlist, error) {
	out := ClientShortlist{
		PacketID: packet.ID, JobID: packet.JobID, JobTitle: job.Title,
		Note: packet.Note, SentAt: packet.SentAt.Time.UTC(),
	}
	picks, err := tx.Q.ListShortlistPicks(ctx, packet.ID)
	if err != nil {
		return ClientShortlist{}, err
	}
	out.Picks = make([]ClientShortlistPick, 0, len(picks))
	for _, pick := range picks {
		app, err := tx.Q.GetApplication(ctx, pick.ApplicationID)
		if err != nil {
			return ClientShortlist{}, err
		}
		stage, err := tx.Q.GetStage(ctx, app.StageID)
		if err != nil {
			return ClientShortlist{}, err
		}
		view := ClientShortlistPick{ApplicationID: pick.ApplicationID, Rank: int(pick.Rank), Label: pick.CandidateName}
		if blind(job.BlindMode, stage.Unblind) {
			view.Blind, view.Label = true, "Candidate "+pick.ApplicationID.String()[:8]
		}
		if score := numericPtr(pick.Score); score != nil {
			view.Score = strconv.FormatFloat(*score, 'f', -1, 64)
		}
		out.Picks = append(out.Picks, view)
	}
	return out, nil
}

// clientCode is the code a sitting was scored on, per problem, with how each
// case went. Nothing here reads the reviewer's verdict or the integrity
// signals: the client is shown the work, not the judgement of it.
func clientCode(ctx context.Context, tx *store.Tx, att db.Attempt) ([]ClientCode, error) {
	a, err := loadAssessment(ctx, tx, att.AssessmentID)
	if err != nil {
		return nil, err
	}
	subs, err := tx.Q.ListSubmissions(ctx, att.ID)
	if err != nil {
		return nil, err
	}
	sources, err := tx.Q.ListAttemptSources(ctx, att.ID)
	if err != nil {
		return nil, err
	}
	out := make([]ClientCode, 0, len(a.Problems))
	for _, problem := range a.Problems {
		language, final := finalSource(problem.ID, subs, sources)
		code := ClientCode{Title: problem.Title, Language: language, FinalSource: final}
		for _, test := range reviewTests(scoredSubmit(problem.ID, subs), problem.TestCases) {
			code.Tests = append(code.Tests, ClientTest{
				Position: test.Position, Name: test.Name, Visibility: test.Visibility, Status: test.Status,
			})
		}
		out = append(out, code)
	}
	return out, nil
}

// packetWithPicks loads a packet's picks in rank order.
func packetWithPicks(ctx context.Context, tx *store.Tx, row db.ShortlistPacket) (ShortlistPacket, error) {
	out := ShortlistPacket{
		ID: row.ID, OrgID: row.OrgID, JobID: row.JobID, Status: row.Status, Note: row.Note,
	}
	if row.SentAt.Valid {
		at := row.SentAt.Time.UTC()
		out.SentAt = &at
	}
	if row.SentBy.Valid {
		out.SentBy = row.SentBy.UUID
	}
	picks, err := tx.Q.ListShortlistPicks(ctx, row.ID)
	if err != nil {
		return ShortlistPacket{}, err
	}
	out.Picks = make([]ShortlistPick, 0, len(picks))
	for _, pick := range picks {
		out.Picks = append(out.Picks, ShortlistPick{
			ApplicationID: pick.ApplicationID, Rank: int(pick.Rank),
			CandidateName: pick.CandidateName, Score: numericPtr(pick.Score),
		})
	}
	return out, nil
}

// wrapShortlist keeps the refusals a screen shows inline unwrapped.
func wrapShortlist(what string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrForbidden), errors.Is(err, ErrPacketSent),
		errors.Is(err, ErrTooManyPicks), errors.Is(err, ErrDuplicatePick), errors.Is(err, ErrNoPicks),
		errors.Is(err, ErrPickRejected):
		return err
	}
	return fmt.Errorf("%s: %w", what, err)
}
