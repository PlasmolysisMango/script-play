// Command server runs the script-play backend: REST + WebSocket API, the game
// engine actors, the LLM providers and the embedded snapshot store.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"script-play/internal/config"
	"script-play/internal/httpapi"
	"script-play/internal/importer"
	"script-play/internal/llm"
	"script-play/internal/realtime"
	"script-play/internal/rooms"
	"script-play/internal/store"
)

func main() {
	graceful := flag.Duration("shutdown-grace", 10*time.Second, "how long to wait for in-flight requests")
	flag.Parse()
	if err := run(*graceful); err != nil {
		slog.Error("server exited with error", "err", err)
		os.Exit(1)
	}
}

func run(grace time.Duration) error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	reg := buildRegistry(cfg)
	if err := httpapi.EnsureAdmin(cfg, st); err != nil {
		// A misconfigured admin must not keep the whole game server down: users
		// can still play, only the console stays locked.
		slog.Warn("admin bootstrap failed", "err", err)
	}

	// The root context is cancelled on SIGINT/SIGTERM and stops every actor.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hub := realtime.NewHub()
	manager := rooms.NewManager(ctx, st, hub, reg, cfg)
	defer manager.Close()

	imp := importer.New(st)
	api := httpapi.New(cfg, st, reg, manager, imp)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No global write timeout: WebSocket connections are long lived.
		IdleTimeout: 120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("script-play listening",
			"addr", cfg.Addr,
			"data", cfg.DataDir,
			"web", cfg.WebDir,
			"provider", cfg.LLM.Default,
			"providers", reg.Names(),
			"models", cfg.LLM.Qoder.Model,
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		cancel()
		return fmt.Errorf("listen: %w", err)
	case sig := <-stop:
		slog.Info("shutting down", "signal", sig.String())
	}

	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), grace)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("graceful shutdown incomplete", "err", err)
	}
	if err := st.Flush(); err != nil {
		slog.Warn("final flush failed", "err", err)
	}
	slog.Info("bye")
	return nil
}

// buildRegistry registers every provider that is actually usable on this host.
// The mock provider is always present so CI and offline demos can run a game.
func buildRegistry(cfg *config.Config) *llm.Registry {
	reg := llm.NewRegistry(cfg.LLM.Default, cfg.LLM.MaxConcurrent)
	reg.Register(llm.NewMock())

	if cfg.LLM.OpenAI.BaseURL != "" {
		reg.Register(llm.NewOpenAI(
			cfg.LLM.OpenAI.BaseURL,
			cfg.LLM.OpenAI.APIKey,
			cfg.LLM.OpenAI.Model,
			cfg.LLM.OpenAI.VisionModel,
			cfg.LLM.OpenAI.Timeout,
		))
	} else {
		slog.Info("openai provider disabled: SP_OPENAI_BASE_URL is empty")
	}

	if bin, err := lookQoder(cfg.LLM.Qoder.Bin); err != nil {
		slog.Warn("qodercli provider disabled", "bin", cfg.LLM.Qoder.Bin, "err", err)
		if reg.Default() == "qodercli" {
			slog.Warn("falling back to mock provider; set SP_LLM_PROVIDER to choose another")
			// Registry keeps the requested default; the engine falls back per
			// request when a name is missing, so the server still boots.
		}
	} else {
		reg.Register(llm.NewQoderCLI(bin, cfg.LLM.Qoder.Model, cfg.LLM.Qoder.Permission, "", cfg.LLM.Qoder.Timeout))
	}
	return reg
}

func lookQoder(bin string) (string, error) {
	if strings.ContainsRune(bin, os.PathSeparator) {
		if info, err := os.Stat(bin); err == nil && !info.IsDir() {
			return bin, nil
		}
		return "", fmt.Errorf("%s is not an executable file", bin)
	}
	return exec.LookPath(bin)
}
