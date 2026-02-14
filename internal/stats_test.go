package internal

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadStats_NewFile(t *testing.T) {
	s, err := LoadStats("/tmp/nonexistent_stats_test.json")
	if err != nil {
		t.Fatalf("LoadStats should not error on missing file: %v", err)
	}
	if s == nil {
		t.Fatal("LoadStats should return non-nil stats")
	}
	if s.TotalScanned != 0 {
		t.Errorf("expected TotalScanned=0, got %d", s.TotalScanned)
	}
	if s.FailuresByType == nil {
		t.Error("FailuresByType should be initialized")
	}
	if s.ResolutionDist == nil {
		t.Error("ResolutionDist should be initialized")
	}
	if s.HourlyStats == nil {
		t.Error("HourlyStats should be initialized")
	}
	if s.DailyStats == nil {
		t.Error("DailyStats should be initialized")
	}
}

func TestLoadStats_ExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stats.json")

	// Create a stats file with known values
	s := NewStats()
	s.TotalScanned = 42
	s.TotalSuccess = 35
	s.TotalFailed = 7
	s.DownloadBytes = 1024 * 1024 * 100
	s.FailuresByType["timeout"] = 3
	s.FailuresByType["stall_metadata"] = 4

	if err := s.Save(path); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Reload
	loaded, err := LoadStats(path)
	if err != nil {
		t.Fatalf("LoadStats failed: %v", err)
	}
	if loaded.TotalScanned != 42 {
		t.Errorf("expected TotalScanned=42, got %d", loaded.TotalScanned)
	}
	if loaded.TotalSuccess != 35 {
		t.Errorf("expected TotalSuccess=35, got %d", loaded.TotalSuccess)
	}
	if loaded.FailuresByType["timeout"] != 3 {
		t.Errorf("expected timeout=3, got %d", loaded.FailuresByType["timeout"])
	}
}

func TestRecordResult_Success(t *testing.T) {
	s := NewStats()

	result := ScanResult{
		InfoHash:  "abc123",
		Status:    "success",
		ElapsedMs: 5000,
		Video: &VideoInfo{
			Codec:  "hevc",
			Width:  1920,
			Height: 1080,
			HDR:    "HDR10",
		},
		Audio: []AudioTrack{
			{Lang: "en", Codec: "aac"},
			{Lang: "es", Codec: "ac3"},
		},
		Languages: []string{"en", "es"},
	}

	s.RecordResult(result, 15*1024*1024)

	if s.TotalScanned != 1 {
		t.Errorf("expected TotalScanned=1, got %d", s.TotalScanned)
	}
	if s.TotalSuccess != 1 {
		t.Errorf("expected TotalSuccess=1, got %d", s.TotalSuccess)
	}
	if s.TotalFailed != 0 {
		t.Errorf("expected TotalFailed=0, got %d", s.TotalFailed)
	}
	if s.ResolutionDist["1080p"] != 1 {
		t.Errorf("expected 1080p=1, got %d", s.ResolutionDist["1080p"])
	}
	if s.CodecDist["hevc"] != 1 {
		t.Errorf("expected hevc=1, got %d", s.CodecDist["hevc"])
	}
	if s.HDRDist["HDR10"] != 1 {
		t.Errorf("expected HDR10=1, got %d", s.HDRDist["HDR10"])
	}
	if s.AudioCodecDist["aac"] != 1 {
		t.Errorf("expected aac=1, got %d", s.AudioCodecDist["aac"])
	}
	if s.AudioCodecDist["ac3"] != 1 {
		t.Errorf("expected ac3=1, got %d", s.AudioCodecDist["ac3"])
	}
	if s.LanguageDist["en"] != 1 {
		t.Errorf("expected en=1, got %d", s.LanguageDist["en"])
	}
	if s.LanguageDist["es"] != 1 {
		t.Errorf("expected es=1, got %d", s.LanguageDist["es"])
	}
	if s.TotalElapsedMs != 5000 {
		t.Errorf("expected TotalElapsedMs=5000, got %d", s.TotalElapsedMs)
	}
	if s.DownloadBytes != 15*1024*1024 {
		t.Errorf("expected DownloadBytes=%d, got %d", 15*1024*1024, s.DownloadBytes)
	}
}

