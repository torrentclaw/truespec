package internal

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ScanWithStats is like Scan but also records stats for each result.
// The stats object is updated concurrently (caller must not access it until channel is closed).
//
// Internally, it tries to use subprocess isolation for crash resilience.
// If os.Executable() fails (e.g., in minimal containers), it falls back to
// in-process execution with a shared Downloader (original behavior).
func ScanWithStats(ctx context.Context, cfg Config, hashes []string, stats *Stats) <-chan ScanResult {
	results := make(chan ScanResult, cfg.Concurrency)

	go func() {
		defer close(results)

		// Try to get executable path for subprocess isolation
		exePath, exeErr := getExePath()
		useIsolation := exeErr == nil

		var dl *Downloader
		if !useIsolation {
			// Fallback: create shared downloader for in-process mode
			dl, exeErr = NewDownloader(DownloadConfig{
				TempDir:      cfg.TempDir,
				StallTimeout: cfg.StallTimeout,
				MaxTimeout:   cfg.MaxTimeout,
				MinBytesMKV:  cfg.MinBytesMKV,
				MinBytesMP4:  cfg.MinBytesMP4,
			})
			if exeErr != nil {
				for _, h := range hashes {
					result := ScanResult{
						InfoHash: h,
						Status:   "error",
						Error:    "failed to create downloader: " + exeErr.Error(),
					}
					if stats != nil {
						stats.RecordResult(result, 0)
					}
					results <- result
				}
				return
			}
			defer dl.Close()
			log.Printf("subprocess isolation unavailable, using in-process mode: %v", exeErr)
		}

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

				var result ScanResult
				var downloaded, uploaded int64

				if useIsolation {
					// Subprocess isolation mode
					workerInput := cfg.ToWorkerInput(h, idx+1, len(hashes))
					workerOutput, wErr := processOneIsolated(ctx, exePath, workerInput, cfg.LogWriter)
					if wErr != nil {
						result = ScanResult{
							InfoHash:  h,
							Status:    "worker_failed",
							Error:     fmt.Sprintf("worker failed: %v", wErr),
							ElapsedMs: 0,
						}
					} else {
						result = workerOutput.Result
						downloaded = workerOutput.Downloaded
						uploaded = workerOutput.Uploaded
					}
				} else {
					// Fallback in-process mode
					result, downloaded, uploaded = processOneInProcess(ctx, dl, cfg, h, idx+1, len(hashes))
				}

				// Record stats
				if stats != nil {
					mu.Lock()
					stats.RecordResult(result, downloaded)
					stats.RecordTraffic(0, uploaded) // download already counted in RecordResult
					mu.Unlock()
				}

				results <- result

				if !useIsolation {
					log.Printf("[%d/%d] %s -> %s (%dms, dl=%d)",
						idx+1, len(hashes), TruncHash(h), result.Status, result.ElapsedMs, downloaded)
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
		if err != nil {
			log.Printf("  [%s] ffprobe error: %v", TruncHash(infoHash), err)
		} else if media != nil {
			log.Printf("  [%s] ffprobe result: audio=%d subs=%d video=%v",
				TruncHash(infoHash), len(media.Audio), len(media.Subtitles), media.Video != nil)
		}
		if err == nil && media != nil && len(media.Audio) > 0 {
			// Success!
			media.InfoHash = infoHash
			media.Status = "success"
			media.File = dlResult.FileName
			media.Languages = ComputeLanguages(nil, media.Audio)
			media.Files = torrentFiles
			media.Swarm = swarmInfo

			// Propagate duration to the main video file in the file listing
			if media.Video != nil && media.Video.Duration > 0 && torrentFiles != nil {
				for i, vf := range torrentFiles.VideoFiles {
					if vf.Path == dlResult.FileName || strings.HasSuffix(vf.Path, "/"+dlResult.FileName) {
						torrentFiles.VideoFiles[i].Duration = media.Video.Duration
						break
					}
				}
			}

			// Probe duration for other video files in multi-file torrents
			if torrentFiles != nil && len(torrentFiles.VideoFiles) > 1 {
				probeOtherVideoDurations(ctx, dl, infoHash, ffprobePath, dlResult.FileName, torrentFiles)
			}

			// Detect language for single "und" audio tracks
			ApplyLangDetection(ctx, langCfg, media, dlResult.FilePath)

			media.ElapsedMs = time.Since(start).Milliseconds()
			return *media
		}

		// ffprobe failed — request more data without re-downloading from scratch
		if attempt < cfg.MaxFFprobeRetries {
			minBytes *= 2
			log.Printf("  [%s] ffprobe failed (attempt %d/%d), requesting more data (%dKB)",
				TruncHash(infoHash), attempt+1, cfg.MaxFFprobeRetries, minBytes/1024)

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
	} else if strings.Contains(errMsg, "not found on disk") {
		status = "file_not_found"
	}
	return ScanResult{
		InfoHash:  infoHash,
		Status:    status,
		Error:     errMsg,
		ElapsedMs: time.Since(start).Milliseconds(),
	}
}

func TruncHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// probeOtherVideoDurations downloads headers and probes duration for non-main video files.
func probeOtherVideoDurations(ctx context.Context, dl *Downloader, infoHash, ffprobePath, mainFileName string, tf *TorrentFiles) {
	const headerBytes = 2 * 1024 * 1024 // 2 MB

	for i, vf := range tf.VideoFiles {
		if vf.Duration > 0 {
			continue // already has duration (main file)
		}
		if vf.Path == mainFileName || strings.HasSuffix(vf.Path, "/"+mainFileName) {
			continue
		}

		localPath, err := dl.DownloadFileHeader(ctx, infoHash, vf.Path, headerBytes)
		if err != nil {
			log.Printf("  [%s] duration probe skip %s: %v", TruncHash(infoHash), filepath.Base(vf.Path), err)
			continue
		}

		dur := ProbeDuration(ctx, ffprobePath, localPath)
		if dur > 0 {
			tf.VideoFiles[i].Duration = dur
			log.Printf("  [%s] duration probe %s: %.1fs", TruncHash(infoHash), filepath.Base(vf.Path), dur)
		}
	}
}

// getExePath returns the path to the current executable.
// Returns an error if the path cannot be determined (e.g., in minimal containers).
func getExePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	// Resolve symlinks to get the real path
	return filepath.EvalSymlinks(exe)
}
