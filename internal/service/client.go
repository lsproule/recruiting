package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"recruiting/internal/domain"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// ClientAccount is one client company as the recruiter's book of accounts
// reads it: who it is, what is open, and how the desk is doing on it.
type ClientAccount struct {
	ID                                   uuid.UUID
	Name, Industry, OwnerName, Brief     string
	ShortlistSLADays                     int // 0 when none was agreed
	OpenJobs, InPipeline, AwaitingClient int
	Placed90d                            int
	// MedianDaysToShortlist is the middle job's wait from opening to its
	// first packet going out. Nil until a packet has been sent at all.
	MedianDaysToShortlist *float64
}

// ClientAccountJob is one row of a client's Jobs tab.
type ClientAccountJob struct {
	ID                                              uuid.UUID
	Title, Seniority, Location, Status, Assessments string
	OpenedAt                                        time.Time
	Applicants, Scored, Shortlisted                 int
}

// ClientPacket is one row of a client's Shortlists tab: the recruiter's own
// view, so drafts are on it too.
type ClientPacket struct {
	ID, JobID uuid.UUID
	JobTitle  string
	Status    string
	Picks     int
	SentAt    *time.Time
}

// ClientDetail is the client screen behind its three tabs.
type ClientDetail struct {
	Account ClientAccount
	Jobs    []ClientAccountJob
	Packets []ClientPacket
}

// ClientBoard is the client's pipeline. A client has no board of its own, so
// one job's columns are merged with the next by stage name and kind; JobID
// narrows it back to a single job.
type ClientBoard struct {
	Account ClientAccount
	Jobs    []ClientAccountJob
	JobID   uuid.UUID // Nil means every job of the client, merged
	Columns []BoardColumn
}

// ClientService is the recruiter's side of a client account. The client's own
// portal is ClientPortalService; nothing here is readable by a client user.
type ClientService struct {
	st   *store.Store
	apps *ApplicationService
}

func NewClientService(st *store.Store, apps *ApplicationService) *ClientService {
	return &ClientService{st: st, apps: apps}
}

// List is the org's client companies with their desk numbers, by name.
func (s *ClientService) List(ctx context.Context, p Principal) ([]ClientAccount, error) {
	if p.Kind != PrincipalOrgUser {
		return nil, ErrForbidden
	}
	var out []ClientAccount
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.ListClientAccounts(ctx, db.ListClientAccountsParams{OrgID: p.OrgID})
		if err != nil {
			return err
		}
		out = make([]ClientAccount, 0, len(rows))
		for _, row := range rows {
			out = append(out, toClientAccount(row))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list clients: %w", err)
	}
	return out, nil
}

// Detail is one account with its jobs and its shortlist packets.
func (s *ClientService) Detail(ctx context.Context, p Principal, companyID uuid.UUID) (ClientDetail, error) {
	if p.Kind != PrincipalOrgUser {
		return ClientDetail{}, ErrForbidden
	}
	var out ClientDetail
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		account, err := s.account(ctx, tx, p, companyID)
		if err != nil {
			return err
		}
		out.Account = account
		if out.Jobs, err = clientJobs(ctx, tx, companyID); err != nil {
			return err
		}
		packets, err := tx.Q.ListShortlistPacketsByCompany(ctx, companyID)
		if err != nil {
			return err
		}
		out.Packets = make([]ClientPacket, 0, len(packets))
		for _, row := range packets {
			out.Packets = append(out.Packets, ClientPacket{
				ID: row.ID, JobID: row.JobID, JobTitle: row.JobTitle,
				Status: row.Status, Picks: int(row.Picks), SentAt: timePtr(row.SentAt),
			})
		}
		return nil
	})
	if err != nil {
		return ClientDetail{}, wrapClientAccount("client detail", err)
	}
	return out, nil
}