func TestRecordResult_Failure(t *testing.T) {
	s := NewStats()

	result := ScanResult{
		InfoHash:  "def456",
		Status:    "stall_metadata",
		ElapsedMs: 90000,
		Error:     "metadata timeout",
	}

	s.RecordResult(result, 0)

	if s.TotalScanned != 1 {
		t.Errorf("expected TotalScanned=1, got %d", s.TotalScanned)
	}
	if s.TotalSuccess != 0 {
		t.Errorf("expected TotalSuccess=0, got %d", s.TotalSuccess)
	}
	if s.TotalFailed != 1 {
		t.Errorf("expected TotalFailed=1, got %d", s.TotalFailed)
	}
	if s.FailuresByType["stall_metadata"] != 1 {
		t.Errorf("expected stall_metadata=1, got %d", s.FailuresByType["stall_metadata"])
	}
	// No quality distribution updates for failures
	if len(s.ResolutionDist) != 0 {
		t.Errorf("expected empty ResolutionDist for failure, got %v", s.ResolutionDist)
	}
}

func TestPruneOldBuckets(t *testing.T) {
	s := NewStats()

	now := time.Now().UTC()
	oldHour := now.Add(-72 * time.Hour).Format("2006-01-02T15")
	recentHour := now.Add(-1 * time.Hour).Format("2006-01-02T15")
	oldDay := now.Add(-45 * 24 * time.Hour).Format("2006-01-02")
	recentDay := now.Add(-1 * 24 * time.Hour).Format("2006-01-02")

	s.HourlyStats = []HourlyBucket{
		{Hour: oldHour, Scanned: 10},
		{Hour: recentHour, Scanned: 5},
	}
	s.DailyStats = []DailyBucket{
		{Day: oldDay, Scanned: 100},
		{Day: recentDay, Scanned: 50},
	}

	s.PruneOldBuckets()

	if len(s.HourlyStats) != 1 {
		t.Errorf("expected 1 hourly bucket after prune, got %d", len(s.HourlyStats))
	}
	if s.HourlyStats[0].Hour != recentHour {
		t.Errorf("expected recent hour bucket, got %s", s.HourlyStats[0].Hour)
	}

	if len(s.DailyStats) != 1 {
		t.Errorf("expected 1 daily bucket after prune, got %d", len(s.DailyStats))
	}
	if s.DailyStats[0].Day != recentDay {
		t.Errorf("expected recent day bucket, got %s", s.DailyStats[0].Day)
	}
}

func TestSave_Atomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "stats.json")

	s := NewStats()
	s.TotalScanned = 99
	s.DownloadBytes = 1024

	if err := s.Save(path); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stats file not created: %v", err)
	}

	// Verify temp file is cleaned up
	tmpPath := path + ".tmp"
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Error("temp file should not exist after save")
	}

	// Reload and verify
	loaded, err := LoadStats(path)
	if err != nil {
		t.Fatalf("LoadStats failed: %v", err)
	}
	if loaded.TotalScanned != 99 {
		t.Errorf("expected TotalScanned=99, got %d", loaded.TotalScanned)
	}
}

func TestResolutionCategory(t *testing.T) {
	tests := []struct {
		width, height int
		expected      string
	}{
		{3840, 2160, "2160p"},
		{2560, 1440, "1080p"}, // 1440 >= 1080
		{1920, 1080, "1080p"},
		{1280, 720, "720p"},
		{720, 480, "480p"},
		{640, 360, "other"},
		{0, 0, "unknown"},
	}

	for _, tt := range tests {
		result := resolutionCategory(tt.width, tt.height)
		if result != tt.expected {
			t.Errorf("resolutionCategory(%d, %d) = %s, want %s",
				tt.width, tt.height, result, tt.expected)
		}
	}
}

func TestCompute(t *testing.T) {
	s := NewStats()
	s.TotalScanned = 10
	s.TotalElapsedMs = 50000
	s.DownloadBytes = 150 * 1024 * 1024

	s.Compute()

	if s.AvgElapsedMs != 5000 {
		t.Errorf("expected AvgElapsedMs=5000, got %d", s.AvgElapsedMs)
	}
	expectedAvgBytes := int64(150 * 1024 * 1024 / 10)
	if s.AvgBytesPerTorrent != expectedAvgBytes {
		t.Errorf("expected AvgBytesPerTorrent=%d, got %d", expectedAvgBytes, s.AvgBytesPerTorrent)
	}
}

func TestCompute_ZeroScanned(t *testing.T) {
	s := NewStats()
	s.Compute() // Should not panic
	if s.AvgElapsedMs != 0 {
		t.Errorf("expected AvgElapsedMs=0, got %d", s.AvgElapsedMs)
	}
}

func TestHumanizeBytes(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.00 GB"},
		{1099511627776, "1.00 TB"},
	}
	for _, tt := range tests {
		result := HumanizeBytes(tt.input)
		if result != tt.expected {
			t.Errorf("HumanizeBytes(%d) = %s, want %s", tt.input, result, tt.expected)
		}
	}
}
