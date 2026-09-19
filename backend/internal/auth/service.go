package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

type Settings struct {
	AccessTTL            time.Duration
	RefreshTTL           time.Duration
	CodeTTL              time.Duration
	CodeAttempts         int
	RequireVerifiedEmail bool
	ResendCooldown       time.Duration
	ProductName          string
}

type Service struct {
	Store    Store
	Mailer   Mailer
	Settings Settings
	// NewID generates account, code, and session identifiers. The projects
	// package supplies the UUIDv4 implementation.
	NewID func() (string, error)
}

const (
	purposeVerify = "verify_email"
	purposeLogin  = "login"
	purposeReset  = "password_reset"
)

func (s Service) settings() Settings {
	set := s.Settings
	if set.AccessTTL <= 0 {
		set.AccessTTL = 24 * time.Hour
	}
	if set.RefreshTTL <= 0 {
		set.RefreshTTL = 30 * 24 * time.Hour
	}
	if set.CodeTTL <= 0 {
		set.CodeTTL = 15 * time.Minute
	}
	if set.CodeAttempts <= 0 {
		set.CodeAttempts = 5
	}
	if set.ResendCooldown <= 0 {
		set.ResendCooldown = time.Minute
	}
	if set.ProductName == "" {
		set.ProductName = "GameDev"
	}
	return set
}

// Register creates a pending account and emails a confirmation code. An
// address that already has an unverified account gets a fresh code instead of
// a second account.
func (s Service) Register(ctx context.Context, ip string, raw RegisterRequest) (PendingResponse, error) {
	req, err := raw.normalized()
	if err != nil {
		return PendingResponse{}, err
	}
	set := s.settings()
	if err := s.Store.rateLimit(ctx, "register:ip", ip, time.Hour, 20); err != nil {
		return PendingResponse{}, err
	}
	if err := s.Store.rateLimit(ctx, "register:email", req.Email, time.Hour, 5); err != nil {
		return PendingResponse{}, err
	}
	hash, err := HashPassword(req.Password)
	if err != nil {
		return PendingResponse{}, err
	}
	status := "pending"
	if !set.RequireVerifiedEmail {
		status = "active"
	}
	id, err := s.NewID()
	if err != nil {
		return PendingResponse{}, err
	}
	created, err := s.Store.CreateUser(ctx, id, req.Email, req.DisplayName, hash, status)
	switch {
	case errors.Is(err, ErrEmailTaken):
		existing, lookupErr := s.Store.accountByEmail(ctx, req.Email)
		if lookupErr != nil {
			return PendingResponse{}, lookupErr
		}
		if existing.EmailVerified {
			return PendingResponse{}, ErrEmailTaken
		}
		// The address is unconfirmed, so nobody has proven ownership of it.
		// Replacing the password here is what lets an interrupted sign-up be
		// retried; the code still has to arrive by email before it works.
		if err := s.Store.setPassword(ctx, existing.ID, hash); err != nil {
			return PendingResponse{}, err
		}
		if err := s.Store.updateDisplayName(ctx, existing.ID, req.DisplayName); err != nil {
			return PendingResponse{}, err
		}
		created = existing
	case err != nil:
		return PendingResponse{}, err
	}
	if !set.RequireVerifiedEmail {
		if err := s.Store.activate(ctx, created.ID); err != nil {
			return PendingResponse{}, err
		}
	}
	if err := s.sendCode(ctx, created, purposeVerify); err != nil {
		return PendingResponse{}, err
	}
	return PendingResponse{Status: "verification_sent", ExpiresIn: int(set.CodeTTL.Seconds())}, nil
}

// ResendVerification never reveals whether the address is registered.
func (s Service) ResendVerification(ctx context.Context, ip string, raw EmailRequest) (PendingResponse, error) {
	return s.requestCode(ctx, ip, raw, purposeVerify)
}

// RequestLoginCode starts the passwordless path.
func (s Service) RequestLoginCode(ctx context.Context, ip string, raw EmailRequest) (PendingResponse, error) {
	return s.requestCode(ctx, ip, raw, purposeLogin)
}

func (s Service) RequestPasswordReset(ctx context.Context, ip string, raw EmailRequest) (PendingResponse, error) {
	return s.requestCode(ctx, ip, raw, purposeReset)
}

