package player

import "testing"

func TestChooseStartSkipsHistory(t *testing.T) {
	t.Parallel()
	queue := []string{"a.mp3", "b.mp3", "c.mp3"}
	skip := map[string]struct{}{"a.mp3": {}, "c.mp3": {}}

	idx, ok := ChooseStart(queue, "a.mp3", skip, false)
	if !ok || queue[idx] != "a.mp3" {
		t.Fatalf("explicit choice should play even if heard, got %d ok=%v", idx, ok)
	}

	idx, ok = ChooseStart(queue, "", skip, false)
	if !ok || queue[idx] != "b.mp3" {
		t.Fatalf("automatic choice should skip history, got %d ok=%v", idx, ok)
	}

	idx, ok = ChooseStart(queue, "b.mp3", skip, false)
	if !ok || idx != 1 {
		t.Fatalf("prefer unplayed, got %d ok=%v", idx, ok)
	}

	_, ok = ChooseStart(queue, "", map[string]struct{}{"a.mp3": {}, "b.mp3": {}, "c.mp3": {}}, true)
	if ok {
		t.Fatal("expected no unplayed tracks")
	}
}

func TestNextUnplayedSequential(t *testing.T) {
	t.Parallel()
	queue := []string{"a.mp3", "b.mp3", "c.mp3"}
	skip := map[string]struct{}{"b.mp3": {}}

	path, idx, ok := NextUnplayed(queue, 0, skip, false, 1)
	if !ok || path != "c.mp3" || idx != 2 {
		t.Fatalf("next = %s %d %v", path, idx, ok)
	}
	_, _, ok = NextUnplayed(queue, 2, skip, false, 1)
	if ok {
		t.Fatal("should stop at end")
	}
	path, idx, ok = NextUnplayed(queue, 2, skip, false, -1)
	if !ok || path != "a.mp3" || idx != 0 {
		t.Fatalf("prev = %s %d %v", path, idx, ok)
	}
}
