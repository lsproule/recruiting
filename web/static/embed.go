// Package static embeds the front-end assets the HTML surfaces load. They are
// vendored rather than fetched from a CDN because candidate assessment pages
// must work on restrictive networks.
package static

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"fmt"
	"io/fs"
	"path"
)

//go:embed *.css *.js assess/*.js replay/*.js
var FS embed.FS

// Asset is one embedded file, with the validator computed from its bytes.
type Asset struct {
	Name        string
	ContentType string
	Body        []byte
	ETag        string // quoted, per RFC 9110
}

// Assets is the closed set of servable files, keyed by path relative to this
// directory ("nocturne.css", "htmx.min.js", "assess/assess.js",
// "replay/replay.js"). Anything not in it does not exist as far as the server
// is concerned.
var Assets = map[string]Asset{}

// contentTypes is keyed by extension; a stylesheet served as text/javascript
// is refused by the browser.
var contentTypes = map[string]string{
	".css": "text/css; charset=utf-8",
	".js":  "text/javascript; charset=utf-8",
}

func init() {
	if err := fs.WalkDir(FS, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := FS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		ct, ok := contentTypes[path.Ext(name)]
		if !ok {
			return fmt.Errorf("no content type for %s", name)
		}
		sum := sha256.Sum256(body)
		Assets[name] = Asset{
			Name:        name,
			ContentType: ct,
			Body:        body,
			ETag:        fmt.Sprintf("%q", base64.RawURLEncoding.EncodeToString(sum[:16])),
		}
		return nil
	}); err != nil {
		panic("static: read embedded assets: " + err.Error())
	}
}
