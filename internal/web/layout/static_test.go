package layout_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"recruiting/internal/web/layout"
	"recruiting/web/static"
)

func staticServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := chi.NewMux()
	layout.MountStatic(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestStaticServesKnownAssetsOnly(t *testing.T) {
	srv := staticServer(t)
	for _, path := range []string{layout.HTMXPath, layout.AlpinePath} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("GET %s: %d, want 200", path, res.StatusCode)
			continue
		}
		if got := res.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("GET %s: X-Content-Type-Options = %q", path, got)
		}
		if got := res.Header.Get("Cache-Control"); !strings.Contains(got, "max-age=3600") {
			t.Errorf("GET %s: Cache-Control = %q", path, got)
		}
		etag := res.Header.Get("ETag")
		if !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) || len(etag) < 4 {
			t.Errorf("GET %s: ETag = %q, want a quoted validator", path, etag)
		}
		// A matching validator revalidates instead of resending the bundle.
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Header.Set("If-None-Match", etag)
		res2, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res2.Body.Close()
		if res2.StatusCode != http.StatusNotModified {
			t.Errorf("GET %s with If-None-Match: %d, want 304", path, res2.StatusCode)
		}
	}
}

func TestStaticHasNoDirectoryListingOrTraversal(t *testing.T) {
	srv := staticServer(t)
	for _, path := range []string{
		layout.StaticPrefix + "/",
		layout.StaticPrefix,
		layout.StaticPrefix + "/nope.js",
		layout.StaticPrefix + "/htmx.min.js/",
		layout.StaticPrefix + "/sub/htmx.min.js",
	} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body := make([]byte, 512)
		n, _ := res.Body.Read(body)
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404 (body: %s)", path, res.StatusCode, body[:n])
		}
	}
}

// Every asset the layout links must actually exist in the binary.
func TestReferencedAssetsAreEmbedded(t *testing.T) {
	for _, path := range []string{layout.HTMXPath, layout.AlpinePath} {
		name := strings.TrimPrefix(path, layout.StaticPrefix+"/")
		a, ok := static.Assets[name]
		if !ok {
			t.Fatalf("%s is referenced by the layout but not embedded", name)
		}
		if len(a.Body) == 0 || a.ETag == "" {
			t.Errorf("%s: empty asset or missing ETag", name)
		}
	}
}

// The content type follows the file extension: a stylesheet served as
// text/javascript is dropped by the browser.
func TestStaticContentTypeFollowsExtension(t *testing.T) {
	srv := staticServer(t)
	for path, want := range map[string]string{
		layout.NocturnePath: "text/css; charset=utf-8",
		layout.AppCSSPath:   "text/css; charset=utf-8",
		layout.HTMXPath:     "text/javascript; charset=utf-8",
		layout.AssessPath:   "text/javascript; charset=utf-8",
	} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("GET %s: %d, want 200", path, res.StatusCode)
			continue
		}
		if got := res.Header.Get("Content-Type"); got != want {
			t.Errorf("GET %s: Content-Type = %q, want %q", path, got, want)
		}
		if res.Header.Get("ETag") == "" {
			t.Errorf("GET %s: no ETag", path)
		}
	}
}
