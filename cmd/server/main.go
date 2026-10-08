// HTTP 服务入口。
//
//	export DEEPSEEK_API_KEY=...
//	go run ./cmd/server
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Zer0Rin/mini-agent/internal/agent"
	"github.com/Zer0Rin/mini-agent/internal/config"
	"github.com/Zer0Rin/mini-agent/internal/llm"
	"github.com/Zer0Rin/mini-agent/internal/server"
	"github.com/Zer0Rin/mini-agent/internal/store"
	"github.com/Zer0Rin/mini-agent/internal/tool"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "server exited:", err)
		os.Exit(1)
	}
}

func run() error {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := openStore(ctx, cfg)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	model := &llm.OpenAICompat{BaseURL: cfg.ModelBaseURL, APIKey: cfg.ModelAPIKey, Model: cfg.ModelName}
	srv := server.New(server.Options{
		Store: st,
		AgentConfig: func() agent.Config {
			return agent.Config{
				Model:        model,
				SystemPrompt: cfg.SystemPrompt,
				Tools:        []agent.Tool{tool.Datetime()},
				MaxTurns:     cfg.MaxTurns,
			}
		},
		Token:  cfg.Token,
		Logger: log,
	})

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// 不设置 WriteTimeout：SSE 是长连接，靠请求的 ctx 和 Agent 的 MaxTurns 控制时长
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "store", cfg.Store, "model", cfg.ModelName, "auth", cfg.Token != "")
		errCh <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	// 优雅关停：最多等 10 秒让进行中的请求结束
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func openStore(ctx context.Context, cfg config.Config) (store.Store, error) {
	if cfg.Store == "memory" {
		return store.NewMemory(), nil
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open(sqliteDriver, cfg.DBPath)
	if err != nil {
		return nil, err
	}
	return store.NewSQLite(ctx, db)
}
