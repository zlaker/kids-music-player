package httpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"kids-music-player/internal/library"
	"kids-music-player/internal/meta"
	"kids-music-player/internal/player"
	"kids-music-player/internal/store"
	"kids-music-player/web"
)

type Server struct {
	lib    *library.Library
	meta   *meta.Reader
	store  *store.Store
	player *player.Player
	logger *slog.Logger
}

func New(lib *library.Library, tags *meta.Reader, st *store.Store, pl *player.Player, logger *slog.Logger) *Server {
	return &Server{lib: lib, meta: tags, store: st, player: pl, logger: logger}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.Handle("GET /app.js", staticFile("app.js", ""))
	mux.Handle("GET /app.css", staticFile("app.css", ""))
	mux.Handle("GET /alpine.min.js", staticFile("alpine.min.js", ""))
	mux.Handle("GET /manifest.webmanifest", staticFile("manifest.webmanifest", "application/manifest+json"))
	mux.Handle("GET /icon-192.png", staticFile("icon-192.png", "image/png"))
	mux.Handle("GET /icon-512.png", staticFile("icon-512.png", "image/png"))
	mux.Handle("GET /apple-touch-icon.png", staticFile("apple-touch-icon.png", "image/png"))
	mux.Handle("GET /favicon-32.png", staticFile("favicon-32.png", "image/png"))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := io.WriteString(w, "ok"); err != nil {
			return
		}
	})

	mux.HandleFunc("GET /api/library", s.handleLibrary)
	mux.HandleFunc("GET /api/stream/{path...}", s.handleStream)
	mux.HandleFunc("GET /api/covers/{path...}", s.handleCover)
	mux.HandleFunc("GET /api/player", s.handlePlayerGet)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("POST /api/player/play", s.handlePlay)
	mux.HandleFunc("POST /api/player/pause", s.handlePause)
	mux.HandleFunc("POST /api/player/resume", s.handleResume)
	mux.HandleFunc("POST /api/player/seek", s.handleSeek)
	mux.HandleFunc("POST /api/player/next", s.handleNext)
	mux.HandleFunc("POST /api/player/prev", s.handlePrev)
	mux.HandleFunc("POST /api/player/position", s.handlePosition)
	mux.HandleFunc("POST /api/player/timer", s.handleTimer)
	mux.HandleFunc("POST /api/player/random", s.handleRandom)
	mux.HandleFunc("GET /api/history", s.handleHistoryGet)
	mux.HandleFunc("DELETE /api/history", s.handleHistoryClear)
	mux.HandleFunc("GET /api/playlists", s.handlePlaylistsGet)
	mux.HandleFunc("POST /api/playlists", s.handlePlaylistsCreate)
	mux.HandleFunc("GET /api/playlists/{id}", s.handlePlaylistGet)
	mux.HandleFunc("PUT /api/playlists/{id}", s.handlePlaylistUpdate)
	mux.HandleFunc("DELETE /api/playlists/{id}", s.handlePlaylistDelete)
	mux.HandleFunc("POST /api/playlists/{id}/play", s.handlePlaylistPlay)
	mux.HandleFunc("POST /api/playlists/{id}/tracks", s.handlePlaylistAddTracks)
	mux.HandleFunc("DELETE /api/playlists/{id}/tracks/{path...}", s.handlePlaylistRemoveTrack)

	return recoverer(s.logger, requestLog(s.logger, mux))
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(web.Files, "index.html")
	if err != nil {
		s.respondError(w, http.StatusInternalServerError, "не удалось открыть страницу")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write(data); err != nil {
		s.logger.Error("write index", slog.Any("err", err))
	}
}

func staticFile(name, contentType string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(web.Files, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	})
}

