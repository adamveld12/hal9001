package auth_test

import (
	"strings"
	"testing"

	"hal9001/internal/auth"
)

func TestHashPasswordProducesVerifiableArgon2idPHC(t *testing.T) {
	h := testPasswordHash(t)
	if !strings.HasPrefix(h, "$argon2id$v=19$m=") {
		t.Fatalf("hash = %q, want an argon2id PHC string", h)
	}
	if ok, err := auth.VerifyPassword(h, testPW); err != nil || !ok {
		t.Errorf("correct password: ok=%v err=%v", ok, err)
	}
	if ok, err := auth.VerifyPassword(h, testPW+"x"); err != nil || ok {
		t.Errorf("wrong password: ok=%v err=%v", ok, err)
	}
	if ok, _ := auth.VerifyPassword(h, ""); ok {
		t.Error("empty password verified")
	}
	h2, err := auth.HashPassword(testPW)
	if err != nil {
		t.Fatal(err)
	}
	if h2 == h {
		t.Error("two hashes of the same password are identical: salt is not random")
	}
}

func TestVerifyPasswordAgainstIndependentImplementation(t *testing.T) {
	// Independent implementation (OpenSSL 4.1):
	//   openssl kdf -keylen 32 -binary -kdfopt pass:password -kdfopt salt:somesalt \
	//     -kdfopt iter:2 -kdfopt memcost:65536 -kdfopt lanes:4 ARGON2ID
	const phc = "$argon2id$v=19$m=65536,t=2,p=4$c29tZXNhbHQ$GpZ3sK/oH9p7VIiV56G/64Zo/8GaUw434IimaPqxwCo"
	if ok, err := auth.VerifyPassword(phc, "password"); err != nil || !ok {
		t.Errorf("OpenSSL vector: ok=%v err=%v", ok, err)
	}
}

func TestVerifyPasswordRejectsMalformedHashes(t *testing.T) {
	for _, bad := range []string{
		"", "plaintext", "$argon2i$v=19$m=65536,t=2,p=1$c29tZXNhbHQ$aGFzaA",
		"$argon2id$v=18$m=65536,t=2,p=1$c29tZXNhbHQ$aGFzaA",
		"$argon2id$v=19$m=x,t=2,p=1$c29tZXNhbHQ$aGFzaA",
		"$argon2id$v=19$m=65536,t=2,p=1$!!!$aGFzaA",
		"$argon2id$v=19$m=4294967295,t=2,p=1$c29tZXNhbHQ$aGFzaA", // memory bomb
		"$argon2id$v=19$m=65536,t=0,p=1$c29tZXNhbHQ$aGFzaA",
	} {
		if ok, err := auth.VerifyPassword(bad, "password"); ok || err == nil {
			t.Errorf("%q: ok=%v err=%v, want an error", bad, ok, err)
		}
	}
	if _, err := auth.HashPassword(""); err == nil {
		t.Error("empty password hashed")
	}
}
