// Package client talks to the sandboxed runner over the wire protocol: POST
// /execute with the shared secret as a bearer token. It is the only way the
// app reaches the runner, and it carries nothing but the code, its tests, and
// the limits — never org context or a candidate's identity.
//
// service.HTTPExecutor still duplicates this; retire it in favour of this
// package.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"recruiting/internal/runner/server"
)

const (
	// timeout bounds one call. The runner caps its own work well below this,
	// and holds a request in its wait queue for at most server.MaxQueueWait
	// before answering; the timeout is against a runner that stops
	// answering altogether.
	timeout = 5 * time.Minute
	// The runner queues requests itself and answers 503 with Retry-After only
	// once its queue is full. Short waits are absorbed here, so a momentary
	// burst costs the job nothing; past retryBudget of waiting, or when the
	// runner asks for longer than what is left of it, the call gives up
	// with ErrSaturated so the job can be snoozed instead of holding a
	// worker slot open.
	retryLimit  = 20
	retryBudget = 2 * time.Minute
	// retryDelay is the wait when a 503 carries no usable Retry-After.
	retryDelay = 5 * time.Second
)

// ErrSaturated reports a runner that stayed too busy for too long. The
// error returned wraps it as a *SaturatedError carrying the Retry-After
// the runner last asked for, so errors.Is(err, ErrSaturated) recognises
// it and errors.As reads the interval.
var ErrSaturated = errors.New("runner: saturated")

// SaturatedError is the error ErrSaturated travels in.
type SaturatedError struct {
	// RetryAfter is when the runner asked to be tried again.
	RetryAfter time.Duration
	Attempts   int
	Waited     time.Duration
}

func (e *SaturatedError) Error() string {
	return fmt.Sprintf("%v: still busy after %d attempts and %s; retry after %s", ErrSaturated, e.Attempts, e.Waited.Round(time.Millisecond), e.RetryAfter)
}

// Is makes errors.Is(err, ErrSaturated) true.
func (e *SaturatedError) Is(target error) bool { return target == ErrSaturated }

// Unwrap lets errors.Is reach ErrSaturated.
func (e *SaturatedError) Unwrap() error { return ErrSaturated }

// Client is one runner endpoint.
type Client struct {
	baseURL string
	secret  string
	http    *http.Client
}

// New builds a client for the runner at url.
func New(url, secret string) *Client {
	return &Client{
		baseURL: strings.TrimRight(url, "/"),
		secret:  secret,
		http:    &http.Client{Timeout: timeout},
	}
}

// Execute posts one request, waiting out a saturated runner: a 503 is
// retried after the interval it asks for, within a fixed attempt and time
// budget, beyond which the call fails with ErrSaturated. The request is
// idempotent by id, so a retry is free.
func (c *Client) Execute(ctx context.Context, req server.Request) (server.Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return server.Response{}, fmt.Errorf("runner request: %w", err)
	}
	start := time.Now()
	deadline := start.Add(retryBudget)
	for attempt := 1; ; attempt++ {
		out, wait, err := c.attempt(ctx, req.ID, body)
		if wait <= 0 {
			return out, err
		}
		if left := time.Until(deadline); attempt >= retryLimit || left <= 0 || wait > left {
			return server.Response{}, &SaturatedError{RetryAfter: wait, Attempts: attempt, Waited: time.Since(start)}
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
func (c *Client) attempt(ctx context.Context, id string, body []byte) (server.Response, time.Duration, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/execute", bytes.NewReader(body))
	if err != nil {
		return server.Response{}, 0, fmt.Errorf("runner request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.secret)
	res, err := c.http.Do(httpReq)
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
	if out.ID != id {
		// The runner is idempotent by id; an answer under another one belongs
		// to a different execution and must not be stored as this one's.
		return server.Response{}, 0, fmt.Errorf("runner: answered for %q, asked about %q", out.ID, id)
	}
	return out, 0, nil
}

// retryAfter reads the header in either form the RFC allows. A zero or absent
// value still yields a tiny wait, so the retry loop always makes progress and
// never spins.
func retryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return retryDelay
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
	return retryDelay
}
