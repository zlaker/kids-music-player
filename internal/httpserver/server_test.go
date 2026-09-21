package httpserver

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kids-music-player/internal/library"
	"kids-music-player/internal/meta"
	"kids-music-player/internal/player"
	"kids-music-player/internal/store"
)

func testServer(t *testing.T) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	album := filepath.Join(root, "Альбом")
	if err := os.Mkdir(album, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(album, "one.mp3"), []byte("id3"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(album, "two.mp3"), []byte("id3"), 0o600); err != nil {
		t.Fatal(err)
	}
	lib, err := library.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lib.Close() })
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := New(lib, meta.New(lib), st, player.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	return s, root
}

func TestLibraryAndTraversal(t *testing.T) {
	s, _ := testServer(t)
	h := s.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/library?path=Альбом", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("library status %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %#v", body["items"])
	}

	req = httptest.NewRequest(http.MethodGet, "/api/library?path=../secret", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("traversal status %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/stream/escape", nil)
	req.SetPathValue("path", "../secret.mp3")
	rec = httptest.NewRecorder()
	s.handleStream(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("stream escape status %d %s", rec.Code, rec.Body.String())
	}
}

func TestPlayAndHistory(t *testing.T) {
	s, _ := testServer(t)
	s.player.OnStart = func(track player.Track) {
		_ = s.store.AddHistory(store.HistoryItem{Path: track.Path, Title: track.Title})
	}
	h := s.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/player/play", strings.NewReader(`{"path":"Альбом/one.mp3"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("play status %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/history", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Альбом/one.mp3") {
		t.Fatalf("history = %s", rec.Body.String())
	}

	s.player.SetRandom(false)
	req = httptest.NewRequest(http.MethodPost, "/api/player/play", strings.NewReader(`{"folder":"Альбом"}`))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("play folder status %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"path":"Альбом/one.mp3"`) {
		t.Fatalf("should skip history track, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Альбом/two.mp3") {
		t.Fatalf("expected remaining track, got %s", rec.Body.String())
	}
}
