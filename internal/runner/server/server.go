// Package server is the HTTP face of the sandboxed runner: it authenticates
// callers with a shared secret, dispatches executions, and caches results by
// request id so retries are idempotent. It holds no application credentials.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// MaxTests bounds one request; larger suites must be split by the caller.
	MaxTests = 100
	// MaxBudget caps the host-side wall clock of one execution however many
	// tests it carries.
	MaxBudget = 10 * time.Minute

	cacheMax    = 10000
	cacheTTL    = time.Hour
	cacheErrTTL = time.Minute
	retryAfter  = "5"
)

// Executor runs one request to completion. Implementations never panic on
// bad input; they report failures through Response.Status.
type Executor interface {
	Execute(ctx context.Context, req *Request) *Response
}

type Config struct {
	Addr   string
	Secret string
	// Runtime is the OCI runtime asked of Docker (default runsc).
	Runtime string
	// AllowInsecureRuntime permits falling back to runc when Runtime is absent.
	AllowInsecureRuntime bool
	ImagePrefix          string
	// SQLAdminURL is a Postgres URL with CREATEDB/CREATEROLE used to provision
	// a throwaway database per SQL execution. Empty disables SQL.
	SQLAdminURL   string
	MaxConcurrent int
}

// ConfigFromEnv reads RUNNER_* variables; secret is passed in by the caller
// because config.Load already requires RUNNER_SECRET.
func ConfigFromEnv(secret string) Config {
	env := func(k, def string) string {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
		return def
	}
	n, _ := strconv.Atoi(env("RUNNER_MAX_CONCURRENT", "2"))
	return Config{
		Addr:                 env("RUNNER_LISTEN", ":8081"),
		Secret:               secret,
		Runtime:              env("RUNNER_RUNTIME", "runsc"),
		AllowInsecureRuntime: env("RUNNER_ALLOW_INSECURE_RUNTIME", "") == "1",
		ImagePrefix:          env("RUNNER_IMAGE_PREFIX", "recruiting-runner-"),
		SQLAdminURL:          env("RUNNER_SQL_URL", ""),
		MaxConcurrent:        n,
	}
}

// Run serves until ctx is cancelled. It refuses to start when the configured
// runtime is missing unless the insecure fallback is explicitly allowed.
func Run(ctx context.Context, logger *slog.Logger, cfg Config) error {
	if cfg.Secret == "" {
		return errors.New("runner: secret is required")
	}
	available, err := availableRuntimes(ctx)
	if err != nil {
		return fmt.Errorf("runner: docker: %w", err)
	}
	runtime, warned, err := resolveRuntime(cfg.Runtime, cfg.AllowInsecureRuntime, available)
	if err != nil {
		return err
	}
	if warned {
		logger.Warn("INSECURE: requested runtime unavailable; candidate code runs under runc without gVisor isolation",
			"requested", cfg.Runtime, "using", runtime)
	}
	exec := &DockerExecutor{Runtime: runtime, ImagePrefix: cfg.ImagePrefix, Logger: logger}
	var sqlExec Executor
	if cfg.SQLAdminURL != "" {
		sqlExec = &SQLExecutor{AdminURL: cfg.SQLAdminURL}
	}
	h := NewHandler(cfg.Secret, exec, sqlExec)
	if cfg.MaxConcurrent > 0 {
		h.sem = make(chan struct{}, cfg.MaxConcurrent)
	}
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				h.cache.sweep()
			}
		}
	}()
	srv := &http.Server{Addr: cfg.Addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	logger.Info("runner listening", "addr", ln.Addr().String(), "runtime", runtime, "sql", sqlExec != nil)
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errc:
		return err
	}
}

// resolveRuntime picks the container runtime: the requested one when Docker
// has it; runc only when explicitly allowed (warn=true); otherwise an error.
func resolveRuntime(requested string, allowInsecure bool, available []string) (runtime string, warn bool, err error) {
	for _, r := range available {
		if r == requested {
			return requested, false, nil
		}
	}
	if allowInsecure {
		return "runc", true, nil
	}
	return "", false, fmt.Errorf("runner: docker runtime %q not installed (have %s); install gVisor or set RUNNER_ALLOW_INSECURE_RUNTIME=1 to run under runc", requested, strings.Join(available, ","))
}

