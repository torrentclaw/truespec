package internal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
)

// VTScanConfig configures the VirusTotal integration for scan results.
type VTScanConfig struct {
	APIKey  string // VirusTotal API key
	Enabled bool   // whether VT scanning is enabled
	Verbose bool   // log progress
}

// EnrichWithVirusTotal scans suspicious files against VirusTotal.
// It modifies the TorrentFiles in-place, adding VT results to suspicious FileInfo entries.
//
// Flow per suspicious file:
//  1. Compute SHA256 from the torrent file on disk (if available)
//  2. Lookup hash on VT
//  3. If found → attach result
//  4. If not found and ≤ 20MB → download full file, upload to VT, poll result
//  5. If not found and > 20MB → mark as suspicious_unscanned
func EnrichWithVirusTotal(ctx context.Context, cfg VTScanConfig, files *TorrentFiles, dl *Downloader, infoHash string) {
	if !cfg.Enabled || cfg.APIKey == "" || files == nil || len(files.Suspicious) == 0 {
		return
	}

	client := NewVTClient(cfg.APIKey)

	for i := range files.Suspicious {
		f := &files.Suspicious[i]

		if cfg.Verbose {
			log.Printf("  [%s] VT: checking %s (%s)", TruncHash(infoHash), filepath.Base(f.Path), HumanizeBytes(f.Size))
		}

		// Try to find file on disk
		localPath := findLocalFile(dl, infoHash, f.Path)

		var sha string
		if localPath != "" {
			var err error
			sha, err = fileSHA256(localPath)
			if err != nil {
				if cfg.Verbose {
					log.Printf("  [%s] VT: sha256 error for %s: %v", TruncHash(infoHash), f.Path, err)
				}
				continue
			}
		}

		// Step 1: Lookup by hash
		if sha != "" {
			report, err := client.LookupHash(ctx, sha)
			if err != nil {
				if cfg.Verbose {
					log.Printf("  [%s] VT: lookup error: %v", TruncHash(infoHash), err)
				}
				f.VT = &VTFileReport{Status: "vt_error"}
				continue
			}

			if report != nil {
				report.Permalink = fmt.Sprintf("%s/%s", vtWebURL, sha)
				f.VT = report
				if cfg.Verbose {
					log.Printf("  [%s] VT: %s → %s (%d/%d)", TruncHash(infoHash),
						filepath.Base(f.Path), report.Status, report.Detections, report.TotalEngines)
				}
				continue
			}
		}

		// Step 2: Not in VT — check if uploadable
		if f.Size > vtMaxUploadB {
			if cfg.Verbose {
				log.Printf("  [%s] VT: %s too large for upload (%s > %dMB)",
					TruncHash(infoHash), filepath.Base(f.Path), HumanizeBytes(f.Size), vtMaxUploadMB)
			}
			f.VT = &VTFileReport{Status: "suspicious_unscanned"}
			continue
		}

		// Download the full file if needed
		fullPath, err := ensureFullFile(ctx, dl, infoHash, f.Path)
		if err != nil {
			if cfg.Verbose {
				log.Printf("  [%s] VT: could not get full file: %v", TruncHash(infoHash), err)
			}
			f.VT = &VTFileReport{Status: "suspicious_unscanned"}
			continue
		}

		// Recompute SHA256 of the complete file
		sha, err = fileSHA256(fullPath)
		if err != nil {
			f.VT = &VTFileReport{Status: "vt_error"}
			continue
		}

		// Check VT again with the full file hash (partial hash may differ)
		report, err := client.LookupHash(ctx, sha)
		if err == nil && report != nil {
			report.Permalink = fmt.Sprintf("%s/%s", vtWebURL, sha)
			f.VT = report
			if cfg.Verbose {
				log.Printf("  [%s] VT: %s found after full download → %s (%d/%d)", TruncHash(infoHash),
					filepath.Base(f.Path), report.Status, report.Detections, report.TotalEngines)
			}
			continue
		}

		// Upload to VT
		if cfg.Verbose {
			log.Printf("  [%s] VT: uploading %s to VirusTotal...", TruncHash(infoHash), filepath.Base(f.Path))
		}

		analysisID, err := client.UploadFile(ctx, fullPath)
		if err != nil {
			if cfg.Verbose {
				log.Printf("  [%s] VT: upload failed: %v", TruncHash(infoHash), err)
			}
			f.VT = &VTFileReport{Status: "vt_error"}
			continue
		}

		// Poll for result
		if cfg.Verbose {
			log.Printf("  [%s] VT: waiting for analysis...", TruncHash(infoHash))
		}

		report, err = client.PollAnalysis(ctx, analysisID)
		if err != nil {
			if cfg.Verbose {
				log.Printf("  [%s] VT: poll failed: %v", TruncHash(infoHash), err)
			}
			f.VT = &VTFileReport{Status: "vt_error"}
			continue
		}

		report.Permalink = fmt.Sprintf("%s/%s", vtWebURL, sha)
		f.VT = report
		if cfg.Verbose {
			log.Printf("  [%s] VT: %s → %s (%d/%d) [uploaded]", TruncHash(infoHash),
				filepath.Base(f.Path), report.Status, report.Detections, report.TotalEngines)
		}
	}

	// Update threat level based on VT results
	updateThreatLevelWithVT(files)
}

// updateThreatLevelWithVT upgrades/confirms threat level based on VT scan results.
func updateThreatLevelWithVT(files *TorrentFiles) {
	hasVTMalware := false
	allVTClean := true
	hasUnscanned := false

	for _, f := range files.Suspicious {
		if f.VT == nil {
			allVTClean = false
			continue
		}
		switch f.VT.Status {
		case "vt_malware":
			hasVTMalware = true
			allVTClean = false
		case "vt_clean":
			// confirmed clean by VT
		case "suspicious_unscanned", "vt_error":
			allVTClean = false
			hasUnscanned = true
		default:
			allVTClean = false
		}
	}

	if hasVTMalware {
		files.ThreatLevel = "vt_malware"
	} else if allVTClean && len(files.Suspicious) > 0 {
		files.ThreatLevel = "vt_clean"
	} else if hasUnscanned {
		files.ThreatLevel = "suspicious_unscanned"
	}
}

// fileSHA256 computes the SHA256 hash of a file.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// findLocalFile tries to find a torrent file on disk via the Downloader.
func findLocalFile(dl *Downloader, infoHash string, filePath string) string {
	if dl == nil {
		return ""
	}
	return dl.FindLocalFile(infoHash, filePath)
}

// ensureFullFile downloads the complete file from the torrent via the Downloader.
func ensureFullFile(ctx context.Context, dl *Downloader, infoHash string, filePath string) (string, error) {
	if dl == nil {
		return "", fmt.Errorf("no downloader available")
	}
	return dl.DownloadFullFile(ctx, infoHash, filePath)
}
