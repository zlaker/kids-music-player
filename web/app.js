function encodePath(rel) {
  return (rel || '').split('/').filter(Boolean).map(encodeURIComponent).join('/');
}

function playerApp() {
  return {
    path: '',
    parent: '',
    query: '',
    items: [],
    loading: false,
    player: { track: null, playing: false, position: 0, seq: 0, sleepUntil: null, queue: [], random: true },
    notice: '',
    lastSeq: -1,
    history: [],
    playlists: [],
    currentPlaylist: null,
    panel: null,
    playlistName: '',
    addTrackPaths: [],
    now: Date.now(),
    duration: 0,
    localPosition: 0,
    applying: false,
    lastReport: 0,
    needsUnlock: false,

    get folders() { return this.items.filter((i) => i.type === 'dir'); },
    get tracks() { return this.items.filter((i) => i.type === 'track'); },
    get visibleTracks() {
      const q = this.query.trim().toLowerCase()
      if (!q) return this.tracks
      return this.tracks.filter((t) => {
        const hay = [t.name, t.title, t.artist, t.album].filter(Boolean).join(' ').toLowerCase()
        return hay.includes(q)
      })
    },
    get crumbs() {
      if (!this.path) return [];
      const parts = this.path.split('/').filter(Boolean);
      return parts.map((name, i) => ({ name, path: parts.slice(0, i + 1).join('/') }));
    },
    get panelTitle() {
      if (this.panel === 'timer') return 'Таймер выключения';
      if (this.panel === 'history') return 'Проигранное';
      if (this.panel === 'playlist' && this.currentPlaylist) return this.currentPlaylist.name;
      if (this.addTrackPaths.length) return 'Добавить в плейлист';
      return 'Плейлисты';
    },
    get sleepLeft() {
      if (!this.player.sleepUntil) return '';
      const left = Math.max(0, new Date(this.player.sleepUntil).getTime() - this.now);
      if (!left) return '';
      return this.fmt(left / 1000);
    },

    async init() {
      setInterval(() => { this.now = Date.now(); }, 1000);
      await this.open('');
      this.connectEvents();
      await this.refreshPlayer();
    },

    coverUrl(rel) {
      return '/api/covers/' + encodePath(rel);
    },

    async open(rel) {
      this.loading = true;
      this.query = '';
      this.path = rel || '';
      try {
        const data = await this.api('/api/library?path=' + encodeURIComponent(this.path));
        this.parent = data.parent || '';
        this.items = data.items || [];
      } finally {
        this.loading = false;
      }
    },

    connectEvents() {
      const es = new EventSource('/api/events');
      es.onmessage = (ev) => this.applyState(JSON.parse(ev.data));
    },

    async refreshPlayer() {
      this.applyState(await this.api('/api/player'));
    },

    applyState(state) {
      this.player = state;
      if (state.track) {
        this.items.forEach((item) => {
          if (item.path === state.track.path) item.played = true;
        });
      }
      if (state.seq <= this.lastSeq) {
        return;
      }
      this.lastSeq = state.seq;
      const audio = this.$refs.audio;
      if (!audio || !state.track) {
        if (audio && !state.playing) audio.pause();
        return;
      }
      const src = '/api/stream/' + encodePath(state.track.path);
      const abs = new URL(src, window.location.href).href;
      this.applying = true;
      if (audio.src !== abs) {
        audio.src = src;
      }
      const applyPos = () => {
        if (Math.abs((audio.currentTime || 0) - (state.position || 0)) > 1.2) {
          audio.currentTime = state.position || 0;
        }
        this.localPosition = state.position || 0;
        if (state.playing) {
          audio.play().catch(() => { this.needsUnlock = true; });
        } else {
          audio.pause();
        }
        this.applying = false;
      };
      if (audio.readyState >= 1) {
        applyPos();
      } else {
        audio.onloadedmetadata = () => {
          this.duration = audio.duration || 0;
          applyPos();
        };
      }
    },

    onTime() {
      const audio = this.$refs.audio;
      if (!audio || this.applying || !this.player.track) return;
      this.localPosition = audio.currentTime || 0;
      this.duration = audio.duration || 0;
      const now = Date.now();
      if (now - this.lastReport < 1000) return;
      this.lastReport = now;
      this.api('/api/player/position', {
        method: 'POST',
        body: { path: this.player.track.path, seconds: this.localPosition },
      });
    },

    unlock() {
      this.needsUnlock = false;
      if (this.player.playing) {
        this.$refs.audio.play().catch(() => { this.needsUnlock = true; });
      }
    },

    async playTrack(path) {
      await this.tryPlay(() => this.api('/api/player/play', { method: 'POST', body: { path } }));
    },
    async playFirstMatch() {
      const first = this.visibleTracks[0]
      if (first) await this.playTrack(first.path)
    },
    async playFolder() {
      await this.tryPlay(() => this.api('/api/player/play', { method: 'POST', body: { folder: this.path } }));
    },
    async playPlaylist(id, path) {
      const body = path ? { path } : {};
      await this.tryPlay(() => this.api('/api/playlists/' + id + '/play', { method: 'POST', body }));
    },
    async tryPlay(fn) {
      this.notice = '';
      try {
        await fn();
      } catch (err) {
        this.notice = err.message || 'Не удалось включить';
      }
    },
    async toggleRandom() {
      await this.api('/api/player/random', { method: 'POST', body: { random: !this.player.random } });
    },
    async toggle() {
      if (!this.player.track) return;
      const url = this.player.playing ? '/api/player/pause' : '/api/player/resume';
      await this.api(url, { method: 'POST', body: {} });
    },
    async next() { await this.tryPlay(() => this.api('/api/player/next', { method: 'POST', body: {} })); },
    async prev() { await this.tryPlay(() => this.api('/api/player/prev', { method: 'POST', body: {} })); },
    async seek(value) {
      const seconds = Number(value);
      this.localPosition = seconds;
      await this.api('/api/player/seek', { method: 'POST', body: { seconds } });
    },
    async setTimer(minutes) {
      await this.api('/api/player/timer', { method: 'POST', body: { minutes } });
      this.closePanel();
    },
    async openHistory() {
      this.panel = 'history';
      const data = await this.api('/api/history');
      this.history = data.items || [];
    },
    async clearHistory() {
      const data = await this.api('/api/history', { method: 'DELETE' });
      this.history = data.items || [];
      this.items.forEach((item) => { item.played = false; });
      this.notice = '';
    },
    async openPlaylists() {
      this.addTrackPaths = [];
      this.currentPlaylist = null;
      this.panel = 'playlists';
      await this.refreshPlaylists();
    },
    async refreshPlaylists() {
      const data = await this.api('/api/playlists');
      this.playlists = data.items || [];
    },
    async startAddTracks(paths) {
      this.addTrackPaths = (paths || []).filter(Boolean);
      this.currentPlaylist = null;
      this.panel = 'playlists';
      await this.refreshPlaylists();
    },
    async openPlaylist(id) {
      this.addTrackPaths = [];
      this.panel = 'playlist';
      this.currentPlaylist = await this.api('/api/playlists/' + id);
    },
    async createPlaylist() {
      const name = this.playlistName.trim();
      if (!name) return;
      const pending = this.addTrackPaths.slice();
      const pl = await this.api('/api/playlists', { method: 'POST', body: { name } });
      this.playlistName = '';
      if (pending.length) {
        await this.api('/api/playlists/' + pl.id + '/tracks', { method: 'POST', body: { paths: pending } });
      }
      await this.openPlaylist(pl.id);
    },
    async createPlaylistFromFolder() {
      await this.startAddTracks(this.tracks.map((t) => t.path));
    },
    async addToPlaylist(pl) {
      if (!this.addTrackPaths.length) {
        await this.openPlaylist(pl.id);
        return;
      }
      const pending = this.addTrackPaths.slice();
      await this.api('/api/playlists/' + pl.id + '/tracks', { method: 'POST', body: { paths: pending } });
      await this.openPlaylist(pl.id);
    },
    async removeFromPlaylist(path) {
      if (!this.currentPlaylist) return;
      this.currentPlaylist = await this.api(
        '/api/playlists/' + this.currentPlaylist.id + '/tracks/' + encodePath(path),
        { method: 'DELETE' },
      );
    },
    async deletePlaylist(id) {
      const pending = this.addTrackPaths.slice();
      const fromDetail = this.panel === 'playlist';
      await fetch('/api/playlists/' + id, { method: 'DELETE' });
      this.currentPlaylist = null;
      await this.refreshPlaylists();
      this.panel = 'playlists';
      this.addTrackPaths = fromDetail ? [] : pending;
    },
    closePanel() {
      this.panel = null;
      this.addTrackPaths = [];
      this.currentPlaylist = null;
    },
    fmt(seconds) {
      seconds = Math.max(0, Math.floor(Number(seconds) || 0));
      const m = Math.floor(seconds / 60);
      const s = seconds % 60;
      return m + ':' + String(s).padStart(2, '0');
    },
    async api(url, opts = {}) {
      const res = await fetch(url, {
        method: opts.method || 'GET',
        headers: opts.body ? { 'Content-Type': 'application/json' } : undefined,
        body: opts.body ? JSON.stringify(opts.body) : undefined,
      });
      if (res.status === 204) return null;
      const data = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(data.error || 'request failed');
      return data;
    },
  };
}
