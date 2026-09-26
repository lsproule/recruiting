package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"recruiting/internal/service"
)

// APIToken is one issued bearer credential. The secret is not part of it: it
// exists only in the answer to the create, and only that once.
type APIToken struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix" doc:"Leading characters of the secret, to tell tokens apart"`
	UserID    uuid.UUID  `json:"user_id"`
	UserEmail string     `json:"user_email,omitempty"`
	IsClient  bool       `json:"is_client" doc:"A client user's token, which reaches only the company surface under /portal"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty" doc:"Absent when the token does not expire"`
}

func apiTokenView(t service.APIToken) APIToken {
	return APIToken{
		ID: t.ID, Name: t.Name, Prefix: t.Prefix, UserID: t.UserID,
		UserEmail: t.UserEmail, IsClient: t.IsClient,
		CreatedAt: t.CreatedAt, ExpiresAt: optionalTime(t.ExpiresAt),
	}
}

type tokensHandlers struct{ d Deps }

func (m *mounter) mountAPITokens() {
	h := tokensHandlers{d: m.d}
	register(m, accessAdmin, huma.Operation{
		OperationID: "list-api-tokens", Method: http.MethodGet, Path: "/api-tokens",
		Summary: "The org's live API tokens", Tags: []string{"api-tokens"},
	}, h.list)
	register(m, accessAdmin, huma.Operation{
		OperationID: "create-api-token", Method: http.MethodPost, Path: "/api-tokens",
		Summary: "Issue an API token for one of the org's users; the secret is shown once",
		Tags:    []string{"api-tokens"}, DefaultStatus: http.StatusCreated,
	}, h.create)
	register(m, accessAdmin, huma.Operation{
		OperationID: "revoke-api-token", Method: http.MethodDelete, Path: "/api-tokens/{token_id}",
		Summary: "Revoke an API token", Tags: []string{"api-tokens"}, DefaultStatus: http.StatusNoContent,
	}, h.revoke)
}

type apiTokensOutput struct {
	Body struct {
		Tokens []APIToken `json:"tokens"`
	}
}

type createAPITokenInput struct {
	Body struct {
		Name       string     `json:"name" minLength:"1"`
		UserID     uuid.UUID  `json:"user_id"`
		ClientUser bool       `json:"client_user,omitempty" doc:"The user id names a client user"`
		ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	}
}

type createAPITokenOutput struct {
	Body struct {
		Token  APIToken `json:"token"`
		Secret string   `json:"secret" doc:"Shown once; it cannot be read back"`
	}
}

type apiTokenInput struct {
	TokenID uuid.UUID `path:"token_id"`
}

func (h tokensHandlers) list(ctx context.Context, _ *struct{}) (*apiTokensOutput, error) {
	ts, err := h.d.APITokens.List(ctx, principal(ctx))
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &apiTokensOutput{}
	out.Body.Tokens = make([]APIToken, 0, len(ts))
	for _, t := range ts {
		out.Body.Tokens = append(out.Body.Tokens, apiTokenView(t))
	}
	return out, nil
}

func (h tokensHandlers) create(ctx context.Context, in *createAPITokenInput) (*createAPITokenOutput, error) {
	tok, secret, err := h.d.APITokens.Issue(ctx, principal(ctx), service.NewAPIToken{
		Name: in.Body.Name, UserID: in.Body.UserID,
		ClientUser: in.Body.ClientUser, ExpiresAt: in.Body.ExpiresAt,
	})
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &createAPITokenOutput{}
	out.Body.Token, out.Body.Secret = apiTokenView(tok), secret
	return out, nil
}

func (h tokensHandlers) revoke(ctx context.Context, in *apiTokenInput) (*struct{}, error) {
	if err := h.d.APITokens.Revoke(ctx, principal(ctx), in.TokenID); err != nil {
		return nil, problemDetail(err)
	}
	return nil, nil
}
