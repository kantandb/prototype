package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const shutdownTimeout = 10 * time.Second

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		log.Error("configuration failed", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, log); err != nil {
		log.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config, log *slog.Logger) (runErr error) {
	store, err := openStore(cfg.dataPath)
	if err != nil {
		return fmt.Errorf("opening storage: %w", err)
	}
	defer func() {
		if err := store.close(); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}()

	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           newHandler(store),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 1)

	go func() {
		errCh <- srv.ListenAndServe()
	}()

	log.Info("server started", "address", cfg.addr, "data_path", cfg.dataPath, "max_body_bytes", cfg.maxBodyBytes)

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return fmt.Errorf("serving HTTP: %w", err)
	case <-ctx.Done():
		log.Info("server stopping")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		closeErr := srv.Close()

		return errors.Join(
			fmt.Errorf("shutting down HTTP server: %w", err),
			wrapCloseErr(closeErr),
		)
	}

	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("stopping HTTP server: %w", err)
	}

	log.Info("server stopped")

	return nil
}

func wrapCloseErr(err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("closing HTTP server: %w", err)
}
