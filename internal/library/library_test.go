package library

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeRejectsTraversal(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"../secret", "..", "/etc/passwd", "..\\secret"} {
		if _, err := Normalize(p); err == nil {
			t.Fatalf("expected invalid path for %q", p)
		}
	}
}

func TestListOnlyMP3AndDirs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "Альбом"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Альбом", "track.mp3"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Альбом", "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret.flac"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	lib, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lib.Close() })

	_, top, err := lib.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 1 || !top[0].IsDir || top[0].Name != "Альбом" {
		t.Fatalf("top level = %#v", top)
	}

	parent, album, err := lib.List("Альбом")
	if err != nil {
		t.Fatal(err)
	}
	if parent != "" {
		t.Fatalf("parent = %q", parent)
	}
	if len(album) != 1 || album[0].Path != "Альбом/track.mp3" {
		t.Fatalf("album = %#v", album)
	}
}

func TestOpenRejectsEscape(t *testing.T) {
	t.Parallel()
	lib, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lib.Close() })
	if _, err := lib.Open("../passwd"); err == nil {
		t.Fatal("escaped root")
	}
}
