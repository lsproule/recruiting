package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"recruiting/internal/runner/server"
)

func TestExecutePostsTheRequestWithTheSharedSecret(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody server.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(server.Response{ID: gotBody.ID, Status: server.StatusOK})
	}))
	defer srv.Close()

	res, err := New(srv.URL+"/", "s3cret").Execute(context.Background(), server.Request{ID: "abc", Language: "python", Source: "print(1)"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if gotAuth != "Bearer s3cret" || gotPath != "/execute" {
		t.Errorf("request = %s %s, want a bearer token on /execute", gotAuth, gotPath)
	}
	if gotBody.Source != "print(1)" || res.Status != server.StatusOK {
		t.Errorf("body = %+v, response = %+v", gotBody, res)
	}
}

// A saturated runner answers 503 with Retry-After instead of queueing, so the
// caller waits it out rather than failing the request.
func TestExecuteWaitsOutASaturatedRunner(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "runner saturated; retry later", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(server.Response{ID: "abc", Status: server.StatusOK})
	}))
	defer srv.Close()

	res, err := New(srv.URL, "s").Execute(context.Background(), server.Request{ID: "abc"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != server.StatusOK {
		t.Errorf("status = %q, want ok", res.Status)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("runner called %d times, want 2 refusals then the answer", got)
	}
}

func TestExecuteGivesUpOnAPermanentlySaturatedRunner(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "0")
		http.Error(w, "runner saturated; retry later", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := New(srv.URL, "s").Execute(context.Background(), server.Request{ID: "abc"})
	if err == nil || !strings.Contains(err.Error(), "saturated") {
		t.Fatalf("err = %v, want the saturation reported", err)
	}
	if got := calls.Load(); got != retryLimit {
		t.Errorf("runner called %d times, want the %d-attempt limit", got, retryLimit)
	}
}

func TestRetryAfterIsReadInBothForms(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   time.Duration
	}{
		{"", retryDelay},
		{"2", 2 * time.Second},
		{"0", time.Millisecond},
		{"nonsense", retryDelay},
		{time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat), time.Millisecond},
	} {
		if got := retryAfter(tc.header); got != tc.want {
			t.Errorf("retryAfter(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

// The runner is idempotent by id, so an answer carrying another one is not
// this request's result and must not be passed off as it.
func TestExecuteRefusesAnAnswerForAnotherRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(server.Response{ID: "someone-else", Status: server.StatusOK})
	}))
	defer srv.Close()

	_, err := New(srv.URL, "s").Execute(context.Background(), server.Request{ID: "abc"})
	if err == nil || !strings.Contains(err.Error(), "someone-else") {
		t.Fatalf("err = %v, want the mismatched id reported", err)
	}
}