func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	parent, entries, err := s.lib.List(rel)
	if err != nil {
		s.libraryError(w, err)
		return
	}
	type item struct {
		Name     string `json:"name"`
		Path     string `json:"path"`
		Type     string `json:"type"`
		Title    string `json:"title,omitempty"`
		Artist   string `json:"artist,omitempty"`
		Album    string `json:"album,omitempty"`
		HasCover bool   `json:"hasCover"`
		Played   bool   `json:"played"`
	}
	played := s.store.HistorySet()
	out := make([]item, 0, len(entries))
	for _, e := range entries {
		it := item{Name: e.Name, Path: e.Path, Type: "dir"}
		if e.IsDir {
			info := s.meta.For(e.Path)
			it.HasCover = info.HasCover
			out = append(out, it)
			continue
		}
		info := s.meta.For(e.Path)
		it.Type = "track"
		it.Title = info.Title
		it.Artist = info.Artist
		it.Album = info.Album
		it.HasCover = info.HasCover
		_, it.Played = played[e.Path]
		out = append(out, it)
	}
	s.respondJSON(w, http.StatusOK, map[string]any{
		"path":   rel,
		"parent": parent,
		"items":  out,
	})
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	rel := r.PathValue("path")
	info, err := s.lib.Stat(rel)
	if err != nil {
		s.libraryError(w, err)
		return
	}
	if info.IsDir() {
		s.respondError(w, http.StatusBadRequest, "это папка")
		return
	}
	if !strings.EqualFold(path.Ext(rel), ".mp3") {
		s.respondError(w, http.StatusBadRequest, "нужен файл mp3")
		return
	}
	f, err := s.lib.Open(rel)
	if err != nil {
		s.libraryError(w, err)
		return
	}
	defer func() {
		if err := f.Close(); err != nil {
			s.logger.Error("close stream", slog.String("path", rel), slog.Any("err", err))
		}
	}()
	w.Header().Set("Content-Type", "audio/mpeg")
	http.ServeContent(w, r, path.Base(rel), info.ModTime(), f)
}

func (s *Server) handleCover(w http.ResponseWriter, r *http.Request) {
	rel := r.PathValue("path")
	cover, ok := s.meta.Cover(rel)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", cover.MIME)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	if _, err := w.Write(cover.Data); err != nil {
		return
	}
}

func (s *Server) handlePlayerGet(w http.ResponseWriter, r *http.Request) {
	s.respondJSON(w, http.StatusOK, s.player.Snapshot())
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.respondError(w, http.StatusInternalServerError, "SSE недоступен")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, cancel := s.player.Subscribe()
	defer cancel()

	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	rc := http.NewResponseController(w)

	writeEvent := func(payload string) bool {
		if err := rc.SetWriteDeadline(time.Now().Add(20 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			s.logger.Error("sse deadline", slog.Any("err", err))
		}
		if _, err := io.WriteString(w, payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			if !writeEvent(": ping\n\n") {
				return
			}
		case state, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(state)
			if err != nil {
				s.logger.Error("encode sse", slog.Any("err", err))
				continue
			}
			if !writeEvent("data: " + string(data) + "\n\n") {
				return
			}
		}
	}
}

type playRequest struct {
	Path       string   `json:"path"`
	Folder     string   `json:"folder"`
	PlaylistID string   `json:"playlistId"`
	Queue      []string `json:"queue"`
}

func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[playRequest](r)
	if err != nil {
		s.respondError(w, http.StatusBadRequest, "некорректный запрос")
		return
	}
	switch {
	case req.PlaylistID != "":
		s.playPlaylist(w, req.PlaylistID, req.Path)
	case req.Folder != "" && req.Path == "":
		s.playFolder(w, req.Folder, "")
	case req.Path != "":
		// A path may point at an album. Treating it as a track would look for
		// its parent folder and answer "no mp3 here" for a folder full of mp3.
		if info, serr := s.lib.Stat(req.Path); serr == nil && info.IsDir() {
			s.playFolder(w, req.Path, "")
			return
		}
		folder := req.Folder
		if folder == "" {
			folder, err = s.lib.FolderOf(req.Path)
			if err != nil {
				s.libraryError(w, err)
				return
			}
		}
		s.playFolder(w, folder, req.Path)
	default:
		s.respondError(w, http.StatusBadRequest, "укажите трек, папку или плейлист")
	}
}

func (s *Server) playFolder(w http.ResponseWriter, folder, start string) {
	queue, err := s.lib.TracksIn(folder)
	if err != nil {
		s.libraryError(w, err)
		return
	}
	if len(queue) == 0 {
		s.respondError(w, http.StatusNotFound, "в папке нет mp3")
		return
	}
	s.startQueue(w, queue, start, "folder:"+folder)
}

