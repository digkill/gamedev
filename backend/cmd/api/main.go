package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/digkill/gamedev/backend/internal/agents"
	"github.com/digkill/gamedev/backend/internal/ai"
	"github.com/digkill/gamedev/backend/internal/auth"
	"github.com/digkill/gamedev/backend/internal/platform/config"
	"github.com/digkill/gamedev/backend/internal/platform/httpapi"
	"github.com/digkill/gamedev/backend/internal/projects"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid config", "error", err)
		os.Exit(1)
	}
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		logger.Error("invalid DATABASE_URL")
		os.Exit(1)
	}
	poolCfg.MaxConns = cfg.DBMaxConns
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err == nil {
		err = pool.Ping(ctx)
	}
	cancel()
	if err != nil {
		logger.Error("database unavailable", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	store := projects.Store{DB: pool}
	if hash := strings.TrimSpace(os.Getenv("BOOTSTRAP_TOKEN_SHA256")); hash != "" {
		bootCtx, bootCancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = store.EnsureBootstrapToken(bootCtx, hash)
		bootCancel()
		if err != nil {
			logger.Error("bootstrap token failed")
			os.Exit(1)
		}
	}

	// kie.ai is preferred when configured: it fronts Claude with a GPT
	// fallback. A direct OpenAI key still works when it is not.
	chat := modelChat(cfg)
	provider := ai.FromChat(chat)
	if !provider.Configured() {
		model := cfg.OpenAIModel
		if model == "" && cfg.Env == "development" && cfg.OpenAIAPIKey != "" {
			model = "gpt-4.1-mini"
		}
		provider = ai.New(cfg.OpenAIAPIKey, model)
	}

	authService := &auth.Service{
		Store:  auth.Store{DB: pool},
		Mailer: auth.NewMailer(auth.SMTPSettings(cfg.SMTP)),
		Settings: auth.Settings{
			AccessTTL:            cfg.Auth.AccessTTL,
			RefreshTTL:           cfg.Auth.RefreshTTL,
			CodeTTL:              cfg.Auth.CodeTTL,
			CodeAttempts:         cfg.Auth.CodeAttempts,
			RequireVerifiedEmail: cfg.Auth.RequireVerifiedEmail,
			ProductName:          "GameDev",
		},
		NewID: projects.NewUUID,
	}

	agentStore := agents.Store{DB: pool}
	worker := &agents.Worker{
		Store: agentStore,
		Pipeline: agents.Pipeline{
			Store:       agentStore,
			Projects:    store,
			Chat:        chat,
			MaxRepairs:  cfg.Pipeline.MaxRepairs,
			StepTimeout: cfg.Pipeline.StepTimeout,
			NewID:       projects.NewUUID,
		},
		Count:      cfg.Pipeline.Workers,
		JobTimeout: cfg.Pipeline.JobTimeout,
	}

	api := httpapi.API{
		DB:                pool,
		Projects:          store,
		AI:                provider,
		Auth:              authService,
		Agents:            agentStore,
		Worker:            worker,
		TrustProxyHeaders: boolEnv("TRUST_PROXY_HEADERS"),
	}
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      180 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	waitForWorkers := worker.Start(sigCtx)
	go housekeeping(sigCtx, authService)
	go func() {
		<-sigCtx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	logger.Info("API listening",
		"address", cfg.HTTPAddr,
		"ai_provider", providerName(cfg, provider),
		"smtp_configured", cfg.SMTP.Configured(),
		"pipeline_workers", cfg.Pipeline.Workers)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
	waitForWorkers()
}

func modelChat(cfg config.Config) ai.Chat {
	if !cfg.Kie.Configured() {
		return nil
	}
	return ai.NewKieChat(ai.KieSettings{
		APIKey:          cfg.Kie.APIKey,
		BaseURL:         cfg.Kie.BaseURL,
		Model:           cfg.Kie.Model,
		FallbackModel:   cfg.Kie.FallbackModel,
		ReasoningEffort: cfg.Kie.ReasoningEffort,
		MaxTokens:       cfg.Kie.MaxTokens,
		Timeout:         cfg.Kie.Timeout,
	})
}

func providerName(cfg config.Config, provider ai.Provider) string {
	switch {
	case !provider.Configured():
		return "none"
	case cfg.Kie.Configured():
		return "kie:" + cfg.Kie.Model + " -> " + cfg.Kie.FallbackModel
	default:
		return "openai"
	}
}

// housekeeping deletes expired codes, tokens, and rate-limit rows.
func housekeeping(ctx context.Context, service *auth.Service) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		purgeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := service.Purge(purgeCtx); err != nil && ctx.Err() == nil {
			slog.Warn("auth housekeeping failed", "error", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func boolEnv(name string) bool {
	value, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(name)))
	return err == nil && value
}
