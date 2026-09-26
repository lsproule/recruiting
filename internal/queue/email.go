package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"recruiting/internal/mail"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// Email log statuses; they mirror the email_log status check.
const (
	EmailQueued = "queued"
	EmailSent   = "sent"
	EmailFailed = "failed"
)

// EmailPayload is the email.send payload.
type EmailPayload struct {
	Template string         `json:"template"`
	To       string         `json:"to"`
	OrgID    uuid.UUID      `json:"org_id"`
	Data     map[string]any `json:"data"`
	// Calendar, when set, attaches the event as an .ics file so either
	// side of an interview can put it straight in their calendar.
	Calendar *CalendarEvent `json:"calendar,omitempty"`
}

// CalendarEvent is the interview an email carries as an invitation.
type CalendarEvent struct {
	UID         string    `json:"uid"`
	Summary     string    `json:"summary"`
	Description string    `json:"description"`
	Location    string    `json:"location"`
	URL         string    `json:"url"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
}

// attachment is the event as a file on the message.
func (e CalendarEvent) attachment() mail.Attachment {
	return mail.ICSAttachment(mail.Event{
		UID: e.UID, Summary: e.Summary, Description: e.Description, Location: e.Location, URL: e.URL,
		Start: e.Start, End: e.End,
	})
}

// TenantStore is the slice of the store an email job needs: a transaction
// scoped to the payload's org, because email_log is a tenant table.
type TenantStore interface {
	WithTx(ctx context.Context, p store.Principal, fn func(ctx context.Context, tx *store.Tx) error) error
}

// orgPrincipal scopes the job's transaction. A queue job acts for the org
// named in its payload, with no user behind it.
type orgPrincipal struct{ orgID uuid.UUID }

func (p orgPrincipal) Scope() store.Scope { return store.Scope{OrgID: p.orgID} }

var errNoRecipient = errors.New("queue: email.send payload has no template, recipient, or org")

// EmailHandler works email.send: it renders the named template with the org's
// name as the only branding, records the attempt in email_log, and sends over
// SMTP. Returning an error retries with backoff; after MaxAttempts the job is
// discarded with the log row left at failed.
//
// It never sends the same message twice. The log row is keyed by the job id,
// so a retry updates the row rather than adding one; a row already reading
// sent means a previous attempt delivered the message and this one stops.
// Once SMTP has accepted a message the send is irreversible, so bookkeeping
// that fails afterwards is logged and the job succeeds — retrying it would
// deliver a second copy to fix a database write.
func EmailHandler(st TenantStore, r *mail.Renderer, sender mail.Sender, logger *slog.Logger) Handler {
	return func(ctx context.Context, job Job) error {
		var p EmailPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			// A payload that does not parse will never parse; retrying it is
			// pointless, but the queue has no way to say "give up" other than
			// exhausting the attempts.
			return fmt.Errorf("queue: email.send payload: %w", err)
		}
		if p.Template == "" || p.To == "" || p.OrgID == uuid.Nil {
			return errNoRecipient
		}
		principal := orgPrincipal{orgID: p.OrgID}
		jobID := job.ID

		var orgName string
		var alreadySent bool
		if err := st.WithTx(ctx, principal, func(ctx context.Context, tx *store.Tx) error {
			org, err := tx.Q.GetOrg(ctx, p.OrgID)
			if err != nil {
				return fmt.Errorf("queue: email.send org %s: %w", p.OrgID, err)
			}
			orgName = org.Name
			prior, err := tx.Q.GetEmailLogByJob(ctx, &jobID)
			switch {
			case err == nil:
				alreadySent = prior.Status == EmailSent
			case errors.Is(err, pgx.ErrNoRows):
				// First attempt: no row yet.
			default:
				return fmt.Errorf("queue: email.send log lookup: %w", err)
			}
			return nil
		}); err != nil {
			return err
		}
		if alreadySent {
			logf(ctx, logger, slog.LevelInfo, "email already sent for this job; not sending again",
				"job_id", jobID, "template", p.Template)
			return nil
		}

		msg, renderErr := r.Render(p.Template, orgName, p.Data)
		subject := msg.Subject
		logErr := st.WithTx(ctx, principal, func(ctx context.Context, tx *store.Tx) error {
			row, err := tx.Q.UpsertEmailLogForJob(ctx, db.UpsertEmailLogForJobParams{
				OrgID:    p.OrgID,
				Template: p.Template,
				ToEmail:  p.To,
				Subject:  subject,
				JobID:    &jobID,
			})
			if err != nil {
				return fmt.Errorf("queue: email.send log: %w", err)
			}
			if renderErr == nil {
				return nil
			}
			return markStatus(ctx, tx, row.ID, EmailFailed, renderErr)
		})
		if renderErr != nil {
			// A payload missing a template variable is a bug at the enqueuing
			// site; it fails before anything is sent.
			return errors.Join(renderErr, logErr)
		}
		if logErr != nil {
			return logErr
		}

		msg.To = p.To
		if p.Calendar != nil {
			msg.Attachments = append(msg.Attachments, p.Calendar.attachment())
		}
		sendErr := sender.Send(ctx, msg)
		status := EmailSent
		if sendErr != nil {
			status = EmailFailed
		}
		bookErr := st.WithTx(ctx, principal, func(ctx context.Context, tx *store.Tx) error {
			row, err := tx.Q.GetEmailLogByJob(ctx, &jobID)
			if err != nil {
				return fmt.Errorf("queue: email.send log lookup: %w", err)
			}
			return markStatus(ctx, tx, row.ID, status, sendErr)
		})
		if sendErr == nil {
			if bookErr != nil {
				// The message is out. Retrying to repair email_log would send
				// it again, so the row is left stale and the loss is logged.
				logf(ctx, logger, slog.LevelError, "email sent but its log row could not be updated",
					"job_id", jobID, "template", p.Template, "error", bookErr)
			}
			return nil
		}
		if bookErr != nil {
			return errors.Join(sendErr, bookErr)
		}
		return sendErr
	}
}

func logf(ctx context.Context, logger *slog.Logger, level slog.Level, msg string, args ...any) {
	if logger == nil {
		return
	}
	logger.Log(ctx, level, msg, args...)
}

func markStatus(ctx context.Context, tx *store.Tx, id uuid.UUID, status string, cause error) error {
	var last *string
	if cause != nil {
		msg := cause.Error()
		last = &msg
	}
	if err := tx.Q.UpdateEmailLogStatus(ctx, db.UpdateEmailLogStatusParams{ID: id, Status: status, LastError: last}); err != nil {
		return fmt.Errorf("queue: email.send status: %w", err)
	}
	return nil
}
