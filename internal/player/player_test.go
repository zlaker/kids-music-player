package player

import (
	"fmt"
	"testing"
	"time"
)

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

func TestBroadcastKeepsLatestWhenSlow(t *testing.T) {
	t.Parallel()
	p := New()
	ch, cancel := p.Subscribe()
	defer cancel()

	var last State
	for i := 0; i < 8; i++ {
		path := fmt.Sprintf("t%d.mp3", i)
		last = p.PlayQueue([]string{path}, 0, Track{Path: path, Title: path}, "test")
	}

	var got State
	for {
		select {
		case st := <-ch:
			got = st
		default:
			if got.Track == nil || got.Track.Path != last.Track.Path || got.Seq != last.Seq {
				t.Fatalf("latest = %#v, got %#v", last.Track, got.Track)
			}
			return
		}
	}
}

func TestPlayIfCurrentIgnoresStaleAdvance(t *testing.T) {
	t.Parallel()
	p := New()
	p.PlayQueue([]string{"a.mp3", "b.mp3"}, 0, Track{Path: "a.mp3"}, "folder")
	st, ok := p.PlayIfCurrent([]string{"a.mp3", "b.mp3"}, 1, Track{Path: "b.mp3"}, "folder", "a.mp3")
	if !ok || st.Track.Path != "b.mp3" {
		t.Fatalf("first advance = %#v ok=%v", st.Track, ok)
	}
	st, ok = p.PlayIfCurrent([]string{"a.mp3", "b.mp3"}, 0, Track{Path: "a.mp3"}, "folder", "a.mp3")
	if ok || st.Track.Path != "b.mp3" {
		t.Fatalf("stale advance applied, track=%#v ok=%v", st.Track, ok)
	}
}

func TestReportPositionIgnoresLag(t *testing.T) {
	t.Parallel()
	p := New()
	p.PlayQueue([]string{"a.mp3"}, 0, Track{Path: "a.mp3"}, "folder")
	p.Seek(20)
	p.ReportPosition("a.mp3", 5)
	if got := p.Snapshot().Position; got != 20 {
		t.Fatalf("position = %v", got)
	}
	p.ReportPosition("a.mp3", 21)
	if got := p.Snapshot().Position; got != 21 {
		t.Fatalf("forward position = %v", got)
	}
}

func TestSeekSeqMovesOnlyOnDeliberateJumps(t *testing.T) {
	t.Parallel()
	p := New()
	start := p.PlayQueue([]string{"a.mp3"}, 0, Track{Path: "a.mp3"}, "folder")
	if start.SeekSeq == 0 {
		t.Fatal("new track should count as a jump")
	}
	p.ReportPosition("a.mp3", 1)
	p.ReportPosition("a.mp3", 2)
	if got := p.Snapshot().SeekSeq; got != start.SeekSeq {
		t.Fatalf("progress reports changed seekSeq: %d -> %d", start.SeekSeq, got)
	}
	if got := p.Seek(30).SeekSeq; got == start.SeekSeq {
		t.Fatal("seek should change seekSeq")
	}
	paused := p.Pause().SeekSeq
	if paused != p.Snapshot().SeekSeq {
		t.Fatal("pause should not change seekSeq")
	}
}

func TestReportPositionGuardsSeekThenAcceptsCatchUp(t *testing.T) {
	t.Parallel()
	p := New()
	p.PlayQueue([]string{"a.mp3"}, 0, Track{Path: "a.mp3"}, "folder")
	p.Seek(10)
	// Another phone still sits at the pre-seek spot.
	p.ReportPosition("a.mp3", 60)
	if got := p.Snapshot().Position; got != 10 {
		t.Fatalf("seek undone, position = %v", got)
	}

	// Once the guard expires a backgrounded tab may report a large jump
	// forward, because its timers were throttled while audio kept playing.
	p.mu.Lock()
	p.posGuardUntil = time.Now().Add(-time.Second)
	p.mu.Unlock()
	p.ReportPosition("a.mp3", 60)
	if got := p.Snapshot().Position; got != 60 {
		t.Fatalf("catch-up rejected, position = %v", got)
	}
}

func TestStaleSleepFireDoesNotCancelNewTimer(t *testing.T) {
	t.Parallel()
	p := New()
	p.PlayQueue([]string{"a.mp3"}, 0, Track{Path: "a.mp3"}, "folder")
	t.Cleanup(func() { p.SetSleep(0) })
	p.SetSleep(30)
	p.mu.Lock()
	stale := p.sleepGen
	p.mu.Unlock()
	fresh := p.SetSleep(45)
	p.sleepFire(stale)
	got := p.Snapshot()
	if !got.Playing || got.SleepUntil == nil || got.SleepUntil.Unix() != fresh.SleepUntil.Unix() {
		t.Fatalf("state = playing %v until %v", got.Playing, got.SleepUntil)
	}
}
