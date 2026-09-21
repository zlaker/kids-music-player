package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const maxHistory = 200

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
	dir       string
	mu        sync.Mutex
	history   []HistoryItem
	playlists []Playlist
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
	if err := s.loadJSON("playlists.json", &s.playlists); err != nil {
		return nil, err
	}
	if s.playlists == nil {
		s.playlists = []Playlist{}
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
	out := make([]HistoryItem, len(s.history))
	copy(out, s.history)
	return out
}

func (s *Store) AddHistory(item HistoryItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append([]HistoryItem{item}, s.history...)
	if len(s.history) > maxHistory {
		s.history = s.history[:maxHistory]
	}
	return s.saveJSON("history.json", s.history)
}

func (s *Store) ClearHistory() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = []HistoryItem{}
	return s.saveJSON("history.json", s.history)
}

func (s *Store) Playlists() []Playlist {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Playlist, len(s.playlists))
	copy(out, s.playlists)
	for i := range out {
		out[i].Tracks = append([]string(nil), s.playlists[i].Tracks...)
	}
	return out
}

func (s *Store) Playlist(id string) (Playlist, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.playlists {
		if p.ID == id {
			p.Tracks = append([]string(nil), p.Tracks...)
			return p, true
		}
	}
	return Playlist{}, false
}

func (s *Store) CreatePlaylist(name string, tracks []string) (Playlist, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := newID()
	if err != nil {
		return Playlist{}, err
	}
	if tracks == nil {
		tracks = []string{}
	}
	p := Playlist{ID: id, Name: name, Tracks: append([]string(nil), tracks...)}
	s.playlists = append(s.playlists, p)
	if err := s.saveJSON("playlists.json", s.playlists); err != nil {
		return Playlist{}, err
	}
	return p, nil
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
			p.Tracks = append([]string(nil), tracks...)
		}
		s.playlists[i] = p
		if err := s.saveJSON("playlists.json", s.playlists); err != nil {
			return Playlist{}, err
		}
		p.Tracks = append([]string(nil), p.Tracks...)
		return p, nil
	}
	return Playlist{}, fmt.Errorf("playlist not found")
}

func (s *Store) DeletePlaylist(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	filtered := s.playlists[:0]
	found := false
	for _, p := range s.playlists {
		if p.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, p)
	}
	if !found {
		return fmt.Errorf("playlist not found")
	}
	s.playlists = filtered
	return s.saveJSON("playlists.json", s.playlists)
}

func (s *Store) loadJSON(name string, dest any) error {
	path := filepath.Join(s.dir, name)
	data, err := os.ReadFile(path)
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
	path := filepath.Join(s.dir, name)
	tmp, err := os.CreateTemp(s.dir, "tmp-*.json")
	if err != nil {
		return fmt.Errorf("create temp %s: %w", name, err)
	}
	tmpName := tmp.Name()
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("encode %s: %w", name, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("chmod %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close %s: %w", name, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("replace %s: %w", name, err)
	}
	return nil
}

func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
