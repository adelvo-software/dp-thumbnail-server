# 🎬 Directors Plan Thumbnail Server

**Fast vMix input thumbnail generator — replaces [vmix-snapshot-proxy](https://github.com/jeffmikels/vmix-snapshot-proxy).**

Generate preview thumbnails for every vMix input over the network. Single `.exe`, zero configuration, just start and go.

**[⬇ Download for Windows](https://github.com/adelvo-software/dp-thumbnail-server/releases/latest/download/dp-thumbnail-server.zip)** · Part of [Directors Plan](https://adelvo.io/directors-plan)

---

![DP Thumbnail Server](docs/screenshot.jpg)

## Why switch from vmix-snapshot-proxy?

Both tools use the vMix SnapshotInput API and support all input types. The difference is speed, reliability, and how inputs are identified:

| | vmix-snapshot-proxy | DP Thumbnail Server |
|---|---|---|
| **Identification** | By input number only — breaks when inputs are reordered | **By key/UUID** — stable across reordering |
| **Speed** | Slow, often unreliable | **Fast** — optimized 320×180 thumbnails with ffmpeg |
| **Thumbnail size** | Full resolution (~200–500 KB) | **Optimized ~10 KB** per thumbnail (with ffmpeg) |
| **Setup** | Command line arguments | **Zero config** — auto-detects vMix |
| **Interface** | Basic input list | **Visual web UI** with grid, auto-refresh, right-click menu |
| **Configuration** | Edit CLI flags | **Browser-based settings** |
| **Dependencies** | Node.js required | **None** — single .exe (ffmpeg optional for resize) |
| **Legacy support** | — | `/{number}.jpg` still works for existing integrations |

## How it works

1. Connects to the vMix Web API
2. Reads all inputs (number, key, type, title)
3. Captures preview images via the vMix SnapshotInput API
4. Optionally resizes to 320×180 with ffmpeg for fast loading
5. Serves thumbnails over HTTP on port `8098`

## API Endpoints

| Endpoint | Description |
|---|---|
| `GET /` | Web UI with visual thumbnail grid |
| `GET /{number}.jpg` | Thumbnail by input number (legacy, proxy-compatible) |
| `GET /key/{uuid}.jpg` | Thumbnail by input key (stable, recommended) |
| `GET /key/{uuid}/items.json` | **Video List:** every item with file path, name, selected flag and duration (ffprobe) |
| `GET /key/{uuid}/item/{n}.jpg` | **Video List:** thumbnail of item *n* (1-based), read straight from the file with ffmpeg |
| `GET /regen` | Regenerate all thumbnails |
| `GET /regen/{number}` | Regenerate single input |

## Video Lists (v1.1)

A vMix Video List is a single input with a single key, so its normal thumbnail only shows the item vMix currently has loaded. Since v1.1 the server also gives you every item of the list — **without touching vMix**: vMix reports each item's file path in its XML, and because the server runs on the vMix machine it reads the files directly with ffmpeg / ffprobe. No `SelectIndex`, nothing loaded to Preview, so a running show never notices.

- `items.json` returns path, name, selected item and duration in ms for every entry.
- `item/{n}.jpg` returns a 320×180 frame (at 1 s, or 10 % into short clips; first frame for stills).
- Only paths vMix lists for that input are read. Needs ffmpeg + ffprobe (same folder or PATH). Works for files, not for streams or cameras inside a list. Durations are file durations; in/out marks set in vMix are not applied.

In the web UI, list inputs get an **items ▾** link that shows all entries with thumbnail and duration.

## Quick Start

1. **[⬇ Download `dp-thumbnail-server.zip`](https://github.com/adelvo-software/dp-thumbnail-server/releases/latest/download/dp-thumbnail-server.zip)** (direct download, latest version)
2. Unzip to any folder on your **vMix machine**
3. Double-click `start.bat`
4. Browser opens automatically at `http://localhost:8098` once the first thumbnails are generated
5. Configure vMix connection in the web UI if needed

**Optional:** Install ffmpeg for optimized small thumbnails and for Video List items (includes ffprobe):
```
winget install ffmpeg
```

## Use with Directors Plan

In Directors Plan go to **Settings → Thumbnail Server** and enter the IP and port of the machine running the server:

```
Host: 192.168.1.100    (IP of the vMix machine)
Port: 8098             (default)
```

Directors Plan handles the rest — it automatically fetches thumbnails for all inputs using stable key-based URLs.

## Network Access

The server listens on all interfaces. Access thumbnails from any machine on your network:

```
http://vmix-machine-ip:8098/
```

## Use Cases

- **Church live streaming** — preview all cameras, lyrics, and video inputs at a glance
- **Sports broadcasts** — monitor replay inputs, graphics, and camera feeds
- **Corporate events** — keep track of presentations, speaker cameras, and lower thirds
- **Any vMix production** — works with every input type vMix supports

## System Requirements

- Windows 10/11 (same machine as vMix)
- vMix with Web API enabled (default port 8088)
- Optional: ffmpeg + ffprobe for optimized thumbnail size and Video List items (`winget install ffmpeg`)

## Part of Directors Plan

DP Thumbnail Server is a free companion tool for [Directors Plan](https://adelvo.io/directors-plan) — professional vMix control and automation software for live productions.

Directors Plan features:
- Full automatic rundown with start times, daily/weekly repeats, or constant loop
- Timeline-based rundown planning
- Drag & drop vMix commands
- Stream Deck / Companion integration
- HTML call sheet export
- 2 Different Logs
- 19+ languages
- ++++++++

**[Learn more at adelvo.io/directors-plan](https://adelvo.io/directors-plan)**

---

© 2026 [Adelvo Software](https://adelvo.io)
