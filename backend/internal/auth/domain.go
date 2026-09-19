package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalid          = errors.New("invalid auth request")
	ErrUnauthorized     = errors.New("invalid credentials")
	ErrEmailTaken       = errors.New("email already registered")
	ErrCodeInvalid      = errors.New("code invalid or expired")
	ErrRateLimited      = errors.New("too many attempts")
	ErrEmailUnverified  = errors.New("email is not verified")
	ErrAccountBlocked   = errors.New("account is blocked")
	ErrNotFound         = errors.New("account not found")
	ErrMailNotDelivered = errors.New("verification message could not be sent")
)

type User struct {
	ID            string    `json:"id"`
	Email         string    `json:"email"`
	DisplayName   string    `json:"display_name"`
	Status        string    `json:"status"`
	EmailVerified bool      `json:"email_verified"`
	CreatedAt     time.Time `json:"created_at"`
}

type Session struct {
	TokenType             string    `json:"token_type"`
	AccessToken           string    `json:"access_token"`
	AccessTokenExpiresAt  time.Time `json:"access_token_expires_at"`
	RefreshToken          string    `json:"refresh_token"`
	RefreshTokenExpiresAt time.Time `json:"refresh_token_expires_at"`
	User                  User      `json:"user"`
}

type RegisterRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

type EmailRequest struct {
	Email string `json:"email"`
}

type CodeRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type ResetRequest struct {
	Email    string `json:"email"`
	Code     string `json:"code"`
	Password string `json:"password"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type LogoutRequest struct {
	RefreshToken  string `json:"refresh_token"`
	AllDevices    bool   `json:"all_devices"`
	KeepThisToken bool   `json:"-"`
}

// PendingResponse is returned where confirming or denying that an address
// exists would leak account membership.
type PendingResponse struct {
	Status    string `json:"status"`
	ExpiresIn int    `json:"expires_in_seconds"`
}

func (r RegisterRequest) normalized() (RegisterRequest, error) {
	r.Email = NormalizeEmail(r.Email)
	r.DisplayName = strings.TrimSpace(r.DisplayName)
	if r.DisplayName == "" {
		if name, _, ok := strings.Cut(r.Email, "@"); ok {
			r.DisplayName = name
		}
	}
	if !ValidEmail(r.Email) || !ValidPassword(r.Password) || !validDisplayName(r.DisplayName) {
		return RegisterRequest{}, ErrInvalid
	}
	return r, nil
}

func (r EmailRequest) normalized() (EmailRequest, error) {
	r.Email = NormalizeEmail(r.Email)
	if !ValidEmail(r.Email) {
		return EmailRequest{}, ErrInvalid
	}
	return r, nil
}

func (r CodeRequest) normalized() (CodeRequest, error) {
	r.Email = NormalizeEmail(r.Email)
	r.Code = strings.TrimSpace(r.Code)
	if !ValidEmail(r.Email) || !validCode(r.Code) {
		return CodeRequest{}, ErrInvalid
	}
	return r, nil
}

func (r LoginRequest) normalized() (LoginRequest, error) {
	r.Email = NormalizeEmail(r.Email)
	if !ValidEmail(r.Email) || r.Password == "" || utf8.RuneCountInString(r.Password) > 200 {
		return LoginRequest{}, ErrInvalid
	}
	return r, nil
}

func (r ResetRequest) normalized() (ResetRequest, error) {
	r.Email = NormalizeEmail(r.Email)
	r.Code = strings.TrimSpace(r.Code)
	if !ValidEmail(r.Email) || !validCode(r.Code) || !ValidPassword(r.Password) {
		return ResetRequest{}, ErrInvalid
	}
	return r, nil
}

func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ValidEmail is deliberately conservative: one @, a dotted domain, no spaces or
// control characters. Deliverability is proven by the confirmation code.
func ValidEmail(email string) bool {
	if len(email) < 6 || len(email) > 254 || email != strings.ToLower(email) {
		return false
	}
	if strings.ContainsAny(email, " \t\r\n<>,;\"'\\") {
		return false
	}
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" || len(local) > 64 || strings.Contains(domain, "@") {
		return false
	}
	if len(domain) < 3 || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") || strings.Contains(domain, "..") {
		return false
	}
	dot := strings.LastIndex(domain, ".")
	return dot > 0 && len(domain)-dot > 2
}

func validDisplayName(name string) bool {
	n := utf8.RuneCountInString(name)
	return n >= 1 && n <= 120 && strings.TrimSpace(name) != "" && !strings.ContainsAny(name, "\r\n")
}

func validCode(code string) bool {
	if len(code) != codeLength {
		return false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

const codeLength = 6

// newCode returns a uniformly distributed decimal code with leading zeros kept.
func newCode() (string, error) {
	max := big.NewInt(1)
	for i := 0; i < codeLength; i++ {
		max.Mul(max, big.NewInt(10))
	}
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	digits := n.String()
	return strings.Repeat("0", codeLength-len(digits)) + digits, nil
}

func newToken(prefix string) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func digest(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}
