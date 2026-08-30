package apply

import (
	"errors"
	"net/http"

	"recruiting/internal/domain"
)

// UploadBodyLimit is the largest request body the upload surfaces accept:
// one resume at its own limit, plus room for the surrounding form fields and
// multipart framing.
const UploadBodyLimit = domain.MaxResumeBytes + 64<<10

// MaxBody caps every request body at limit bytes and answers 413 when a
// client sends more.
//
// It lives here because the apply page is the surface that takes uploads; the
// recruiter's manual-add screen shares it.
//
// It must be installed ahead of CSRF: reading the token from a multipart form
// parses the whole body, which without a cap spools an unbounded upload to
// disk before any handler runs. So the parse happens here, inside the cap,
// and CSRF then reads the form this middleware already populated.
func MaxBody(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			// A truthful Content-Length is refused without reading anything.
			if r.ContentLength > limit {
				tooLarge(w, r)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			// maxMemory is the limit itself, so nothing under the cap ever
			// reaches a temporary file and nothing over it is read at all.
			if err := r.ParseMultipartForm(limit); err != nil {
				var tooBig *http.MaxBytesError
				if errors.As(err, &tooBig) {
					tooLarge(w, r)
					return
				}
				// Any other parse failure — including "not multipart" — is
				// the handler's business, not this middleware's.
			}
			next.ServeHTTP(w, r)
		})
	}
}

func tooLarge(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusRequestEntityTooLarge)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte(tooLargePage))
}

const tooLargePage = `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><title>Upload too large</title></head>
<body><h1>That upload is too large</h1>
<p>A resume may be up to 10 MB. Please send a smaller file.</p></body></html>
`
