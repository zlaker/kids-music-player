package store

import (
	"errors"
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
