function encodePath(rel) {
  return (rel || '').split('/').filter(Boolean).map(encodeURIComponent).join('/');
}

function playerApp() {
  let es = null;
  let seekTimer = null;

  return {
    path: '',
    parent: '',
    query: '',
    items: [],
    loading: false,
    player: { track: null, playing: false, position: 0, seq: 0, sleepUntil: null, queue: [], random: true },
    notice: '',
    lastSeq: -1,
    lastSeekSeq: -1,
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
    seeking: false,
    lastReport: 0,
    needsUnlock: false,
    isLeader: false,
    coverBroken: false,
    confirmClear: false,
    confirmDeleteId: '',
    volume: 1,

    get folders() { return this.items.filter((i) => i.type === 'dir'); },
    get tracks() { return this.items.filter((i) => i.type === 'track'); },
    get visibleTracks() {
      const q = this.query.trim().toLowerCase();
      if (!q) return this.tracks;
      return this.tracks.filter((t) => {
        const hay = [t.name, t.title, t.artist, t.album].filter(Boolean).join(' ').toLowerCase();
        return hay.includes(q);
      });
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

    account: '',

    async init() {
      this.volume = readVolume();
      setInterval(() => { this.now = Date.now(); }, 1000);
      const me = await this.api('/api/me');
      this.account = me && me.name || '';
      await this.open('');
      this.connectEvents();
      await this.refreshPlayer();
      this.watchWake();
      this.claimLeader();
      this.applyVolume();
    },

    coverUrl(rel) {
      return '/api/covers/' + encodePath(rel);
    },

    async open(rel) {
      this.loading = true;
      this.query = '';
      try {
        const data = await this.api('/api/library?path=' + encodeURIComponent(rel || ''));
        this.path = rel || '';
        this.parent = data.parent || '';
        this.items = data.items || [];
      } catch (err) {
        this.notice = 'Нет связи. Список не обновился.';
      } finally {
        this.loading = false;
      }
    },

    connectEvents() {
      if (es) es.close();
      es = new EventSource('/api/events');
      es.onmessage = (ev) => {
        try {
          this.applyState(JSON.parse(ev.data));
        } catch (err) {
          // A partial frame is ignored; the next event replaces it.
        }
      };
      es.onopen = () => {
        if (this.notice.indexOf('Нет связи') === 0) this.notice = '';
        this.onWake();
      };
      es.onerror = () => {
        this.notice = 'Нет связи, переподключаюсь…';
      };
    },

    watchWake() {
      document.addEventListener('visibilitychange', () => {
        if (document.visibilityState === 'visible') this.onWake();
      });
      window.addEventListener('pageshow', () => this.onWake());
    },

    async onWake() {
      try {
        const state = await this.api('/api/player');
        this.applyState(state, { force: true });
      } catch (err) {
        this.notice = 'Нет связи с плеером';
      }
    },

    claimLeader() {
      const become = () => {
        this.isLeader = true;
        this.applyPlayback(this.player, true);
        this.updateMediaSession();
      };
      if (!navigator.locks) {
        become();
        return;
      }
      navigator.locks.request('kids-music-playback', () => {
        become();
        return new Promise(() => {});
      });
    },

    async refreshPlayer() {
      try {
        this.applyState(await this.api('/api/player'));
      } catch (err) {
        this.notice = 'Нет связи с плеером';
      }
    },

    applyState(state, opts) {
      opts = opts || {};
      if (!state || typeof state.seq !== 'number') return;
      if (state.seq < this.lastSeq) return;
      const same = state.seq === this.lastSeq;
      this.lastSeq = state.seq;
      const prev = this.player.track && this.player.track.path;
      this.player = state;
      if (state.track && state.track.path !== prev) this.coverBroken = false;
      if (state.track) {
        this.items.forEach((item) => {
          if (item.path === state.track.path) item.played = true;
        });
      }
      this.updateMediaSession();
      if (same && !opts.force) return;
      if (!this.isLeader) {
        this.localPosition = state.position || 0;
        this.prepareFollower(state);
        return;
      }
      this.applyPlayback(state, !!opts.force);
    },

    prepareFollower(state) {
      const audio = this.$refs.audio;
      if (!audio || !state.track) return;
      const src = '/api/stream/' + encodePath(state.track.path);
      const abs = new URL(src, window.location.href).href;
      if (audio.src !== abs) {
        audio.src = src;
        audio.pause();
      }
    },

    applyPlayback(state, force) {
      const audio = this.$refs.audio;
      if (!audio) return;
      this.applyVolume();
      if (!state.track) {
        if (!state.playing) audio.pause();
        return;
      }
      const src = '/api/stream/' + encodePath(state.track.path);
      const abs = new URL(src, window.location.href).href;
      const srcChanged = audio.src !== abs;
      this.applying = true;
      if (srcChanged) audio.src = src;
      const seekSeq = state.seekSeq || 0;
      const jumped = seekSeq !== this.lastSeekSeq;
      this.lastSeekSeq = seekSeq;
      const applyPos = () => {
        // While we are the one playing, the server position is just our own
        // report a second late — following it would stutter, and after a
        // backgrounded tab stops reporting it would rewind the track. Move
        // the playhead only on a real jump, a new track, or while paused.
        if (srcChanged || jumped || audio.paused) {
          try { audio.currentTime = state.position || 0; } catch (err) { /* metadata not ready yet */ }
        }
        this.localPosition = audio.currentTime || state.position || 0;
        if (state.playing) {
          if (audio.paused || srcChanged) {
            audio.play().then(() => { this.needsUnlock = false; }).catch(() => { this.needsUnlock = true; });
          }
        } else if (!audio.paused) {
          audio.pause();
        }
        this.applying = false;
        this.updateMediaSession();
      };
      if (!srcChanged || audio.readyState >= 1) applyPos();
      else {
        audio.onloadedmetadata = () => {
          this.duration = audio.duration || 0;
          applyPos();
        };
      }
    },

    updateMediaSession() {
      if (!('mediaSession' in navigator)) return;
      const track = this.isLeader ? this.player.track : null;
      if (!track) {
        navigator.mediaSession.metadata = null;
        return;
      }
      navigator.mediaSession.metadata = new MediaMetadata({
        title: track.title || 'Музыка',
        artist: track.artist || '',
        album: track.album || '',
      });
      const set = (action, fn) => {
        try { navigator.mediaSession.setActionHandler(action, fn); } catch (err) { /* unsupported action */ }
      };
      set('play', () => { this.toggle(); });
      set('pause', () => { this.toggle(); });
      set('previoustrack', () => { this.prev(); });
      set('nexttrack', () => { this.next(); });
      if (this.duration > 0 && navigator.mediaSession.setPositionState) {
        const pos = Math.min(Math.max(0, this.localPosition || 0), this.duration);
        try {
          navigator.mediaSession.setPositionState({ duration: this.duration, playbackRate: 1, position: pos });
        } catch (err) { /* duration is still settling */ }
      }
    },

    onTime() {
      const audio = this.$refs.audio;
      if (!audio || !this.isLeader || this.applying || this.seeking || !this.player.track) return;
      this.localPosition = audio.currentTime || 0;
      this.duration = audio.duration || 0;
      const now = Date.now();
      if (now - this.lastReport < 1000) return;
      this.lastReport = now;
      this.api('/api/player/position', {
        method: 'POST',
        body: { path: this.player.track.path, seconds: this.localPosition },
      }).catch(() => {});
      this.updateMediaSession();
    },

    onEnded() {
      if (!this.isLeader) return;
      this.next();
    },

    onAudioError() {
      // Without this the element stays "applying" and stops reporting, and a
      // broken file looks like the player simply froze with the previous
      // track's time still on the bar.
      this.applying = false;
      this.duration = 0;
      this.localPosition = 0;
      if (!this.isLeader || !this.player.track) return;
      this.notice = 'Не удалось проиграть «' + (this.player.track.title || this.player.track.path) + '»';
      if (this.player.playing) {
        this.api('/api/player/pause', { method: 'POST', body: {} }).catch(() => {});
      }
    },

    unlock() {
      this.needsUnlock = false;
      if (this.isLeader && this.player.playing && this.$refs.audio) {
        this.$refs.audio.play().catch(() => { this.needsUnlock = true; });
      }
    },

    async playTrack(path) {
      await this.tryPlay(() => this.api('/api/player/play', { method: 'POST', body: { path } }));
    },
    async playFirstMatch() {
      const first = this.visibleTracks[0];
      if (first) await this.playTrack(first.path);
    },
    async playFolder() {
      await this.tryPlay(() => this.api('/api/player/play', { method: 'POST', body: { folder: this.path } }));
    },
    async playPlaylist(id, path) {
      const body = path ? { path } : {};
      await this.tryPlay(() => this.api('/api/playlists/' + encodeURIComponent(id) + '/play', { method: 'POST', body }));
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
      try {
        await this.api('/api/player/random', { method: 'POST', body: { random: !this.player.random } });
      } catch (err) {
        this.notice = err.message || 'Нет связи';
      }
    },
    async toggle() {
      if (!this.player.track) return;
      const url = this.player.playing ? '/api/player/pause' : '/api/player/resume';
      try {
        await this.api(url, { method: 'POST', body: {} });
      } catch (err) {
        this.notice = err.message || 'Нет связи';
      }
    },
    async next() {
      const path = this.player.track ? this.player.track.path : '';
      await this.tryPlay(() => this.api('/api/player/next', { method: 'POST', body: { path } }));
    },
    async prev() {
      const path = this.player.track ? this.player.track.path : '';
      await this.tryPlay(() => this.api('/api/player/prev', { method: 'POST', body: { path } }));
    },
    seek(value) {
      const seconds = Number(value);
      this.localPosition = seconds;
      this.seeking = true;
      const audio = this.$refs.audio;
      if (this.isLeader && audio && Number.isFinite(seconds)) {
        try { audio.currentTime = seconds; } catch (err) { /* element is not seekable yet */ }
      }
      clearTimeout(seekTimer);
      seekTimer = setTimeout(() => {
        this.seeking = false;
        this.api('/api/player/seek', { method: 'POST', body: { seconds } }).catch((err) => {
          this.notice = err.message || 'Не удалось перемотать';
        });
      }, 200);
    },
    setVolume(value) {
      const volume = Number(value);
      if (!Number.isFinite(volume)) return;
      this.volume = Math.min(1, Math.max(0, volume));
      this.applyVolume();
      try { localStorage.setItem('kids-music-volume', String(this.volume)); } catch (err) { /* private mode */ }
    },
    applyVolume() {
      const audio = this.$refs.audio;
      if (audio) audio.volume = this.volume;
    },
    async setTimer(minutes) {
      try {
        await this.api('/api/player/timer', { method: 'POST', body: { minutes } });
        this.closePanel();
      } catch (err) {
        this.notice = err.message || 'Нет связи';
      }
    },
    async openHistory() {
      this.confirmClear = false;
      this.panel = 'history';
      try {
        const data = await this.api('/api/history');
        this.history = data.items || [];
      } catch (err) {
        this.notice = err.message || 'Нет связи';
      }
    },
    async clearHistory() {
      try {
        const data = await this.api('/api/history', { method: 'DELETE' });
        this.history = data.items || [];
        this.items.forEach((item) => { item.played = false; });
        this.confirmClear = false;
        this.notice = '';
      } catch (err) {
        this.notice = err.message || 'Не удалось очистить историю';
      }
    },
    async openPlaylists() {
      this.addTrackPaths = [];
      this.currentPlaylist = null;
      this.confirmDeleteId = '';
      this.panel = 'playlists';
      await this.refreshPlaylists();
    },
    async refreshPlaylists() {
      try {
        const data = await this.api('/api/playlists');
        this.playlists = data.items || [];
      } catch (err) {
        this.notice = err.message || 'Нет связи';
      }
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
      try {
        await this.api('/api/playlists/' + encodeURIComponent(pl.id) + '/tracks', { method: 'POST', body: { paths: pending } });
      } catch (err) {
        this.notice = err.message || 'Не удалось добавить трек';
        return;
      }
      await this.openPlaylist(pl.id);
    },
    async removeFromPlaylist(path) {
      if (!this.currentPlaylist) return;
      try {
        this.currentPlaylist = await this.api(
          '/api/playlists/' + encodeURIComponent(this.currentPlaylist.id) + '/tracks/' + encodePath(path),
          { method: 'DELETE' },
        );
      } catch (err) {
        this.notice = err.message || 'Не удалось убрать трек';
      }
    },
    async deletePlaylist(id) {
      const pending = this.addTrackPaths.slice();
      const fromDetail = this.panel === 'playlist';
      try {
        await this.api('/api/playlists/' + encodeURIComponent(id), { method: 'DELETE' });
      } catch (err) {
        this.notice = err.message || 'Не удалось удалить плейлист';
        return;
      }
      this.confirmDeleteId = '';
      this.currentPlaylist = null;
      await this.refreshPlaylists();
      this.panel = 'playlists';
      this.addTrackPaths = fromDetail ? [] : pending;
    },
    closePanel() {
      this.panel = null;
      this.addTrackPaths = [];
      this.currentPlaylist = null;
      this.confirmClear = false;
      this.confirmDeleteId = '';
    },
    fmt(seconds) {
      seconds = Math.max(0, Math.floor(Number(seconds) || 0));
      const m = Math.floor(seconds / 60);
      const s = seconds % 60;
      return m + ':' + String(s).padStart(2, '0');
    },
    async logout() {
      await fetch('/logout', { method: 'POST' });
      window.location.href = '/login';
    },
    async api(url, opts = {}) {
      const res = await fetch(url, {
        method: opts.method || 'GET',
        headers: opts.body ? { 'Content-Type': 'application/json' } : undefined,
        body: opts.body ? JSON.stringify(opts.body) : undefined,
      });
      if (res.status === 401) {
        window.location.href = '/login';
        return null;
      }
      if (res.status === 204) return null;
      const data = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(data.error || 'Нет связи');
      return data;
    },
  };
}

function readVolume() {
  try {
    const raw = localStorage.getItem('kids-music-volume');
    const n = Number(raw);
    if (raw !== null && Number.isFinite(n)) return Math.min(1, Math.max(0, n));
  } catch (err) { /* private mode */ }
  return 1;
}
