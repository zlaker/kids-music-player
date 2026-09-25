package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	ErrPlaylistNotFound = errors.New("playlist not found")
	ErrTrackNotFound    = errors.New("track not found")
)

const (
	// maxHistoryList is how many recent plays the history screen shows.
	maxHistoryList = 200
	// maxHistoryTracks bounds the skip-set. Entries are unique by path, so
	// this grows with the library, not with every replay. Dropping the tail
	// is only a safety valve for a runaway file.
	maxHistoryTracks = 5000
)

type HistoryItem struct {
	Path     string    `json:"path"`
	Title    string    `json:"title"`
	Artist   string    `json:"artist"`
	PlayedAt time.Time `json:"playedAt"`
}

type Playlist struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Tracks []string `json:"tracks"`
}

type Store struct {
	dir        string
	mu         sync.Mutex
	history    []HistoryItem
	playlists  []Playlist
	progress   progressFile
	progressAt time.Time
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	s := &Store{dir: dir}
	if err := s.loadJSON("history.json", &s.history); err != nil {
		return nil, err
	}
	if s.history == nil {
		s.history = []HistoryItem{}
	}
	// Files written by older versions may repeat a path. Dedupe in memory only;
	// the next history write persists it. Startup must not depend on the state
	// directory being writable.
	s.history = dedupeHistory(s.history)
	if err := s.loadJSON("playlists.json", &s.playlists); err != nil {
		return nil, err
	}
	if s.playlists == nil {
		s.playlists = []Playlist{}
	}
	if err := s.loadJSON("progress.json", &s.progress); err != nil {
		return nil, err
	}
	if s.progress.Albums == nil {
		s.progress.Albums = map[string]AlbumProgress{}
	}
	return s, nil
}

func (s *Store) HistorySet() map[string]struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]struct{}, len(s.history))
	for _, item := range s.history {
		out[item.Path] = struct{}{}
	}
	return out
}

func (s *Store) History() []HistoryItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.history)
	if n > maxHistoryList {
		n = maxHistoryList
	}
	out := make([]HistoryItem, n)
	copy(out, s.history[:n])
	return out
}

func (s *Store) AddHistory(item HistoryItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := append([]HistoryItem(nil), s.history...)
	next := make([]HistoryItem, 0, len(s.history)+1)
	next = append(next, item)
	for _, h := range s.history {
		if h.Path == item.Path {
			continue
		}
		next = append(next, h)
	}
	if len(next) > maxHistoryTracks {
		next = next[:maxHistoryTracks]
	}
	s.history = next
	if err := s.saveJSON("history.json", s.history); err != nil {
		s.history = prev
		return err
	}
	return nil
}

func (s *Store) ClearHistory() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.history
	s.history = []HistoryItem{}
	if err := s.saveJSON("history.json", s.history); err != nil {
		s.history = prev
		return err
	}
	return nil
}

func (s *Store) Playlists() []Playlist {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Playlist, len(s.playlists))
	copy(out, s.playlists)
	for i := range out {
		out[i].Tracks = copyTracks(s.playlists[i].Tracks)
	}
	return out
}

func (s *Store) Playlist(id string) (Playlist, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.playlists {
		if p.ID == id {
			p.Tracks = copyTracks(p.Tracks)
			return p, true
		}
	}
	return Playlist{}, false
}

func (s *Store) CreatePlaylist(name string) (Playlist, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := newID()
	if err != nil {
		return Playlist{}, err
	}
	p := Playlist{ID: id, Name: name, Tracks: []string{}}
	s.playlists = append(s.playlists, p)
	if err := s.saveJSON("playlists.json", s.playlists); err != nil {
		s.playlists = s.playlists[:len(s.playlists)-1]
		return Playlist{}, err
	}
	return p, nil
}

func (s *Store) AddTracks(id string, tracks []string) (Playlist, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.playlists {
		if p.ID != id {
			continue
		}
		// Copy before appending: the append below may reuse the backing array,
		// so the rollback copy has to be taken first.
		prev := p
		prev.Tracks = copyTracks(p.Tracks)
		seen := make(map[string]struct{}, len(p.Tracks)+len(tracks))
		for _, t := range p.Tracks {
			seen[t] = struct{}{}
		}
		for _, t := range tracks {
			if t == "" {
				continue
			}
			if _, ok := seen[t]; ok {
				continue
			}
			p.Tracks = append(p.Tracks, t)
			seen[t] = struct{}{}
		}
		s.playlists[i] = p
		if err := s.saveJSON("playlists.json", s.playlists); err != nil {
			s.playlists[i] = prev
			return Playlist{}, err
		}
		p.Tracks = copyTracks(p.Tracks)
		return p, nil
	}
	return Playlist{}, ErrPlaylistNotFound
}

