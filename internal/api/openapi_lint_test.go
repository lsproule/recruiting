//go:build openapilint

// Guarded by a build tag so the OpenAPI contract check is a separate gate from
// the unit-test run.

package api_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi-validator/schema_validation"

	"recruiting/internal/api"
)

// TestOpenAPIDocumentIsValid fetches the served document and validates it
// against the OpenAPI specification schema.
func TestOpenAPIDocumentIsValid(t *testing.T) {
	r := api.NewRouter()
	api.MountAll(r, api.Deps{})
	srv := httptest.NewServer(r.Mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/openapi.json")
	if err != nil {
		t.Fatalf("get openapi.json: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	doc, err := libopenapi.NewDocument(raw)
	if err != nil {
		t.Fatalf("parse document: %v", err)
	}

	valid, validationErrors := schema_validation.ValidateOpenAPIDocument(doc)
	if !valid {
		for _, ve := range validationErrors {
			t.Errorf("%s: %s", ve.Message, ve.Reason)
		}
		t.Fatal("OpenAPI document is invalid")
	}

	if _, err := doc.BuildV3Model(); err != nil {
		t.Fatalf("OpenAPI document could not be resolved: %v", err)
	}
}
