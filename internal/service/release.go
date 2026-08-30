package service

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"recruiting/internal/mail"
	"recruiting/internal/queue"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// clientJobPath is where the release notice lands; the client surface serves it.
const clientJobPath = "/client/jobs/"

// Enqueuer is the slice of the queue a release needs; queue.Client satisfies it.
type Enqueuer interface {
	Enqueue(ctx context.Context, tx pgx.Tx, kind string, payload any) (int64, error)
	EnqueueAt(ctx context.Context, tx pgx.Tx, kind string, payload any, runAt time.Time) (int64, error)
	CancelTx(ctx context.Context, tx pgx.Tx, id int64) error
}

// enqueued drops the job id from an Enqueue result for callers that only
// need to know whether the job was queued.
func enqueued(_ int64, err error) error { return err }

// ReleaseService is the recruiter's release switch. A release sets
// released_at, records the event, and queues the notice to the client
// company's users in one transaction, so a notice that cannot be queued
// leaves the application unreleased; an unrelease hides it again at once.
type ReleaseService struct {
	st      *store.Store
	q       Enqueuer
	baseURL string
}

// NewReleaseService wires the store and the queue the notice goes to. A nil
// queue disables the notice, which suits tests of the visibility alone.
func NewReleaseService(st *store.Store, q Enqueuer, baseURL string) *ReleaseService {
	return &ReleaseService{st: st, q: q, baseURL: strings.TrimRight(baseURL, "/")}
}

// Release makes the application visible to the client and tells every user
// of the client company. Concurrent releases serialise on the row lock, so
// the second sees it already released and does nothing.
func (s *ReleaseService) Release(ctx context.Context, p Principal, id uuid.UUID) (Application, error) {
	return s.setReleased(ctx, p, id, true)
}

// Unrelease hides the application from the client again.
func (s *ReleaseService) Unrelease(ctx context.Context, p Principal, id uuid.UUID) (Application, error) {
	return s.setReleased(ctx, p, id, false)
}

func (s *ReleaseService) setReleased(ctx context.Context, p Principal, id uuid.UUID, released bool) (Application, error) {
	if err := requireRecruiter(p); err != nil {
		return Application{}, err
	}
	var out Application
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		app, err := tx.Q.GetApplicationForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if app.ReleasedAt.Valid == released {
			// Already in the asked-for state: nothing to record or send.
			out, err = card(ctx, tx, id)
			return err
		}
		at := ts(time.Now())
		if !released {
			at.Valid = false
		}
		if _, err := tx.Q.SetApplicationReleased(ctx, db.SetApplicationReleasedParams{ID: id, ReleasedAt: at}); err != nil {
			return err
		}
		kind := EventUnreleased
		if released {
			kind = EventReleased
		}
		if _, err := tx.Q.CreateApplicationEvent(ctx, db.CreateApplicationEventParams{
			OrgID: p.OrgID, ApplicationID: id, ActorKind: actorKind(p),
			ActorID: uuid.NullUUID{UUID: p.UserID, Valid: p.UserID != uuid.Nil},
			Kind:    kind, Payload: []byte("{}"),
		}); err != nil {
			return err
		}
		if out, err = card(ctx, tx, id); err != nil {
			return err
		}
		if !released || s.q == nil {
			return nil
		}
		return s.notify(ctx, tx, p.OrgID, app.ClientCompanyID, out)
	})
	if err != nil {
		return Application{}, wrapMove("release application", err)
	}
	return out, nil
}

// notify queues one release notice per client user of the company, inside
// the release's own transaction.
func (s *ReleaseService) notify(ctx context.Context, tx *store.Tx, orgID, companyID uuid.UUID, app Application) error {
	users, err := tx.Q.ListClientUsersByCompany(ctx, companyID)
	if err != nil {
		return err
	}
	for _, u := range users {
		_, err := s.q.Enqueue(ctx, tx, queue.KindEmailSend, queue.EmailPayload{
			Template: mail.TemplateClientReleaseNotice, To: u.Email, OrgID: orgID,
			Data: map[string]any{
				"ContactName":    u.Name,
				"JobTitle":       app.JobTitle,
				"CandidateCount": 1,
				"PortalURL":      s.baseURL + clientJobPath + app.JobID.String(),
			},
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// card is ApplicationService's card for a caller without one.
func card(ctx context.Context, tx *store.Tx, id uuid.UUID) (Application, error) {
	return (*ApplicationService)(nil).card(ctx, tx, id)
}
