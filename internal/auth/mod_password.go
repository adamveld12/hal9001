package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters for new hashes (OWASP: m=19 MiB, t=2, p=1 minimum;
// these are stronger and still well under a second).
const (
	argonMemoryKiB = 64 * 1024
	argonTime      = 3
	argonThreads   = 4
	argonKeyLen    = 32
	argonSaltLen   = 16
	// maxArgonMemoryKiB bounds parameters read from a stored hash so a
	// corrupted config cannot make a login attempt allocate gigabytes.
	maxArgonMemoryKiB = 1024 * 1024
)

// HashPassword returns an argon2id PHC string for password, for
// `hal9001 auth set-password`.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("auth: empty password")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks password against an argon2id PHC string in constant
// time. A malformed hash is an error, not a mismatch.
func VerifyPassword(phc, password string) (bool, error) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errors.New("auth: password hash is not an argon2id PHC string")
	}
	if parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return false, fmt.Errorf("auth: unsupported argon2 version %q", parts[2])
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, fmt.Errorf("auth: bad argon2 parameters: %w", err)
	}
	if m == 0 || m > maxArgonMemoryKiB || t == 0 || p == 0 {
		return false, errors.New("auth: argon2 parameters out of range")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false, errors.New("auth: bad argon2 salt")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errors.New("auth: bad argon2 key")
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want))) //nolint:gosec // len bounded by decoded config value
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// Password authenticates the single owner with the argon2id hash from the
// config. It applies only to interactive logins that present a password.
type Password struct{}

func (Password) Name() string { return "password" }
func (Password) Interactive() {}

func (Password) Authenticate(_ context.Context, req *AuthRequest) Result {
	if !req.Interactive {
		return Ignore("password login is interactive only")
	}
	if req.Method != "" && req.Method != "password" {
		return Ignore("another login method was selected")
	}
	pw := req.StringParam("password")
	if pw == "" {
		return Ignore("no password presented")
	}
	if req.Settings.PasswordHash == "" {
		return Fail("no password is configured")
	}
	ok, err := VerifyPassword(req.Settings.PasswordHash, pw)
	if err != nil {
		return Fail("configured password hash is invalid")
	}
	if !ok {
		return Fail("wrong password")
	}
	return Success(Principal{Name: "owner"})
}
