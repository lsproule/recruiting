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
