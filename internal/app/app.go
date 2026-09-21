package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"kids-music-player/internal/httpserver"
	"kids-music-player/internal/library"
	"kids-music-player/internal/meta"
	"kids-music-player/internal/player"
	"kids-music-player/internal/store"
)

type Config struct {
	MusicDir string
	Listen   string
	StateDir string
}

func Run(ctx context.Context, logger *slog.Logger, cfg Config) error {
	lib, err := library.Open(cfg.MusicDir)
	if err != nil {
		return err
	}
	defer lib.Close()

	stateDir := cfg.StateDir
	if stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("home dir: %w", err)
		}
		stateDir = filepath.Join(home, ".kids-music-player")
	}
	st, err := store.Open(stateDir)
	if err != nil {
		return err
	}

	tags := meta.New(lib)
	pl := player.New()
	pl.OnStart = func(track player.Track) {
		if err := st.AddHistory(store.HistoryItem{
			Path:     track.Path,
			Title:    track.Title,
			Artist:   track.Artist,
			PlayedAt: time.Now(),
		}); err != nil {
			logger.Error("save history", slog.Any("err", err))
		}
	}

	handler := httpserver.New(lib, tags, st, pl, logger).Handler()
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", slog.String("addr", cfg.Listen), slog.String("music", lib.Root()), slog.String("state", stateDir))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("listen: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	logger.Info("stopped")
	return nil
}
