package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/kailas-cloud/vecdex/internal/app"
	"github.com/kailas-cloud/vecdex/internal/config"
	logpkg "github.com/kailas-cloud/vecdex/internal/logger"
	"github.com/kailas-cloud/vecdex/internal/server"
	"github.com/kailas-cloud/vecdex/internal/version"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	env := config.GetEnv()
	cfg, err := config.Load(env)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	logger, err := logpkg.NewLogger(env, cfg.Logging.Level)
	if err != nil {
		return fmt.Errorf("create logger: %w", err)
	}
	defer func() { _ = logger.Sync() }()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	runtime, err := app.New(ctx, &cfg, logger)
	if err != nil {
		return fmt.Errorf("initialize engine: %w", err)
	}
	defer runtime.Close()
	handler, err := server.New(runtime, cfg.Auth, logger)
	if err != nil {
		return fmt.Errorf("initialize MCP: %w", err)
	}
	defer func() { _ = handler.Close() }()
	httpServer := &http.Server{
		Addr: fmt.Sprintf(":%d", cfg.HTTP.Port), Handler: handler,
		ReadHeaderTimeout: time.Duration(cfg.HTTP.ReadTimeoutSec) * time.Second,
		IdleTimeout:       90 * time.Second,
		// A subscriptions/listen response can outlive an ordinary request deadline.
		// MCP handlers own per-operation cancellation and deadlines.
	}
	failures := make(chan error, 1)
	go func() { failures <- httpServer.ListenAndServe() }()
	logger.Info("Vecdex MCP started", zap.String("version", version.Version), zap.Int("port", cfg.HTTP.Port))
	var serveError error
	select {
	case <-ctx.Done():
	case err = <-failures:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server stopped", zap.Error(err))
			serveError = fmt.Errorf("serve HTTP: %w", err)
		}
	}
	_ = handler.Close()
	shutdown, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.HTTP.ShutdownSec)*time.Second)
	defer cancel()
	if err = httpServer.Shutdown(shutdown); err != nil {
		logger.Error("HTTP shutdown", zap.Error(err))
		return errors.Join(serveError, fmt.Errorf("shutdown HTTP: %w", err))
	}
	return serveError
}
