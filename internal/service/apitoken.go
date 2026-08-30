package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// APITokenPrefixLen is how much of the secret is kept in the clear so an
// admin can tell one live token from another in the list.
const APITokenPrefixLen = 8

// ErrTokenName is returned when a token is issued without a name; the name is
// the only thing that tells the list apart once the secret is gone.
var ErrTokenName = errors.New("service: an API token needs a name")

// ErrTokenExpired is returned when a token is issued already expired; a
// credential that cannot be used is a mistake, not a revocation.
var ErrTokenExpired = errors.New("service: an API token cannot expire in the past")

// APIToken is one issued credential as the admin screen shows it. The secret
// is returned once, by Issue, and never again.
type APIToken struct {
	ID        uuid.UUID
	Name      string
	Prefix    string
	UserID    uuid.UUID
	UserEmail string
	IsClient  bool
	CreatedAt time.Time
	ExpiresAt time.Time // zero when the token does not expire
}

// NewAPIToken is what an admin fills in to issue a token. UserID is the org
// user or client user the token acts as; ClientUser says which of the two.
// ExpiresAt is optional.
type NewAPIToken struct {
	Name       string
	UserID     uuid.UUID
	ClientUser bool
	ExpiresAt  *time.Time
}

// APITokenService issues, lists, and revokes the per-user bearer tokens the
// JSON API accepts beside the session cookie.
type APITokenService struct{ st *store.Store }

func NewAPITokenService(st *store.Store) *APITokenService { return &APITokenService{st: st} }

// Issue mints a token for one of the org's users and returns the row along
// with the raw secret, which is the only time it exists outside the caller.
func (s *APITokenService) Issue(ctx context.Context, p Principal, in NewAPIToken) (APIToken, string, error) {
	if err := requireAdmin(p); err != nil {
		return APIToken{}, "", err
	}
	if strings.TrimSpace(in.Name) == "" {
		return APIToken{}, "", ErrTokenName
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(time.Now()) {
		return APIToken{}, "", ErrTokenExpired
	}
	raw, hash, err := newToken()
	if err != nil {
		return APIToken{}, "", err
	}
	params := db.CreateAPITokenParams{
		OrgID: p.OrgID, Name: strings.TrimSpace(in.Name),
		Prefix: raw[:APITokenPrefixLen], TokenHash: hash,
	}
	if in.ExpiresAt != nil {
		params.ExpiresAt = ts(*in.ExpiresAt)
	}
	var out APIToken
	err = s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		email, err := s.subject(ctx, tx, p.OrgID, in)
		if err != nil {
			return err
		}
		if in.ClientUser {
			params.ClientUserID = uuid.NullUUID{UUID: in.UserID, Valid: true}
		} else {
			params.OrgUserID = uuid.NullUUID{UUID: in.UserID, Valid: true}
		}
		row, err := tx.Q.CreateAPIToken(ctx, params)
		if err != nil {
			return err
		}
		out = toAPIToken(row)
		out.UserEmail = email
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return APIToken{}, "", err
		}
		return APIToken{}, "", fmt.Errorf("issue api token: %w", err)
	}
	return out, raw, nil
}

// subject checks the token's user belongs to the admin's org and returns
// their address for the list.
func (s *APITokenService) subject(ctx context.Context, tx *store.Tx, orgID uuid.UUID, in NewAPIToken) (string, error) {
	if in.ClientUser {
		u, err := tx.Q.GetClientUser(ctx, in.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return u.Email, err
	}
	u, err := tx.Q.GetOrgUser(ctx, db.GetOrgUserParams{ID: in.UserID, OrgID: orgID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return u.Email, err
}

// List is the org's live tokens, newest first: revoked and expired ones are
// gone from it, so the screen shows only credentials that still work. Each
// carries the address of the user it acts as.
func (s *APITokenService) List(ctx context.Context, p Principal) ([]APIToken, error) {
	if err := requireAdmin(p); err != nil {
		return nil, err
	}
	var out []APIToken
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.ListAPITokens(ctx, p.OrgID)
		if err != nil {
			return err
		}
		out = make([]APIToken, 0, len(rows))
		for _, r := range rows {
			t := toAPIToken(r.ApiToken)
			t.UserEmail = r.UserEmail
			out = append(out, t)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	return out, nil
}

// Revoke retires a token. Revoking an unknown or already revoked token is
// ErrNotFound, so the screen does not report a revocation that did nothing.
func (s *APITokenService) Revoke(ctx context.Context, p Principal, id uuid.UUID) error {
	if err := requireAdmin(p); err != nil {
		return err
	}
	var n int64
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		var err error
		n, err = tx.Q.RevokeAPIToken(ctx, id)
		return err
	})
	if err != nil {
		return fmt.Errorf("revoke api token: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ResolveToken turns a bearer secret into its principal. An unknown, revoked,
// or expired token is ErrNoSession, the same answer a stale session cookie
// gets, so the API cannot be used to tell the two apart.
func (s *APITokenService) ResolveToken(ctx context.Context, raw string) (Principal, error) {
	if raw == "" {
		return Principal{}, ErrNoSession
	}
	var row db.ApiToken
	err := s.st.WithLookupTx(ctx, store.Lookup{TokenHash: hashToken(raw)}, func(ctx context.Context, tx *store.Tx) error {
		var err error
		row, err = tx.Q.LookupAPIToken(ctx, hashToken(raw))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrNoSession
	}
	if err != nil {
		return Principal{}, fmt.Errorf("lookup api token: %w", err)
	}
	p := Principal{OrgID: row.OrgID}
	if row.OrgUserID.Valid {
		p.Kind, p.UserID = PrincipalOrgUser, row.OrgUserID.UUID
	} else {
		p.Kind, p.UserID = PrincipalClientUser, row.ClientUserID.UUID
	}
	err = s.st.WithTx(ctx, orgScoped(row.OrgID), func(ctx context.Context, tx *store.Tx) error {
		switch p.Kind {
		case PrincipalOrgUser:
			roles, err := tx.Q.ListOrgUserRoles(ctx, p.UserID)
			if err != nil {
				return err
			}
			p.Roles = roles
		case PrincipalClientUser:
			u, err := tx.Q.GetClientUser(ctx, p.UserID)
			if err != nil {
				return err
			}
			p.ClientCompanyID = u.ClientCompanyID
		}
		return nil
	})
	if err != nil {
		return Principal{}, fmt.Errorf("load api token principal: %w", err)
	}
	return p, nil
}

func toAPIToken(r db.ApiToken) APIToken {
	out := APIToken{ID: r.ID, Name: r.Name, Prefix: r.Prefix, CreatedAt: r.CreatedAt.Time}
	if r.OrgUserID.Valid {
		out.UserID = r.OrgUserID.UUID
	} else {
		out.UserID, out.IsClient = r.ClientUserID.UUID, true
	}
	if r.ExpiresAt.Valid {
		out.ExpiresAt = r.ExpiresAt.Time
	}
	return out
}
