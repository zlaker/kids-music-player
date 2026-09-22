package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryAndPlaylistRoundTrip(t *testing.T) {
	t.Parallel()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if err := s.AddHistory(HistoryItem{Path: "a.mp3", Title: "A", PlayedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got := s.History(); len(got) != 1 || got[0].Title != "A" {
		t.Fatalf("history = %#v", got)
	}

	pl, err := s.CreatePlaylist("Сказки")
	if err != nil {
		t.Fatal(err)
	}
	if len(pl.Tracks) != 0 {
		t.Fatalf("new playlist must be empty, got %#v", pl.Tracks)
	}
	pl, err = s.AddTracks(pl.ID, []string{"a.mp3", "b.mp3"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplacePlaylist(pl.ID, "", []string{"a.mp3"}); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Playlist(pl.ID)
	if !ok || len(got.Tracks) != 1 {
		t.Fatalf("playlist = %#v", got)
	}
	if err := s.ClearHistory(); err != nil {
		t.Fatal(err)
	}
	if len(s.History()) != 0 {
		t.Fatal("history not cleared")
	}
	if err := s.DeletePlaylist(pl.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Playlist(pl.ID); ok {
		t.Fatal("playlist still there")
	}
}

func TestPlaylistAddRemove(t *testing.T) {
	t.Parallel()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	pl, err := s.CreatePlaylist("Вечер")
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.AddTracks(pl.ID, []string{"a.mp3", "a.mp3", "b.mp3", ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tracks) != 2 || got.Tracks[0] != "a.mp3" || got.Tracks[1] != "b.mp3" {
		t.Fatalf("tracks after add = %#v", got.Tracks)
	}

	got, err = s.RemoveTrack(pl.ID, "a.mp3")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tracks) != 1 || got.Tracks[0] != "b.mp3" {
		t.Fatalf("tracks after remove = %#v", got.Tracks)
	}

	if _, err := s.RemoveTrack(pl.ID, "missing.mp3"); !errors.Is(err, ErrTrackNotFound) {
		t.Fatalf("missing track err = %v", err)
	}
	if _, err := s.AddTracks("nope", []string{"a.mp3"}); !errors.Is(err, ErrPlaylistNotFound) {
		t.Fatalf("missing playlist add err = %v", err)
	}
	if _, err := s.RemoveTrack("nope", "a.mp3"); !errors.Is(err, ErrPlaylistNotFound) {
		t.Fatalf("missing playlist remove err = %v", err)
	}

	got, err = s.RemoveTrack(pl.ID, "b.mp3")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tracks == nil || len(got.Tracks) != 0 {
		t.Fatalf("empty playlist tracks = %#v", got.Tracks)
	}
}

func TestHistoryDedupKeepsSkipSetBeyondDisplayCap(t *testing.T) {
	t.Parallel()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddHistory(HistoryItem{Path: "a.mp3", Title: "A", PlayedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddHistory(HistoryItem{Path: "a.mp3", Title: "A2", PlayedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got := s.History(); len(got) != 1 || got[0].Title != "A2" {
		t.Fatalf("dedup history = %#v", got)
	}

	for i := 0; i < maxHistoryList+1; i++ {
		item := HistoryItem{Path: fmt.Sprintf("t%03d.mp3", i), PlayedAt: time.Now()}
		if err := s.AddHistory(item); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(s.History()); got != maxHistoryList {
		t.Fatalf("display history = %d", got)
	}
	// The first displayed row is the newest path. The oldest unique path stays
	// in the skip set even though the screen does not list it.
	if _, ok := s.HistorySet()["t000.mp3"]; !ok {
		t.Fatal("oldest track fell out of the skip set")
	}
	if _, ok := s.HistorySet()["a.mp3"]; !ok {
		t.Fatal("earlier track fell out of the skip set")
	}
}

func TestHistoryDedupeOnOpen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	raw := []HistoryItem{
		{Path: "a.mp3", Title: "new"},
		{Path: "a.mp3", Title: "old"},
		{Path: "b.mp3", Title: "b"},
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "history.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := s.History()
	if len(got) != 2 || got[0].Title != "new" || got[1].Path != "b.mp3" {
		t.Fatalf("history = %#v", got)
	}
	if _, ok := s.HistorySet()["a.mp3"]; !ok {
		t.Fatal("missing skip path")
	}
}

func TestOpenSucceedsOnReadOnlyStateDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	raw := []HistoryItem{{Path: "a.mp3"}, {Path: "a.mp3"}}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "history.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o550); err != nil { //nolint:gosec // G302: directory mode is the fixture
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o750); err != nil { //nolint:gosec // G302: restore so the harness can delete it
			t.Error(err)
		}
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open read-only state dir: %v", err)
	}
	if got := s.History(); len(got) != 1 {
		t.Fatalf("history = %#v", got)
	}
}

func TestClearHistoryRollsBackWhenSaveFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddHistory(HistoryItem{Path: "a.mp3", Title: "A", PlayedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// 0550 drops the write bit so the next save must fail and roll back.
	if err := os.Chmod(dir, 0o550); err != nil { //nolint:gosec // G302: directory mode is the fixture, not a secret file
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o750); err != nil { //nolint:gosec // G302: restore the temp dir so the test harness can delete it
			t.Error(err)
		}
	})
	if err := s.ClearHistory(); err == nil {
		t.Fatal("expected save error")
	}
	if got := s.History(); len(got) != 1 || got[0].Path != "a.mp3" {
		t.Fatalf("history after failed clear = %#v", got)
	}
}
