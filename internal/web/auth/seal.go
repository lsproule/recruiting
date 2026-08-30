package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

// sealer encrypts the assessment cookie with AES-GCM under a key derived
// from the configured secret, binding it to the "assessment" purpose via AAD
// and to an issue time enforced server-side.
type sealer struct {
	aead cipher.AEAD
}

var sealAAD = []byte("assessment")

type sealedPayload struct {
	Token    string `json:"t"`
	IssuedAt int64  `json:"i"` // unix seconds
}

func newSealer(secret []byte) *sealer {
	key := sha256.Sum256(secret)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return &sealer{aead: aead}
}

func (s *sealer) seal(token string, now time.Time) (string, error) {
	plain, err := json.Marshal(sealedPayload{Token: token, IssuedAt: now.Unix()})
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := s.aead.Seal(nonce, nonce, plain, sealAAD)
	return base64.RawURLEncoding.EncodeToString(out), nil
}

var errSealStale = errors.New("sealed value expired")

// open returns the token unless the payload is malformed, forged, or older
// than assessmentTTL at now.
func (s *sealer) open(sealed string, now time.Time) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil || len(raw) < s.aead.NonceSize() {
		return "", errors.New("malformed sealed value")
	}
	n := s.aead.NonceSize()
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], sealAAD)
	if err != nil {
		return "", err
	}
	var p sealedPayload
	if err := json.Unmarshal(plain, &p); err != nil {
		return "", err
	}
	issued := time.Unix(p.IssuedAt, 0)
	if issued.After(now.Add(time.Minute)) || now.Sub(issued) > assessmentTTL {
		return "", errSealStale
	}
	return p.Token, nil
}
