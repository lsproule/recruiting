package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"recruiting/internal/domain"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// QueueKind names one step of the hiring process that is waiting on a
// person. The queue bubbles up, for every client at once, exactly what has
// to happen next for each candidate: review the résumé, get the call booked,
// file the feedback, send or review the exam, forward the candidate, plan the
// shortlist, interview, decide. A step that can be done from the queue
// carries its decision inline; the rest carry the one link that resolves
// them.
type QueueKind string

const (
	// QueueResume: someone applied and nobody has looked at the résumé.
	QueueResume QueueKind = "resume"
	// QueueCallUnbooked: the candidate was sent a booking link and has not
	// picked a time.
	QueueCallUnbooked QueueKind = "call_unbooked"
	// QueueFeedback: an interview ended and its scorecard is missing.
	QueueFeedback QueueKind = "feedback"
	// QueueExamUnopened: the exam invite is out and unopened.
	QueueExamUnopened QueueKind = "exam_unopened"
	// QueueExamReview: a sitting was scored and nobody has reviewed it.
	QueueExamReview QueueKind = "exam_review"
	// QueueDecision: the stage has what it needs (feedback, a verdict, a
	// rating) and the application is waiting to be advanced or rejected.
	QueueDecision QueueKind = "decision"
	// QueueForward: the candidate reached a client stage and the client
	// cannot see them yet.
	QueueForward QueueKind = "forward"
	// QueueClientWaiting: the client asked something and got no answer.
	QueueClientWaiting QueueKind = "client_waiting"
	// QueueClientSilent: the client has had the candidate for days and said
	// nothing.
	QueueClientSilent QueueKind = "client_silent"
	// QueueShortlistPlan: the candidate is in a sprint stage with no sprint
	// planned: a round-robin, or targeted interviews instead.
	QueueShortlistPlan QueueKind = "shortlist_plan"
	// QueueSprintRating: a sprint conversation ended and its interviewer
	// never rated the candidate.
	QueueSprintRating QueueKind = "sprint_rating"
	// QueueShortlistDraft: a packet was built and never sent.
	QueueShortlistDraft QueueKind = "shortlist_draft"
	// QueueTalentIntro is a company that asked to meet a match and a
	// recruiter who has not yet sent the person the opportunity.
	QueueTalentIntro QueueKind = "talent_intro"
)

// QueueKinds is every rule in the order of the hiring process, which is how
// the screen's filter lists them.
var QueueKinds = []QueueKind{
	QueueResume, QueueCallUnbooked, QueueFeedback, QueueExamUnopened, QueueExamReview,
	QueueDecision, QueueForward, QueueClientWaiting, QueueClientSilent,
	QueueShortlistPlan, QueueSprintRating, QueueShortlistDraft, QueueTalentIntro,
}

// Step is the rule's place in the hiring process, one to nine, as the
// recruiter counts the steps: résumé, call, feedback, exam sent, exam done,
// shortlist, interview, further rounds, decision. Work that belongs to no
// candidate's step (a draft packet, an introduction) is nine as well: it is
// a decision somebody owes.
func (k QueueKind) Step() int {
	switch k {
	case QueueResume:
		return 1
	case QueueCallUnbooked:
		return 2
	case QueueFeedback:
		return 3
	case QueueExamUnopened:
		return 4
	case QueueExamReview, QueueForward, QueueClientWaiting, QueueClientSilent:
		return 5
	case QueueShortlistPlan, QueueSprintRating, QueueShortlistDraft:
		return 6
	case QueueDecision:
		return 7
	}
	return 9
}

// How long each step may wait before the queue counts it late. None of
// these move anything; they only order the screen and colour the row.
const (
	ResumeSLA         = 48 * time.Hour
	CallBookingSLA    = 72 * time.Hour
	ExamReviewSLA     = 24 * time.Hour
	DecisionSLA       = 48 * time.Hour
	ForwardSLA        = 24 * time.Hour
	ClientSilentAfter = 72 * time.Hour
)

