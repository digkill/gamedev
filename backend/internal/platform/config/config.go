package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Env           string
	HTTPAddr      string
	PublicBaseURL string
	DatabaseURL   string
	DBMaxConns    int32
	OpenAIAPIKey  string
	OpenAIModel   string
	Kie           Kie
	SMTP          SMTP
	Auth          Auth
	Pipeline      Pipeline
}

// Kie routes model calls through kie.ai. Claude speaks the Anthropic Messages
// schema under /claude/v1/messages; the GPT fallback speaks the Responses
// schema under /codex/v1/responses. One key covers both.
type Kie struct {
	APIKey          string
	BaseURL         string
	Model           string
	FallbackModel   string
	ReasoningEffort string
	MaxTokens       int
	Timeout         time.Duration
}

func (k Kie) Configured() bool { return k.APIKey != "" && k.Model != "" }

type SMTP struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	FromName string
}

func (s SMTP) Configured() bool { return s.Host != "" && s.Port != 0 && s.From != "" }

type Auth struct {
	AccessTTL    time.Duration
	RefreshTTL   time.Duration
	CodeTTL      time.Duration
	CodeAttempts int
	// RequireVerifiedEmail refuses a password login until the address is
	// confirmed. Turning it off is only sensible while SMTP is unavailable.
	RequireVerifiedEmail bool
}

type Pipeline struct {
	Workers     int
	MaxRepairs  int
	StepTimeout time.Duration
	JobTimeout  time.Duration
}

func Load() (Config, error) {
	env := strings.TrimSpace(os.Getenv("APP_ENV"))
	if env == "" || env == "development" {
		if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("load .env: %w", err)
		}
	}
	return Parse(os.Getenv)
}

func Parse(getenv func(string) string) (Config, error) {
	c := Config{
		Env:           strings.TrimSpace(getenv("APP_ENV")),
		HTTPAddr:      strings.TrimSpace(getenv("HTTP_ADDR")),
		PublicBaseURL: strings.TrimSpace(getenv("PUBLIC_BASE_URL")),
		DatabaseURL:   strings.TrimSpace(getenv("DATABASE_URL")),
		DBMaxConns:    20,
		OpenAIAPIKey:  strings.TrimSpace(getenv("OPENAI_API_KEY")),
		OpenAIModel:   strings.TrimSpace(getenv("OPENAI_MODEL")),
	}
	if c.Env == "" {
		c.Env = "development"
	}
	if c.Env != "development" && c.Env != "staging" && c.Env != "production" {
		return Config{}, errors.New("APP_ENV must be development, staging, or production")
	}
	if c.HTTPAddr == "" || c.DatabaseURL == "" || c.PublicBaseURL == "" {
		return Config{}, errors.New("HTTP_ADDR, DATABASE_URL, and PUBLIC_BASE_URL are required")
	}
	if _, _, err := net.SplitHostPort(c.HTTPAddr); err != nil {
		return Config{}, errors.New("HTTP_ADDR must be host:port")
	}
	publicURL, err := url.Parse(c.PublicBaseURL)
	if err != nil || publicURL.Host == "" || (publicURL.Scheme != "http" && publicURL.Scheme != "https") {
		return Config{}, errors.New("PUBLIC_BASE_URL must be an absolute HTTP URL")
	}
	dbURL, err := url.Parse(c.DatabaseURL)
	if err != nil || (dbURL.Scheme != "postgres" && dbURL.Scheme != "postgresql") || dbURL.Host == "" || dbURL.User == nil {
		return Config{}, errors.New("DATABASE_URL must be a PostgreSQL URL with credentials")
	}
	sslmode := dbURL.Query().Get("sslmode")
	if c.Env != "development" {
		if publicURL.Scheme != "https" {
			return Config{}, errors.New("PUBLIC_BASE_URL must use HTTPS outside development")
		}
		if sslmode != "verify-full" && sslmode != "require" && sslmode != "disable" {
			return Config{}, errors.New("DATABASE_URL must set sslmode outside development")
		}
		if dbURL.User.Username() == "sandbox" {
			return Config{}, errors.New("DATABASE_URL must not use example credentials outside development")
		}
	}
	if raw := strings.TrimSpace(getenv("DB_MAX_CONNS")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || n < 1 || n > 100 {
			return Config{}, errors.New("DB_MAX_CONNS must be 1..100")
		}
		c.DBMaxConns = int32(n)
	}
	if c.Kie, err = parseKie(getenv); err != nil {
		return Config{}, err
	}
	if c.SMTP, err = parseSMTP(getenv); err != nil {
		return Config{}, err
	}
	if c.Auth, err = parseAuth(getenv, c); err != nil {
		return Config{}, err
	}
	if c.Pipeline, err = parsePipeline(getenv); err != nil {
		return Config{}, err
	}
	return c, nil
}

