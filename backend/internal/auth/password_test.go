package auth

import (
	"strings"
	"testing"
)

func TestPasswordHashAndUniqueSalt(t *testing.T) {
	password := "Секретная фраза 🌱"
	first, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || strings.Contains(first, password) {
		t.Fatal("hash must use a unique salt and hide password")
	}
	if !VerifyPassword(password, first) || VerifyPassword("wrong password", first) {
		t.Fatal("password verification failed")
	}
	for _, malformed := range []string{"", "$argon2id$v=19$m=999999999,t=999,p=1$a$b", strings.Replace(first, "v=19", "v=20", 1), strings.Replace(first, "argon2id", "argon2i", 1)} {
		if VerifyPassword(password, malformed) {
			t.Fatalf("accepted malformed hash: %s", malformed)
		}
	}
}

func TestSessionTokens(t *testing.T) {
	first, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if !ValidToken(first) || first == second || len(TokenHash(first)) != 64 || TokenHash(first) == first {
		t.Fatal("invalid session token")
	}
	if ValidToken("short") || ValidToken(strings.Repeat("!", 43)) {
		t.Fatal("accepted invalid token")
	}
}
