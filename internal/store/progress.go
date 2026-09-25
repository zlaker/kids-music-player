package store

import "time"

const progressSaveMin = 5 * time.Second

// AlbumProgress is the track and second to resume an album from.
type AlbumProgress struct {
	Folder    string    `json:"folder"`
	Path      string    `json:"path"`
	Seconds   float64   `json:"seconds"`
	Duration  float64   `json:"duration"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type progressFile struct {
	Last   string                   `json:"last"`
	Albums map[string]AlbumProgress `json:"albums"`
}

func (s *Store) LastProgress() (AlbumProgress, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.progress.Albums[s.progress.Last]
	if !ok || p.Path == "" {
		return AlbumProgress{}, false
	}
	return p, true
}

func (s *Store) AlbumProgress(folder string) (AlbumProgress, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.progress.Albums[folder]
	if !ok || p.Path == "" {
		return AlbumProgress{}, false
	}
	return p, true
}

// SaveProgress remembers where an album stopped. Reports closer than
// progressSaveMin are kept in memory and written on the next forced save.
func (s *Store) SaveProgress(folder, file string, seconds, duration float64, force bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if file == "" {
		return nil
	}
	if seconds < 0 {
		seconds = 0
	}
	if s.progress.Albums == nil {
		s.progress.Albums = map[string]AlbumProgress{}
	}
	prev := s.progress.Albums[folder]
	if duration <= 0 && file == prev.Path && prev.Duration > 0 {
		duration = prev.Duration
	}
	now := time.Now()
	s.progress.Last = folder
	s.progress.Albums[folder] = AlbumProgress{
		Folder:    folder,
		Path:      file,
		Seconds:   seconds,
		Duration:  duration,
		UpdatedAt: now,
	}
	if !force && !s.progressAt.IsZero() && now.Sub(s.progressAt) < progressSaveMin {
		return nil
	}
	s.progressAt = now
	return s.saveJSON("progress.json", s.progress)
}
