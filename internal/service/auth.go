package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// Surface is the login surface: org users use the app, client users the
// client portal. A credential only ever logs in on its own surface.
type Surface int

const (
	SurfaceApp Surface = iota
	SurfaceClient
)

var (
	ErrInvalidCredentials = errors.New("service: invalid email or password")
	ErrNoSession          = errors.New("service: no such session")
	ErrResetInvalid       = errors.New("service: password reset link is invalid, expired, or already used")
	ErrPasswordTooShort   = errors.New("service: password must be at least 10 characters")
)

const (
	SessionTTL       = 14 * 24 * time.Hour
	PasswordResetTTL = time.Hour
	minPasswordLen   = 10
)

// AuthService handles password login, server-side sessions, and resets.
type AuthService struct {
	st *store.Store
}

func NewAuthService(st *store.Store) *AuthService { return &AuthService{st: st} }

type LoginResult struct {
	Token     string // raw session token for the cookie; only its hash is stored
	ExpiresAt time.Time
	Principal Principal
}

// Login verifies the password and creates a fresh session, deleting
// priorToken's session (if any) so the id rotates on every login.
func (a *AuthService) Login(ctx context.Context, surface Surface, email, password, priorToken string) (LoginResult, error) {
	var p Principal
	var hash string
	err := a.st.WithLookupTx(ctx, store.Lookup{Email: email}, func(ctx context.Context, tx *store.Tx) error {
		switch surface {
		case SurfaceApp:
			rows, err := tx.Q.LookupOrgUsersByEmail(ctx, email)
			if err != nil {
				return err
			}
			for _, r := range rows {
				if ok, _ := VerifyPassword(r.PasswordHash, password); ok {
					p = Principal{Kind: PrincipalOrgUser, OrgID: r.OrgUser.OrgID, UserID: r.OrgUser.ID}
					hash = r.PasswordHash
					return nil
				}
			}
		case SurfaceClient:
			rows, err := tx.Q.LookupClientUsersByEmail(ctx, email)
			if err != nil {
				return err
			}
			for _, r := range rows {
				if ok, _ := VerifyPassword(r.PasswordHash, password); ok {
					p = Principal{Kind: PrincipalClientUser, OrgID: r.ClientUser.OrgID, UserID: r.ClientUser.ID, ClientCompanyID: r.ClientUser.ClientCompanyID}
					hash = r.PasswordHash
					return nil
				}
			}
		}
		return nil
	})
	if err != nil {
		return LoginResult{}, fmt.Errorf("login lookup: %w", err)
	}
	if hash == "" {
		// Burn comparable time so unknown emails are not distinguishable.
		_, _ = VerifyPassword(dummyHash, password)
		return LoginResult{}, ErrInvalidCredentials
	}
	if priorToken != "" {
		_ = a.Logout(ctx, priorToken)
	}
	token, tokenHash, err := newToken()
	if err != nil {
		return LoginResult{}, err
	}
	expires := time.Now().Add(SessionTTL)
	err = a.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		if p.Kind == PrincipalOrgUser {
			roles, err := tx.Q.ListOrgUserRoles(ctx, p.UserID)
			if err != nil {
				return err
			}
			p.Roles = roles
		}
		params := db.CreateSessionParams{OrgID: p.OrgID, TokenHash: tokenHash, ExpiresAt: ts(expires)}
		if p.Kind == PrincipalOrgUser {
			params.OrgUserID = uuid.NullUUID{UUID: p.UserID, Valid: true}
		} else {
			params.ClientUserID = uuid.NullUUID{UUID: p.UserID, Valid: true}
		}
		_, err := tx.Q.CreateSession(ctx, params)
		return err
	})
	if err != nil {
		return LoginResult{}, fmt.Errorf("create session: %w", err)
	}
	return LoginResult{Token: token, ExpiresAt: expires, Principal: p}, nil
}

