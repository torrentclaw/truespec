package internal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Stats holds accumulated statistics across all TrueSpec scan sessions.
type Stats struct {
	// Metadata
	Version       string `json:"version"`
	StartedAt     string `json:"started_at"`
	LastUpdatedAt string `json:"last_updated_at"`

	// Traffic
	DownloadBytes           int64 `json:"download_bytes"`
	UploadBytes             int64 `json:"upload_bytes"`
	PeakDownloadBytesPerSec int64 `json:"peak_download_bytes_per_sec"`

	// Torrents scanned
	TotalScanned   int64            `json:"total_scanned"`
	TotalSuccess   int64            `json:"total_success"`
	TotalFailed    int64            `json:"total_failed"`
	FailuresByType map[string]int64 `json:"failures_by_type"`

	// Performance
	TotalElapsedMs        int64 `json:"total_elapsed_ms"`
	AvgElapsedMs          int64 `json:"avg_elapsed_ms"`
	PeakConcurrent        int   `json:"peak_concurrent"`
	TotalPiecesDownloaded int64 `json:"total_pieces_downloaded"`
	AvgBytesPerTorrent    int64 `json:"avg_bytes_per_torrent"`

	// Quality distribution
	ResolutionDist map[string]int64 `json:"resolution_dist"`
	CodecDist      map[string]int64 `json:"codec_dist"`
	HDRDist        map[string]int64 `json:"hdr_dist"`
	AudioCodecDist map[string]int64 `json:"audio_codec_dist"`
	LanguageDist   map[string]int64 `json:"language_dist"`

	// Temporal
	HourlyStats []HourlyBucket `json:"hourly_stats"`
	DailyStats  []DailyBucket  `json:"daily_stats"`

	// Sessions
	TotalSessions int64 `json:"total_sessions"`
}

// HourlyBucket holds stats for a single hour.
type HourlyBucket struct {
	Hour          string `json:"hour"` // "2026-02-14T19"
	Scanned       int64  `json:"scanned"`
	Success       int64  `json:"success"`
	Failed        int64  `json:"failed"`
	DownloadBytes int64  `json:"download_bytes"`
}

// DailyBucket holds stats for a single day.
type DailyBucket struct {
	Day           string `json:"day"` // "2026-02-14"
	Scanned       int64  `json:"scanned"`
	Success       int64  `json:"success"`
	Failed        int64  `json:"failed"`
	DownloadBytes int64  `json:"download_bytes"`
}

// NewStats creates a new Stats with all maps initialized.
func NewStats() *Stats {
	return &Stats{
		FailuresByType: make(map[string]int64),
		ResolutionDist: make(map[string]int64),
		CodecDist:      make(map[string]int64),
		HDRDist:        make(map[string]int64),
		AudioCodecDist: make(map[string]int64),
		LanguageDist:   make(map[string]int64),
		HourlyStats:    []HourlyBucket{},
		DailyStats:     []DailyBucket{},
	}
}

// LoadStats loads stats from a JSON file. Returns empty stats if the file does not exist.
func LoadStats(path string) (*Stats, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewStats(), nil
		}
		return nil, fmt.Errorf("read stats file: %w", err)
	}

	s := NewStats()
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("parse stats file: %w", err)
	}

	// Ensure maps are never nil after unmarshaling
	if s.FailuresByType == nil {
		s.FailuresByType = make(map[string]int64)
	}
	if s.ResolutionDist == nil {
		s.ResolutionDist = make(map[string]int64)
	}
	if s.CodecDist == nil {
		s.CodecDist = make(map[string]int64)
	}
	if s.HDRDist == nil {
		s.HDRDist = make(map[string]int64)
	}
	if s.AudioCodecDist == nil {
		s.AudioCodecDist = make(map[string]int64)
	}
	if s.LanguageDist == nil {
		s.LanguageDist = make(map[string]int64)
	}
	if s.HourlyStats == nil {
		s.HourlyStats = []HourlyBucket{}
	}
	if s.DailyStats == nil {
		s.DailyStats = []DailyBucket{}
	}

	return s, nil
}

