package meta

import (
	"io"
	"path"
	"strings"
	"sync"

	"github.com/dhowden/tag"

	"kids-music-player/internal/library"
)

type Info struct {
	Title    string
	Artist   string
	Album    string
	HasCover bool
}

type Cover struct {
	MIME string
	Data []byte
}

type Reader struct {
	lib   *library.Library
	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	mod  int64
	info Info
}

func New(lib *library.Library) *Reader {
	return &Reader{lib: lib, cache: make(map[string]cached)}
}

func (r *Reader) For(rel string) Info {
	info, _ := r.read(rel)
	if info.Title == "" {
		base := path.Base(rel)
		info.Title = strings.TrimSuffix(base, path.Ext(base))
	}
	if !info.HasCover {
		info.HasCover = r.folderCoverPath(rel) != ""
	}
	if !info.HasCover {
		if stat, err := r.lib.Stat(rel); err == nil && stat.IsDir() {
			if tracks, err := r.lib.TracksIn(rel); err == nil {
				for _, t := range tracks {
					if r.For(t).HasCover {
						info.HasCover = true
						break
					}
				}
			}
		}
	}
	return info
}

func (r *Reader) Cover(rel string) (Cover, bool) {
	if pic := r.embeddedCover(rel); pic != nil {
		return Cover{MIME: mimeOf(pic.MIMEType, pic.Ext), Data: pic.Data}, true
	}
	if coverRel := r.folderCoverPath(rel); coverRel != "" {
		f, err := r.lib.Open(coverRel)
		if err != nil {
			return Cover{}, false
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 8<<20))
		if err != nil || len(data) == 0 {
			return Cover{}, false
		}
		return Cover{MIME: detectImageMIME(data), Data: data}, true
	}
	if stat, err := r.lib.Stat(rel); err == nil && stat.IsDir() {
		if tracks, err := r.lib.TracksIn(rel); err == nil {
			for _, t := range tracks {
				if cover, ok := r.Cover(t); ok {
					return cover, true
				}
			}
		}
	}
	return Cover{}, false
}

func (r *Reader) read(rel string) (Info, error) {
	info, err := r.lib.Stat(rel)
	if err != nil {
		return Info{}, err
	}
	if info.IsDir() {
		return Info{HasCover: r.folderCoverPath(rel) != ""}, nil
	}

	mod := info.ModTime().UnixNano()
	r.mu.Lock()
	if c, ok := r.cache[rel]; ok && c.mod == mod {
		r.mu.Unlock()
		return c.info, nil
	}
	r.mu.Unlock()

	meta, hasPic, err := r.readTags(rel)
	if err != nil {
		return Info{}, err
	}
	meta.HasCover = hasPic
	r.mu.Lock()
	r.cache[rel] = cached{mod: mod, info: meta}
	r.mu.Unlock()
	return meta, nil
}

func (r *Reader) readTags(rel string) (Info, bool, error) {
	f, err := r.lib.Open(rel)
	if err != nil {
		return Info{}, false, err
	}
	defer f.Close()

	m, err := tag.ReadFrom(f)
	if err != nil {
		return Info{}, false, err
	}
	out := Info{
		Title:  strings.TrimSpace(m.Title()),
		Artist: strings.TrimSpace(m.Artist()),
		Album:  strings.TrimSpace(m.Album()),
	}
	return out, m.Picture() != nil && len(m.Picture().Data) > 0, nil
}

func (r *Reader) embeddedCover(rel string) *tag.Picture {
	f, err := r.lib.Open(rel)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return nil
	}
	m, err := tag.ReadFrom(f)
	if err != nil {
		return nil
	}
	pic := m.Picture()
	if pic == nil || len(pic.Data) == 0 {
		return nil
	}
	return pic
}

func (r *Reader) folderCoverPath(rel string) string {
	dir := rel
	if info, err := r.lib.Stat(rel); err == nil && !info.IsDir() {
		var ferr error
		dir, ferr = r.lib.FolderOf(rel)
		if ferr != nil {
			return ""
		}
	}
	for _, name := range []string{"folder.jpg", "cover.jpg", "folder.png", "cover.png", "Folder.jpg", "Cover.jpg"} {
		candidate := name
		if dir != "" {
			candidate = path.Join(dir, name)
		}
		if info, err := r.lib.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func mimeOf(mime, ext string) string {
	if mime != "" {
		return mime
	}
	switch strings.ToLower(ext) {
	case "png":
		return "image/png"
	default:
		return "image/jpeg"
	}
}

func detectImageMIME(data []byte) string {
	if len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n" {
		return "image/png"
	}
	return "image/jpeg"
}
