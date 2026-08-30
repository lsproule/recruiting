package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"recruiting/internal/api"
	"recruiting/internal/blob"
	"recruiting/internal/config"
	"recruiting/internal/mail"
	"recruiting/internal/queue"
	"recruiting/internal/service"
	"recruiting/internal/store"
	"recruiting/internal/web/admin"
	applyweb "recruiting/internal/web/apply"
	"recruiting/internal/web/auth"
	candidatesweb "recruiting/internal/web/candidates"
	"recruiting/internal/web/jobs"
	"recruiting/internal/web/layout"
)

// defaultListenAddr is where serve binds when LISTEN_ADDR is unset. The
// address is serve's alone, so it stays out of the shared configuration every
// mode has to satisfy.
const defaultListenAddr = ":8080"

// shutdownGrace bounds how long in-flight requests have once a signal arrives.
const shutdownGrace = 20 * time.Second

// readHeaderTimeout caps how long a client may take to send its headers.
const readHeaderTimeout = 10 * time.Second

func listenAddr() string {
	if v := strings.TrimSpace(os.Getenv("LISTEN_ADDR")); v != "" {
		return v
	}
	return defaultListenAddr
}

// serve opens the tenant store, builds the HTTP surface, and serves it on addr
// until ctx is cancelled. ready, when set, receives the bound address, which
// matters when addr asks the kernel for a port.
func serve(ctx context.Context, logger *slog.Logger, cfg *config.Config, addr string, ready func(net.Addr)) error {
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	defer st.Close()

	handler, err := appHandler(ctx, logger, cfg, st)
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("serve: listen on %s: %w", addr, err)
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: readHeaderTimeout}

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown", "error", err)
		}
	}()

	logger.Info("listening", "addr", ln.Addr().String(), "base_url", cfg.BaseURL)
	if ready != nil {
		ready(ln.Addr())
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	<-stopped
	return nil
}

// appHandler is the composition root of the serve mode: every surface the
// binary exposes is mounted here.
//
// The JSON API and the health check sit on the outer mux so the HTML
// surface's CSRF and session middleware never touch them. Everything under
// the HTML surface goes on a second mux mounted at the root, because
// auth.Mount installs that middleware and chi only accepts middleware before
// the first route. Add new HTML routes after auth.Mount on `web`; add API
// operations to r.API.
func appHandler(ctx context.Context, logger *slog.Logger, cfg *config.Config, st *store.Store) (http.Handler, error) {
	blobs, err := objectStore(ctx, logger, cfg)
	if err != nil {
		return nil, err
	}
	resumes := service.NewResumeService(st, blobs)
	resumes.Logger = logger
	candidates := service.NewCandidateService(st, resumes)

	q, err := queue.New(st.Pool(), queue.Config{Logger: logger})
	if err != nil {
		return nil, err
	}

	r := api.NewRouter()
	layout.MountStatic(r.Mux)

	web := chi.NewMux()
	// The body cap goes on before auth.Mount: CSRF reads the multipart form
	// the upload surfaces post, and would otherwise parse an unbounded body.
	web.Use(applyweb.MaxBody(applyweb.UploadBodyLimit))
	if _, err := auth.Mount(web, auth.Deps{
		Auth:              service.NewAuthService(st),
		Links:             service.NewMagicLinkService(st),
		BaseURL:           cfg.BaseURL,
		CookieSecret:      []byte(cfg.SessionSecret),
		SendPasswordReset: sendPasswordReset(st, q, cfg.BaseURL),
	}); err != nil {
		return nil, err
	}
	org := service.NewOrgService(st)
	jobService := service.NewJobService(st)
	admin.Mount(web, admin.Deps{Org: org, BaseURL: cfg.BaseURL})
	jobs.Mount(web, jobs.Deps{Jobs: jobService, Org: org, Logger: logger})
	applyweb.Mount(web, applyweb.Deps{Candidates: candidates, Logger: logger})
	candidatesweb.Mount(web, candidatesweb.Deps{Candidates: candidates, Jobs: jobService, Org: org, Logger: logger})
	r.Mux.Mount("/", web)
	return r.Mux, nil
}

// objectStore builds the resume/recording bucket. Configuration is required
// of a deployment, but an embedding that leaves it out — a test of the HTML
// surface, say — still gets a server; the upload paths then refuse with
// service.ErrNoBlobStore rather than the process failing to start.
func objectStore(ctx context.Context, logger *slog.Logger, cfg *config.Config) (service.BlobStore, error) {
	client, err := blob.New(blob.Config{
		Endpoint: cfg.BlobEndpoint,
		Bucket:   cfg.BlobBucket,
		Key:      cfg.BlobKey,
		Secret:   cfg.BlobSecret,
	})
	if errors.Is(err, blob.ErrNotConfigured) {
		logger.Warn("object storage is not configured; resume upload and download are disabled")
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := client.EnsureBucket(ctx); err != nil {
		return nil, err
	}
	return client, nil
}

// resetToken reads the token out of a reset link, which is its last path
// segment. Parsing rather than slicing keeps a query string or a trailing
// slash from being taken for part of the token.
func resetToken(link string) (string, error) {
	u, err := url.Parse(link)
	if err != nil {
		return "", fmt.Errorf("password reset link: %w", err)
	}
	token := path.Base(strings.TrimRight(u.Path, "/"))
	if token == "" || token == "." || token == "/" {
		return "", fmt.Errorf("password reset link %q carries no token", link)
	}
	return token, nil
}

// sendPasswordReset queues the reset email. The reset surface knows only the
// address and the link, and email_log is a tenant table, so the org comes
// from the reset row the link's token addresses — the one lookup that works
// before any principal exists.
//
// The link goes into the job payload, which means a live reset URL sits in
// the queue until the job is worked and then cleaned up; queue.CompletedRetention
// is short for exactly this reason.
func sendPasswordReset(st *store.Store, q *queue.Client, baseURL string) func(context.Context, string, string) error {
	return func(ctx context.Context, email, link string) error {
		token, err := resetToken(link)
		if err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(token))
		var orgID uuid.UUID
		if err := st.WithLookupTx(ctx, store.Lookup{TokenHash: hex.EncodeToString(sum[:])}, func(ctx context.Context, tx *store.Tx) error {
			reset, err := tx.Q.LookupPasswordReset(ctx, hex.EncodeToString(sum[:]))
			if err != nil {
				return fmt.Errorf("password reset lookup: %w", err)
			}
			orgID = reset.OrgID
			return nil
		}); err != nil {
			return err
		}
		p := service.Principal{Kind: service.PrincipalSystem, OrgID: orgID}
		return st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
			return q.Enqueue(ctx, tx, queue.KindEmailSend, queue.EmailPayload{
				Template: mail.TemplatePasswordReset,
				To:       email,
				OrgID:    orgID,
				Data:     map[string]any{"ResetURL": link, "BaseURL": baseURL},
			})
		})
	}
}
