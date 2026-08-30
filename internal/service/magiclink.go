package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// Magic link purposes. apply is single-use; book and assessment stay valid
// until expiry or revocation because candidates return to those pages.
const (
	LinkApply      = "apply"
	LinkBook       = "book"
	LinkAssessment = "assessment"
)

var (
	ErrLinkInvalid = errors.New("service: unknown link")
	ErrLinkPurpose = errors.New("service: link is for a different purpose")
	ErrLinkExpired = errors.New("service: link has expired")
	ErrLinkRevoked = errors.New("service: link has been revoked")
	ErrLinkUsed    = errors.New("service: link has already been used")
)

type MagicLinkService struct {
	st *store.Store
}

func NewMagicLinkService(st *store.Store) *MagicLinkService { return &MagicLinkService{st: st} }

// Issue creates a link in p's org and returns the raw token (for the URL)
// and the link id (for revocation).
func (m *MagicLinkService) Issue(ctx context.Context, p Principal, purpose string, subject uuid.UUID, ttl time.Duration) (string, uuid.UUID, error) {
	switch purpose {
	case LinkApply, LinkBook, LinkAssessment:
	default:
		return "", uuid.Nil, fmt.Errorf("issue link: bad purpose %q", purpose)
	}
	token, hash, err := newToken()
	if err != nil {
		return "", uuid.Nil, err
	}
	var id uuid.UUID
	err = m.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.CreateMagicLink(ctx, db.CreateMagicLinkParams{
			OrgID: p.OrgID, TokenHash: hash, Purpose: purpose, SubjectID: subject, ExpiresAt: ts(time.Now().Add(ttl)),
		})
		id = row.ID
		return err
	})
	if err != nil {
		return "", uuid.Nil, fmt.Errorf("issue link: %w", err)
	}
	return token, id, nil
}

// Resolve validates the token for purpose without consuming it.
func (m *MagicLinkService) Resolve(ctx context.Context, token, purpose string) (Principal, error) {
	link, err := m.lookup(ctx, token, purpose)
	if err != nil {
		return Principal{}, err
	}
	return linkPrincipal(link), nil
}

// Consume validates the token and, for apply links, marks it used.
func (m *MagicLinkService) Consume(ctx context.Context, token, purpose string) (Principal, error) {
	link, err := m.lookup(ctx, token, purpose)
	if err != nil {
		return Principal{}, err
	}
	if link.Purpose == LinkApply {
		// The conditional update is the single-use gate; the lookup above
		// only classifies errors and may race with another Consume.
		var n int64
		err = m.st.WithTx(ctx, orgScoped(link.OrgID), func(ctx context.Context, tx *store.Tx) error {
			var err error
			n, err = tx.Q.MarkMagicLinkUsed(ctx, link.ID)
			return err
		})
		if err != nil {
			return Principal{}, fmt.Errorf("consume link: %w", err)
		}
		if n == 0 {
			if _, err := m.lookup(ctx, token, purpose); err != nil {
				return Principal{}, err
			}
			return Principal{}, ErrLinkUsed
		}
	}
	return linkPrincipal(link), nil
}

// Revoke invalidates a link in p's org.
func (m *MagicLinkService) Revoke(ctx context.Context, p Principal, id uuid.UUID) error {
	return m.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		return tx.Q.RevokeMagicLink(ctx, id)
	})
}

func (m *MagicLinkService) lookup(ctx context.Context, token, purpose string) (db.MagicLink, error) {
	var link db.MagicLink
	if token == "" {
		return link, ErrLinkInvalid
	}
	hash := hashToken(token)
	err := m.st.WithLookupTx(ctx, store.Lookup{TokenHash: hash}, func(ctx context.Context, tx *store.Tx) error {
		var err error
		link, err = tx.Q.LookupMagicLink(ctx, hash)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return link, ErrLinkInvalid
	}
	if err != nil {
		return link, fmt.Errorf("lookup link: %w", err)
	}
	switch {
	case link.Purpose != purpose:
		return link, ErrLinkPurpose
	case link.RevokedAt.Valid:
		return link, ErrLinkRevoked
	case !link.ExpiresAt.Time.After(time.Now()):
		return link, ErrLinkExpired
	case link.UsedAt.Valid:
		return link, ErrLinkUsed
	}
	return link, nil
}

func linkPrincipal(l db.MagicLink) Principal {
	return Principal{Kind: PrincipalMagicLink, OrgID: l.OrgID, MagicPurpose: l.Purpose, SubjectID: l.SubjectID}
}
