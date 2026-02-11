package internal

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

// Scan processes a list of info hashes concurrently, returning results via channel.
// Results are emitted as each torrent completes (not in input order).
func Scan(ctx context.Context, cfg Config, hashes []string) <-chan ScanResult {
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
				results <- ScanResult{
					InfoHash: h,
					Status:   "error",
					Error:    "failed to create downloader: " + err.Error(),
				}
			}
			return
		}
		defer dl.Close()

		sem := make(chan struct{}, cfg.Concurrency)
		var wg sync.WaitGroup

		for i, hash := range hashes {
			select {
			case <-ctx.Done():
				for _, h := range hashes[i:] {
					results <- ScanResult{
						InfoHash: h,
						Status:   "error",
						Error:    "cancelled",
					}
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
				results <- result

				if cfg.Verbose {
					log.Printf("[%d/%d] %s -> %s (%dms)",
						idx+1, len(hashes), truncHash(h), result.Status, result.ElapsedMs)
				}
			}(hash, i)
		}

		wg.Wait()
	}()

	return results
}

func processOne(ctx context.Context, dl *Downloader, cfg Config, infoHash string) ScanResult {
	start := time.Now()

	// Start with the smaller MKV threshold — the downloader will automatically
	// also request end pieces for MP4 files (for moov atom).
	minBytes := cfg.MinBytesMKV

	// Initial download
	dlResult, err := dl.PartialDownload(ctx, infoHash, minBytes)
	if err != nil {
		return errorResult(infoHash, err, start)
	}

	// If MP4, the initial download already got start+end pieces.
	// Adjust minBytes for retry calculations.
	if dlResult.Ext == ".mp4" || dlResult.Ext == ".m4v" {
		minBytes = cfg.MinBytesMP4
	}

	// Resolve ffprobe (done per-torrent to support concurrent access)
	ffprobePath, err := ResolveFFprobe(cfg.FFprobePath)
	if err != nil {
		dl.Cleanup(infoHash)
		return ScanResult{
			InfoHash:  infoHash,
			Status:    "error",
			Error:     err.Error(),
			ElapsedMs: time.Since(start).Milliseconds(),
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
			media.ElapsedMs = time.Since(start).Milliseconds()
			dl.Cleanup(infoHash)
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
				dl.Cleanup(infoHash)
				return errorResult(infoHash, err, start)
			}
			continue
		}
	}

	// All retries exhausted
	dl.Cleanup(infoHash)
	return ScanResult{
		InfoHash:  infoHash,
		Status:    "ffprobe_failed",
		File:      dlResult.FileName,
		ElapsedMs: time.Since(start).Milliseconds(),
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