// Save writes stats to a JSON file atomically (temp file + rename).
func (s *Stats) Save(path string) error {
	s.LastUpdatedAt = time.Now().UTC().Format(time.RFC3339)

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create stats dir: %w", err)
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal stats: %w", err)
	}

	tmpFile := path + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0o644); err != nil {
		return fmt.Errorf("write temp stats: %w", err)
	}

	if err := atomicRename(tmpFile, path); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("rename stats file: %w", err)
	}

	return nil
}

// RecordResult updates stats from a single scan result.
func (s *Stats) RecordResult(result ScanResult, downloadedBytes int64) {
	now := time.Now().UTC()

	if s.StartedAt == "" {
		s.StartedAt = now.Format(time.RFC3339)
	}

	s.TotalScanned++
	s.TotalElapsedMs += result.ElapsedMs
	s.DownloadBytes += downloadedBytes

	hourKey := now.Format("2006-01-02T15")
	dayKey := now.Format("2006-01-02")

	isSuccess := result.Status == "success"

	if isSuccess {
		s.TotalSuccess++
		s.recordQuality(result)
	} else {
		s.TotalFailed++
		s.FailuresByType[result.Status]++
	}

	// Update hourly bucket
	s.updateHourlyBucket(hourKey, isSuccess, downloadedBytes)

	// Update daily bucket
	s.updateDailyBucket(dayKey, isSuccess, downloadedBytes)
}