func (s *Store) RemoveTrack(id, track string) (Playlist, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.playlists {
		if p.ID != id {
			continue
		}
		filtered := make([]string, 0, len(p.Tracks))
		found := false
		for _, t := range p.Tracks {
			if t == track {
				found = true
				continue
			}
			filtered = append(filtered, t)
		}
		if !found {
			return Playlist{}, ErrTrackNotFound
		}
		prev := p
		p.Tracks = filtered
		s.playlists[i] = p
		if err := s.saveJSON("playlists.json", s.playlists); err != nil {
			s.playlists[i] = prev
			return Playlist{}, err
		}
		p.Tracks = copyTracks(p.Tracks)
		return p, nil
	}
	return Playlist{}, ErrPlaylistNotFound
}

func (s *Store) ReplacePlaylist(id, name string, tracks []string) (Playlist, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.playlists {
		if p.ID != id {
			continue
		}
		if name != "" {
			p.Name = name
		}
		if tracks != nil {
			p.Tracks = copyTracks(tracks)
		}
		prev := s.playlists[i]
		s.playlists[i] = p
		if err := s.saveJSON("playlists.json", s.playlists); err != nil {
			s.playlists[i] = prev
			return Playlist{}, err
		}
		p.Tracks = copyTracks(p.Tracks)
		return p, nil
	}
	return Playlist{}, ErrPlaylistNotFound
}

func (s *Store) DeletePlaylist(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := append([]Playlist(nil), s.playlists...)
	next := make([]Playlist, 0, len(s.playlists))
	found := false
	for _, p := range s.playlists {
		if p.ID == id {
			found = true
			continue
		}
		cp := p
		cp.Tracks = append([]string(nil), p.Tracks...)
		next = append(next, cp)
	}
	if !found {
		return ErrPlaylistNotFound
	}
	s.playlists = next
	if err := s.saveJSON("playlists.json", s.playlists); err != nil {
		s.playlists = prev
		return err
	}
	return nil
}

func (s *Store) loadJSON(name string, dest any) error {
	if err := knownStateFile(name); err != nil {
		return err
	}
	// Name is only history.json or playlists.json inside the state directory.
	data, err := os.ReadFile(filepath.Join(s.dir, name)) //nolint:gosec // G304: name is an internal state filename, not a request path
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", name, err)
	}
	if len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, dest); err != nil {
		return fmt.Errorf("decode %s: %w", name, err)
	}
	return nil
}

func (s *Store) saveJSON(name string, v any) error {
	if err := knownStateFile(name); err != nil {
		return err
	}
	path := filepath.Join(s.dir, name)
	tmp, err := os.CreateTemp(s.dir, "tmp-*.json")
	if err != nil {
		return fmt.Errorf("create temp %s: %w", name, err)
	}
	tmpName := tmp.Name()
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return cleanupTemp(tmp, tmpName, fmt.Errorf("encode %s: %w", name, err))
	}
	if err := tmp.Chmod(0o600); err != nil {
		return cleanupTemp(tmp, tmpName, fmt.Errorf("chmod %s: %w", name, err))
	}
	if err := tmp.Close(); err != nil {
		return cleanupTemp(nil, tmpName, fmt.Errorf("close %s: %w", name, err))
	}
	if err := os.Rename(tmpName, path); err != nil {
		return cleanupTemp(nil, tmpName, fmt.Errorf("replace %s: %w", name, err))
	}
	return nil
}

func copyTracks(tracks []string) []string {
	out := make([]string, len(tracks))
	copy(out, tracks)
	return out
}

func dedupeHistory(items []HistoryItem) []HistoryItem {
	seen := make(map[string]struct{}, len(items))
	out := make([]HistoryItem, 0, len(items))
	for _, item := range items {
		if _, ok := seen[item.Path]; ok {
			continue
		}
		seen[item.Path] = struct{}{}
		out = append(out, item)
	}
	return out
}

func knownStateFile(name string) error {
	switch name {
	case "history.json", "playlists.json", "progress.json":
		return nil
	default:
		return fmt.Errorf("unknown state file %s", name)
	}
}

func cleanupTemp(tmp *os.File, name string, err error) error {
	var cerr, rerr error
	if tmp != nil {
		cerr = tmp.Close()
	}
	if name != "" {
		rerr = os.Remove(name)
	}
	return errors.Join(err, cerr, rerr)
}

func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