// Handler serves /execute and /healthz.
type Handler struct {
	secret string
	code   Executor
	sql    Executor
	// sem bounds concurrent executions; nil means unbounded.
	sem chan struct{}

	mu       sync.Mutex
	inflight map[string]*entry
	cache    *resultCache
}

type entry struct {
	done chan struct{}
	resp *Response
}

func NewHandler(secret string, code, sql Executor) *Handler {
	return &Handler{secret: secret, code: code, sql: sql, inflight: map[string]*entry{}, cache: newResultCache(cacheMax, cacheTTL, cacheErrTTL)}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz" && r.Method == http.MethodGet:
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	case r.URL.Path == "/execute" && r.Method == http.MethodPost:
		h.execute(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) authorized(r *http.Request) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && subtle.ConstantTimeCompare([]byte(got), []byte(h.secret)) == 1
}

func (h *Handler) execute(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := validate(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp, code := h.run(r.Context(), &req)
	if code == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", retryAfter)
		http.Error(w, "runner saturated; retry later", code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func validate(req *Request) error {
	switch {
	case req.ID == "":
		return errors.New("id is required")
	case !Languages[req.Language]:
		return fmt.Errorf("unsupported language %q", req.Language)
	case req.Source == "":
		return errors.New("source is required")
	case len(req.Tests) == 0:
		return errors.New("at least one test is required")
	case len(req.Tests) > MaxTests:
		return fmt.Errorf("too many tests: %d > %d", len(req.Tests), MaxTests)
	}
	for i := range req.Tests {
		if req.Tests[i].ID == "" {
			return fmt.Errorf("tests[%d].id is required", i)
		}
	}
	req.Limits = req.Limits.normalized()
	return nil
}

// run executes once per id: a cached or in-flight result is shared with every
// caller; a new execution needs a free slot (else 503) and a still-connected
// client, so abandoned requests never reach a container.
func (h *Handler) run(ctx context.Context, req *Request) (*Response, int) {
	if resp, ok := h.cache.get(req.ID); ok {
		return resp, 0
	}
	h.mu.Lock()
	e, seen := h.inflight[req.ID]
	if !seen {
		if resp, ok := h.cache.get(req.ID); ok {
			h.mu.Unlock()
			return resp, 0
		}
		if ctx.Err() != nil {
			h.mu.Unlock()
			return &Response{ID: req.ID, Status: StatusError, CompileOutput: "client disconnected"}, 0
		}
		if h.sem != nil {
			select {
			case h.sem <- struct{}{}:
			default:
				h.mu.Unlock()
				return nil, http.StatusServiceUnavailable
			}
		}
		e = &entry{done: make(chan struct{})}
		h.inflight[req.ID] = e
	}
	h.mu.Unlock()
	if seen {
		select {
		case <-e.done:
			return e.resp, 0
		case <-ctx.Done():
			return &Response{ID: req.ID, Status: StatusError, CompileOutput: "client disconnected"}, 0
		}
	}
	// Detached from the request context: once a slot is taken the run
	// completes and is cached so a retry of the same id gets its result.
	go func() {
		defer close(e.done)
		defer func() {
			if h.sem != nil {
				<-h.sem
			}
		}()
		e.resp = h.dispatch(context.Background(), req)
		h.mu.Lock()
		h.cache.put(req.ID, e.resp)
		delete(h.inflight, req.ID)
		h.mu.Unlock()
	}()
	select {
	case <-e.done:
		return e.resp, 0
	case <-ctx.Done():
		return &Response{ID: req.ID, Status: StatusError, CompileOutput: "client disconnected"}, 0
	}
}
func (h *Handler) dispatch(ctx context.Context, req *Request) (resp *Response) {
	defer func() {
		if r := recover(); r != nil {
			resp = &Response{ID: req.ID, Status: StatusError, CompileOutput: fmt.Sprint("runner panic: ", r)}
		}
	}()
	exec := h.code
	if req.Language == "sql" {
		exec = h.sql
	}
	if exec == nil {
		return &Response{ID: req.ID, Status: StatusError, CompileOutput: "language not enabled on this runner"}
	}
	resp = exec.Execute(ctx, req)
	if resp == nil {
		return &Response{ID: req.ID, Status: StatusError}
	}
	resp.ID = req.ID
	if resp.Results == nil {
		resp.Results = []TestResult{}
	}
	return resp
}
