package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters. Memory dominates the cost; the login handler is rate
// limited so a single request holding 64 MiB is acceptable.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

var errBadHash = errors.New("unreadable password hash")

// HashPassword returns a self-describing PHC string, so the cost parameters can
// change later without invalidating existing hashes.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether the password matches. An unreadable stored
// hash is a mismatch, never a panic.
func VerifyPassword(encoded, password string) bool {
	memory, time32, threads, salt, want, err := parseHash(encoded)
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, time32, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func parseHash(encoded string) (memory, time32 uint32, threads uint8, salt, key []byte, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return 0, 0, 0, nil, nil, errBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return 0, 0, 0, nil, nil, errBadHash
	}
	var m, t uint64
	var p uint64
	for _, field := range strings.Split(parts[3], ",") {
		name, value, ok := strings.Cut(field, "=")
		if !ok {
			return 0, 0, 0, nil, nil, errBadHash
		}
		n, convErr := strconv.ParseUint(value, 10, 32)
		if convErr != nil {
			return 0, 0, 0, nil, nil, errBadHash
		}
		switch name {
		case "m":
			m = n
		case "t":
			t = n
		case "p":
			p = n
		default:
			return 0, 0, 0, nil, nil, errBadHash
		}
	}
	if m == 0 || t == 0 || p == 0 || p > 255 {
		return 0, 0, 0, nil, nil, errBadHash
	}
	salt, err = base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return 0, 0, 0, nil, nil, errBadHash
	}
	key, err = base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) < 16 {
		return 0, 0, 0, nil, nil, errBadHash
	}
	return uint32(m), uint32(t), uint8(p), salt, key, nil
}

// ValidPassword keeps the rule short and predictable: length plus two
// character classes. Length carries most of the strength.
func ValidPassword(password string) bool {
	n := utf8.RuneCountInString(password)
	if n < 10 || n > 200 || strings.TrimSpace(password) == "" {
		return false
	}
	var letters, others bool
	for _, r := range password {
		switch {
		case unicode.IsLetter(r):
			letters = true
		default:
			others = true
		}
	}
	return letters && others
}