func (s *Server) playPlaylist(w http.ResponseWriter, id, start string) {
	pl, ok := s.store.Playlist(id)
	if !ok {
		s.respondError(w, http.StatusNotFound, "плейлист не найден")
		return
	}
	queue := make([]string, 0, len(pl.Tracks))
	for _, rel := range pl.Tracks {
		if _, err := s.lib.Stat(rel); err != nil {
			continue
		}
		queue = append(queue, rel)
	}
	if len(queue) == 0 {
		s.respondError(w, http.StatusNotFound, "в плейлисте нет доступных треков")
		return
	}
	s.startQueue(w, queue, start, "playlist:"+id)
}

func (s *Server) startQueue(w http.ResponseWriter, queue []string, prefer, source string) {
	index, ok := player.ChooseStart(queue, prefer, s.store.HistorySet(), s.player.Random())
	if !ok {
		s.respondError(w, http.StatusConflict, "все треки уже звучали — очистите историю")
		return
	}
	s.respondJSON(w, http.StatusOK, s.player.PlayQueue(queue, index, s.trackOf(queue[index]), source))
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	s.respondJSON(w, http.StatusOK, s.player.Pause())
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	s.respondJSON(w, http.StatusOK, s.player.Resume())
}

func (s *Server) handleSeek(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[struct {
		Seconds float64 `json:"seconds"`
	}](r)
	if err != nil {
		s.respondError(w, http.StatusBadRequest, "некорректный запрос")
		return
	}
	s.respondJSON(w, http.StatusOK, s.player.Seek(req.Seconds))
}

func (s *Server) handleNext(w http.ResponseWriter, r *http.Request) {
	s.step(w, r, 1)
}

func (s *Server) handlePrev(w http.ResponseWriter, r *http.Request) {
	s.step(w, r, -1)
}

func (s *Server) step(w http.ResponseWriter, r *http.Request, delta int) {
	req, err := decodeJSON[struct {
		Path string `json:"path"`
	}](r)
	if err != nil && !errors.Is(err, io.EOF) {
		s.respondError(w, http.StatusBadRequest, "некорректный запрос")
		return
	}
	expect := req.Path
	hist := s.store.HistorySet()
	rel, index, ok := s.player.PeekUnplayed(hist, delta)
	snap := s.player.Snapshot()
	if expect != "" && (snap.Track == nil || snap.Track.Path != expect) {
		s.respondJSON(w, http.StatusOK, snap)
		return
	}
	if !ok {
		if player.HasUnplayed(snap.Queue, hist) {
			s.respondError(w, http.StatusConflict, "в эту сторону треков больше нет")
			return
		}
		s.player.Stop()
		s.respondError(w, http.StatusConflict, "все треки уже звучали — очистите историю")
		return
	}
	// A second tab pressing next at the same time gets the state that won,
	// not a second advance.
	st, _ := s.player.PlayIfCurrent(snap.Queue, index, s.trackOf(rel), snap.Source, expect)
	s.respondJSON(w, http.StatusOK, st)
}

func (s *Server) handleRandom(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[struct {
		Random *bool `json:"random"`
	}](r)
	if err != nil || req.Random == nil {
		s.respondError(w, http.StatusBadRequest, "некорректный запрос")
		return
	}
	s.respondJSON(w, http.StatusOK, s.player.SetRandom(*req.Random))
}

