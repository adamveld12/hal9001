package auth_test

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"

	"hal9001/internal/auth"
)

// cheapHash is an argon2id PHC string with minimal cost, so that tests that verify many wrong passwords stay fast.
func cheapHash(password string) string {
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte(password), salt, 1, 64, 1, 32)
	return fmt.Sprintf("$argon2id$v=%d$m=64,t=1,p=1$%s$%s", argon2.Version, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

func TestValidatePasswordCountsCharactersNotBytes(t *testing.T) {
	for pw, ok := range map[string]bool{
		"":                               false,
		"elevenchars":                    false,
		"twelve chars":                   true,
		strings.Repeat("é", 11):          false, // 22 bytes, 11 characters
		strings.Repeat("é", 12):          true,
		strings.Repeat("a", 1024):        true,
		strings.Repeat("a", 1025):        false,
		"correct horse battery staple 1": true,
	} {
		if err := auth.ValidatePassword(pw); (err == nil) != ok {
			t.Errorf("ValidatePassword(%.20q) = %v, want ok=%v", pw, err, ok)
		}
	}
}

func TestPasswordLoginsAreRefusedAfterFiveFailuresFromOneAddressUntilTheWindowPasses(t *testing.T) {
	cfg := stackConfig(entry("password", auth.Sufficient))
	cfg.PasswordHash = cheapHash(testPW)
	e := newEnv(t, withConfig(cfg))
	login := func(pw string) *req {
		return post("/api/auth/api/begin", map[string]string{"method": "password", "password": pw})
	}

	for range 5 {
		wantStatus(t, e.do(login("wrong")), 401)
		e.clock.Advance(time.Minute)
	}
	rec := e.do(login(testPW))
	wantStatus(t, rec, 429)
	if got := rec.Header().Get("Retry-After"); got != "600" { // the first failure leaves the 15 minute window in 10 minutes
		t.Errorf("Retry-After = %q, want 600", got)
	}
	if ev := e.lastEvent(); ev.Action != "login_begin" || ev.Outcome != auth.OutcomeFail || !strings.Contains(ev.Reason, "too many") {
		t.Errorf("last event = %+v", ev)
	}
	// Another address is not affected.
	wantStatus(t, e.do(login(testPW).from("127.0.0.2:50000")), 200)

	e.clock.Advance(10 * time.Minute)
	wantStatus(t, e.do(login(testPW)), 200)
	// A success wipes the count: four more failures do not block.
	for range 4 {
		wantStatus(t, e.do(login("wrong")), 401)
	}
	wantStatus(t, e.do(login(testPW)), 200)
}
