package httpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"kids-music-player/internal/auth"
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

func TestLoginCookieIdentifiesTheUser(t *testing.T) {
	s, _ := testServer(t)
	accounts, err := auth.Open(filepath.Join(t.TempDir(), "database.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = accounts.Close() })
	accounts.SetCostForTest(bcrypt.MinCost)
	if err := accounts.Seed("дом", "секрет"); err != nil {
		t.Fatal(err)
	}
	if err := s.UseAccounts(accounts); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("page without a session: %d %s", rec.Code, rec.Header().Get("Location"))
	}

	req = httptest.NewRequest(http.MethodGet, "/api/player", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("api without a session: %d", rec.Code)
	}

	form := url.Values{"name": {"дом"}, "password": {"не тот"}}
	req = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", rec.Code)
	}

	form.Set("password", "секрет")
	req = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	cookie := rec.Result().Cookies()
	if len(cookie) != 1 || cookie[0].Name != auth.CookieName || !cookie[0].HttpOnly {
		t.Fatalf("cookie = %#v", cookie)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(cookie[0])
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"дом"`) {
		t.Fatalf("me: %d %s", rec.Code, rec.Body.String())
	}

	if err := accounts.SetStatus("дом", auth.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/player", nil)
	req.AddCookie(cookie[0])
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("disabled account still entered: %d", rec.Code)
	}
}

func TestInstallIcons(t *testing.T) {
	s, _ := testServer(t)
	h := s.Handler()
	for _, path := range []string{"/manifest.webmanifest", "/icon-192.png", "/icon-512.png", "/apple-touch-icon.png", "/favicon-32.png"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status %d", path, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/manifest+json") {
		t.Fatalf("manifest content-type %q", got)
	}
	if !strings.Contains(rec.Body.String(), `"name": "Детский плеер"`) {
		t.Fatalf("manifest = %s", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/icon-192.png", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte("\x89PNG")) {
		t.Fatal("icon is not a png")
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `rel="manifest"`) || !strings.Contains(rec.Body.String(), "/apple-touch-icon.png") {
		t.Fatal("index is missing install links")
	}
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

func TestPlayFolderResumesSavedTrack(t *testing.T) {
	s, _ := testServer(t)
	h := s.Handler()
	if err := s.store.SaveProgress("Альбом", "Альбом/two.mp3", 12.5, 40, true); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, h, http.MethodPost, "/api/player/play", `{"folder":"Альбом"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("play status %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"path":"Альбом/two.mp3"`) || !strings.Contains(rec.Body.String(), `"position":12.5`) {
		t.Fatalf("did not resume: %s", rec.Body.String())
	}

	lib := doJSON(t, h, http.MethodGet, "/api/library", "")
	if !strings.Contains(lib.Body.String(), `"continue"`) || !strings.Contains(lib.Body.String(), "Альбом/two.mp3") {
		t.Fatalf("home has no continue card: %s", lib.Body.String())
	}
	inside := doJSON(t, h, http.MethodGet, "/api/library?path="+url.QueryEscape("Альбом"), "")
	if !strings.Contains(inside.Body.String(), `"progress"`) {
		t.Fatalf("album has no progress: %s", inside.Body.String())
	}
}

func TestPlayAcceptsFolderAsPath(t *testing.T) {
	s, _ := testServer(t)
	h := s.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/player/play", strings.NewReader(`{"path":"Альбом"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("play album by path: status %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Альбом/") {
		t.Fatalf("no track from the album started: %s", rec.Body.String())
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
	// A saved place wins over the history skip, so the album continues where it stopped.
	if !strings.Contains(rec.Body.String(), `"path":"Альбом/one.mp3"`) {
		t.Fatalf("folder play should resume the saved track, got %s", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/player/play", strings.NewReader(`{"path":"Альбом/one.mp3"}`))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"path":"Альбом/one.mp3"`) {
		t.Fatalf("explicit replay status %d %s", rec.Code, rec.Body.String())
	}
}

func TestPrevAtStartDoesNotStop(t *testing.T) {
	s, _ := testServer(t)
	s.player.SetRandom(false)
	h := s.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/player/play", strings.NewReader(`{"path":"Альбом/one.mp3"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("play %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/player/prev", strings.NewReader(`{"path":"Альбом/one.mp3"}`))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("prev status %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "в эту сторону") {
		t.Fatalf("prev body %s", rec.Body.String())
	}
	if !s.player.Snapshot().Playing {
		t.Fatal("prev at start stopped playback")
	}
}

func TestStreamMP3AndRange(t *testing.T) {
	s, root := testServer(t)
	h := s.Handler()

	notes := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(notes, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/stream/notes.txt", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-mp3 status %d %s", rec.Code, rec.Body.String())
	}

	body := []byte("0123456789abcdef")
	if err := os.WriteFile(filepath.Join(root, "Альбом", "one.mp3"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/stream/"+url.PathEscape("Альбом")+"/one.mp3", nil)
	req.Header.Set("Range", "bytes=0-3")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("range status %d %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "0123" {
		t.Fatalf("range body %q", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 0-3/16" {
		t.Fatalf("content-range %q", got)
	}
}

func TestStreamFullBodyOverTCP(t *testing.T) {
	s, root := testServer(t)
	body := bytes.Repeat([]byte("abcdefgh"), 200)
	if err := os.WriteFile(filepath.Join(root, "Альбом", "one.mp3"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/api/stream/" + url.PathEscape("Альбом") + "/one.mp3")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := res.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	got, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(body) || string(got) != string(body) {
		t.Fatalf("body len %d, want %d", len(got), len(body))
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/stream/"+url.PathEscape("Альбом")+"/one.mp3", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=10-19")
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := res2.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
	if res2.StatusCode != http.StatusPartialContent {
		t.Fatalf("range status %d", res2.StatusCode)
	}
	part, err := io.ReadAll(res2.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(part) != string(body[10:20]) {
		t.Fatalf("range body %q", part)
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
