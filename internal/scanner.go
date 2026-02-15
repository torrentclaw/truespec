package internal

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

// ScanWithStats is like Scan but also records stats for each result.
// The stats object is updated concurrently (caller must not access it until channel is closed).
func ScanWithStats(ctx context.Context, cfg Config, hashes []string, stats *Stats) <-chan ScanResult {
	results := make(chan ScanResult, cfg.Concurrency)

	go func() {
		defer close(results)

		dl, err := NewDownloader(DownloadConfig{
			TempDir:      cfg.TempDir,
			StallTimeout: cfg.StallTimeout,
			MaxTimeout:   cfg.MaxTimeout,
			Verbose:      cfg.Verbose,
			MinBytesMKV:  cfg.MinBytesMKV,
			MinBytesMP4:  cfg.MinBytesMP4,
		})
		if err != nil {
			for _, h := range hashes {
				result := ScanResult{
					InfoHash: h,
					Status:   "error",
					Error:    "failed to create downloader: " + err.Error(),
				}
				if stats != nil {
					stats.RecordResult(result, 0)
				}
				results <- result
			}
			return
		}
		defer dl.Close()

		sem := make(chan struct{}, cfg.Concurrency)
		var wg sync.WaitGroup
		var mu sync.Mutex // protects stats

		for i, hash := range hashes {
			select {
			case <-ctx.Done():
				for _, h := range hashes[i:] {
					result := ScanResult{
						InfoHash: h,
						Status:   "error",
						Error:    "cancelled",
					}
					if stats != nil {
						mu.Lock()
						stats.RecordResult(result, 0)
						mu.Unlock()
					}
					results <- result
				}
				wg.Wait()
				return
			case sem <- struct{}{}:
			}

			wg.Add(1)
			go func(h string, idx int) {
				defer wg.Done()
				defer func() { <-sem }()

				if cfg.Verbose {
					log.Printf("[%d/%d] scanning %s", idx+1, len(hashes), truncHash(h))
				}

				result := processOne(ctx, dl, cfg, h)

				// Capture traffic stats BEFORE cleanup — the torrent must
				// still be in the client for stats to be available.
				var downloaded, uploaded int64
				if stats != nil {
					downloaded, uploaded = dl.GetTorrentStats(h)
					mu.Lock()
					stats.RecordResult(result, downloaded)
					stats.RecordTraffic(0, uploaded) // download already counted in RecordResult
					mu.Unlock()
				}

				// Cleanup AFTER stats capture — this drops the torrent from
				// the client and removes temporary files.
				dl.Cleanup(h)

				results <- result

				if cfg.Verbose {
					log.Printf("[%d/%d] %s -> %s (%dms, dl=%d)",
						idx+1, len(hashes), truncHash(h), result.Status, result.ElapsedMs, downloaded)
				}
			}(hash, i)
		}

		wg.Wait()
	}()

	return results
}

// Scan processes a list of info hashes concurrently, returning results via channel.
// Results are emitted as each torrent completes (not in input order).
func Scan(ctx context.Context, cfg Config, hashes []string) <-chan ScanResult {
	return ScanWithStats(ctx, cfg, hashes, nil)
}

