package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"recruiting/internal/api"
)

func TestHealthEndpoint(t *testing.T) {
	srv := httptest.NewServer(api.NewRouter().Mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("get /healthz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestOpenAPIDocumentIsServed(t *testing.T) {
	srv := httptest.NewServer(api.NewRouter().Mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/openapi.json")
	if err != nil {
		t.Fatalf("get openapi.json: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}
