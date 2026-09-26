package service

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// QueueKind names one rule of the work queue. Each is a separate read, so a
// rule can be reasoned about and tested without the other four.
type QueueKind string

const (
	// QueueReview: a sitting was scored and nobody has reviewed it.
	QueueReview QueueKind = "review"
	// QueueExpiring: an invite lapses within the day and was never started.
	QueueExpiring QueueKind = "expiring"
	// QueueClientWaiting: the client asked something and got no answer.
	QueueClientWaiting QueueKind = "client_waiting"
	// QueueShortlistDraft: a packet was built and never sent.
	QueueShortlistDraft QueueKind = "shortlist_draft"
	// QueueScorecardOverdue: an interview ended over a day ago with no scorecard.
	QueueScorecardOverdue QueueKind = "scorecard_overdue"
	// QueueSprintRating: a sprint conversation ended and its interviewer
	// never rated the candidate.
	QueueSprintRating QueueKind = "sprint_rating"
	// QueueTalentIntro is a company that asked to meet a match and a
	// recruiter who has not yet sent the person the opportunity.
	QueueTalentIntro QueueKind = "talent_intro"
)

// QueueKinds is the queue's rules in the order the screen groups them.
var QueueKinds = []QueueKind{QueueReview, QueueExpiring, QueueClientWaiting, QueueShortlistDraft, QueueScorecardOverdue, QueueSprintRating, QueueTalentIntro}

// SnoozeWindow is how long "not now" lasts.
const SnoozeWindow = 24 * time.Hour

// Where a queue row's primary action lands. The service names the paths
// because the item carries the link; the web packages own the screens.
const (
	queueReviewPath    = "/app/reviews/"
	queueScorecardPath = "/scorecard/"
	queueShortlistPath = "/shortlist"
	queueJobPath       = "/app/jobs/"
	queueSprintPath    = "/app/sprints/"
)

// QueueItem is one pending action: who it is about, what it is, and the one
// link that resolves it.
type QueueItem struct {
	Kind                              QueueKind
	Who, Detail, JobTitle, ClientName string
	// Due is when the item stops being merely late, where the rule has such
	// a moment: the invite's expiry, the question's age, the interview's end.
	Due                    *time.Time
	ActionLabel, ActionURL string
	// SubjectID is the row the rule fired on — an attempt, application,
	// packet, or slot — and the key a snooze is filed under.
	SubjectID uuid.UUID
}

// WorkQueueService reads the queue. It owns no table of its own: every row
// is derived, so an item disappears the moment the work behind it is done.
type WorkQueueService struct{ st *store.Store }

func NewWorkQueueService(st *store.Store) *WorkQueueService { return &WorkQueueService{st: st} }

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

// Counts is how many items each rule holds for this user, for the filter
// chips and the sidebar badge.
func (s *WorkQueueService) Counts(ctx context.Context, p Principal) (map[QueueKind]int, error) {
	items, err := s.List(ctx, p, "")
	if err != nil {
		return nil, err
	}
	out := make(map[QueueKind]int, len(QueueKinds))
	for _, kind := range QueueKinds {
		out[kind] = 0
	}
	for _, item := range items {
		out[item.Kind]++
	}
	return out, nil
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
		return fmt.Errorf("snooze queue item: %w", err)
	}
	return nil
}

// NavCounts is every sidebar badge in one request: the queue's own total and
// the plain sizes of the other destinations. The keys are the nav keys the
// layout uses.
func (s *WorkQueueService) NavCounts(ctx context.Context, p Principal) (map[string]int, error) {
	if p.Kind != PrincipalOrgUser {
		return nil, ErrForbidden
	}
	counts, err := s.Counts(ctx, p)
	if err != nil {
		return nil, err
	}
	queued := 0
	for _, n := range counts {
		queued += n
	}
	out := map[string]int{"queue": queued}
	err = s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.CountNavSubjects(ctx, p.OrgID)
		if err != nil {
			return err
		}
		out["clients"] = int(row.Clients)
		out["candidates"] = int(row.Candidates)
		out["problems"] = int(row.Problems)
		out["assessments"] = int(row.Assessments)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("nav counts: %w", err)
	}
	return out, nil
}

func knownQueueKind(kind QueueKind) bool {
	for _, k := range QueueKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// collect runs the rules the filter asks for and drops what the user has
// snoozed.
func (s *WorkQueueService) collect(ctx context.Context, tx *store.Tx, p Principal, filter QueueKind) ([]QueueItem, error) {
	snoozed, err := s.snoozed(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	rules := map[QueueKind]func(context.Context, *store.Tx) ([]QueueItem, error){
		QueueReview:           reviewItems,
		QueueExpiring:         expiringItems,
		QueueClientWaiting:    clientWaitingItems,
		QueueShortlistDraft:   shortlistDraftItems,
		QueueScorecardOverdue: scorecardOverdueItems,
		QueueSprintRating:     sprintRatingItems,
		QueueTalentIntro:      talentIntroItems,
	}
	out := []QueueItem{}
	for _, kind := range QueueKinds {
		if filter != "" && filter != kind {
			continue
		}
		items, err := rules[kind](ctx, tx)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if snoozed[snoozeKey{item.Kind, item.SubjectID}] {
				continue
			}
			out = append(out, item)
		}
	}
	return out, nil
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

func reviewItems(ctx context.Context, tx *store.Tx) ([]QueueItem, error) {
	rows, err := tx.Q.ListQueueReview(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]QueueItem, 0, len(rows))
	for _, row := range rows {
		detail := "Scored sitting, no verdict yet"
		if score := numericPtr(row.Score); score != nil {
			detail = "Scored " + strconv.FormatFloat(*score, 'f', -1, 64) + ", no verdict yet"
		}
		out = append(out, QueueItem{
			Kind: QueueReview, Who: row.CandidateName, Detail: detail,
			JobTitle: row.JobTitle, ClientName: row.ClientName, Due: timePtr(row.FinishedAt),
			ActionLabel: "Review", ActionURL: queueReviewPath + row.AttemptID.String(),
			SubjectID: row.AttemptID,
		})
	}
	return out, nil
}

func expiringItems(ctx context.Context, tx *store.Tx) ([]QueueItem, error) {
	rows, err := tx.Q.ListQueueExpiring(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]QueueItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, QueueItem{
			Kind: QueueExpiring, Who: row.CandidateName, Detail: "Invite unopened, window closing",
			JobTitle: row.JobTitle, ClientName: row.ClientName, Due: timePtr(row.InviteExpiresAt),
			ActionLabel: "Open application", ActionURL: appPath + row.ApplicationID.String(),
			SubjectID: row.AttemptID,
		})
	}
	return out, nil
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
			SubjectID: row.ApplicationID,
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

func scorecardOverdueItems(ctx context.Context, tx *store.Tx) ([]QueueItem, error) {
	rows, err := tx.Q.ListQueueScorecardsOverdue(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]QueueItem, 0, len(rows))
	for _, row := range rows {
		item := QueueItem{
			Kind: QueueScorecardOverdue, Who: row.CandidateName,
			Detail:   row.StageName + " with " + row.VetterName + ", no scorecard",
			JobTitle: row.JobTitle, ClientName: row.ClientName, Due: timePtr(row.EndsAt),
			ActionLabel: "File scorecard", SubjectID: row.SlotID,
		}
		if row.ApplicationID.Valid && row.StageID.Valid {
			item.ActionURL = appPath + row.ApplicationID.UUID.String() + queueScorecardPath + row.StageID.UUID.String()
		}
		out = append(out, item)
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
