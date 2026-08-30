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
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"recruiting/internal/runner/server"
)

const (
	// timeout bounds one call. The runner caps its own work well below this;
	// the timeout is against a runner that stops answering.
	timeout = 5 * time.Minute
	// A saturated runner answers 503 with Retry-After rather than queueing, so
	// the caller is the queue: it waits out the busy period, up to these
	// bounds, before giving up on the request.
	retryLimit  = 6
	retryBudget = 60 * time.Second
	// retryDelay is the wait when a 503 carries no usable Retry-After.
	retryDelay = 5 * time.Second
)

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
// budget. The request is idempotent by id, so a retry is free.
func (c *Client) Execute(ctx context.Context, req server.Request) (server.Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return server.Response{}, fmt.Errorf("runner request: %w", err)
	}
	deadline := time.Now().Add(retryBudget)
	for attempt := 1; ; attempt++ {
		out, wait, err := c.attempt(ctx, req.ID, body)
		if wait <= 0 {
			return out, err
		}
		if attempt >= retryLimit {
			return server.Response{}, fmt.Errorf("runner: still saturated after %d attempts", attempt)
		}
		if left := time.Until(deadline); left <= 0 || wait > left {
			return server.Response{}, fmt.Errorf("runner: still saturated after %s", retryBudget)
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
