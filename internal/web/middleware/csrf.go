package middleware

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
)

// Double-submit cookie: the token lives in a cookie and must be echoed by
// every non-safe request, as a form field or (for htmx) a header.
const (
	CSRFCookie       = "csrf"
	CSRFCookieSecure = "__Host-csrf" // __Host- prefix pins Secure, Path=/, no Domain
	CSRFField        = "_csrf"
	CSRFHeader       = "X-CSRF-Token"
)

type csrfKey struct{}

// CSRF issues the token cookie on safe requests and rejects unsafe requests
// whose token does not match it with 403. secure selects the __Host- cookie
// and the Secure flag; decide it from configuration, not the connection.
func CSRF(secure bool) func(http.Handler) http.Handler {
	name := CSRFCookie
	if secure {
		name = CSRFCookieSecure
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := ""
			if c, err := r.Cookie(name); err == nil {
				token = c.Value
			}
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				if token == "" {
					b := make([]byte, 32)
					if _, err := rand.Read(b); err != nil {
						http.Error(w, "csrf token", http.StatusInternalServerError)
						return
					}
					token = base64.RawURLEncoding.EncodeToString(b)
					http.SetCookie(w, &http.Cookie{
						Name: name, Value: token, Path: "/", HttpOnly: true,
						SameSite: http.SameSiteLaxMode, Secure: secure,
					})
				}
			default:
				sent := r.Header.Get(CSRFHeader)
				if sent == "" {
					sent = r.PostFormValue(CSRFField)
				}
				if token == "" || sent == "" || subtle.ConstantTimeCompare([]byte(sent), []byte(token)) != 1 {
					http.Error(w, "invalid or missing CSRF token", http.StatusForbidden)
					return
				}
			}
			next.ServeHTTP(w, r.WithContext(contextWithCSRF(r, token)))
		})
	}
}

// CSRFToken returns the token a page must embed in its forms.
func CSRFToken(r *http.Request) string {
	t, _ := r.Context().Value(csrfKey{}).(string)
	return t
}

func contextWithCSRF(r *http.Request, token string) context.Context {
	return context.WithValue(r.Context(), csrfKey{}, token)
}
