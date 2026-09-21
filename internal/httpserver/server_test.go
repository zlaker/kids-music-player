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

type playlistAPI struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Tracks []playlistTrack `json:"tracks"`
}

func doJSON(t *testing.T, h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodePlaylist(t *testing.T, rec *httptest.ResponseRecorder) playlistAPI {
	t.Helper()
	var pl playlistAPI
	if err := json.Unmarshal(rec.Body.Bytes(), &pl); err != nil {
		t.Fatalf("decode playlist: %v body=%s", err, rec.Body.String())
	}
	return pl
}

func TestPlaylistFlow(t *testing.T) {
	s, root := testServer(t)
	h := s.Handler()

	rec := doJSON(t, h, http.MethodPost, "/api/playlists", `{"name":"Вечер","tracks":["Альбом/one.mp3","Альбом/two.mp3"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status %d %s", rec.Code, rec.Body.String())
	}
	created := decodePlaylist(t, rec)
	if created.ID == "" || created.Name != "Вечер" {
		t.Fatalf("create body = %s", rec.Body.String())
	}
	if created.Tracks == nil || len(created.Tracks) != 0 {
		t.Fatalf("playlist must be created empty, got %s", rec.Body.String())
	}

	rec = doJSON(t, h, http.MethodGet, "/api/playlists/"+created.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status %d %s", rec.Code, rec.Body.String())
	}
	opened := decodePlaylist(t, rec)
	if len(opened.Tracks) != 0 {
		t.Fatalf("open empty playlist = %s", rec.Body.String())
	}

	rec = doJSON(t, h, http.MethodPost, "/api/playlists/"+created.ID+"/tracks", `{"paths":["Альбом/one.mp3"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("add status %d %s", rec.Code, rec.Body.String())
	}
	added := decodePlaylist(t, rec)
	if len(added.Tracks) != 1 || added.Tracks[0].Path != "Альбом/one.mp3" || added.Tracks[0].Type != "track" {
		t.Fatalf("after add one = %s", rec.Body.String())
	}
	if added.Tracks[0].Title == "" || added.Tracks[0].Name == "" {
		t.Fatalf("open playlist must list files with names, got %s", rec.Body.String())
	}

	rec = doJSON(t, h, http.MethodPost, "/api/playlists/"+created.ID+"/tracks", `{"path":"Альбом/two.mp3"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("add second status %d %s", rec.Code, rec.Body.String())
	}
	added = decodePlaylist(t, rec)
	if len(added.Tracks) != 2 {
		t.Fatalf("after add two = %s", rec.Body.String())
	}

	rec = doJSON(t, h, http.MethodPost, "/api/playlists/"+created.ID+"/tracks", `{"paths":["Альбом/one.mp3"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("dup add status %d %s", rec.Code, rec.Body.String())
	}
	if got := decodePlaylist(t, rec); len(got.Tracks) != 2 {
		t.Fatalf("duplicate add changed list: %s", rec.Body.String())
	}

	rec = doJSON(t, h, http.MethodGet, "/api/playlists/"+created.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("open after add status %d %s", rec.Code, rec.Body.String())
	}
	listed := decodePlaylist(t, rec)
	if len(listed.Tracks) != 2 {
		t.Fatalf("open file list = %s", rec.Body.String())
	}
	if listed.Tracks[0].Path != "Альбом/one.mp3" || listed.Tracks[1].Path != "Альбом/two.mp3" {
		t.Fatalf("file order = %s", rec.Body.String())
	}

	s.player.SetRandom(false)
	rec = doJSON(t, h, http.MethodPost, "/api/playlists/"+created.ID+"/play", `{"path":"Альбом/two.mp3"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("play from list status %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"path":"Альбом/two.mp3"`) {
		t.Fatalf("should start at tapped file, got %s", rec.Body.String())
	}

	rec = doJSON(t, h, http.MethodPost, "/api/playlists/"+created.ID+"/tracks", `{"paths":["../secret.mp3"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("traversal add status %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodPost, "/api/playlists/"+created.ID+"/tracks", `{"paths":["Альбом"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("folder add status %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodPost, "/api/playlists/"+created.ID+"/tracks", `{"paths":["missing.mp3"]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing add status %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodPost, "/api/playlists/"+created.ID+"/tracks", `{"paths":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty add status %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, h, http.MethodDelete, "/api/playlists/"+created.ID+"/tracks/Альбом/one.mp3", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("remove status %d %s", rec.Code, rec.Body.String())
	}
	if got := decodePlaylist(t, rec); len(got.Tracks) != 1 || got.Tracks[0].Path != "Альбом/two.mp3" {
		t.Fatalf("after remove = %s", rec.Body.String())
	}

	if err := os.Remove(filepath.Join(root, "Альбом", "two.mp3")); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, h, http.MethodGet, "/api/playlists/"+created.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("open missing file status %d %s", rec.Code, rec.Body.String())
	}
	missing := decodePlaylist(t, rec)
	if len(missing.Tracks) != 1 || missing.Tracks[0].Path != "Альбом/two.mp3" || !missing.Tracks[0].Missing {
		t.Fatalf("missing file should stay in the list, got %s", rec.Body.String())
	}

	rec = doJSON(t, h, http.MethodPost, "/api/playlists/"+created.ID+"/play", `{}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("play missing-only playlist status %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, h, http.MethodDelete, "/api/playlists/"+created.ID+"/tracks/Альбом/two.mp3", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("remove last status %d %s", rec.Code, rec.Body.String())
	}
	if got := decodePlaylist(t, rec); got.Tracks == nil || len(got.Tracks) != 0 {
		t.Fatalf("empty after last remove = %s", rec.Body.String())
	}

	rec = doJSON(t, h, http.MethodGet, "/api/playlists/nope", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing playlist status %d %s", rec.Code, rec.Body.String())
	}
}

func TestPlaylistClientFlow(t *testing.T) {
	s, _ := testServer(t)
	h := s.Handler()

	rec := doJSON(t, h, http.MethodGet, "/app.js", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("app.js status %d", rec.Code)
	}
	js := rec.Body.String()
	if strings.Contains(js, "body: { name, tracks }") {
		t.Fatal("creating a playlist must not send tracks")
	}
	for _, want := range []string{
		"startAddTracks",
		"openPlaylist",
		"addTrackPaths",
		"/tracks",
		"body: { name }",
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("app.js missing playlist flow %q", want)
		}
	}

	rec = doJSON(t, h, http.MethodGet, "/", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("index status %d", rec.Code)
	}
	html := rec.Body.String()
	for _, want := range []string{
		`startAddTracks([item.path])`,
		`panel === 'playlist'`,
		`currentPlaylist.tracks`,
		`Пока нет треков`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("index.html missing playlist UI %q", want)
		}
	}
}
