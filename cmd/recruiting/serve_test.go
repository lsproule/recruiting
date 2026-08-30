package main

import "testing"

// The reset token is the last path segment of the link the auth surface
// built. A query string or a trailing slash must not end up inside it — the
// token is hashed and looked up verbatim, so a stray character means no row.
func TestResetTokenReadsTheLastPathSegment(t *testing.T) {
	const token = "N2xk-9Q_tokenvalue"
	for _, tc := range []struct{ name, link string }{
		{"plain", "http://localhost:8080/app/reset/" + token},
		{"https", "https://example.test/client/reset/" + token},
		{"trailing slash", "https://example.test/app/reset/" + token + "/"},
		{"query string", "https://example.test/app/reset/" + token + "?from=email"},
		{"fragment", "https://example.test/app/reset/" + token + "#top"},
		{"port", "http://example.test:8443/app/reset/" + token},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resetToken(tc.link)
			if err != nil {
				t.Fatal(err)
			}
			if got != token {
				t.Errorf("resetToken(%q) = %q, want %q", tc.link, got, token)
			}
		})
	}
}

func TestResetTokenRejectsALinkWithoutOne(t *testing.T) {
	for _, link := range []string{"", "https://example.test/", "https://example.test"} {
		if got, err := resetToken(link); err == nil {
			t.Errorf("resetToken(%q) = %q, want an error", link, got)
		}
	}
}