func (s *Server) handlePosition(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[struct {
		Path    string  `json:"path"`
		Seconds float64 `json:"seconds"`
	}](r)
	if err != nil {
		s.respondError(w, http.StatusBadRequest, "некорректный запрос")
		return
	}
	s.player.ReportPosition(req.Path, req.Seconds)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTimer(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[struct {
		Minutes int `json:"minutes"`
	}](r)
	if err != nil {
		s.respondError(w, http.StatusBadRequest, "некорректный запрос")
		return
	}
	s.respondJSON(w, http.StatusOK, s.player.SetSleep(req.Minutes))
}

func (s *Server) handleHistoryGet(w http.ResponseWriter, r *http.Request) {
	s.respondJSON(w, http.StatusOK, map[string]any{"items": s.store.History()})
}

func (s *Server) handleHistoryClear(w http.ResponseWriter, r *http.Request) {
	if err := s.store.ClearHistory(); err != nil {
		s.logger.Error("clear history", slog.Any("err", err))
		s.respondError(w, http.StatusInternalServerError, "не удалось очистить историю")
		return
	}
	s.respondJSON(w, http.StatusOK, map[string]any{"items": []store.HistoryItem{}})
}

func (s *Server) handlePlaylistsGet(w http.ResponseWriter, r *http.Request) {
	s.respondJSON(w, http.StatusOK, map[string]any{"items": s.store.Playlists()})
}

func (s *Server) handlePlaylistsCreate(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[struct {
		Name string `json:"name"`
	}](r)
	if err != nil || strings.TrimSpace(req.Name) == "" {
		s.respondError(w, http.StatusBadRequest, "нужно имя плейлиста")
		return
	}
	pl, err := s.store.CreatePlaylist(strings.TrimSpace(req.Name))
	if err != nil {
		s.logger.Error("create playlist", slog.Any("err", err))
		s.respondError(w, http.StatusInternalServerError, "не удалось создать плейлист")
		return
	}
	s.respondJSON(w, http.StatusCreated, s.playlistPayload(pl))
}

func (s *Server) handlePlaylistGet(w http.ResponseWriter, r *http.Request) {
	pl, ok := s.store.Playlist(r.PathValue("id"))
	if !ok {
		s.respondError(w, http.StatusNotFound, "плейлист не найден")
		return
	}
	s.respondJSON(w, http.StatusOK, s.playlistPayload(pl))
}

func (s *Server) handlePlaylistUpdate(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[struct {
		Name   string   `json:"name"`
		Tracks []string `json:"tracks"`
	}](r)
	if err != nil {
		s.respondError(w, http.StatusBadRequest, "некорректный запрос")
		return
	}
	pl, err := s.store.ReplacePlaylist(r.PathValue("id"), strings.TrimSpace(req.Name), req.Tracks)
	if err != nil {
		if errors.Is(err, store.ErrPlaylistNotFound) {
			s.respondError(w, http.StatusNotFound, "плейлист не найден")
			return
		}
		s.logger.Error("update playlist", slog.Any("err", err))
		s.respondError(w, http.StatusInternalServerError, "не удалось сохранить плейлист")
		return
	}
	s.respondJSON(w, http.StatusOK, s.playlistPayload(pl))
}

func (s *Server) handlePlaylistDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeletePlaylist(r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrPlaylistNotFound) {
			s.respondError(w, http.StatusNotFound, "плейлист не найден")
			return
		}
		s.logger.Error("delete playlist", slog.Any("err", err))
		s.respondError(w, http.StatusInternalServerError, "не удалось удалить плейлист")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePlaylistPlay(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[struct {
		Path string `json:"path"`
	}](r)
	start := ""
	if err == nil {
		start = req.Path
	}
	s.playPlaylist(w, r.PathValue("id"), start)
}

func (s *Server) handlePlaylistAddTracks(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[struct {
		Path  string   `json:"path"`
		Paths []string `json:"paths"`
	}](r)
	if err != nil {
		s.respondError(w, http.StatusBadRequest, "некорректный запрос")
		return
	}
	paths := append([]string(nil), req.Paths...)
	if req.Path != "" {
		paths = append(paths, req.Path)
	}
	if len(paths) == 0 {
		s.respondError(w, http.StatusBadRequest, "укажите треки")
		return
	}
	cleaned := make([]string, 0, len(paths))
	for _, rel := range paths {
		name, err := s.resolveTrack(rel)
		if err != nil {
			s.libraryError(w, err)
			return
		}
		cleaned = append(cleaned, name)
	}
	pl, err := s.store.AddTracks(r.PathValue("id"), cleaned)
	if err != nil {
		if errors.Is(err, store.ErrPlaylistNotFound) {
			s.respondError(w, http.StatusNotFound, "плейлист не найден")
			return
		}
		s.logger.Error("add playlist tracks", slog.Any("err", err))
		s.respondError(w, http.StatusInternalServerError, "не удалось добавить треки")
		return
	}
	s.respondJSON(w, http.StatusOK, s.playlistPayload(pl))
}

func (s *Server) handlePlaylistRemoveTrack(w http.ResponseWriter, r *http.Request) {
	rel, err := library.Normalize(r.PathValue("path"))
	if err != nil || rel == "" {
		s.respondError(w, http.StatusBadRequest, "некорректный путь")
		return
	}
	pl, err := s.store.RemoveTrack(r.PathValue("id"), rel)
	if err != nil {
		if errors.Is(err, store.ErrPlaylistNotFound) {
			s.respondError(w, http.StatusNotFound, "плейлист не найден")
			return
		}
		if errors.Is(err, store.ErrTrackNotFound) {
			s.respondError(w, http.StatusNotFound, "трек не найден")
			return
		}
		s.logger.Error("remove playlist track", slog.Any("err", err))
		s.respondError(w, http.StatusInternalServerError, "не удалось убрать трек")
		return
	}
	s.respondJSON(w, http.StatusOK, s.playlistPayload(pl))
}

type playlistTrack struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Type     string `json:"type"`
	Title    string `json:"title,omitempty"`
	Artist   string `json:"artist,omitempty"`
	Album    string `json:"album,omitempty"`
	HasCover bool   `json:"hasCover"`
	Played   bool   `json:"played"`
	Missing  bool   `json:"missing,omitempty"`
}