// SnoozeWindow is how long "not now" lasts.
const SnoozeWindow = 24 * time.Hour

// NavCountsTTL is how long the sidebar badges are served from memory before
// the queue is scanned again for them. Every screen of the app draws the
// badges, so without it every page view would run the queue's full scan;
// with it the scan runs at most once per user per TTL, and sooner when a
// decision or a snooze taken through this service changes the queue.
const NavCountsTTL = 20 * time.Second

// Where a queue row's primary action lands. The service names the paths
// because the item carries the link; the web packages own the screens.
const (
	queueReviewPath    = "/app/reviews/"
	queueScorecardPath = "/scorecard/"
	queueShortlistPath = "/shortlist"
	queueJobPath       = "/app/jobs/"
	queueSprintPath    = "/app/sprints/"
)

// The decisions a row can carry. Advance and reject are moves; release
// makes the candidate visible to the client.
const (
	QueueActionAdvance = "advance"
	QueueActionReject  = "reject"
	QueueActionRelease = "release"
)

// QueueAction is one decision that can be taken from the row itself.
type QueueAction struct {
	Kind  string
	Label string
	// ToStageID is where an advance or a reject sends the application.
	ToStageID uuid.UUID
	// NeedsReason marks a decision the rules refuse without one (a reject).
	NeedsReason bool
}

// QueueItem is one pending action: who it is about, what it is, where it
// belongs, and how it is resolved — a link, or a decision on the row.
type QueueItem struct {
	Kind                              QueueKind
	Who, Detail, JobTitle, ClientName string
	ClientID                          uuid.UUID
	// Due is when the item stops being merely pending and becomes late: the
	// step's allowance, the invite's expiry, the interview's end.
	Due                    *time.Time
	ActionLabel, ActionURL string
	// SubjectID is the row the rule fired on — an application, attempt,
	// packet, pairing, or introduction — and the key a snooze is filed under.
	SubjectID uuid.UUID
	// ApplicationID is the application the row concerns, where it concerns
	// one; a draft packet or an introduction has none.
	ApplicationID uuid.UUID
	// Actions are the decisions the row offers inline.
	Actions []QueueAction
}

// DecideRequest is one decision taken from the queue.
type DecideRequest struct {
	ApplicationID uuid.UUID
	Action        string
	ToStageID     uuid.UUID
	Reason        string
}

// ErrBadDecision is a decision the queue does not offer.
var ErrBadDecision = errors.New("service: that is not a decision the queue offers")

// Releaser makes an application visible to its client; the release service
// is the one the pipeline screens use, notice to the client included.
type Releaser interface {
	Release(ctx context.Context, p Principal, id uuid.UUID) (Application, error)
}

// WorkQueueService reads the queue and takes the decisions it offers. It
// owns no table of its own: every row is derived, so an item disappears the
// moment the work behind it is done.
type WorkQueueService struct {
	st *store.Store
	// Apps and Releases carry out the inline decisions; nil disables them
	// and the rows show links only.
	Apps     *ApplicationService
	Releases Releaser
	// Now is the clock the deadlines are measured against; tests replace it.
	Now func() time.Time

	// nav caches the sidebar badges, one entry per user, for NavCountsTTL.
	// A user's badges are their own because snoozes are: the same org's
	// queue reads differently to each recruiter. The entries hold the five
	// numbers and nothing else.
	navMu sync.Mutex
	nav   map[navKey]*navEntry
	// navLoads counts how many times the badges were computed from the
	// database rather than served from the cache.
	navLoads atomic.Int64
}

// navKey is whose badges an entry holds.
type navKey struct{ org, user uuid.UUID }

// navEntry is one user's badges. While a computation is in flight, done is
// open and every other request for the same key waits on it rather than
// scanning the queue again; once it closes, counts and err are set and the
// entry serves until expires.
type navEntry struct {
	done    chan struct{}
	counts  map[string]int
	err     error
	expires time.Time
}