// RecordSession increments the session counter.
func (s *Stats) RecordSession() {
	s.TotalSessions++
	if s.StartedAt == "" {
		s.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
}

// RecordTraffic updates traffic counters.
func (s *Stats) RecordTraffic(downloaded, uploaded int64) {
	s.DownloadBytes += downloaded
	s.UploadBytes += uploaded
}

// RecordPeakSpeed updates peak download speed if current is higher.
func (s *Stats) RecordPeakSpeed(bytesPerSec int64) {
	if bytesPerSec > s.PeakDownloadBytesPerSec {
		s.PeakDownloadBytesPerSec = bytesPerSec
	}
}

// PruneOldBuckets removes hourly buckets older than 48h and daily older than 30 days.
func (s *Stats) PruneOldBuckets() {
	now := time.Now().UTC()
	cutoffHour := now.Add(-48 * time.Hour).Format("2006-01-02T15")
	cutoffDay := now.Add(-30 * 24 * time.Hour).Format("2006-01-02")

	var prunedHourly []HourlyBucket
	for _, b := range s.HourlyStats {
		if b.Hour >= cutoffHour {
			prunedHourly = append(prunedHourly, b)
		}
	}
	if prunedHourly == nil {
		prunedHourly = []HourlyBucket{}
	}
	s.HourlyStats = prunedHourly

	var prunedDaily []DailyBucket
	for _, b := range s.DailyStats {
		if b.Day >= cutoffDay {
			prunedDaily = append(prunedDaily, b)
		}
	}
	if prunedDaily == nil {
		prunedDaily = []DailyBucket{}
	}
	s.DailyStats = prunedDaily
}

// Compute recalculates derived fields.
func (s *Stats) Compute() {
	if s.TotalScanned > 0 {
		s.AvgElapsedMs = s.TotalElapsedMs / s.TotalScanned
		s.AvgBytesPerTorrent = s.DownloadBytes / s.TotalScanned
	}
}

func (s *Stats) recordQuality(result ScanResult) {
	if result.Video != nil {
		// Resolution
		res := resolutionCategory(result.Video.Width, result.Video.Height)
		s.ResolutionDist[res]++

		// Codec
		codec := strings.ToLower(result.Video.Codec)
		if codec != "" {
			s.CodecDist[codec]++
		}

		// HDR
		hdr := result.Video.HDR
		if hdr == "" {
			hdr = "SDR"
		}
		s.HDRDist[hdr]++
	}

	// Audio codecs
	for _, a := range result.Audio {
		codec := strings.ToLower(a.Codec)
		if codec != "" {
			s.AudioCodecDist[codec]++
		}
	}

	// Languages
	for _, lang := range result.Languages {
		if lang != "" && lang != "und" {
			s.LanguageDist[lang]++
		}
	}
}

func (s *Stats) updateHourlyBucket(hourKey string, success bool, downloadedBytes int64) {
	// Use index map for O(1) lookup instead of linear scan
	idx := s.hourlyIndex(hourKey)
	s.HourlyStats[idx].Scanned++
	s.HourlyStats[idx].DownloadBytes += downloadedBytes
	if success {
		s.HourlyStats[idx].Success++
	} else {
		s.HourlyStats[idx].Failed++
	}
}

// hourlyIndex returns the index for the given hour key, creating a new bucket if needed.
func (s *Stats) hourlyIndex(hourKey string) int {
	for i := len(s.HourlyStats) - 1; i >= 0; i-- {
		if s.HourlyStats[i].Hour == hourKey {
			return i
		}
	}
	s.HourlyStats = append(s.HourlyStats, HourlyBucket{Hour: hourKey})
	return len(s.HourlyStats) - 1
}

func (s *Stats) updateDailyBucket(dayKey string, success bool, downloadedBytes int64) {
	idx := s.dailyIndex(dayKey)
	s.DailyStats[idx].Scanned++
	s.DailyStats[idx].DownloadBytes += downloadedBytes
	if success {
		s.DailyStats[idx].Success++
	} else {
		s.DailyStats[idx].Failed++
	}
}

// dailyIndex returns the index for the given day key, creating a new bucket if needed.
func (s *Stats) dailyIndex(dayKey string) int {
	for i := len(s.DailyStats) - 1; i >= 0; i-- {
		if s.DailyStats[i].Day == dayKey {
			return i
		}
	}
	s.DailyStats = append(s.DailyStats, DailyBucket{Day: dayKey})
	return len(s.DailyStats) - 1
}

// resolutionCategory maps width/height to a resolution category.
func resolutionCategory(width, height int) string {
	// Use height as primary indicator
	h := height
	if width > height {
		// landscape — use height
		h = height
	} else {
		// portrait or square — use width
		h = width
	}

	switch {
	case h >= 2160:
		return "2160p"
	case h >= 1080:
		return "1080p"
	case h >= 720:
		return "720p"
	case h >= 480:
		return "480p"
	case h > 0:
		return "other"
	default:
		return "unknown"
	}
}

// HumanizeBytes formats bytes into a human-readable string.
func HumanizeBytes(b int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
		TB = 1024 * GB
	)

	switch {
	case b >= TB:
		return fmt.Sprintf("%.2f TB", float64(b)/float64(TB))
	case b >= GB:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// FormatStats returns a human-readable stats summary.
func FormatStats(s *Stats) string {
	var sb strings.Builder

	sb.WriteString("TrueSpec Stats\n")
	sb.WriteString("══════════════════════════════════════\n\n")

	// Traffic
	sb.WriteString("Traffic\n")
	sb.WriteString(fmt.Sprintf("  Downloaded:    %s\n", HumanizeBytes(s.DownloadBytes)))
	sb.WriteString(fmt.Sprintf("  Uploaded:      %s\n", HumanizeBytes(s.UploadBytes)))
	sb.WriteString(fmt.Sprintf("  Peak speed:    %s/s\n", HumanizeBytes(s.PeakDownloadBytesPerSec)))
	sb.WriteString("\n")

	// Scans
	sb.WriteString("Scans\n")
	sb.WriteString(fmt.Sprintf("  Total:         %d\n", s.TotalScanned))
	if s.TotalScanned > 0 {
		successPct := float64(s.TotalSuccess) / float64(s.TotalScanned) * 100
		failPct := float64(s.TotalFailed) / float64(s.TotalScanned) * 100
		sb.WriteString(fmt.Sprintf("  Success:       %d (%.1f%%)\n", s.TotalSuccess, successPct))
		sb.WriteString(fmt.Sprintf("  Failed:        %d (%.1f%%)\n", s.TotalFailed, failPct))

		// Failure breakdown sorted by count descending
		if len(s.FailuresByType) > 0 {
			type kv struct {
				Key   string
				Value int64
			}
			var sorted []kv
			for k, v := range s.FailuresByType {
				sorted = append(sorted, kv{k, v})
			}
			sort.Slice(sorted, func(i, j int) bool { return sorted[i].Value > sorted[j].Value })
			for _, pair := range sorted {
				sb.WriteString(fmt.Sprintf("    %-20s %d\n", pair.Key+":", pair.Value))
			}
		}
	}
	sb.WriteString("\n")

	// Performance
	sb.WriteString("Performance\n")
	if s.TotalScanned > 0 {
		sb.WriteString(fmt.Sprintf("  Avg time/torrent:  %.1fs\n", float64(s.AvgElapsedMs)/1000))
		sb.WriteString(fmt.Sprintf("  Avg bytes/torrent: %s\n", HumanizeBytes(s.AvgBytesPerTorrent)))
	}
	sb.WriteString(fmt.Sprintf("  Sessions:          %d\n", s.TotalSessions))
	sb.WriteString("\n")

	// Quality Distribution
	if s.TotalSuccess > 0 {
		sb.WriteString("Quality Distribution\n")

		// Resolution
		sb.WriteString("  Resolution:  ")
		sb.WriteString(formatDistribution(s.ResolutionDist, s.TotalSuccess))
		sb.WriteString("\n")

		// Codec
		sb.WriteString("  Codec:       ")
		sb.WriteString(formatDistribution(s.CodecDist, s.TotalSuccess))
		sb.WriteString("\n")

		// HDR
		sb.WriteString("  HDR:         ")
		sb.WriteString(formatDistribution(s.HDRDist, s.TotalSuccess))
		sb.WriteString("\n")

		// Top languages
		sb.WriteString("  Top langs:   ")
		sb.WriteString(formatDistributionTop(s.LanguageDist, 5))
		sb.WriteString("\n")

		sb.WriteString("\n")
	}

	// Today
	today := time.Now().UTC().Format("2006-01-02")
	for _, d := range s.DailyStats {
		if d.Day == today {
			sb.WriteString(fmt.Sprintf("Today (%s)\n", today))
			sb.WriteString(fmt.Sprintf("  Scanned: %d  |  Success: %d  |  Downloaded: %s\n",
				d.Scanned, d.Success, HumanizeBytes(d.DownloadBytes)))
			sb.WriteString("\n")
			break
		}
	}

	// Uptime
	if s.StartedAt != "" {
		if started, err := time.Parse(time.RFC3339, s.StartedAt); err == nil {
			uptime := time.Since(started)
			days := int(uptime.Hours() / 24)
			if days > 0 {
				sb.WriteString(fmt.Sprintf("Running since: %s (%d days)\n", s.StartedAt, days))
			} else {
				sb.WriteString(fmt.Sprintf("Running since: %s (%.0f hours)\n", s.StartedAt, uptime.Hours()))
			}
		}
	}
	if s.LastUpdatedAt != "" {
		sb.WriteString(fmt.Sprintf("Last updated: %s\n", s.LastUpdatedAt))
	}

	return sb.String()
}

func formatDistribution(dist map[string]int64, total int64) string {
	if len(dist) == 0 {
		return "no data"
	}

	type kv struct {
		Key   string
		Value int64
	}
	var sorted []kv
	for k, v := range dist {
		sorted = append(sorted, kv{k, v})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Value > sorted[j].Value })

	var sum int64
	for _, p := range sorted {
		sum += p.Value
	}

	var parts []string
	for _, p := range sorted {
		pct := float64(p.Value) / float64(sum) * 100
		parts = append(parts, fmt.Sprintf("%s: %.0f%%", p.Key, pct))
	}
	return strings.Join(parts, " | ")
}

func formatDistributionTop(dist map[string]int64, topN int) string {
	if len(dist) == 0 {
		return "no data"
	}

	type kv struct {
		Key   string
		Value int64
	}
	var sorted []kv
	for k, v := range dist {
		sorted = append(sorted, kv{k, v})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Value > sorted[j].Value })

	var sum int64
	for _, p := range sorted {
		sum += p.Value
	}

	if len(sorted) > topN {
		sorted = sorted[:topN]
	}

	var parts []string
	for _, p := range sorted {
		pct := float64(p.Value) / float64(sum) * 100
		parts = append(parts, fmt.Sprintf("%s: %.0f%%", p.Key, pct))
	}
	return strings.Join(parts, " | ")
}