func parseKie(getenv func(string) string) (Kie, error) {
	k := Kie{
		APIKey:          strings.TrimSpace(getenv("KIE_API_KEY")),
		BaseURL:         strings.TrimSpace(getenv("KIE_BASE_URL")),
		Model:           strings.TrimSpace(getenv("KIE_MODEL")),
		FallbackModel:   strings.TrimSpace(getenv("KIE_FALLBACK_MODEL")),
		ReasoningEffort: strings.TrimSpace(getenv("KIE_REASONING_EFFORT")),
		MaxTokens:       16000,
		Timeout:         300 * time.Second,
	}
	if k.APIKey == "" {
		return Kie{}, nil
	}
	if k.BaseURL == "" {
		k.BaseURL = "https://api.kie.ai"
	}
	base, err := url.Parse(k.BaseURL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return Kie{}, errors.New("KIE_BASE_URL must be an absolute HTTP URL")
	}
	k.BaseURL = strings.TrimRight(k.BaseURL, "/")
	if k.Model == "" {
		k.Model = "claude-opus-5"
	}
	if k.FallbackModel == "" {
		k.FallbackModel = "gpt-5-6-terra"
	}
	if k.ReasoningEffort == "" {
		k.ReasoningEffort = "low"
	}
	switch k.ReasoningEffort {
	case "low", "medium", "high", "xhigh":
	default:
		return Kie{}, errors.New("KIE_REASONING_EFFORT must be low, medium, high, or xhigh")
	}
	if raw := strings.TrimSpace(getenv("KIE_MAX_TOKENS")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1000 || n > 200000 {
			return Kie{}, errors.New("KIE_MAX_TOKENS must be 1000..200000")
		}
		k.MaxTokens = n
	}
	timeout, err := durationSeconds(getenv, "KIE_TIMEOUT_SECONDS", k.Timeout, 10*time.Second, 900*time.Second)
	if err != nil {
		return Kie{}, err
	}
	k.Timeout = timeout
	return k, nil
}

func parseSMTP(getenv func(string) string) (SMTP, error) {
	s := SMTP{
		Host:     strings.TrimSpace(getenv("SMTP_HOST")),
		Username: strings.TrimSpace(getenv("SMTP_USERNAME")),
		Password: getenv("SMTP_PASSWORD"),
		From:     strings.TrimSpace(getenv("SMTP_FROM")),
		FromName: strings.TrimSpace(getenv("SMTP_FROM_NAME")),
	}
	rawPort := strings.TrimSpace(getenv("SMTP_PORT"))
	if s.Host == "" && s.From == "" && rawPort == "" {
		return SMTP{}, nil
	}
	if s.Host == "" || s.From == "" {
		return SMTP{}, errors.New("SMTP_HOST and SMTP_FROM are required when SMTP is configured")
	}
	port := 465
	if rawPort != "" {
		n, err := strconv.Atoi(rawPort)
		if err != nil || n < 1 || n > 65535 {
			return SMTP{}, errors.New("SMTP_PORT must be 1..65535")
		}
		port = n
	}
	s.Port = port
	if !strings.Contains(s.From, "@") || strings.ContainsAny(s.From, " \r\n") {
		return SMTP{}, errors.New("SMTP_FROM must be an email address")
	}
	if strings.ContainsAny(s.FromName, "\r\n") {
		return SMTP{}, errors.New("SMTP_FROM_NAME must not contain line breaks")
	}
	if s.FromName == "" {
		s.FromName = "GameDev"
	}
	return s, nil
}