func NewWorkQueueService(st *store.Store) *WorkQueueService {
	return &WorkQueueService{st: st, Now: time.Now, nav: map[navKey]*navEntry{}}
}

// List is the queue as one org user sees it, most urgent first. An empty
// filter returns every rule. Items this user has snoozed are left out; they
// stay in every colleague's queue.
func (s *WorkQueueService) List(ctx context.Context, p Principal, filter QueueKind) ([]QueueItem, error) {
	if p.Kind != PrincipalOrgUser {
		return nil, ErrForbidden
	}
	var out []QueueItem
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		var err error
		out, err = s.collect(ctx, tx, p, filter)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("work queue: %w", err)
	}
	sortQueue(out)
	return out, nil
}

// Counts is how many items each rule holds for this user. It is a full read
// of the queue: a screen that already holds the list derives the same
// numbers from it with CountItems instead of reading twice.
func (s *WorkQueueService) Counts(ctx context.Context, p Principal) (map[QueueKind]int, error) {
	items, err := s.List(ctx, p, "")
	if err != nil {
		return nil, err
	}
	return CountItems(items), nil
}

// CountItems is how many rows of an unfiltered queue each rule holds, every
// rule present so a chip can read its zero.
func CountItems(items []QueueItem) map[QueueKind]int {
	out := make(map[QueueKind]int, len(QueueKinds))
	for _, kind := range QueueKinds {
		out[kind] = 0
	}
	for _, item := range items {
		out[item.Kind]++
	}
	return out
}

// FilterItems is the rows of one rule out of an unfiltered queue, in the
// order they came; an empty filter is every row.
func FilterItems(items []QueueItem, filter QueueKind) []QueueItem {
	if filter == "" {
		return items
	}
	out := []QueueItem{}
	for _, item := range items {
		if item.Kind == filter {
			out = append(out, item)
		}
	}
	return out
}

// Decide takes one of the decisions a row offers: advance or reject the
// application, or release it to the client. The move rules apply as they do
// anywhere else, so a reject still needs a reason and a stage still needs
// its prerequisite.
func (s *WorkQueueService) Decide(ctx context.Context, p Principal, req DecideRequest) error {
	if err := requireRecruiter(p); err != nil {
		return err
	}
	if req.ApplicationID == uuid.Nil {
		return ErrNotFound
	}
	var err error
	switch req.Action {
	case QueueActionAdvance, QueueActionReject:
		if s.Apps == nil || req.ToStageID == uuid.Nil {
			return ErrBadDecision
		}
		_, err = s.Apps.Move(ctx, p, MoveRequest{ApplicationID: req.ApplicationID, ToStageID: req.ToStageID, Reason: req.Reason})
	case QueueActionRelease:
		if s.Releases == nil {
			return ErrBadDecision
		}
		_, err = s.Releases.Release(ctx, p, req.ApplicationID)
	default:
		return ErrBadDecision
	}
	if err != nil {
		return err
	}
	s.Invalidate(p.OrgID)
	return nil
}

// Snooze hides one item from one user until the given moment. Snoozing an
// item that is already snoozed moves the deadline rather than failing.
func (s *WorkQueueService) Snooze(ctx context.Context, p Principal, kind QueueKind, subjectID uuid.UUID, until time.Time) error {
	if p.Kind != PrincipalOrgUser {
		return ErrForbidden
	}
	if !knownQueueKind(kind) {
		return ErrNotFound
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		return tx.Q.UpsertQueueSnooze(ctx, db.UpsertQueueSnoozeParams{
			OrgID: p.OrgID, UserID: p.UserID, Kind: string(kind), SubjectID: subjectID, Until: ts(until),
		})
	})
	if err != nil {
		return fmt.Errorf("snooze: %w", err)
	}
	s.Invalidate(p.OrgID)
	return nil
}