// ResolveSession turns a session token into its principal and slides the
// expiry forward.
func (a *AuthService) ResolveSession(ctx context.Context, token string) (Principal, error) {
	if token == "" {
		return Principal{}, ErrNoSession
	}
	var sess db.Session
	err := a.st.WithLookupTx(ctx, store.Lookup{TokenHash: hashToken(token)}, func(ctx context.Context, tx *store.Tx) error {
		var err error
		sess, err = tx.Q.LookupSession(ctx, hashToken(token))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrNoSession
	}
	if err != nil {
		return Principal{}, fmt.Errorf("lookup session: %w", err)
	}
	p := Principal{OrgID: sess.OrgID}
	if sess.OrgUserID.Valid {
		p.Kind, p.UserID = PrincipalOrgUser, sess.OrgUserID.UUID
	} else {
		p.Kind, p.UserID = PrincipalClientUser, sess.ClientUserID.UUID
	}
	err = a.st.WithTx(ctx, orgScoped(sess.OrgID), func(ctx context.Context, tx *store.Tx) error {
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
		return tx.Q.TouchSession(ctx, db.TouchSessionParams{ID: sess.ID, ExpiresAt: ts(time.Now().Add(SessionTTL))})
	})
	if err != nil {
		return Principal{}, fmt.Errorf("load session principal: %w", err)
	}
	return p, nil
}

// Logout deletes the session; unknown tokens are not an error.
func (a *AuthService) Logout(ctx context.Context, token string) error {
	var sess db.Session
	err := a.st.WithLookupTx(ctx, store.Lookup{TokenHash: hashToken(token)}, func(ctx context.Context, tx *store.Tx) error {
		var err error
		sess, err = tx.Q.LookupSession(ctx, hashToken(token))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return a.st.WithTx(ctx, orgScoped(sess.OrgID), func(ctx context.Context, tx *store.Tx) error {
		return tx.Q.DeleteSessionByID(ctx, sess.ID)
	})
}

// RequestPasswordReset issues a reset token for the surface's user with that
// email. Returns "" without error when no such user exists so callers cannot
// enumerate accounts. The caller delivers the token by email.
func (a *AuthService) RequestPasswordReset(ctx context.Context, surface Surface, email string) (string, error) {
	var orgID, userID uuid.UUID
	err := a.st.WithLookupTx(ctx, store.Lookup{Email: email}, func(ctx context.Context, tx *store.Tx) error {
		switch surface {
		case SurfaceApp:
			rows, err := tx.Q.LookupOrgUsersByEmail(ctx, email)
			if err != nil || len(rows) == 0 {
				return err
			}
			orgID, userID = rows[0].OrgUser.OrgID, rows[0].OrgUser.ID
		case SurfaceClient:
			rows, err := tx.Q.LookupClientUsersByEmail(ctx, email)
			if err != nil || len(rows) == 0 {
				return err
			}
			orgID, userID = rows[0].ClientUser.OrgID, rows[0].ClientUser.ID
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("reset lookup: %w", err)
	}
	if userID == uuid.Nil {
		return "", nil
	}
	token, tokenHash, err := newToken()
	if err != nil {
		return "", err
	}
	params := db.CreatePasswordResetParams{OrgID: orgID, TokenHash: tokenHash, ExpiresAt: ts(time.Now().Add(PasswordResetTTL))}
	if surface == SurfaceApp {
		params.OrgUserID = uuid.NullUUID{UUID: userID, Valid: true}
	} else {
		params.ClientUserID = uuid.NullUUID{UUID: userID, Valid: true}
	}
	err = a.st.WithTx(ctx, orgScoped(orgID), func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.Q.CreatePasswordReset(ctx, params)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("create reset: %w", err)
	}
	return token, nil
}

// CheckPasswordReset reports whether the token can still be used.
func (a *AuthService) CheckPasswordReset(ctx context.Context, token string) error {
	_, err := a.lookupReset(ctx, token)
	return err
}

func (a *AuthService) lookupReset(ctx context.Context, token string) (db.PasswordReset, error) {
	var r db.PasswordReset
	err := a.st.WithLookupTx(ctx, store.Lookup{TokenHash: hashToken(token)}, func(ctx context.Context, tx *store.Tx) error {
		var err error
		r, err = tx.Q.LookupPasswordReset(ctx, hashToken(token))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrResetInvalid
	}
	if err != nil {
		return r, fmt.Errorf("lookup reset: %w", err)
	}
	if r.UsedAt.Valid || !r.ExpiresAt.Time.After(time.Now()) {
		return r, ErrResetInvalid
	}
	return r, nil
}

// ResetPassword consumes the token and stores the new password hash.
func (a *AuthService) ResetPassword(ctx context.Context, token, newPassword string) error {
	if len(newPassword) < minPasswordLen {
		return ErrPasswordTooShort
	}
	r, err := a.lookupReset(ctx, token)
	if err != nil {
		return err
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	return a.st.WithTx(ctx, orgScoped(r.OrgID), func(ctx context.Context, tx *store.Tx) error {
		n, err := tx.Q.MarkPasswordResetUsed(ctx, r.ID)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrResetInvalid
		}
		// A reset ends every live session and any other outstanding reset.
		if r.OrgUserID.Valid {
			if err := tx.Q.DeleteSessionsForOrgUser(ctx, r.OrgUserID); err != nil {
				return err
			}
			if err := tx.Q.MarkPasswordResetsUsedForOrgUser(ctx, r.OrgUserID); err != nil {
				return err
			}
			return tx.Q.UpsertOrgUserCredential(ctx, db.UpsertOrgUserCredentialParams{OrgUserID: r.OrgUserID.UUID, OrgID: r.OrgID, PasswordHash: hash})
		}
		if err := tx.Q.DeleteSessionsForClientUser(ctx, r.ClientUserID); err != nil {
			return err
		}
		if err := tx.Q.MarkPasswordResetsUsedForClientUser(ctx, r.ClientUserID); err != nil {
			return err
		}
		return tx.Q.UpsertClientUserCredential(ctx, db.UpsertClientUserCredentialParams{ClientUserID: r.ClientUserID.UUID, OrgID: r.OrgID, PasswordHash: hash})
	})
}

// orgScoped is the org-wide scope used to finish work on a credential that
// was resolved by token before any user principal existed.
func orgScoped(orgID uuid.UUID) Principal {
	return Principal{Kind: PrincipalSystem, OrgID: orgID}
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t.UTC(), Valid: true} }

// dummyHash equalises login timing when the email is unknown.
var dummyHash = func() string { h, _ := HashPassword("dummy"); return h }()