// processOne handles a single torrent scan. It does NOT call Cleanup —
// the caller is responsible for cleanup after capturing stats.
func processOne(ctx context.Context, dl *Downloader, cfg Config, infoHash string) ScanResult {
	// Resolve language detection config once (cached after first call)
	langCfg := ResolveLangDetect()
	start := time.Now()

	// Start with the smaller MKV threshold — the downloader will automatically
	// also request end pieces for MP4 files (for moov atom).
	minBytes := cfg.MinBytesMKV

	// Initial download
	dlResult, err := dl.PartialDownload(ctx, infoHash, minBytes)
	if err != nil {
		// Even on download failure, try to capture file listing if metadata was resolved
		result := errorResult(infoHash, err, start)
		fileList := dl.GetFileList(infoHash)
		if len(fileList) > 0 {
			result.Files = AnalyzeFiles(fileList)
		}
		swarm := dl.GetSwarmInfo(infoHash)
		if swarm != nil {
			result.Swarm = swarm
		}
		return result
	}

	// Capture file listing (available since metadata is resolved)
	fileList := dl.GetFileList(infoHash)
	var torrentFiles *TorrentFiles
	if len(fileList) > 0 {
		torrentFiles = AnalyzeFiles(fileList)
	}

	// Capture swarm info before any cleanup
	swarmInfo := dl.GetSwarmInfo(infoHash)

	// If MP4, the initial download already got start+end pieces.
	// Adjust minBytes for retry calculations.
	if dlResult.Ext == ".mp4" || dlResult.Ext == ".m4v" {
		minBytes = cfg.MinBytesMP4
	}

	// Resolve ffprobe (done per-torrent to support concurrent access)
	ffprobePath, err := ResolveFFprobe(cfg.FFprobePath)
	if err != nil {
		return ScanResult{
			InfoHash:  infoHash,
			Status:    "error",
			Error:     err.Error(),
			ElapsedMs: time.Since(start).Milliseconds(),
			Files:     torrentFiles,
			Swarm:     swarmInfo,
		}
	}

	// Try ffprobe, with retries requesting more data
	for attempt := 0; attempt <= cfg.MaxFFprobeRetries; attempt++ {
		media, err := ExtractMediaInfo(ctx, ffprobePath, dlResult.FilePath)
		if cfg.Verbose {
			if err != nil {
				log.Printf("  [%s] ffprobe error: %v", truncHash(infoHash), err)
			} else if media != nil {
				log.Printf("  [%s] ffprobe result: audio=%d subs=%d video=%v",
					truncHash(infoHash), len(media.Audio), len(media.Subtitles), media.Video != nil)
			}
		}
		if err == nil && media != nil && len(media.Audio) > 0 {
			// Success!
			media.InfoHash = infoHash
			media.Status = "success"
			media.File = dlResult.FileName
			media.Languages = ComputeLanguages(nil, media.Audio)
			media.Files = torrentFiles
			media.Swarm = swarmInfo

			// Detect language for single "und" audio tracks
			ApplyLangDetection(ctx, langCfg, media, dlResult.FilePath)

			media.ElapsedMs = time.Since(start).Milliseconds()
			return *media
		}

		// ffprobe failed — request more data without re-downloading from scratch
		if attempt < cfg.MaxFFprobeRetries {
			minBytes *= 2
			if cfg.Verbose {
				log.Printf("  [%s] ffprobe failed (attempt %d/%d), requesting more data (%dKB)",
					truncHash(infoHash), attempt+1, cfg.MaxFFprobeRetries, minBytes/1024)
			}

			if err := dl.RequestMorePieces(ctx, infoHash, minBytes); err != nil {
				result := errorResult(infoHash, err, start)
				result.Files = torrentFiles
				result.Swarm = swarmInfo
				return result
			}
			continue
		}
	}

	// All retries exhausted
	return ScanResult{
		InfoHash:  infoHash,
		Status:    "ffprobe_failed",
		File:      dlResult.FileName,
		ElapsedMs: time.Since(start).Milliseconds(),
		Files:     torrentFiles,
		Swarm:     swarmInfo,
	}
}

func errorResult(infoHash string, err error, start time.Time) ScanResult {
	status := "error"
	errMsg := err.Error()
	if strings.Contains(errMsg, "metadata timeout") {
		status = "stall_metadata"
	} else if strings.Contains(errMsg, "stall") {
		status = "stall_download"
	} else if strings.Contains(errMsg, "max timeout") {
		status = "timeout"
	} else if strings.Contains(errMsg, "no video file") {
		status = "no_video"
	}
	return ScanResult{
		InfoHash:  infoHash,
		Status:    status,
		Error:     errMsg,
		ElapsedMs: time.Since(start).Milliseconds(),
	}
}

func truncHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}