// Board is the client's pipeline: one job's board when jobID is set, or every
// job's merged into shared columns. Merging is by stage name and kind, since
// two jobs of one client run the same pipeline under their own stage rows.
func (s *ClientService) Board(ctx context.Context, p Principal, companyID, jobID uuid.UUID) (ClientBoard, error) {
	if p.Kind != PrincipalOrgUser {
		return ClientBoard{}, ErrForbidden
	}
	out := ClientBoard{JobID: jobID}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		account, err := s.account(ctx, tx, p, companyID)
		if err != nil {
			return err
		}
		out.Account = account
		out.Jobs, err = clientJobs(ctx, tx, companyID)
		return err
	})
	if err != nil {
		return ClientBoard{}, wrapClientAccount("client board", err)
	}
	boards := make([]Board, 0, len(out.Jobs))
	for _, job := range out.Jobs {
		if jobID != uuid.Nil && job.ID != jobID {
			continue
		}
		board, err := s.apps.Board(ctx, p, job.ID)
		if err != nil {
			return ClientBoard{}, wrapClientAccount("client board", err)
		}
		// Merged columns mix jobs, so each card has to say which it is on.
		for i := range board.Columns {
			for j := range board.Columns[i].Cards {
				board.Columns[i].Cards[j].JobTitle = board.Job.Title
			}
		}
		boards = append(boards, board)
	}
	if jobID != uuid.Nil && len(boards) == 0 {
		return ClientBoard{}, ErrNotFound
	}
	out.Columns = mergeBoards(boards)
	return out, nil
}

// account reads one company's row, or reports it missing.
func (s *ClientService) account(ctx context.Context, tx *store.Tx, p Principal, companyID uuid.UUID) (ClientAccount, error) {
	rows, err := tx.Q.ListClientAccounts(ctx, db.ListClientAccountsParams{
		OrgID: p.OrgID, ID: uuid.NullUUID{UUID: companyID, Valid: true},
	})
	if err != nil {
		return ClientAccount{}, err
	}
	if len(rows) == 0 {
		return ClientAccount{}, ErrNotFound
	}
	return toClientAccount(rows[0]), nil
}

func clientJobs(ctx context.Context, tx *store.Tx, companyID uuid.UUID) ([]ClientAccountJob, error) {
	rows, err := tx.Q.ListClientAccountJobs(ctx, companyID)
	if err != nil {
		return nil, err
	}
	out := make([]ClientAccountJob, 0, len(rows))
	for _, row := range rows {
		out = append(out, ClientAccountJob{
			ID: row.ID, Title: row.Title, Seniority: deref(row.Seniority),
			Location: deref(row.Location), Status: row.Status, Assessments: row.AssessmentNames,
			OpenedAt:   row.CreatedAt.Time,
			Applicants: int(row.Applicants), Scored: int(row.Scored), Shortlisted: int(row.Shortlisted),
		})
	}
	return out, nil
}

// mergeBoards folds several jobs' boards into one set of columns. Stages that
// share a name and a kind are the same step of the same process, whichever
// job wrote them; the earliest position any job gives a step orders it.
func mergeBoards(boards []Board) []BoardColumn {
	type merged struct {
		stage    domain.Stage
		position int
		cards    []Application
	}
	order := []string{}
	byKey := map[string]*merged{}
	for _, board := range boards {
		for _, col := range board.Columns {
			key := strings.ToLower(strings.TrimSpace(col.Stage.Name)) + "\x00" + string(col.Stage.Kind)
			m, ok := byKey[key]
			if !ok {
				m = &merged{stage: col.Stage, position: col.Stage.Position, cards: []Application{}}
				byKey[key] = m
				order = append(order, key)
			}
			if col.Stage.Position < m.position {
				m.position = col.Stage.Position
			}
			m.cards = append(m.cards, col.Cards...)
		}
	}
	out := make([]BoardColumn, 0, len(order))
	for _, key := range order {
		m := byKey[key]
		stage := m.stage
		stage.Position = m.position
		out = append(out, BoardColumn{Stage: stage, Cards: m.cards})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Stage.Position != out[j].Stage.Position {
			return out[i].Stage.Position < out[j].Stage.Position
		}
		return out[i].Stage.Name < out[j].Stage.Name
	})
	return out
}

func toClientAccount(row db.ListClientAccountsRow) ClientAccount {
	return ClientAccount{
		ID: row.ID, Name: row.Name, Industry: row.Industry, OwnerName: row.OwnerName,
		Brief: row.Brief, ShortlistSLADays: derefInt(row.ShortlistSlaDays),
		OpenJobs: int(row.OpenJobs), InPipeline: int(row.InPipeline),
		AwaitingClient: int(row.AwaitingClient), Placed90d: int(row.Placed90d),
		MedianDaysToShortlist: numericPtr(row.MedianDaysToShortlist),
	}
}

func wrapClientAccount(what string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrForbidden):
		return err
	}
	return fmt.Errorf("%s: %w", what, err)
}
