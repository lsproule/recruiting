package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"recruiting/internal/api"
	"recruiting/internal/service"
)

func TestAttemptRoutesRefuseARequestWithoutTheAssessmentCookie(t *testing.T) {
	r := api.NewRouter()
	api.MountAttempts(r.API, api.AttemptsDeps{
		Attempts: service.NewAttemptService(nil, nil, ""),
		Resolve: api.ResolveWith(func(http.Handler) http.Handler {
			// Stands in for the auth cookie middleware refusing the request.
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "invalid link", http.StatusGone) })
		}),
	})
	srv := httptest.NewServer(r.Mux)
	defer srv.Close()

	id := "11111111-1111-1111-1111-111111111111"
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/attempts/" + id},
		{http.MethodPost, "/attempts/" + id + "/events"},
		{http.MethodGet, "/attempts/" + id + "/submissions/" + id},
	} {
		req, _ := http.NewRequest(tc.method, srv.URL+"/api/v1"+tc.path, strings.NewReader(`{"attempt_id":"`+id+`","events":[]}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", tc.method, tc.path, resp.StatusCode)
		}
	}
}
