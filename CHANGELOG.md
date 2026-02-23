# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Configurable verbose levels** — new `VerboseLevel` setting (0=normal, 1=verbose) configurable via `truespec config` wizard or `--verbose`/`-v` CLI flag. Normal mode shows a compact progress display on stderr while saving detailed logs to a rotating file. Verbose mode prints all logs to stderr (traditional behavior).
- **Log rotation** — in normal mode, detailed scan logs are written to `~/.truespec/logs/truespec.log` with automatic size-based rotation (10 MB max, 5 rotated files). Logs are always produced regardless of verbose level.
- **Progress display** — in normal mode (non-verbose), a live spinner with scan counters is shown on stderr: `⠹ Scanning [3/10]  ✓ 2  ✗ 1  (12s)`. Automatically disabled when stderr is not a TTY.
- **Subprocess isolation** — each torrent scan runs in an isolated subprocess for crash resilience. If a worker crashes (SIGBUS, SIGSEGV, panic), it is reported as `worker_crashed` without affecting other scans. Falls back to in-process mode if `os.Executable()` fails.
- **Video duration** — `duration` field (seconds) now included in `video` and `files.video_files[]` in scan results. For multi-file torrents, durations of secondary video files are probed by downloading 2 MB headers.
- **Multi-track Whisper detection** — language detection now supports up to N audio tracks per torrent (configurable via `whisper_max_tracks`, default 3). Previously limited to single-track torrents.
- **VirusTotal integration** — scan suspicious files against 70+ antivirus engines using the VirusTotal API v3. Flow: SHA256 hash lookup, and if unknown and file ≤ 20MB, download the full file, upload to VT, and poll for results. Rate-limited to 4 req/min (free API tier). New threat levels: `vt_clean`, `vt_malware`, `suspicious_unscanned`.
- **Statistics tracking** — persistent scan stats saved to `~/.truespec/stats.json` with traffic totals, quality distributions (resolution, HDR, codecs, languages), and temporal buckets (hourly/daily). New subcommand: `truespec stats [--json] [--reset] [--file <path>]`. New flags: `--stats-file`, `--no-stats`. New env var: `TRUESPEC_STATS_FILE`.
- **File listing and threat detection** — extract complete torrent file listing from metadata. Categorize files by type (video, audio, subtitle, image, other, suspicious). Detect 30+ dangerous extensions (`.exe`, `.bat`, `.ps1`, `.dll`, `.lnk`...) and 11 warning extensions (`.zip`, `.rar`, `.iso`...). Threat levels: `clean`, `warning`, `dangerous`.
- **Swarm health monitoring** — capture real-time seeder count, active/total peers, and cumulative traffic bytes from the BitTorrent swarm during each scan.
- **Whisper language detection** — auto-detect audio language for tracks with undefined (`und`) language using whisper.cpp (tiny model, CPU-only). Extracts 30s audio via ffmpeg, runs `whisper-cli --detect-language`.
- **Configuration wizard** — new `truespec config` command with interactive wizard for first-time setup. Configure stats, threat detection, Whisper, VirusTotal API key, scan defaults, and output mode. Saves to `~/.truespec/config.json`. Subcommands: `--show`, `--json`, `--reset`.
- **Auto-download whisper-cli and model** — when enabling language detection via `truespec config`, auto-downloads whisper-cli from the latest whisper.cpp GitHub release and ggml-tiny.bin model (~75MB) from HuggingFace. Cached to `~/.truespec/bin/` and `~/.truespec/models/`.
- **Auto-download ffprobe** — when ffprobe is not found in PATH, automatically downloads a static binary from [ffbinaries.com](https://ffbinaries.com) and caches it to `~/.cache/truespec/bin/ffprobe`. Supports Linux (amd64/arm64), macOS (amd64/arm64), and Windows (amd64).
- **SHA256 checksum verification** for downloaded whisper model files.

### Changed

- **Verbose is always on internally** — the `Verbose bool` field has been removed from `Config`, `DownloadConfig`, `WorkerInput`, and `VTScanConfig`. All log statements are now unconditional. The `VerboseLevel` setting controls where logs are routed (stderr vs file), not whether they are produced.
- **Worker stderr routing** — worker subprocess stderr is routed through `prefixWriter` to the parent's configured log destination (rotating log file or stderr), rather than always going to stderr.

### Fixed

- **prefixWriter io.Writer contract** — `Write()` now returns `len(data)` on success (previously returned bytes written to the underlying writer including prefix bytes). Prefix and line content are batched into a single `Write()` call instead of two separate calls.
- **File lookup hardening** — retry logic with 1-second delay for files not yet flushed to disk, stale piece-completion database cleanup, recursive directory walk fallback for torrents with wrapper directories.
- **Nil panic guards** — `GetTorrentStats`, `GetFileList`, `GetSwarmInfo`, `FindLocalFile`, `DownloadFullFile`, and `DownloadFileHeader` now recover from panics caused by stale torrent handles.
- **`file_not_found` status** — new status code when downloaded file cannot be located on disk (previously reported as generic `error`).
- **Download stats capture** — stats are now captured before torrent cleanup to avoid stale handle panics.
- **Whisper build from source** — on Linux/macOS, whisper-cli is built from source if no prebuilt binary matches the platform. Windows uses prebuilt zip assets.
- **Path deduplication** — input normalization now deduplicates resolved hashes.
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
