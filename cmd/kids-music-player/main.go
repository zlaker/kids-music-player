package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"kids-music-player/internal/app"
)

func main() {
	os.Exit(run())
}

func run() int {
	musicDir := flag.String("music", "", "directory with mp3 albums (required)")
	listen := flag.String("listen", ":21983", "HTTP listen address")
	stateDir := flag.String("state-dir", "", "directory for playlists and history (default ~/.kids-music-player)")
	flag.Parse()

	if *musicDir == "" {
		fmt.Fprintln(os.Stderr, "usage: kids-music-player -music /path/to/music [-listen :21983] [-state-dir DIR]")
		return 2
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx, logger, app.Config{
		MusicDir: *musicDir,
		Listen:   *listen,
		StateDir: *stateDir,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "kids-music-player: %v\n", err)
		return 1
	}
	return 0
}
