package service

import "testing"

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if hash == "correct horse" || len(hash) < 20 || hash[:9] != "$argon2id" {
		t.Fatalf("hash not argon2id encoded: %q", hash)
	}
	if ok, err := VerifyPassword(hash, "correct horse"); err != nil || !ok {
		t.Fatalf("correct password rejected: ok=%v err=%v", ok, err)
	}
	if ok, err := VerifyPassword(hash, "wrong"); err != nil || ok {
		t.Fatalf("wrong password accepted: ok=%v err=%v", ok, err)
	}
	if _, err := VerifyPassword("garbage", "x"); err == nil {
		t.Fatal("malformed hash should error")
	}
}
