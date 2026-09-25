package player

import (
	"math"
	"sync"
	"time"
)

type Track struct {
	Path   string `json:"path"`
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Album  string `json:"album"`
}

type State struct {
	Seq int64 `json:"seq"`
	// SeekSeq changes only when the playhead was moved on purpose: a seek or a
	// new track. Progress reports leave it alone, so a client that is playing
	// can tell "someone jumped" from "the server is a second behind me".
	SeekSeq    int64      `json:"seekSeq"`
	Track      *Track     `json:"track"`
	Queue      []string   `json:"queue"`
	QueueIndex int        `json:"queueIndex"`
	Playing    bool       `json:"playing"`
	Position   float64    `json:"position"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	SleepUntil *time.Time `json:"sleepUntil,omitempty"`
	Source     string     `json:"source"`
	Random     bool       `json:"random"`
}

const (
	maxSleepMinutes = 180
	// posGuard is how long after a seek or track change position reports from
	// other clients are treated as stale.
	posGuard = 2 * time.Second
)

type Player struct {
	mu               sync.Mutex
	state            State
	subs             map[int]chan State
	nextID           int
	timer            *time.Timer
	sleepGen         int
	lastPosBroadcast time.Time
	posGuardUntil    time.Time

	OnStart func(Track)
}

func New() *Player {
	return &Player{
		state: State{
			Queue:     []string{},
			UpdatedAt: time.Now(),
			Random:    true,
		},
		subs: make(map[int]chan State),
	}
}

func (p *Player) Snapshot() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return clone(p.state)
}

func (p *Player) Subscribe() (<-chan State, func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	id := p.nextID
	p.nextID++
	ch := make(chan State, 4)
	p.subs[id] = ch
	ch <- clone(p.state)
	return ch, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if c, ok := p.subs[id]; ok {
			close(c)
			delete(p.subs, id)
		}
	}
}

func (p *Player) PlayQueue(queue []string, index int, track Track, source string) State {
	return p.PlayQueueAt(queue, index, track, source, 0)
}

// PlayQueueAt starts the queue at index and a position inside that track.
func (p *Player) PlayQueueAt(queue []string, index int, track Track, source string, position float64) State {
	st, _ := p.play(queue, index, track, source, "", position)
	return st
}

// PlayIfCurrent starts track only when the current track path is still expectPath.
// An empty expectPath always applies. The bool is false when a concurrent
// advance already moved on, so a second next/prev from another tab is ignored.
func (p *Player) PlayIfCurrent(queue []string, index int, track Track, source, expectPath string) (State, bool) {
	return p.play(queue, index, track, source, expectPath, 0)
}

func (p *Player) play(queue []string, index int, track Track, source, expectPath string, position float64) (State, bool) {
	p.mu.Lock()
	if expectPath != "" && (p.state.Track == nil || p.state.Track.Path != expectPath) {
		out := clone(p.state)
		p.mu.Unlock()
		return out, false
	}
	if index < 0 || index >= len(queue) {
		out := clone(p.state)
		p.mu.Unlock()
		return out, false
	}
	p.state.Queue = append([]string(nil), queue...)
	p.state.QueueIndex = index
	p.state.Track = &track
	if position < 0 {
		position = 0
	}
	p.state.Playing = true
	p.state.Position = position
	p.state.Source = source
	p.posGuardUntil = time.Now().Add(posGuard)
	p.state.SeekSeq++
	p.bumpLocked()
	out := clone(p.state)
	p.mu.Unlock()
	// History is written before subscribers are told, so a fast next cannot
	// skip a track that has not been recorded yet. Clients drop a snapshot
	// whose seq is older than one they already applied.
	if p.OnStart != nil {
		p.OnStart(track)
	}
	p.broadcast(out)
	return out, true
}

func (p *Player) Pause() State {
	p.mu.Lock()
	p.state.Playing = false
	p.bumpLocked()
	out := clone(p.state)
	p.mu.Unlock()
	p.broadcast(out)
	return out
}

func (p *Player) Resume() State {
	p.mu.Lock()
	if p.state.Track != nil {
		p.state.Playing = true
		p.bumpLocked()
	}
	out := clone(p.state)
	p.mu.Unlock()
	p.broadcast(out)
	return out
}

func (p *Player) Seek(seconds float64) State {
	if seconds < 0 {
		seconds = 0
	}
	p.mu.Lock()
	p.state.Position = seconds
	p.posGuardUntil = time.Now().Add(posGuard)
	p.state.SeekSeq++
	p.bumpLocked()
	out := clone(p.state)
	p.mu.Unlock()
	p.broadcast(out)
	return out
}

func (p *Player) ReportPosition(path string, seconds float64) {
	if seconds < 0 {
		seconds = 0
	}
	p.mu.Lock()
	if p.state.Track == nil || p.state.Track.Path != path {
		p.mu.Unlock()
		return
	}
	// Right after a seek or a track change, another phone is still playing the
	// old spot and would undo it. Outside that window a big jump forward is
	// normal: a backgrounded tab keeps playing while its timers are throttled.
	if time.Now().Before(p.posGuardUntil) && math.Abs(seconds-p.state.Position) > 5 {
		p.mu.Unlock()
		return
	}
	// A tab lagging behind must not pull the shared playhead backwards.
	if p.state.Position-seconds > 1.5 {
		p.mu.Unlock()
		return
	}
	p.state.Position = seconds
	p.state.UpdatedAt = time.Now()
	if time.Since(p.lastPosBroadcast) < time.Second {
		p.mu.Unlock()
		return
	}
	p.lastPosBroadcast = time.Now()
	p.bumpLocked()
	out := clone(p.state)
	p.mu.Unlock()
	p.broadcast(out)
}

func (p *Player) Peek(delta int) (path string, index int, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	next := p.state.QueueIndex + delta
	if next < 0 || next >= len(p.state.Queue) {
		return "", 0, false
	}
	return p.state.Queue[next], next, true
}

func (p *Player) PeekUnplayed(skip map[string]struct{}, delta int) (path string, index int, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return NextUnplayed(p.state.Queue, p.state.QueueIndex, skip, p.state.Random, delta)
}

func (p *Player) Random() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state.Random
}

func (p *Player) SetRandom(on bool) State {
	p.mu.Lock()
	p.state.Random = on
	p.bumpLocked()
	out := clone(p.state)
	p.mu.Unlock()
	p.broadcast(out)
	return out
}

func (p *Player) Stop() State {
	p.mu.Lock()
	p.state.Playing = false
	p.bumpLocked()
	out := clone(p.state)
	p.mu.Unlock()
	p.broadcast(out)
	return out
}

func (p *Player) SetTrackMeta(track Track) {
	p.mu.Lock()
	if p.state.Track != nil && p.state.Track.Path == track.Path {
		p.state.Track = &track
	}
	out := clone(p.state)
	p.mu.Unlock()
	p.broadcast(out)
}

func (p *Player) SetSleep(minutes int) State {
	p.mu.Lock()
	p.sleepGen++
	gen := p.sleepGen
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	if minutes <= 0 {
		p.state.SleepUntil = nil
		p.bumpLocked()
		out := clone(p.state)
		p.mu.Unlock()
		p.broadcast(out)
		return out
	}
	if minutes > maxSleepMinutes {
		minutes = maxSleepMinutes
	}
	until := time.Now().Add(time.Duration(minutes) * time.Minute)
	p.state.SleepUntil = &until
	p.timer = time.AfterFunc(time.Until(until), func() { p.sleepFire(gen) })
	p.bumpLocked()
	out := clone(p.state)
	p.mu.Unlock()
	p.broadcast(out)
	return out
}

func (p *Player) sleepFire(gen int) {
	p.mu.Lock()
	if gen != p.sleepGen {
		p.mu.Unlock()
		return
	}
	p.state.Playing = false
	p.state.SleepUntil = nil
	p.timer = nil
	p.bumpLocked()
	out := clone(p.state)
	p.mu.Unlock()
	p.broadcast(out)
}

func (p *Player) bumpLocked() {
	p.state.Seq++
	p.state.UpdatedAt = time.Now()
}

func (p *Player) broadcast(state State) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ch := range p.subs {
		msg := clone(state)
		select {
		case ch <- msg:
		default:
			// Drop one stale snapshot so the newest state still fits.
			// The subscriber applies seq and ignores anything older.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- msg:
			default:
			}
		}
	}
}

func clone(s State) State {
	out := s
	if s.Track != nil {
		t := *s.Track
		out.Track = &t
	}
	out.Queue = append([]string(nil), s.Queue...)
	if s.SleepUntil != nil {
		t := *s.SleepUntil
		out.SleepUntil = &t
	}
	return out
}
