package player

import "testing"

func TestPlayAndPeek(t *testing.T) {
	t.Parallel()
	p := New()
	state := p.PlayQueue([]string{"a.mp3", "b.mp3"}, 0, Track{Path: "a.mp3", Title: "A"}, "folder:")
	if !state.Playing || state.Track.Path != "a.mp3" {
		t.Fatalf("state = %#v", state)
	}
	path, idx, ok := p.Peek(1)
	if !ok || path != "b.mp3" || idx != 1 {
		t.Fatalf("peek next = %s %d %v", path, idx, ok)
	}
	if _, _, ok := p.Peek(-1); ok {
		t.Fatal("peek prev should fail at start")
	}
	p.PlayQueue([]string{"a.mp3", "b.mp3"}, 1, Track{Path: "b.mp3"}, "folder:")
	if _, _, ok := p.Peek(1); ok {
		t.Fatal("peek next should fail at end")
	}
	stopped := p.Stop()
	if stopped.Playing {
		t.Fatal("still playing")
	}
}