// NavCounts is every sidebar badge in one request: the queue's own total and
// the counts the other nav entries show. The badges are served from memory
// for NavCountsTTL after they are computed, and concurrent requests for the
// same user's badges share one computation, so the queue's scan runs at most
// once per user per TTL however many pages are opened. A decision or a
// snooze taken through this service refreshes them at once; a move made
// elsewhere (the board, the client portal, an automation) shows on the badge
// within the TTL unless that caller invalidates.
func (s *WorkQueueService) NavCounts(ctx context.Context, p Principal) (map[string]int, error) {
	if p.Kind != PrincipalOrgUser {
		return nil, ErrForbidden
	}
	key := navKey{p.OrgID, p.UserID}
	s.navMu.Lock()
	e := s.nav[key]
	if e != nil && e.done == nil && s.now().Before(e.expires) {
		s.navMu.Unlock()
		return copyCounts(e.counts), nil
	}
	if e != nil && e.done != nil {
		done := e.done
		s.navMu.Unlock()
		select {
		case <-done:
			return copyCounts(e.counts), e.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	e = &navEntry{done: make(chan struct{})}
	s.nav[key] = e
	s.navMu.Unlock()

	// The computation outlives the request that started it: the ones
	// waiting on it would otherwise inherit a cancellation none of them
	// asked for.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	counts, err := s.loadNavCounts(cctx, p, nil)
	s.finish(key, e, counts, err)
	return copyCounts(counts), err
}

// NavCountsFrom is NavCounts for a screen that has just listed the whole
// queue: the queue badge is counted off the rows it already holds, only the
// other badges are read, and the cache takes the fresh numbers.
func (s *WorkQueueService) NavCountsFrom(ctx context.Context, p Principal, items []QueueItem) (map[string]int, error) {
	if p.Kind != PrincipalOrgUser {
		return nil, ErrForbidden
	}
	if items == nil {
		items = []QueueItem{}
	}
	counts, err := s.loadNavCounts(ctx, p, items)
	if err != nil {
		return nil, err
	}
	s.navMu.Lock()
	s.nav[navKey{p.OrgID, p.UserID}] = &navEntry{counts: counts, expires: s.now().Add(NavCountsTTL)}
	s.navMu.Unlock()
	return copyCounts(counts), nil
}

// finish publishes one computation's result to whoever waited on it and
// keeps it for the TTL, unless the entry was invalidated meanwhile, in
// which case the result is served to the waiters only.
func (s *WorkQueueService) finish(key navKey, e *navEntry, counts map[string]int, err error) {
	s.navMu.Lock()
	defer s.navMu.Unlock()
	e.counts, e.err, e.expires = counts, err, s.now().Add(NavCountsTTL)
	close(e.done)
	if err != nil || s.nav[key] != e {
		if s.nav[key] == e {
			delete(s.nav, key)
		}
		return
	}
	e.done = nil
}

// Invalidate drops the cached badges of every user of one org, so the next
// page they open recomputes them. Every write through this service calls
// it; a service that moves applications on its own may call it too, and one
// that does not is covered by the TTL.
func (s *WorkQueueService) Invalidate(orgID uuid.UUID) {
	s.navMu.Lock()
	defer s.navMu.Unlock()
	// An entry still being computed is left to its waiters; finish sees it
	// is no longer the map's and does not keep it.
	for key := range s.nav {
		if key.org == orgID {
			delete(s.nav, key)
		}
	}
}

// NavLoads is how many times the badges were computed from the database
// since the service started: the cache's misses. It is for tests and for
// anyone watching whether the cache earns its keep.
func (s *WorkQueueService) NavLoads() int64 { return s.navLoads.Load() }

// loadNavCounts computes the badges: the queue's total from the rows given,
// or from a read of the queue when none are, and the other four from one
// count query. The queue rules stay in Go rather than being repeated in SQL
// for a count: the two would drift.
func (s *WorkQueueService) loadNavCounts(ctx context.Context, p Principal, items []QueueItem) (map[string]int, error) {
	s.navLoads.Add(1)
	if items == nil {
		var err error
		if items, err = s.List(ctx, p, ""); err != nil {
			return nil, err
		}
	}
	out := map[string]int{"queue": len(items)}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.CountNavSubjects(ctx, p.OrgID)
		if err != nil {
			return err
		}
		out["clients"], out["candidates"] = int(row.Clients), int(row.Candidates)
		out["problems"], out["assessments"] = int(row.Problems), int(row.Assessments)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("nav counts: %w", err)
	}
	return out, nil
}

// copyCounts hands a caller its own map: the cached one is shared.
func copyCounts(counts map[string]int) map[string]int {
	if counts == nil {
		return nil
	}
	out := make(map[string]int, len(counts))
	for key, n := range counts {
		out[key] = n
	}
	return out
}

func knownQueueKind(kind QueueKind) bool {
	for _, k := range QueueKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// collect runs every rule and drops what the user has snoozed. The step
// rules share one scan of the open applications; the rest are their own
// reads.
func (s *WorkQueueService) collect(ctx context.Context, tx *store.Tx, p Principal, filter QueueKind) ([]QueueItem, error) {
	snoozed, err := s.snoozed(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	now := s.now()
	var all []QueueItem
	steps, err := stepItems(ctx, tx, now)
	if err != nil {
		return nil, err
	}
	all = append(all, steps...)
	for _, rule := range []func(context.Context, *store.Tx) ([]QueueItem, error){
		clientWaitingItems, shortlistDraftItems, sprintRatingItems, talentIntroItems,
	} {
		items, err := rule(ctx, tx)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
	}
	out := []QueueItem{}
	for _, item := range all {
		if filter != "" && filter != item.Kind {
			continue
		}
		if snoozed[snoozeKey{item.Kind, item.SubjectID}] {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *WorkQueueService) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

type snoozeKey struct {
	kind    QueueKind
	subject uuid.UUID
}

func (s *WorkQueueService) snoozed(ctx context.Context, tx *store.Tx, p Principal) (map[snoozeKey]bool, error) {
	rows, err := tx.Q.ListActiveQueueSnoozes(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	out := make(map[snoozeKey]bool, len(rows))
	for _, row := range rows {
		out[snoozeKey{QueueKind(row.Kind), row.SubjectID}] = true
	}
	return out, nil
}

// stepItems reads every open application once and asks each what it is
// waiting for.
func stepItems(ctx context.Context, tx *store.Tx, now time.Time) ([]QueueItem, error) {
	rows, err := tx.Q.ListQueueSteps(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]QueueItem, 0, len(rows))
	for _, row := range rows {
		if item, ok := stepItem(row, now); ok {
			out = append(out, item)
		}
	}
	return out, nil
}

// stepItem is the one rule per stage kind: given where an application stands
// and what its stage has collected, the next thing a person has to do, or
// nothing when the ball is in someone else's court (a booked call, an exam
// in progress, a client who has the candidate and has not gone quiet).
func stepItem(r db.ListQueueStepsRow, now time.Time) (QueueItem, bool) {
	item := QueueItem{
		Who: r.CandidateName, JobTitle: r.JobTitle, ClientName: r.ClientName, ClientID: r.ClientID,
		ApplicationID: r.ApplicationID, SubjectID: r.ApplicationID,
		ActionLabel: "Open application", ActionURL: appPath + r.ApplicationID.String(),
	}
	entered := r.EnteredAt.Time
	decide := decisions(r)
	switch domain.StageKind(r.StageKind) {
	case domain.StageGeneric:
		if r.FirstStage {
			item.Kind, item.Detail = QueueResume, "New applicant: does the résumé fit?"
			item.Due, item.Actions = after(entered, ResumeSLA), decide
			return item, true
		}
		item.Kind, item.Detail = QueueDecision, "In "+r.StageName+" since "+dayText(entered, now)+": advance or reject"
		item.Due, item.Actions = after(entered, DecisionSLA), decide
		return item, true
	case domain.StageInterview:
		switch {
		case r.SlotID == uuid.Nil:
			item.Kind, item.Detail = QueueCallUnbooked, r.StageName+": booking link sent, no time picked"
			item.Due = after(entered, CallBookingSLA)
			return item, true
		case r.SlotEndsAt.Valid && r.SlotEndsAt.Time.After(now) && r.SlotStatus != "completed":
			return QueueItem{}, false // booked and still to come
		case !r.HasScorecard:
			item.Kind, item.Detail = QueueFeedback, r.StageName+" ended, feedback due"
			item.Due, item.ActionLabel = timePtr(r.SlotEndsAt), "File scorecard"
			item.ActionURL = appPath + r.ApplicationID.String() + queueScorecardPath + r.StageID.String()
			return item, true
		default:
			item.Kind, item.Detail = QueueDecision, "Feedback in for "+r.StageName+": advance or reject"
			item.Due, item.Actions = after(r.SlotEndsAt.Time, DecisionSLA), decide
			return item, true
		}
	case domain.StageAssessment:
		score := ""
		if v := numericPtr(r.AttemptScore); v != nil {
			score = "scored " + strconv.FormatFloat(*v, 'f', -1, 64)
		}
		switch r.AttemptStatus {
		case "", AttemptInvited:
			item.Kind, item.Detail = QueueExamUnopened, "Exam sent, not opened"
			item.Due = timePtr(r.AttemptInviteExpiresAt)
			if item.Due != nil && item.Due.Before(now) {
				item.Detail = "Exam invite lapsed unopened: re-invite or reject"
				item.Kind, item.Actions = QueueDecision, decide
			}
			return item, true
		case AttemptStarted, AttemptSubmitted:
			return QueueItem{}, false // with the candidate, or with the runner
		case AttemptExpired:
			item.Kind, item.Detail = QueueDecision, "Exam window closed unfinished: re-invite or reject"
			item.Due, item.Actions = after(entered, DecisionSLA), decide
			return item, true
		}
		if r.Verdict == "" {
			item.Kind, item.Detail = QueueExamReview, strings.TrimSpace("Exam done, "+score+", no verdict yet")
			item.Due, item.ActionLabel = after(r.AttemptFinishedAt.Time, ExamReviewSLA), "Review"
			item.ActionURL, item.SubjectID = queueReviewPath+r.AttemptID.String(), r.AttemptID
			return item, true
		}
		item.Kind = QueueDecision
		item.Detail = "Verdict " + r.Verdict + " (" + score + "): advance or reject"
		item.Due, item.Actions = after(r.AttemptFinishedAt.Time, DecisionSLA), decide
		return item, true
	case domain.StageSprint:
		switch {
		case !r.InSprint:
			item.Kind, item.Detail = QueueShortlistPlan, "Waiting to be shortlisted: run a round-robin sprint or book targeted interviews"
			item.Due, item.ActionLabel, item.ActionURL = after(entered, DecisionSLA), "Plan the sprint", queueJobPath+r.JobID.String()+"/sprints"
			return item, true
		case r.HasRating:
			item.Kind, item.Detail = QueueDecision, "Sprint rated: advance or reject"
			item.Due, item.Actions = after(entered, DecisionSLA), decide
			return item, true
		}
		return QueueItem{}, false // the rating rule speaks for the pairings
	case domain.StageClientReview:
		switch {
		case !r.ReleasedAt.Valid:
			item.Kind, item.Detail = QueueForward, "Ready for "+r.ClientName+": forward the résumé, exam, and recording"
			item.Due = after(entered, ForwardSLA)
			item.Actions = []QueueAction{{Kind: QueueActionRelease, Label: "Forward to client"}}
			return item, true
		case (!r.LastClientAt.Valid || r.LastClientAt.Time.Before(r.ReleasedAt.Time)) && r.ReleasedAt.Time.Add(ClientSilentAfter).Before(now):
			item.Kind, item.Detail = QueueClientSilent, "With "+r.ClientName+" since "+dayText(r.ReleasedAt.Time, now)+", nothing back"
			item.Due, item.ActionLabel = after(r.ReleasedAt.Time, ClientSilentAfter), "Nudge the client"
			return item, true
		}
		return QueueItem{}, false // the client has them and is engaged
	}
	return QueueItem{}, false
}

// decisions are the two moves a row offers when the stage has what it
// needs: on to the next stage, or out.
func decisions(r db.ListQueueStepsRow) []QueueAction {
	var out []QueueAction
	if r.NextStageID != uuid.Nil {
		out = append(out, QueueAction{Kind: QueueActionAdvance, Label: "Advance to " + r.NextStageName, ToStageID: r.NextStageID})
	}
	if r.RejectStageID != uuid.Nil {
		out = append(out, QueueAction{Kind: QueueActionReject, Label: "Reject", ToStageID: r.RejectStageID, NeedsReason: true})
	}
	return out
}

func after(t time.Time, d time.Duration) *time.Time {
	at := t.Add(d)
	return &at
}

// dayText says how long ago a moment was, in days, as a row reads.
func dayText(t, now time.Time) string {
	days := int(now.Sub(t).Hours() / 24)
	switch {
	case days <= 0:
		return "today"
	case days == 1:
		return "yesterday"
	}
	return strconv.Itoa(days) + " days ago"
}

func clientWaitingItems(ctx context.Context, tx *store.Tx) ([]QueueItem, error) {
	rows, err := tx.Q.ListQueueClientWaiting(ctx, EventRequestInfo)
	if err != nil {
		return nil, err
	}
	out := make([]QueueItem, 0, len(rows))
	for _, row := range rows {
		detail := "Client asked for more"
		if row.Message != nil && *row.Message != "" {
			detail = *row.Message
		}
		out = append(out, QueueItem{
			Kind: QueueClientWaiting, Who: row.CandidateName, Detail: detail,
			JobTitle: row.JobTitle, ClientName: row.ClientName, Due: timePtr(row.CreatedAt),
			ActionLabel: "Answer", ActionURL: appPath + row.ApplicationID.String(),
			SubjectID: row.ApplicationID, ApplicationID: row.ApplicationID,
		})
	}
	return out, nil
}

func shortlistDraftItems(ctx context.Context, tx *store.Tx) ([]QueueItem, error) {
	rows, err := tx.Q.ListQueueShortlistDrafts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]QueueItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, QueueItem{
			Kind: QueueShortlistDraft, Who: "Draft shortlist",
			Detail:   strconv.Itoa(int(row.Picks)) + " picked, never sent",
			JobTitle: row.JobTitle, ClientName: row.ClientName, Due: timePtr(row.UpdatedAt),
			ActionLabel: "Open builder", ActionURL: queueJobPath + row.JobID.String() + queueShortlistPath,
			SubjectID: row.PacketID,
		})
	}
	return out, nil
}

func sprintRatingItems(ctx context.Context, tx *store.Tx) ([]QueueItem, error) {
	rows, err := tx.Q.ListQueueSprintRatingsMissing(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]QueueItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, QueueItem{
			Kind: QueueSprintRating, Who: row.CandidateName,
			Detail:   row.SprintName + " with " + row.InterviewerName + ", no rating",
			JobTitle: row.JobTitle, ClientName: row.ClientName, Due: timePtr(row.EndedAt),
			ActionLabel: "Rate", ActionURL: queueSprintPath + row.SprintID.String() + "/console",
			SubjectID: row.PairingID,
		})
	}
	return out, nil
}

// sortQueue puts the most overdue work first: everything with a deadline in
// deadline order, then the rest by rule.
func sortQueue(items []QueueItem) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if (a.Due == nil) != (b.Due == nil) {
			return a.Due != nil
		}
		if a.Due != nil && !a.Due.Equal(*b.Due) {
			return a.Due.Before(*b.Due)
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Who < b.Who
	})
}

// timePtr reads a nullable timestamp; a null is no deadline rather than the
// zero time, which would sort as the most urgent row on the screen.
func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	at := t.Time
	return &at
}