func (s Service) requestCode(ctx context.Context, ip string, raw EmailRequest, purpose string) (PendingResponse, error) {
	req, err := raw.normalized()
	if err != nil {
		return PendingResponse{}, err
	}
	set := s.settings()
	pending := PendingResponse{Status: "code_sent", ExpiresIn: int(set.CodeTTL.Seconds())}
	if err := s.Store.rateLimit(ctx, "code:ip", ip, time.Hour, 30); err != nil {
		return PendingResponse{}, err
	}
	if err := s.Store.rateLimit(ctx, "code:email", req.Email, time.Hour, 6); err != nil {
		return PendingResponse{}, err
	}
	user, err := s.Store.accountByEmail(ctx, req.Email)
	if errors.Is(err, ErrNotFound) {
		return pending, nil
	}
	if err != nil {
		return PendingResponse{}, err
	}
	if user.Status == "blocked" {
		return pending, nil
	}
	if purpose == purposeVerify && user.EmailVerified {
		return pending, nil
	}
	if purpose != purposeVerify && !user.EmailVerified && set.RequireVerifiedEmail {
		// An unconfirmed address cannot be used to sign in or reset a password;
		// send the confirmation code instead so the user is not stuck.
		purpose = purposeVerify
	}
	recent, err := s.Store.codeIssuedWithin(ctx, user.ID, purpose, set.ResendCooldown)
	if err != nil {
		return PendingResponse{}, err
	}
	if recent {
		return pending, nil
	}
	if err := s.sendCode(ctx, user, purpose); err != nil {
		return PendingResponse{}, err
	}
	return pending, nil
}

func (s Service) sendCode(ctx context.Context, user account, purpose string) error {
	set := s.settings()
	code, err := newCode()
	if err != nil {
		return err
	}
	id, err := s.NewID()
	if err != nil {
		return err
	}
	if err := s.Store.putCode(ctx, id, user.ID, purpose, digest(code), time.Now().Add(set.CodeTTL)); err != nil {
		return err
	}
	subject, body := message(set, user, purpose, code)
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.Mailer.Send(sendCtx, user.Email, subject, body); err != nil {
		slog.Error("verification email failed", "purpose", purpose, "error", err)
		return ErrMailNotDelivered
	}
	return nil
}

func message(set Settings, user account, purpose, code string) (string, string) {
	minutes := int(set.CodeTTL.Minutes())
	name := strings.TrimSpace(user.DisplayName)
	if name == "" {
		name = "разработчик"
	}
	switch purpose {
	case purposeLogin:
		return set.ProductName + ": код для входа",
			fmt.Sprintf("Здравствуйте, %s!\n\nКод для входа: %s\nОн действует %d минут.\n\nЕсли вход запрашивали не вы, просто удалите это письмо.\n\n%s",
				name, code, minutes, set.ProductName)
	case purposeReset:
		return set.ProductName + ": смена пароля",
			fmt.Sprintf("Здравствуйте, %s!\n\nКод для смены пароля: %s\nОн действует %d минут.\n\nЕсли смену пароля запрашивали не вы, пароль останется прежним.\n\n%s",
				name, code, minutes, set.ProductName)
	default:
		return set.ProductName + ": подтверждение адреса",
			fmt.Sprintf("Здравствуйте, %s!\n\nКод подтверждения: %s\nОн действует %d минут.\n\nПосле подтверждения адреса аккаунт станет активным.\n\n%s",
				name, code, minutes, set.ProductName)
	}
}

func (s Service) VerifyEmail(ctx context.Context, ip string, raw CodeRequest) (Session, error) {
	user, err := s.checkCode(ctx, ip, raw, purposeVerify)
	if err != nil {
		return Session{}, err
	}
	if err := s.Store.activate(ctx, user.ID); err != nil {
		return Session{}, err
	}
	user.Status, user.EmailVerified = "active", true
	return s.issue(ctx, user)
}

func (s Service) LoginWithCode(ctx context.Context, ip string, raw CodeRequest) (Session, error) {
	user, err := s.checkCode(ctx, ip, raw, purposeLogin)
	if err != nil {
		return Session{}, err
	}
	return s.issue(ctx, user)
}

func (s Service) ResetPassword(ctx context.Context, ip string, raw ResetRequest) (Session, error) {
	req, err := raw.normalized()
	if err != nil {
		return Session{}, err
	}
	user, err := s.checkCode(ctx, ip, CodeRequest{Email: req.Email, Code: req.Code}, purposeReset)
	if err != nil {
		return Session{}, err
	}
	hash, err := HashPassword(req.Password)
	if err != nil {
		return Session{}, err
	}
	if err := s.Store.setPassword(ctx, user.ID, hash); err != nil {
		return Session{}, err
	}
	// A password change ends every other session.
	if err := s.Store.revokeAll(ctx, user.ID); err != nil {
		return Session{}, err
	}
	return s.issue(ctx, user)
}

func (s Service) checkCode(ctx context.Context, ip string, raw CodeRequest, purpose string) (account, error) {
	req, err := raw.normalized()
	if err != nil {
		return account{}, err
	}
	set := s.settings()
	if err := s.Store.rateLimit(ctx, "verify:ip", ip, time.Hour, 60); err != nil {
		return account{}, err
	}
	if err := s.Store.rateLimit(ctx, "verify:email", req.Email, time.Hour, 20); err != nil {
		return account{}, err
	}
	user, err := s.Store.accountByEmail(ctx, req.Email)
	if errors.Is(err, ErrNotFound) {
		return account{}, ErrCodeInvalid
	}
	if err != nil {
		return account{}, err
	}
	if user.Status == "blocked" {
		return account{}, ErrAccountBlocked
	}
	if err := s.Store.consumeCode(ctx, user.ID, purpose, digest(req.Code), set.CodeAttempts); err != nil {
		return account{}, err
	}
	return user, nil
}

