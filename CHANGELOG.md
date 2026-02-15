# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **VirusTotal integration** — scan suspicious files against 70+ antivirus engines using the VirusTotal API v3. Flow: SHA256 hash lookup, and if unknown and file ≤ 20MB, download the full file, upload to VT, and poll for results. Rate-limited to 4 req/min (free API tier). New threat levels: `vt_clean`, `vt_malware`, `suspicious_unscanned`.
- **Statistics tracking** — persistent scan stats saved to `~/.truespec/stats.json` with traffic totals, quality distributions (resolution, HDR, codecs, languages), and temporal buckets (hourly/daily). New subcommand: `truespec stats [--json] [--reset] [--file <path>]`. New flags: `--stats-file`, `--no-stats`. New env var: `TRUESPEC_STATS_FILE`.
- **File listing and threat detection** — extract complete torrent file listing from metadata. Categorize files by type (video, audio, subtitle, image, other, suspicious). Detect 30+ dangerous extensions (`.exe`, `.bat`, `.ps1`, `.dll`, `.lnk`...) and 11 warning extensions (`.zip`, `.rar`, `.iso`...). Threat levels: `clean`, `warning`, `dangerous`.
- **Swarm health monitoring** — capture real-time seeder count, active/total peers, and cumulative traffic bytes from the BitTorrent swarm during each scan.
- **Whisper language detection** — auto-detect audio language for single-track torrents with undefined (`und`) language using whisper.cpp (tiny model, CPU-only). Extracts 30s audio via ffmpeg, runs `whisper-cli --detect-language`.
- **Configuration wizard** — new `truespec config` command with interactive wizard for first-time setup. Configure stats, threat detection, Whisper, VirusTotal API key, and scan defaults. Saves to `~/.truespec/config.json`. Subcommands: `--show`, `--json`, `--reset`.
- **Auto-download whisper-cli and model** — when enabling language detection via `truespec config`, auto-downloads whisper-cli from the latest whisper.cpp GitHub release and ggml-tiny.bin model (~75MB) from HuggingFace. Cached to `~/.truespec/bin/` and `~/.truespec/models/`.
- **Auto-download ffprobe** — when ffprobe is not found in PATH, automatically downloads a static binary from [ffbinaries.com](https://ffbinaries.com) and caches it to `~/.cache/truespec/bin/ffprobe`. Supports Linux (amd64/arm64), macOS (amd64/arm64), and Windows (amd64).
- **SHA256 checksum verification** for downloaded whisper model files.

### Fixed

- **Windows compatibility** — `atomicRename()` removes destination before renaming on Windows (where `os.Rename` cannot overwrite). Affects stats and config file saves.
- **Windows binary names** — auto-downloaded binaries use `.exe` suffix on Windows (`ffprobe.exe`, `whisper-cli.exe`). Whisper asset patterns support `win-x86_64` and `win-arm64`.
- **Temp directory** — default changed from hardcoded `/tmp/truespec` to `os.TempDir()/truespec` for cross-platform correctness.
- **HTTP timeouts for whisper downloads** — API calls now have 30s timeout, binary/model downloads have 10-minute timeout. Previously used `http.DefaultClient` (no timeout).
- **Download size limits** — max 500MB for tar.gz extraction, max 200MB for model downloads. Prevents zip bombs and disk-fill attacks.
- **Panic in ShowConfig** — crash when VirusTotal API key shorter than 4 characters. New `maskAPIKey()` handles keys of any length.
- **`.js` extension reclassified** — moved from dangerous to warning category to reduce false positives in web application torrents.
- **SwarmInfo field names** — renamed `DownloadBps`/`UploadBps` to `DownloadBytesTotal`/`UploadBytesTotal` (values are cumulative bytes, not rates).
- **Cache language detection resolution** — `ResolveLangDetect()` now cached with `sync.Once` (runs once per process instead of once per torrent).
- **Optimize temporal bucket lookups** — hourly/daily bucket scans now iterate in reverse (most recent first).
