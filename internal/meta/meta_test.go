package meta

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"kids-music-player/internal/library"
)

// id3WithCover builds a minimal ID3v2.4 tag holding a title and an attached
// picture, so tests do not need an external encoder or a binary fixture.
func id3WithCover(t *testing.T, title string, picture []byte) []byte {
	t.Helper()

	frame := func(id string, payload []byte) []byte {
		out := make([]byte, 0, 10+len(payload))
		out = append(out, id...)
		out = append(out, synchsafe(len(payload))...)
		out = append(out, 0, 0)
		return append(out, payload...)
	}

	titlePayload := append([]byte{3}, title...)

	picPayload := []byte{3}
	picPayload = append(picPayload, "image/jpeg"...)
	picPayload = append(picPayload, 0)
	picPayload = append(picPayload, 3) // front cover
	picPayload = append(picPayload, 0) // empty description
	picPayload = append(picPayload, picture...)

	body := append(frame("TIT2", titlePayload), frame("APIC", picPayload)...)

	out := []byte{'I', 'D', '3', 4, 0, 0}
	out = append(out, synchsafe(len(body))...)
	out = append(out, body...)
	return append(out, bytes.Repeat([]byte{0xFF, 0xFB, 0x90, 0x00}, 8)...)
}

func synchsafe(n int) []byte {
	return []byte{
		byte(n >> 21 & 0x7f),
		byte(n >> 14 & 0x7f),
		byte(n >> 7 & 0x7f),
		byte(n & 0x7f),
	}
}

func testReader(t *testing.T, files map[string][]byte) *Reader {
	t.Helper()
	root := t.TempDir()
	for name, data := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	lib, err := library.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lib.Close() })
	return New(lib)
}

func TestEmbeddedCoverAndTags(t *testing.T) {
	t.Parallel()
	picture := bytes.Repeat([]byte{0x11, 0x22}, 64)
	r := testReader(t, map[string][]byte{
		"Альбом/one.mp3": id3WithCover(t, "Песня", picture),
	})

	info := r.For("Альбом/one.mp3")
	if info.Title != "Песня" || !info.HasCover {
		t.Fatalf("info = %#v", info)
	}
	cover, ok := r.Cover("Альбом/one.mp3")
	if !ok || !bytes.Equal(cover.Data, picture) || cover.MIME != "image/jpeg" {
		t.Fatalf("cover ok=%v mime=%q len=%d", ok, cover.MIME, len(cover.Data))
	}

	// The album inherits the cover of a track inside it.
	if !r.For("Альбом").HasCover {
		t.Fatal("folder should report a cover")
	}
	if _, ok := r.Cover("Альбом"); !ok {
		t.Fatal("folder cover missing")
	}
}

func TestCoverServedBeyondCacheCap(t *testing.T) {
	t.Parallel()
	files := make(map[string][]byte, maxCachedCovers+1)
	for i := range maxCachedCovers + 1 {
		name := filepath.Join("Альбом", string(rune('a'+i%26))+string(rune('a'+i/26))+".mp3")
		files[filepath.ToSlash(name)] = id3WithCover(t, "t", []byte{byte(i), 0x55, 0x66})
	}
	r := testReader(t, files)

	for name := range files {
		if !r.For(name).HasCover {
			t.Fatalf("%s lost its cover flag", name)
		}
	}
	// Every track must still serve art once the in-memory cap is reached.
	for name := range files {
		cover, ok := r.Cover(name)
		if !ok || len(cover.Data) == 0 {
			t.Fatalf("%s returned no cover past the cache cap", name)
		}
	}
	if r.covers > maxCachedCovers {
		t.Fatalf("cached covers = %d, cap %d", r.covers, maxCachedCovers)
	}
}

func TestFolderImageUsedWhenTrackHasNoArt(t *testing.T) {
	t.Parallel()
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 32)...)
	r := testReader(t, map[string][]byte{
		"Сказки/one.mp3":   bytes.Repeat([]byte{0xFF, 0xFB, 0x90, 0x00}, 8),
		"Сказки/cover.png": png,
	})

	if !r.For("Сказки/one.mp3").HasCover {
		t.Fatal("track should fall back to the folder image")
	}
	cover, ok := r.Cover("Сказки/one.mp3")
	if !ok || cover.MIME != "image/png" || !bytes.Equal(cover.Data, png) {
		t.Fatalf("cover ok=%v mime=%q", ok, cover.MIME)
	}
}

func TestTagsRereadAfterFileChanges(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "one.mp3")
	if err := os.WriteFile(path, id3WithCover(t, "Первая", []byte{1, 2, 3}), 0o600); err != nil {
		t.Fatal(err)
	}
	lib, err := library.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lib.Close() })
	r := New(lib)

	if got := r.For("one.mp3").Title; got != "Первая" {
		t.Fatalf("title = %q", got)
	}
	if err := os.WriteFile(path, id3WithCover(t, "Вторая", []byte{1, 2, 3}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, time.Now().Add(time.Second), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := r.For("one.mp3").Title; got != "Вторая" {
		t.Fatalf("stale title = %q", got)
	}
}
