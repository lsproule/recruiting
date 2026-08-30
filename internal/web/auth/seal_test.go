package auth

import (
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func TestSealerRejectsStaleAndForeignValues(t *testing.T) {
	s := newSealer([]byte("secret"))
	now := time.Now()
	v, err := s.seal("tok", now)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.open(v, now.Add(time.Hour)); err != nil || got != "tok" {
		t.Fatalf("fresh open: %q %v", got, err)
	}
	if _, err := s.open(v, now.Add(assessmentTTL+time.Minute)); err == nil {
		t.Fatal("stale value accepted")
	}
	if _, err := newSealer([]byte("other")).open(v, now); err == nil {
		t.Fatal("value sealed under another key accepted")
	}
	if _, err := s.open("garbage", now); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestMountRequiresCookieSecret(t *testing.T) {
	if _, err := Mount(chi.NewMux(), Deps{}); err == nil {
		t.Fatal("Mount without CookieSecret succeeded")
	}
}

func TestSecureDerivedFromBaseURL(t *testing.T) {
	if !(Deps{BaseURL: "https://x.example"}).secure() || (Deps{BaseURL: "http://x.example"}).secure() {
		t.Fatal("secure not derived from BaseURL scheme")
	}
	f := false
	if (Deps{BaseURL: "https://x.example", SecureCookies: &f}).secure() {
		t.Fatal("explicit SecureCookies not honoured")
	}
}
