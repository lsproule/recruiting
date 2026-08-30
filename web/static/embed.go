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
)

//go:embed *.js assess/*.js replay/*.js
var FS embed.FS

// Asset is one embedded file, with the validator computed from its bytes.
type Asset struct {
	Name        string
	ContentType string
	Body        []byte
	ETag        string // quoted, per RFC 9110
}

// Assets is the closed set of servable files, keyed by path relative to this
// directory ("htmx.min.js", "assess/assess.js", "replay/replay.js"). Anything not in it does not
// exist as far as the server is concerned.
var Assets = map[string]Asset{}

func init() {
	if err := fs.WalkDir(FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := FS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		sum := sha256.Sum256(body)
		Assets[path] = Asset{
			Name:        path,
			ContentType: "text/javascript; charset=utf-8",
			Body:        body,
			ETag:        fmt.Sprintf("%q", base64.RawURLEncoding.EncodeToString(sum[:16])),
		}
		return nil
	}); err != nil {
		panic("static: read embedded assets: " + err.Error())
	}
}
