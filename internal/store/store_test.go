package store

import (
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

	pl, err := s.CreatePlaylist("Сказки", []string{"a.mp3", "b.mp3"})
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