func (s *Server) playlistPayload(pl store.Playlist) map[string]any {
	played := s.store.HistorySet()
	tracks := make([]playlistTrack, 0, len(pl.Tracks))
	for _, rel := range pl.Tracks {
		it := playlistTrack{
			Name: path.Base(rel),
			Path: rel,
			Type: "track",
		}
		info, err := s.lib.Stat(rel)
		if err != nil || info.IsDir() {
			it.Title = strings.TrimSuffix(it.Name, path.Ext(it.Name))
			it.Missing = true
			tracks = append(tracks, it)
			continue
		}
		tags := s.meta.For(rel)
		it.Title = tags.Title
		it.Artist = tags.Artist
		it.Album = tags.Album
		it.HasCover = tags.HasCover
		_, it.Played = played[rel]
		tracks = append(tracks, it)
	}
	return map[string]any{
		"id":     pl.ID,
		"name":   pl.Name,
		"tracks": tracks,
	}
}

func (s *Server) resolveTrack(rel string) (string, error) {
	name, err := library.Normalize(rel)
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", library.ErrInvalidPath
	}
	info, err := s.lib.Stat(name)
	if err != nil {
		return "", err
	}
	if info.IsDir() || !strings.EqualFold(path.Ext(name), ".mp3") {
		return "", library.ErrInvalidPath
	}
	return name, nil
}

func (s *Server) trackOf(rel string) player.Track {
	info := s.meta.For(rel)
	return player.Track{
		Path:   rel,
		Title:  info.Title,
		Artist: info.Artist,
		Album:  info.Album,
	}
}

func (s *Server) libraryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, library.ErrInvalidPath):
		s.respondError(w, http.StatusBadRequest, "некорректный путь")
	case errors.Is(err, library.ErrNotFound):
		s.respondError(w, http.StatusNotFound, "не найдено")
	default:
		s.logger.Error("library", slog.Any("err", err))
		s.respondError(w, http.StatusInternalServerError, "ошибка библиотеки")
	}
}

func (s *Server) respondJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		s.logger.Error("encode json", slog.Any("err", err))
	}
}

func (s *Server) respondError(w http.ResponseWriter, status int, msg string) {
	s.respondJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON[T any](r *http.Request) (T, error) {
	var req T
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	err := dec.Decode(&req)
	if cerr := r.Body.Close(); cerr != nil && err == nil {
		err = cerr
	}
	return req, err
}

func recoverer(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.Error("panic recovered", slog.Any("panic", rec))
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func requestLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/events") || strings.HasPrefix(r.URL.Path, "/api/player/position") {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		logger.Info("http",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", sw.status),
			slog.Duration("dur", time.Since(start)),
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// ReadFrom is intentionally absent. http.ServeContent copies through
// io.ReaderFrom when the writer has it, and the TCP connection's sendfile
// path then stops after the 512-byte sniff, so the client gets a short body
// with a full Content-Length. Playback and seeking need the whole range.
