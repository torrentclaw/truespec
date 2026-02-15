# TrueSpec

[![Go Version](https://img.shields.io/github/go-mod/go-version/torrentclaw/truespec)](https://go.dev/)
[![Release](https://img.shields.io/github/v/release/torrentclaw/truespec)](https://github.com/torrentclaw/truespec/releases)
[![License: AGPL v3](https://img.shields.io/badge/license-AGPL--3.0-blue)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/torrentclaw/truespec)](https://goreportcard.com/report/github.com/torrentclaw/truespec)
[![CI](https://github.com/torrentclaw/truespec/actions/workflows/release.yml/badge.svg)](https://github.com/torrentclaw/truespec/actions/workflows/release.yml)

Torrent metadata often lies. A release might claim to be "4K HDR Dual Audio" but actually be 1080p SDR with a single audio track. TrueSpec solves this by **verifying the real media specs directly from the torrent data** — without downloading the full file.

TrueSpec is developed by [TorrentClaw](https://torrentclaw.com) and used internally to improve the quality of torrent metadata across its platform. We release it as open source so that other platforms, tools, and communities can benefit from it too — and together we can improve the accuracy of torrent search engines and the health of the torrent network.

Given torrent info hashes, magnet links, or `.torrent` files, TrueSpec:

1. **Partially downloads** only the bytes needed (headers/atoms, not the full file)
2. **Runs ffprobe** on the downloaded fragment to extract real metadata
3. **Outputs a JSON report** with verified specs for each torrent

## What does it detect?

- **Video**: codec (H.264, HEVC, AV1...), resolution, bit depth, HDR format (HDR10, Dolby Vision, HLG), frame rate, profile
- **Audio**: all tracks with language, codec (AAC, AC3, DTS...), channel count (stereo, 5.1, 7.1...)
- **Subtitles**: all tracks with language, format (SRT, ASS...), forced/default flags
- **Languages**: normalized ISO 639-1 codes extracted from audio tracks (with Whisper detection for unknown languages)
- **File threats**: detects 30+ dangerous file extensions (.exe, .bat, .dll...) in torrent contents
- **VirusTotal integration**: scans suspicious files against 70+ antivirus engines (hash lookup + auto-upload for files ≤ 20MB)
- **Swarm health**: real-time seeder count, peer count, and traffic stats

## Use cases

- **Media indexers**: automatically verify specs before cataloging releases
- **Quality assurance**: detect mislabeled torrents (fake 4K, missing audio tracks, etc.)
- **Batch auditing**: scan hundreds of hashes and get a structured JSON report
- **API integration**: feed the JSON output into your own tools or databases

## Features

- **Interactive mode** — guided wizard when run without arguments
- **Flexible input** — accepts info hashes, magnet links, `.torrent` files, or folders of `.torrent` files
- **Partial download** — only fetches the minimum bytes needed, not the full file (typically < 20 MB)
- **Parallel scanning** with configurable concurrency
- **Smart piece selection** — handles MP4 moov atoms at end of file
- **Stall detection** and automatic retries with increasing byte thresholds
- **Language normalization** — maps all language tags to ISO 639-1 codes
- **Whisper language detection** — detects audio language for "und" tracks using whisper.cpp (offline, CPU-only)
- **File threat analysis** — scans torrent contents for dangerous files (executables, scripts, suspicious patterns)
- **VirusTotal integration** — checks suspicious files against 70+ antivirus engines (free API, no file uploads for known hashes)
- **Statistics tracking** — persistent scan stats with hourly/daily breakdowns, quality distribution, traffic totals
- **Configuration wizard** — `truespec config` for first-time setup (Whisper, VirusTotal, scan defaults)
- **Cross-platform** — Linux, macOS, Windows (amd64 & arm64)

## Installation

### From releases

Download the latest binary from [Releases](https://github.com/torrentclaw/truespec/releases).

### From source

```bash
go install github.com/torrentclaw/truespec/cmd/truespec@latest
```

### Requirements

- [ffprobe](https://ffmpeg.org/ffprobe.html) must be available in your `PATH` (or specify with `--ffprobe`)

## Usage

```bash
# Interactive mode (guided wizard)
truespec

# Scan by info hash
truespec scan abc123def456...

# Scan by magnet link
truespec scan "magnet:?xt=urn:btih:abc123..."

# Scan a .torrent file
truespec scan movie.torrent

# Scan all .torrent files in a folder
truespec scan ./torrents/

# Scan from a file (one hash or magnet per line)
truespec scan -f hashes.txt

# Scan from stdin
cat hashes.txt | truespec scan --stdin

# With options
truespec scan \
  --concurrency 3 \
  --verbose \
  --output results.json \
  -f examples/hashes.txt

# Configure TrueSpec (interactive wizard)
truespec config

# Show current configuration
truespec config --show

# View scan statistics
truespec stats

# Reset statistics
truespec stats --reset
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--concurrency` | `-c` | `5` | Max concurrent downloads |
| `--stall-timeout` | | `90` | Seconds before killing a stalled torrent |
| `--max-timeout` | | `600` | Absolute max seconds per torrent |
| `--ffprobe` | | auto | Path to ffprobe binary |
| `--temp-dir` | | `/tmp/truespec` | Temp directory for downloads |
| `--verbose` | `-v` | `false` | Print progress to stderr |
| `--output` | `-o` | `results_<timestamp>.json` | Output file path |
| `-f` | | | Read hashes/magnets from file |
| `--stdin` | | `false` | Read hashes/magnets from stdin |

### Environment Variables

| Variable | Description |
|----------|-------------|
| `TRUESPEC_CONCURRENCY` | Default concurrency |
| `TRUESPEC_STALL_TIMEOUT` | Stall timeout in seconds |
| `TRUESPEC_MAX_TIMEOUT` | Max timeout in seconds |
| `TRUESPEC_TEMP_DIR` | Temp directory |
| `FFPROBE_PATH` | Path to ffprobe |
| `WHISPER_PATH` | Path to whisper-cli binary |
| `WHISPER_MODEL` | Path to whisper ggml model |

## Output

Results are saved as JSON:

```json
{
  "version": "0.1.0",
  "scanned_at": "2025-01-15T12:00:00Z",
  "elapsed_ms": 45000,
  "total": 2,
  "stats": { "success": 1, "stall_metadata": 1 },
  "results": [
    {
      "info_hash": "abc123...",
      "status": "success",
      "file": "Movie.mkv",
      "video": {
        "codec": "hevc",
        "width": 3840,
        "height": 2160,
        "bit_depth": 10,
        "hdr": "HDR10",
        "frame_rate": 23.976,
        "profile": "Main 10"
      },
      "audio": [
        { "lang": "en", "codec": "ac3", "channels": 6, "default": true }
      ],
      "subtitles": [
        { "lang": "es", "codec": "subrip", "forced": false, "default": false }
      ],
      "languages": ["en"],
      "files": {
        "total": 5,
        "total_size": 4500000000,
        "video_files": [{"path": "Movie.mkv", "size": 4400000000, "ext": ".mkv"}],
        "suspicious": [
          {
            "path": "setup.exe",
            "size": 2100000,
            "ext": ".exe",
            "reason": "Windows executable",
            "vt": {
              "detected": true,
              "detections": 18,
              "total_engines": 72,
              "malware_names": ["Trojan.GenericKD", "Win32.Malware"],
              "permalink": "https://www.virustotal.com/gui/file/sha256...",
              "status": "vt_malware"
            }
          }
        ],
        "threat_level": "vt_malware"
      },
      "swarm": {
        "active_peers": 12,
        "total_peers": 45,
        "seeds": 8,
        "download_bytes_total": 15728640,
        "upload_bytes_total": 0
      },
      "elapsed_ms": 32000
    }
  ]
}
```

### Status Codes

| Status | Meaning |
|--------|---------|
| `success` | Metadata extracted successfully |
| `stall_metadata` | Timed out waiting for torrent metadata |
| `stall_download` | Timed out during piece download |
| `no_video` | No video file found in the torrent |
| `ffprobe_failed` | ffprobe could not extract metadata |
| `timeout` | Exceeded absolute max timeout |
| `error` | Unexpected error |

### Threat Levels

| Level | Meaning |
|-------|---------|
| `clean` | No suspicious files found |
| `warning` | Archives found (.zip, .rar) that may contain executables |
| `dangerous` | Executable or script files found (not yet verified by VT) |
| `vt_clean` | Suspicious files were scanned by VirusTotal and confirmed clean |
| `vt_malware` | VirusTotal confirmed malware (N/72+ engines detected) |
| `suspicious_unscanned` | File too large for VT upload (>20MB) or VT unavailable |

## Project Structure

```
.
├── cmd/
│   └── truespec/
│       └── main.go          # CLI entry point
├── internal/
│   ├── config.go            # Configuration & defaults
│   ├── downloader.go        # BitTorrent partial download engine
│   ├── input.go             # Input normalization (hash, magnet, .torrent)
│   ├── lang.go              # Language code normalization
│   ├── media.go             # ffprobe integration & metadata extraction
│   ├── scanner.go           # Scan orchestration & retry logic
│   ├── stats.go             # Persistent statistics tracking
│   ├── threat.go            # File threat detection (30+ extensions)
│   ├── virustotal.go        # VirusTotal API v3 client
│   ├── vtscan.go            # VT integration for scan results
│   ├── langdetect.go        # Whisper-based audio language detection
│   ├── userconfig.go        # User configuration (~/.truespec/config.json)
│   ├── whisper_download.go  # Auto-download whisper-cli & models
│   └── types.go             # Data structures
├── examples/
│   └── hashes.txt           # Sample info hashes
├── .github/
│   └── workflows/
│       ├── ci.yml           # PR validation (commit lint, build, test)
│       └── release.yml      # Release pipeline (goreleaser)
├── .goreleaser.yml          # Cross-platform build config
├── lefthook.yml             # Git hooks (commit lint, gofmt, govet)
├── Makefile                 # Dev workflow (build, test, lint, release)
├── LICENSE                  # AGPL-3.0
├── CONTRIBUTING.md          # Contribution guidelines
└── README.md
```

## Roadmap

- [x] **File threat analysis** — detect dangerous files in torrent contents
- [x] **VirusTotal integration** — scan suspicious files against 70+ antivirus engines
- [x] **Whisper language detection** — offline audio language detection for "und" tracks
- [x] **Statistics tracking** — persistent scan stats with quality distribution
- [x] **Configuration wizard** — interactive setup for all features
- [ ] **TorrentClaw API integration** — `truespec scan --push` to submit verified specs directly to the [torrentclaw.com](https://torrentclaw.com) central database
- [ ] **Daemon mode** — run as a background service that continuously processes hashes from a queue
- [ ] **Docker image** — prebuilt container with ffprobe and whisper included
- [ ] **More output formats** — CSV, NDJSON for streaming pipelines
- [ ] **Concurrency optimizations** — smarter scheduling, connection pooling
- [ ] **Bandwidth reduction** — smarter piece selection, metadata caching, skip re-scans

> **Note:** TrueSpec uses BitTorrent traffic to fetch torrent fragments. It is not recommended to run it on VPS or servers with limited/metered bandwidth, as scan volume can add up quickly.

## About TorrentClaw

[TorrentClaw](https://torrentclaw.com) is an open platform focused on improving the quality and reliability of torrent metadata. Our mission is to make torrent search engines more accurate and the torrent ecosystem healthier — by building tools that verify, enrich, and standardize metadata across the network.

TrueSpec is the first open-source tool in the TorrentClaw ecosystem.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

This project is licensed under the [GNU Affero General Public License v3.0](LICENSE).

**You are free to:**
- Use, copy, modify, and distribute the software for any purpose (including commercial)

**You must:**
- Disclose source code of any modifications
- License modifications under AGPL-3.0
- Provide source access to users interacting with the software over a network
