package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/digkill/gamedev/backend/internal/auth"
)

func (a API) authRoutes(mux *http.ServeMux) {
	if a.Auth == nil {
		return
	}
	mux.HandleFunc("POST /api/v1/auth/register", a.register)
	mux.HandleFunc("POST /api/v1/auth/verify", a.verifyEmail)
	mux.HandleFunc("POST /api/v1/auth/resend", a.resendCode)
	mux.HandleFunc("POST /api/v1/auth/login", a.login)
	mux.HandleFunc("POST /api/v1/auth/login/code", a.requestLoginCode)
	mux.HandleFunc("POST /api/v1/auth/login/code/verify", a.loginWithCode)
	mux.HandleFunc("POST /api/v1/auth/password/forgot", a.forgotPassword)
	mux.HandleFunc("POST /api/v1/auth/password/reset", a.resetPassword)
	mux.HandleFunc("POST /api/v1/auth/refresh", a.refresh)
	mux.HandleFunc("POST /api/v1/auth/logout", a.authorize(a.logout))
	mux.HandleFunc("GET /api/v1/auth/me", a.authorize(a.me))
	mux.HandleFunc("PATCH /api/v1/auth/me", a.authorize(a.renameMe))
}

func (a API) register(w http.ResponseWriter, r *http.Request) {
	var req auth.RegisterRequest
	if !readJSON(w, r, &req) {
		return
	}
	pending, err := a.Auth.Register(r.Context(), clientIP(r), req)
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, 202, pending)
}

func (a API) verifyEmail(w http.ResponseWriter, r *http.Request) {
	var req auth.CodeRequest
	if !readJSON(w, r, &req) {
		return
	}
	session, err := a.Auth.VerifyEmail(r.Context(), clientIP(r), req)
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, 200, session)
}

func (a API) resendCode(w http.ResponseWriter, r *http.Request) {
	a.pending(w, r, a.Auth.ResendVerification)
}

func (a API) requestLoginCode(w http.ResponseWriter, r *http.Request) {
	a.pending(w, r, a.Auth.RequestLoginCode)
}

func (a API) forgotPassword(w http.ResponseWriter, r *http.Request) {
	a.pending(w, r, a.Auth.RequestPasswordReset)
}

type pendingFunc func(ctx context.Context, ip string, req auth.EmailRequest) (auth.PendingResponse, error)

func (a API) pending(w http.ResponseWriter, r *http.Request, fn pendingFunc) {
	var req auth.EmailRequest
	if !readJSON(w, r, &req) {
		return
	}
	result, err := fn(r.Context(), clientIP(r), req)
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, 202, result)
}

func (a API) login(w http.ResponseWriter, r *http.Request) {
	var req auth.LoginRequest
	if !readJSON(w, r, &req) {
		return
	}
	session, err := a.Auth.Login(r.Context(), clientIP(r), req)
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, 200, session)
}

func (a API) loginWithCode(w http.ResponseWriter, r *http.Request) {
	var req auth.CodeRequest
	if !readJSON(w, r, &req) {
		return
	}
	session, err := a.Auth.LoginWithCode(r.Context(), clientIP(r), req)
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, 200, session)
}

func (a API) resetPassword(w http.ResponseWriter, r *http.Request) {
	var req auth.ResetRequest
	if !readJSON(w, r, &req) {
		return
	}
	session, err := a.Auth.ResetPassword(r.Context(), clientIP(r), req)
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, 200, session)
}

func (a API) refresh(w http.ResponseWriter, r *http.Request) {
	var req auth.RefreshRequest
	if !readJSON(w, r, &req) {
		return
	}
	session, err := a.Auth.Refresh(r.Context(), clientIP(r), req)
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, 200, session)
}

func (a API) logout(w http.ResponseWriter, r *http.Request) {
	var req auth.LogoutRequest
	if r.ContentLength > 0 && !readJSON(w, r, &req) {
		return
	}
	if err := a.Auth.Logout(r.Context(), actor(r), bearer(r), req); err != nil {
		authError(w, r, err)
		return
	}
	w.WriteHeader(204)
}

func (a API) me(w http.ResponseWriter, r *http.Request) {
	user, err := a.Auth.Me(r.Context(), actor(r))
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, 200, user)
}

func (a API) renameMe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DisplayName string `json:"display_name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	user, err := a.Auth.Rename(r.Context(), actor(r), req.DisplayName)
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, 200, user)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	body, ok := bodyBytes(w, r)
	if !ok {
		return false
	}
	if !decodeStrict(body, v) {
		fail(w, r, 422, "VALIDATION_FAILED", "Некорректное тело запроса", false, nil)
		return false
	}
	return true
}

func authError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrInvalid):
		fail(w, r, 422, "VALIDATION_FAILED", "Проверьте адрес и пароль: пароль от 10 символов, буквы и цифры", false, nil)
	case errors.Is(err, auth.ErrEmailTaken):
		fail(w, r, 409, "EMAIL_TAKEN", "Аккаунт с таким адресом уже существует", false, nil)
	case errors.Is(err, auth.ErrUnauthorized):
		fail(w, r, 401, "UNAUTHORIZED", "Неверный адрес или пароль", false, nil)
	case errors.Is(err, auth.ErrCodeInvalid):
		fail(w, r, 400, "CODE_INVALID", "Код неверен или истёк", false, nil)
	case errors.Is(err, auth.ErrEmailUnverified):
		fail(w, r, 403, "EMAIL_UNVERIFIED", "Подтвердите адрес: код отправлен на почту", false, nil)
	case errors.Is(err, auth.ErrAccountBlocked):
		fail(w, r, 403, "ACCOUNT_BLOCKED", "Аккаунт заблокирован", false, nil)
	case errors.Is(err, auth.ErrRateLimited):
		fail(w, r, 429, "TOO_MANY_ATTEMPTS", "Слишком много попыток, попробуйте позже", true, nil)
	case errors.Is(err, auth.ErrMailNotDelivered):
		fail(w, r, 503, "MAIL_UNAVAILABLE", "Не удалось отправить письмо, попробуйте позже", true, nil)
	case errors.Is(err, auth.ErrNotFound):
		fail(w, r, 404, "NOT_FOUND", "Аккаунт не найден", false, nil)
	default:
		fail(w, r, 503, "DEPENDENCY_UNAVAILABLE", "Временная ошибка сервера", true, nil)
	}
}

// clientIP is used only for rate limiting. X-Forwarded-For is honoured only
// when the deployment declares that it sits behind a trusted proxy, because a
// direct client can set the header itself.
func clientIP(r *http.Request) string {
	if trusted, _ := r.Context().Value(trustProxyKey{}).(bool); trusted {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			first, _, _ := strings.Cut(forwarded, ",")
			if ip := strings.TrimSpace(first); ip != "" && len(ip) <= 64 {
				return ip
			}
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}
