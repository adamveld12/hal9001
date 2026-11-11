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

// phc is a parsed argon2id PHC string.
type phc struct {
	m, t uint32
	p    uint8
	salt []byte
	key  []byte
}

// parsePHC parses and range-checks an argon2id PHC string.
func parsePHC(s string) (phc, error) {
	parts := strings.Split(s, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return phc{}, errors.New("auth: password hash is not an argon2id PHC string")
	}
	if parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return phc{}, fmt.Errorf("auth: unsupported argon2 version %q", parts[2])
	}
	var h phc
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &h.m, &h.t, &h.p); err != nil {
		return phc{}, fmt.Errorf("auth: bad argon2 parameters: %w", err)
	}
	if h.m == 0 || h.m > maxArgonMemoryKiB || h.t == 0 || h.p == 0 {
		return phc{}, errors.New("auth: argon2 parameters out of range")
	}
	var err error
	if h.salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil || len(h.salt) == 0 {
		return phc{}, errors.New("auth: bad argon2 salt")
	}
	if h.key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil || len(h.key) == 0 {
		return phc{}, errors.New("auth: bad argon2 key")
	}
	return h, nil
}

// ValidatePasswordHash reports why s cannot be used as a stored password hash, or nil.
func ValidatePasswordHash(s string) error {
	_, err := parsePHC(s)
	return err
}

// VerifyPassword checks password against an argon2id PHC string in constant
// time. A malformed hash is an error, not a mismatch.
func VerifyPassword(s, password string) (bool, error) {
	h, err := parsePHC(s)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), h.salt, h.t, h.m, h.p, uint32(len(h.key))) //nolint:gosec // len bounded by decoded config value
	return subtle.ConstantTimeCompare(got, h.key) == 1, nil
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
