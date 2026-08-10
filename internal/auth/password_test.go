package auth

import "testing"

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword("correct horse battery staple", hash) {
		t.Fatal("expected correct password to verify")
	}
	if VerifyPassword("wrong horse battery staple", hash) {
		t.Fatal("expected wrong password to fail")
	}
}

func TestValidatePassword(t *testing.T) {
	if err := ValidatePassword("short"); err == nil {
		t.Fatal("expected short password error")
	}
	if VerifyPassword("anything", "malformed") {
		t.Fatal("malformed hashes must never authenticate")
	}
}

func TestTokenRoundTrip(t *testing.T) {
	token, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyToken(token, TokenHash(token)) {
		t.Fatal("expected token to verify")
	}
	if VerifyToken(token+"x", TokenHash(token)) {
		t.Fatal("expected altered token to fail")
	}
}