func parseAuth(getenv func(string) string, c Config) (Auth, error) {
	a := Auth{
		AccessTTL:            24 * time.Hour,
		RefreshTTL:           30 * 24 * time.Hour,
		CodeTTL:              15 * time.Minute,
		CodeAttempts:         5,
		RequireVerifiedEmail: true,
	}
	var err error
	if a.AccessTTL, err = durationSeconds(getenv, "AUTH_ACCESS_TTL_SECONDS", a.AccessTTL, time.Minute, 30*24*time.Hour); err != nil {
		return Auth{}, err
	}
	if a.RefreshTTL, err = durationSeconds(getenv, "AUTH_REFRESH_TTL_SECONDS", a.RefreshTTL, time.Hour, 365*24*time.Hour); err != nil {
		return Auth{}, err
	}
	if a.CodeTTL, err = durationSeconds(getenv, "AUTH_CODE_TTL_SECONDS", a.CodeTTL, time.Minute, 24*time.Hour); err != nil {
		return Auth{}, err
	}
	if raw := strings.TrimSpace(getenv("AUTH_CODE_ATTEMPTS")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 20 {
			return Auth{}, errors.New("AUTH_CODE_ATTEMPTS must be 1..20")
		}
		a.CodeAttempts = n
	}
	if a.RefreshTTL < a.AccessTTL {
		return Auth{}, errors.New("AUTH_REFRESH_TTL_SECONDS must not be shorter than AUTH_ACCESS_TTL_SECONDS")
	}
	if raw := strings.TrimSpace(getenv("AUTH_REQUIRE_VERIFIED_EMAIL")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return Auth{}, errors.New("AUTH_REQUIRE_VERIFIED_EMAIL must be a boolean")
		}
		if !value && c.Env != "development" {
			return Auth{}, errors.New("AUTH_REQUIRE_VERIFIED_EMAIL may be disabled only in development")
		}
		a.RequireVerifiedEmail = value
	}
	return a, nil
}

func parsePipeline(getenv func(string) string) (Pipeline, error) {
	p := Pipeline{Workers: 2, MaxRepairs: 2, StepTimeout: 330 * time.Second, JobTimeout: 30 * time.Minute}
	if raw := strings.TrimSpace(getenv("AI_PIPELINE_WORKERS")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 32 {
			return Pipeline{}, errors.New("AI_PIPELINE_WORKERS must be 1..32")
		}
		p.Workers = n
	}
	if raw := strings.TrimSpace(getenv("AI_MAX_REPAIRS")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 5 {
			return Pipeline{}, errors.New("AI_MAX_REPAIRS must be 0..5")
		}
		p.MaxRepairs = n
	}
	var err error
	if p.StepTimeout, err = durationSeconds(getenv, "AI_STEP_TIMEOUT_SECONDS", p.StepTimeout, 30*time.Second, 900*time.Second); err != nil {
		return Pipeline{}, err
	}
	if p.JobTimeout, err = durationSeconds(getenv, "AI_JOB_TIMEOUT_SECONDS", p.JobTimeout, time.Minute, 2*time.Hour); err != nil {
		return Pipeline{}, err
	}
	if p.JobTimeout < p.StepTimeout {
		return Pipeline{}, errors.New("AI_JOB_TIMEOUT_SECONDS must not be shorter than AI_STEP_TIMEOUT_SECONDS")
	}
	return p, nil
}

func durationSeconds(getenv func(string) string, name string, fallback, low, high time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number of seconds", name)
	}
	value := time.Duration(n) * time.Second
	if value < low || value > high {
		return 0, fmt.Errorf("%s must be %d..%d seconds", name, int(low.Seconds()), int(high.Seconds()))
	}
	return value, nil
}
