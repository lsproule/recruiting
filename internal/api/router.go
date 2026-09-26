// Package api exposes the JSON API. Handlers here call internal/service only;
// they never reach internal/store directly.
package api

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Version is the OpenAPI document version reported for the API.
const Version = "1.0.0"

// Router carries the HTTP mux and the Huma API mounted on it so callers can
// register operations without re-deriving the mount point.
type Router struct {
	Mux *chi.Mux
	API huma.API
}

// NewRouter builds the chi mux with the health endpoint and mounts the Huma
// API under /api/v1, which serves OpenAPI 3.1 at /api/v1/openapi.json.
func NewRouter() *Router {
	mux := chi.NewMux()
	mux.Use(middleware.RequestID, middleware.Recoverer)

	mux.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	v1 := chi.NewMux()
	mux.Mount("/api/v1", v1)

	cfg := huma.DefaultConfig("Recruiting API", Version)
	cfg.Info.Description = apiDescription
	cfg.Tags = apiTags
	cfg.OpenAPIPath = "/openapi"
	cfg.DocsPath = "/docs"
	cfg.SchemasPath = "/schemas"
	// The API takes a per-user token as a bearer credential beside the
	// session cookie the browser islands send; only the token is a scheme a
	// client presents, so only it is described.
	if cfg.Components == nil {
		cfg.Components = &huma.Components{}
	}
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		BearerScheme: {Type: "http", Scheme: "bearer", Description: "A per-user API token issued by an org admin"},
	}

	return &Router{Mux: mux, API: humachi.New(v1, cfg)}
}

// apiDescription opens the served document; docs/api.md is the narrative
// guide it points at.
const apiDescription = "The platform's JSON API. Two surfaces share it: the **org surface**, everything a " +
	"recruiter or admin does in the console, reached with a token an admin issues for an org user; and the " +
	"**company surface** under `/portal`, everything a client company sees in its portal, reached with a " +
	"token a client user issues for themselves. A credential reaches its own surface and nothing else.\n\n" +
	"Send the token as `Authorization: Bearer <secret>`. Replies are JSON; refusals are RFC 9457 problem " +
	"details (`application/problem+json`) whose `status` and `detail` say what was wrong. Collection reads " +
	"page with `limit` and `offset`; the company change feed (`GET /portal/events`) pages with a `since` " +
	"cursor. The narrative guide, with worked examples for consuming candidates and searching the talent " +
	"network, is `docs/api.md` in the platform repository."

// apiTags describe the operation groups in the served document.
var apiTags = []*huma.Tag{
	{Name: "client-portal", Description: "The company surface: a client company's jobs, the applications released to it, its actions, its change feed, and its own tokens."},
	{Name: "talent-network", Description: "People who asked to be found. A company describes who it wants and reads anonymised matches; the recruiter makes the introductions."},
	{Name: "jobs", Description: "A client company's roles and the pipeline each moves candidates through."},
	{Name: "applications", Description: "A candidate on a job: where they are in the pipeline and what happened to them."},
	{Name: "candidates", Description: "People the org has in play, with their applications and résumés."},
	{Name: "processes", Description: "The org's hiring processes: the pipelines a job starts from."},
	{Name: "sprints", Description: "Screening sprints: short video conversations on a clock, every candidate meeting every interviewer."},
	{Name: "api-tokens", Description: "Bearer credentials an org admin issues for org users and client users."},
}
