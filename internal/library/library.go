package library

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

var (
	ErrInvalidPath = errors.New("invalid path")
	ErrNotFound    = errors.New("not found")
)

type Library struct {
	root *os.Root
	abs  string
}

type Entry struct {
	Name  string
	Path  string
	IsDir bool
}

func Open(musicDir string) (*Library, error) {
	abs, err := filepath.Abs(musicDir)
	if err != nil {
		return nil, fmt.Errorf("resolve music dir: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("stat music dir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("music path is not a directory: %s", abs)
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, fmt.Errorf("open music root: %w", err)
	}
	return &Library{root: root, abs: abs}, nil
}

func (l *Library) Close() error {
	if l == nil || l.root == nil {
		return nil
	}
	return l.root.Close()
}

func (l *Library) Root() string {
	return l.abs
}

func Normalize(rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	rel = strings.ReplaceAll(rel, "\\", "/")
	if rel == "" || rel == "." {
		return "", nil
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return "", ErrInvalidPath
	}
	if !filepath.IsLocal(rel) {
		return "", ErrInvalidPath
	}
	cleaned := path.Clean(rel)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", ErrInvalidPath
	}
	return cleaned, nil
}

func (l *Library) Open(rel string) (*os.File, error) {
	name, err := Normalize(rel)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = "."
	}
	f, err := l.root.Open(name)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		// os.Root refuses to follow a link out of the music directory and
		// reports it with an unexported error. The name exists yet cannot be
		// opened inside the root, so treat it as a bad path, not a server
		// fault: a stray symlink must not turn requests into 500s.
		if _, lerr := l.root.Lstat(name); lerr == nil {
			return nil, fmt.Errorf("%w: %s", ErrInvalidPath, name)
		}
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	return f, nil
}

func (l *Library) Stat(rel string) (info fs.FileInfo, err error) {
	f, err := l.Open(rel)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close %s: %w", rel, cerr)
		}
	}()
	info, err = f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", rel, err)
	}
	return info, nil
}

func (l *Library) List(rel string) (parent string, entries []Entry, err error) {
	name, err := Normalize(rel)
	if err != nil {
		return "", nil, err
	}
	f, err := l.Open(name)
	if err != nil {
		return "", nil, err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close listing: %w", cerr)
		}
	}()

	info, err := f.Stat()
	if err != nil {
		return "", nil, fmt.Errorf("stat listing: %w", err)
	}
	if !info.IsDir() {
		return "", nil, fmt.Errorf("not a directory")
	}

	items, err := f.ReadDir(-1)
	if err != nil {
		return "", nil, fmt.Errorf("read dir: %w", err)
	}

	entries = make([]Entry, 0, len(items))
	for _, item := range items {
		base := item.Name()
		if strings.HasPrefix(base, ".") {
			continue
		}
		child := base
		if name != "" {
			child = path.Join(name, base)
		}
		if item.IsDir() {
			entries = append(entries, Entry{Name: base, Path: child, IsDir: true})
			continue
		}
		if !isMP3(base) {
			continue
		}
		entries = append(entries, Entry{Name: base, Path: child, IsDir: false})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	if name != "" {
		parent = path.Dir(name)
		if parent == "." {
			parent = ""
		}
	}
	return parent, entries, nil
}

func (l *Library) TracksIn(rel string) ([]string, error) {
	_, entries, err := l.List(rel)
	if err != nil {
		return nil, err
	}
	tracks := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir {
			tracks = append(tracks, e.Path)
		}
	}
	return tracks, nil
}

func (l *Library) FolderOf(rel string) (string, error) {
	name, err := Normalize(rel)
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", nil
	}
	dir := path.Dir(name)
	if dir == "." {
		return "", nil
	}
	return dir, nil
}

func isMP3(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".mp3")
}
