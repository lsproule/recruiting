//go:build integration

package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/service"
	"recruiting/internal/store"
)

func TestAPITokenIssueResolveRevoke(t *testing.T) {
	f := newFixture(t)
	svc := service.NewAPITokenService(f.st)

	tok, raw, err := svc.Issue(f.ctx, f.admin(), service.NewAPIToken{Name: "ci", UserID: f.userID})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if raw == "" || !strings.HasPrefix(raw, tok.Prefix) {
		t.Fatalf("token %q does not start with its shown prefix %q", raw, tok.Prefix)
	}
	if tok.Prefix == raw {
		t.Fatal("the whole secret was kept as the prefix")
	}

	// The secret is not stored: only its hash, and the row never carries it.
	var stored string
	err = f.st.WithTx(f.ctx, f.admin(), func(ctx context.Context, tx *store.Tx) error {
		return tx.QueryRow(ctx, `select token_hash from api_token where id = $1`, tok.ID).Scan(&stored)
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored == raw || stored == "" {
		t.Fatalf("token hash %q is not a hash of the secret", stored)
	}

	p, err := svc.ResolveToken(f.ctx, raw)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p.Kind != service.PrincipalOrgUser || p.OrgID != f.orgID || p.UserID != f.userID || !p.HasRole("admin") {
		t.Fatalf("resolved principal = %+v", p)
	}

	list, err := svc.List(f.ctx, f.admin())
	if err != nil || len(list) != 1 || list[0].ID != tok.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if list[0].UserEmail != f.email {
		t.Fatalf("listed user_email = %q, want %q", list[0].UserEmail, f.email)
	}

	if err := svc.Revoke(f.ctx, f.admin(), tok.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := svc.ResolveToken(f.ctx, raw); !errors.Is(err, service.ErrNoSession) {
		t.Fatalf("revoked token resolved: %v", err)
	}
}

func TestAPITokenExpiryIsOptionalAndEnforced(t *testing.T) {
	f := newFixture(t)
	svc := service.NewAPITokenService(f.st)

	past := time.Now().Add(-time.Minute)
	if _, _, err := svc.Issue(f.ctx, f.admin(), service.NewAPIToken{Name: "expired", UserID: f.userID, ExpiresAt: &past}); !errors.Is(err, service.ErrTokenExpired) {
		t.Fatalf("issued a token that had already expired: %v", err)
	}

	future := time.Now().Add(time.Hour)
	tok, raw, err := svc.Issue(f.ctx, f.admin(), service.NewAPIToken{Name: "live", UserID: f.userID, ExpiresAt: &future})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := svc.ResolveToken(f.ctx, raw); err != nil {
		t.Fatalf("unexpired token: %v", err)
	}

	// Walk the expiry back; a token past it neither resolves nor lists.
	err = f.st.WithTx(f.ctx, f.admin(), func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.Exec(ctx, `update api_token set expires_at = $1 where id = $2`, past, tok.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveToken(f.ctx, raw); !errors.Is(err, service.ErrNoSession) {
		t.Fatalf("expired token resolved: %v", err)
	}
	list, err := svc.List(f.ctx, f.admin())
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range list {
		if l.ID == tok.ID {
			t.Fatal("an expired token is still listed")
		}
	}
}

func TestAPITokenIsAdminOnly(t *testing.T) {
	f := newFixture(t)
	svc := service.NewAPITokenService(f.st)
	recruiter := service.Principal{Kind: service.PrincipalOrgUser, OrgID: f.orgID, UserID: f.userID, Roles: []string{"recruiter"}}

	if _, _, err := svc.Issue(f.ctx, recruiter, service.NewAPIToken{Name: "x", UserID: f.userID}); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("recruiter issued a token: %v", err)
	}
	if _, err := svc.List(f.ctx, recruiter); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("recruiter listed tokens: %v", err)
	}
	if err := svc.Revoke(f.ctx, recruiter, uuid.New()); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("recruiter revoked a token: %v", err)
	}
}

func TestAPITokenRefusesAnUnknownUser(t *testing.T) {
	f := newFixture(t)
	svc := service.NewAPITokenService(f.st)
	if _, _, err := svc.Issue(f.ctx, f.admin(), service.NewAPIToken{Name: "x", UserID: uuid.New()}); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("issued a token for a user outside the org: %v", err)
	}
}