func (s Service) Login(ctx context.Context, ip string, raw LoginRequest) (Session, error) {
	req, err := raw.normalized()
	if err != nil {
		return Session{}, err
	}
	set := s.settings()
	if err := s.Store.rateLimit(ctx, "login:ip", ip, time.Hour, 60); err != nil {
		return Session{}, err
	}
	if err := s.Store.rateLimit(ctx, "login:email", req.Email, 15*time.Minute, 10); err != nil {
		return Session{}, err
	}
	user, err := s.Store.accountByEmail(ctx, req.Email)
	if errors.Is(err, ErrNotFound) {
		// Spend comparable time on a missing account so the response time does
		// not reveal whether the address exists.
		HashPassword(req.Password)
		return Session{}, ErrUnauthorized
	}
	if err != nil {
		return Session{}, err
	}
	if user.PasswordHash == "" || !VerifyPassword(user.PasswordHash, req.Password) {
		return Session{}, ErrUnauthorized
	}
	if user.Status == "blocked" {
		return Session{}, ErrAccountBlocked
	}
	if set.RequireVerifiedEmail && !user.EmailVerified {
		if _, err := s.requestCode(ctx, ip, EmailRequest{Email: req.Email}, purposeVerify); err != nil && !errors.Is(err, ErrRateLimited) {
			slog.Warn("could not resend verification during login", "error", err)
		}
		return Session{}, ErrEmailUnverified
	}
	return s.issue(ctx, user)
}

func (s Service) Refresh(ctx context.Context, ip string, raw RefreshRequest) (Session, error) {
	token := strings.TrimSpace(raw.RefreshToken)
	if len(token) < 32 || len(token) > 512 {
		return Session{}, ErrInvalid
	}
	if err := s.Store.rateLimit(ctx, "refresh:ip", ip, time.Hour, 120); err != nil {
		return Session{}, err
	}
	userID, err := s.Store.rotateRefresh(ctx, digest(token))
	if err != nil {
		return Session{}, err
	}
	user, err := s.Store.User(ctx, userID)
	if err != nil {
		return Session{}, err
	}
	return s.issue(ctx, account{
		ID: user.ID, Email: user.Email, DisplayName: user.DisplayName,
		Status: user.Status, EmailVerified: user.EmailVerified, CreatedAt: user.CreatedAt,
	})
}

func (s Service) Logout(ctx context.Context, userID, accessToken string, req LogoutRequest) error {
	if req.AllDevices {
		return s.Store.revokeAll(ctx, userID)
	}
	if token := strings.TrimSpace(req.RefreshToken); token != "" {
		if err := s.Store.revokeRefresh(ctx, userID, digest(token)); err != nil {
			return err
		}
	}
	if accessToken != "" {
		return s.Store.revokeAccess(ctx, digest(accessToken))
	}
	return nil
}

func (s Service) Me(ctx context.Context, userID string) (User, error) {
	return s.Store.User(ctx, userID)
}

func (s Service) Rename(ctx context.Context, userID, displayName string) (User, error) {
	displayName = strings.TrimSpace(displayName)
	if !validDisplayName(displayName) {
		return User{}, ErrInvalid
	}
	if err := s.Store.updateDisplayName(ctx, userID, displayName); err != nil {
		return User{}, err
	}
	return s.Store.User(ctx, userID)
}

func (s Service) issue(ctx context.Context, user account) (Session, error) {
	set := s.settings()
	if user.Status != "active" {
		if set.RequireVerifiedEmail {
			return Session{}, ErrEmailUnverified
		}
		if err := s.Store.activate(ctx, user.ID); err != nil {
			return Session{}, err
		}
		user.Status = "active"
	}
	access, err := newToken("sbx_at_")
	if err != nil {
		return Session{}, err
	}
	refresh, err := newToken("sbx_rt_")
	if err != nil {
		return Session{}, err
	}
	accessExpires := time.Now().Add(set.AccessTTL)
	refreshExpires := time.Now().Add(set.RefreshTTL)
	if err := s.Store.issueSession(ctx, user.ID, digest(access), accessExpires, digest(refresh), refreshExpires); err != nil {
		return Session{}, err
	}
	current, err := s.Store.User(ctx, user.ID)
	if err != nil {
		return Session{}, err
	}
	return Session{
		TokenType:             "Bearer",
		AccessToken:           access,
		AccessTokenExpiresAt:  accessExpires.UTC(),
		RefreshToken:          refresh,
		RefreshTokenExpiresAt: refreshExpires.UTC(),
		User:                  current,
	}, nil
}

// Purge removes expired codes, tokens, and rate-limit counters. The API calls
// it on a timer.
func (s Service) Purge(ctx context.Context) error { return s.Store.purgeExpired(ctx) }
