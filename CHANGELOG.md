# DP Thumbnail Server — Changelog

## 1.1.0 — 2026-10-08
- **Video List items without touching vMix:** `GET /key/{key}/items.json` (path, name, selected, duration via ffprobe) and `GET /key/{key}/item/{n}.jpg` (frame read from the file with ffmpeg). vMix lists the file paths in its XML; the server reads them directly on the vMix machine — no SelectIndex, no Preview, safe during a live show. Reported by MaxSpecs in the vMix forum (list thumbnail stayed on the first item).
- Web UI: list inputs get an "items ▾" link with all entries, thumbnails and durations.
- ffmpeg / ffprobe are also found next to the .exe (Go no longer searches the current directory).
- First start: the web UI opened before the automatic generation had finished and stayed empty until a reload or Generate All. The browser now opens only after the startup run has finished. If vMix is not running yet, it opens right away, refreshes itself until the count is stable, and the startup run waits for vMix.
- vMix XML for the list routes is cached for 3 s (one fetch per expand instead of one per item).

## 1.0.0 — 2026-04-08
- Key-based URLs, web UI, auto-refresh, single .exe.
