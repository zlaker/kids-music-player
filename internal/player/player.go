package player

import (
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
	Seq        int64      `json:"seq"`
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

type Player struct {
	mu     sync.Mutex
	state  State
	subs   map[int]chan State
	nextID int
	timer  *time.Timer

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
	started := false
	p.mu.Lock()
	if index < 0 || index >= len(queue) {
		p.mu.Unlock()
		return clone(p.state)
	}
	p.state.Queue = append([]string(nil), queue...)
	p.state.QueueIndex = index
	p.state.Track = &track
	p.state.Playing = true
	p.state.Position = 0
	p.state.Source = source
	p.bumpLocked()
	started = true
	out := clone(p.state)
	p.mu.Unlock()
	if started && p.OnStart != nil {
		p.OnStart(track)
	}
	p.broadcast(out)
	return out
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
	if p.state.Track != nil && p.state.Track.Path == path {
		p.state.Position = seconds
		p.state.UpdatedAt = time.Now()
	}
	p.mu.Unlock()
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
	until := time.Now().Add(time.Duration(minutes) * time.Minute)
	p.state.SleepUntil = &until
	p.timer = time.AfterFunc(time.Until(until), p.sleepFire)
	p.bumpLocked()
	out := clone(p.state)
	p.mu.Unlock()
	p.broadcast(out)
	return out
}

func (p *Player) sleepFire() {
	p.mu.Lock()
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
		select {
		case ch <- clone(state):
		default:
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
