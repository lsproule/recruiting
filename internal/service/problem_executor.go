package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"recruiting/internal/runner/server"
)

// Executor runs one execution request against the sandboxed runner. The
// import path needs the runner synchronously — a reference solution is proof
// the problem is solvable — so it calls this rather than the queue.
type Executor interface {
	Execute(ctx context.Context, req server.Request) (server.Response, error)
}

// HTTPExecutor talks to a runner over the wire protocol: POST /execute with
// the shared secret as a bearer token.
type HTTPExecutor struct {
	baseURL string
	secret  string
	client  *http.Client
}

const (
	// executorTimeout bounds one call. The runner caps its own work well below
	// this; the timeout is against a runner that stops answering.
	executorTimeout = 5 * time.Minute
	// A saturated runner answers 503 with Retry-After rather than queueing, so
	// the caller is the queue: it waits out the busy period, up to these
	// bounds, before giving up on the request.
	executorRetryLimit  = 6
	executorRetryBudget = 60 * time.Second
	// executorRetryDelay is the wait when a 503 carries no usable Retry-After.
	executorRetryDelay = 5 * time.Second
)

// NewHTTPExecutor builds an executor for the runner at url.
func NewHTTPExecutor(url, secret string) *HTTPExecutor {
	return &HTTPExecutor{
		baseURL: strings.TrimRight(url, "/"),
		secret:  secret,
		client:  &http.Client{Timeout: executorTimeout},
	}
}

// Execute posts one request, waiting out a saturated runner: a 503 is
// retried after the interval it asks for, within a fixed attempt and time
// budget. The request is idempotent by id, so a retry is free.
func (e *HTTPExecutor) Execute(ctx context.Context, req server.Request) (server.Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return server.Response{}, fmt.Errorf("runner request: %w", err)
	}
	deadline := time.Now().Add(executorRetryBudget)
	for attempt := 1; ; attempt++ {
		out, wait, err := e.attempt(ctx, body)
		if wait <= 0 {
			return out, err
		}
		if attempt >= executorRetryLimit {
			return server.Response{}, fmt.Errorf("runner: still saturated after %d attempts", attempt)
		}
		if left := time.Until(deadline); left <= 0 || wait > left {
			return server.Response{}, fmt.Errorf("runner: still saturated after %s", executorRetryBudget)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return server.Response{}, ctx.Err()
		case <-timer.C:
		}
	}
}

// attempt makes one call. A non-zero wait means the runner is saturated and
// asks to be tried again after that interval.
func (e *HTTPExecutor) attempt(ctx context.Context, body []byte) (server.Response, time.Duration, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/execute", bytes.NewReader(body))
	if err != nil {
		return server.Response{}, 0, fmt.Errorf("runner request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+e.secret)
	res, err := e.client.Do(httpReq)
	if err != nil {
		return server.Response{}, 0, fmt.Errorf("runner: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusServiceUnavailable {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 2048))
		return server.Response{}, retryAfter(res.Header.Get("Retry-After")), nil
	}
	if res.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return server.Response{}, 0, fmt.Errorf("runner: %s: %s", res.Status, strings.TrimSpace(string(detail)))
	}
	var out server.Response
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return server.Response{}, 0, fmt.Errorf("runner response: %w", err)
	}
	return out, 0, nil
}

// retryAfter reads the header in either form the RFC allows. A zero or absent
// value still yields a tiny wait, so the retry loop always makes progress and
// never spins.
func retryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return executorRetryDelay
	}
	if secs, err := strconv.Atoi(header); err == nil {
		if secs <= 0 {
			return time.Millisecond
		}
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(header); err == nil {
		if wait := time.Until(at); wait > 0 {
			return wait
		}
		return time.Millisecond
	}
	return executorRetryDelay
}

var _ Executor = (*HTTPExecutor)(nil)
