package meta

import (
	"hash/fnv"
	"io"
	"path"
	"strings"
	"sync"

	"github.com/dhowden/tag"

	"kids-music-player/internal/library"
)

const (
	maxCoverBytes   = 2 << 20
	maxCachedCovers = 128
	// readShards keeps concurrent tag reads of the same file from piling up
	// without growing a lock per path.
	readShards = 64
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
	lib     *library.Library
	mu      sync.Mutex
	cache   map[string]cached
	folders map[string]folderHit
	reads   [readShards]sync.Mutex
	covers  int
}

type cached struct {
	mod          int64
	info         Info
	embedded     []byte
	embeddedMIME string
}

type folderHit struct {
	mod int64
	rel string
	ok  bool
}

func New(lib *library.Library) *Reader {
	return &Reader{
		lib:     lib,
		cache:   make(map[string]cached),
		folders: make(map[string]folderHit),
	}
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
	if info.HasCover {
		return info
	}
	stat, err := r.lib.Stat(rel)
	if err != nil || !stat.IsDir() {
		return info
	}
	if tracks, err := r.lib.TracksIn(rel); err == nil {
		for _, t := range tracks {
			if r.For(t).HasCover {
				info.HasCover = true
				break
			}
		}
	}
	return info
}

func (r *Reader) Cover(rel string) (Cover, bool) {
	if cover, ok := r.embeddedCover(rel); ok {
		return cover, true
	}
	if coverRel := r.folderCoverPath(rel); coverRel != "" {
		f, err := r.lib.Open(coverRel)
		if err != nil {
			return Cover{}, false
		}
		data, err := io.ReadAll(io.LimitReader(f, maxCoverBytes))
		cerr := f.Close()
		if err != nil || cerr != nil || len(data) == 0 {
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

func (r *Reader) embeddedCover(rel string) (Cover, bool) {
	info, err := r.lib.Stat(rel)
	if err != nil || info.IsDir() {
		return Cover{}, false
	}
	meta, err := r.read(rel)
	if err != nil || !meta.HasCover {
		return Cover{}, false
	}
	mod := info.ModTime().UnixNano()
	r.mu.Lock()
	c, ok := r.cache[rel]
	r.mu.Unlock()
	if ok && c.mod == mod && len(c.embedded) > 0 {
		return Cover{MIME: c.embeddedMIME, Data: c.embedded}, true
	}
	// Past the cache cap the picture is not kept in memory, so read it again
	// rather than answering 404 for a track that has art.
	_, data, mimeType, err := r.readTags(rel)
	if err != nil || len(data) == 0 {
		return Cover{}, false
	}
	return Cover{MIME: mimeType, Data: data}, true
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
	if c, ok := r.lookup(rel, mod); ok {
		return c.info, nil
	}

	lock := r.keyLock(rel)
	lock.Lock()
	defer lock.Unlock()
	if c, ok := r.lookup(rel, mod); ok {
		return c.info, nil
	}

	meta, embedded, mimeType, err := r.readTags(rel)
	if err != nil {
		return Info{}, err
	}
	r.store(rel, mod, meta, embedded, mimeType)
	return meta, nil
}

func (r *Reader) lookup(rel string, mod int64) (cached, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.cache[rel]
	if !ok || c.mod != mod {
		return cached{}, false
	}
	return c, true
}

func (r *Reader) store(rel string, mod int64, meta Info, embedded []byte, mimeType string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prev := r.cache[rel]
	entry := cached{mod: mod, info: meta}
	if len(embedded) > 0 && (len(prev.embedded) > 0 || r.covers < maxCachedCovers) {
		if len(prev.embedded) == 0 {
			r.covers++
		}
		entry.embedded = embedded
		entry.embeddedMIME = mimeType
	}
	r.cache[rel] = entry
}

func (r *Reader) keyLock(rel string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(rel))
	return &r.reads[h.Sum32()%readShards]
}

func (r *Reader) readTags(rel string) (Info, []byte, string, error) {
	f, err := r.lib.Open(rel)
	if err != nil {
		return Info{}, nil, "", err
	}
	m, err := tag.ReadFrom(f)
	cerr := f.Close()
	if err != nil {
		return Info{}, nil, "", err
	}
	if cerr != nil {
		return Info{}, nil, "", cerr
	}
	out := Info{
		Title:  strings.TrimSpace(m.Title()),
		Artist: strings.TrimSpace(m.Artist()),
		Album:  strings.TrimSpace(m.Album()),
	}
	pic := m.Picture()
	if pic == nil || len(pic.Data) == 0 || len(pic.Data) > maxCoverBytes {
		return out, nil, "", nil
	}
	out.HasCover = true
	data := append([]byte(nil), pic.Data...)
	return out, data, mimeOf(pic.MIMEType, pic.Ext), nil
}

func (r *Reader) folderCoverPath(rel string) string {
	dir := rel
	if info, err := r.lib.Stat(rel); err == nil && !info.IsDir() {
		parent, ferr := r.lib.FolderOf(rel)
		if ferr != nil {
			return ""
		}
		dir = parent
	}
	mod := int64(0)
	if info, err := r.lib.Stat(dir); err == nil {
		mod = info.ModTime().UnixNano()
	}
	r.mu.Lock()
	if hit, ok := r.folders[dir]; ok && hit.ok && hit.mod == mod {
		r.mu.Unlock()
		return hit.rel
	}
	r.mu.Unlock()

	found := r.findFolderCover(dir)
	r.mu.Lock()
	r.folders[dir] = folderHit{mod: mod, rel: found, ok: true}
	r.mu.Unlock()
	return found
}

func (r *Reader) findFolderCover(dir string) string {
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
