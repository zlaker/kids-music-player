---
name: kids-music-player
description: Product and architecture constraints for the kids music player Go web server. Use when implementing, changing, or reviewing this player, its HTTP API, playback, playlists, history, sleep timer, covers, or Ubuntu deploy.
---

# Kids music player

Home LAN music player. One Go binary. Simple web UI. Runs on Ubuntu Server.

## Decided

- Language: Go, one binary, `go:embed` for UI
- Process: `net/http` web server, default port **21983**
- CLI: required music directory flag; optional `--listen` / `--port`
- UI: Alpine.js + Tailwind (no Vue, no npm runtime)
- Features: file list, sleep timer, simple playlists, played-files history with clear, progress + seek, next track, covers + tags in player and list
- Deploy: must survive a closed SSH session (systemd unit, not `nohup` as the documented path)
- Playback: **HTML5 audio in the browser** — server only serves files + API, no mpv/ALSA
- Auth: open on the LAN while the accounts database is empty. `-user` and `-password` seed an account; once any account exists, a session cookie is required. The cookie holds a random id, and the server resolves it to the user and rejects a disabled account
- Sleep timer: **stop playback only** (no host shutdown, no fade)
- Formats: **mp3 only**
- Library: **folders as albums**, navigate into them
- Clients: **one shared player** — every open page shows the same now-playing / position

## Defaults unless the user overrides

- Stdlib `net/http` only
- Alpine over Vue
- JSON files on disk for playlists + history (no SQLite unless asked)
- Do not delete or move music files from the UI
- Path-safe file serving only inside the music root
- Large tap targets; Russian UI labels
- Shared state over SSE or short polling so all tabs stay in sync
- History records a track when it actually starts playing

## Still open (non-blocking defaults)

- State dir: `~/.kids-music-player` unless `--state-dir` is set
- Cover: embedded ID3 art, else `folder.jpg` / `cover.jpg` in the folder
- Playlist: named list of file paths, play in order, add/remove/delete list
- Previous track: yes, next to next-track
- Shuffle/repeat: random is on by default; skip tracks already in history until history is cleared

## Implementation notes

- Keep `main` thin. Handlers on a struct with deps.
- History and playlists persist across restarts.
- Covers: embedded art first, then `folder.jpg` / `cover.jpg`.
- Progress/seek and next-track must work with the chosen playback backend.
- systemd unit + `Restart=on-failure`; document `audio` group only if server-side playback is chosen.
