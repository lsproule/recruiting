// Package layout holds the shared page chrome — document head, navigation,
// and flash messages — plus the local asset mount every HTML surface uses.
package layout

import (
	"bytes"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"recruiting/web/static"
)

// StaticPrefix is where the vendored htmx and Alpine bundles are served.
// Assets are local so pages keep working on networks that block CDNs.
const StaticPrefix = "/static"

// Asset paths the layout references.
const (
	HTMXPath   = StaticPrefix + "/htmx.min.js"
	AlpinePath = StaticPrefix + "/alpine.min.js"
	AssessPath = StaticPrefix + "/assess/assess.js"
)

// staticMaxAge is short enough that a redeploy is picked up quickly; the ETag
// makes the revalidation cheap.
const staticMaxAge = "public, max-age=3600"

// MountStatic serves the embedded assets under StaticPrefix. Only the exact
// names baked into the binary resolve; there is no directory listing and no
// path below the prefix that maps to the filesystem.
func MountStatic(r chi.Router) {
	r.Get(StaticPrefix+"/{name}", serveAsset)
	r.Head(StaticPrefix+"/{name}", serveAsset)
	r.Get(StaticPrefix+"/{dir}/{name}", serveAsset)
	r.Head(StaticPrefix+"/{dir}/{name}", serveAsset)
}

func serveAsset(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if dir := chi.URLParam(r, "dir"); dir != "" {
		name = dir + "/" + name
	}
	asset, ok := static.Assets[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", asset.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", staticMaxAge)
	w.Header().Set("ETag", asset.ETag)
	// Zero modtime so ServeContent uses the ETag alone for revalidation.
	http.ServeContent(w, r, asset.Name, time.Time{}, bytes.NewReader(asset.Body))
}
