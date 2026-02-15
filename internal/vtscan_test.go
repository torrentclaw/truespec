package internal

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestFileSHA256(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")

	content := []byte("hello world")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}

	got, err := fileSHA256(path)
	if err != nil {
		t.Fatalf("fileSHA256 error: %v", err)
	}

	h := sha256.Sum256(content)
	expected := hex.EncodeToString(h[:])

	if got != expected {
		t.Errorf("fileSHA256 = %s, want %s", got, expected)
	}
}

func TestFileSHA256_NotFound(t *testing.T) {
	_, err := fileSHA256("/nonexistent/file.bin")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestUpdateThreatLevelWithVT_Malware(t *testing.T) {
	files := &TorrentFiles{
		ThreatLevel: "dangerous",
		Suspicious: []FileInfo{
			{Path: "virus.exe", VT: &VTFileReport{Status: "vt_malware", Detections: 15}},
			{Path: "clean.exe", VT: &VTFileReport{Status: "vt_clean", Detections: 0}},
		},
	}
	updateThreatLevelWithVT(files)
	if files.ThreatLevel != "vt_malware" {
		t.Errorf("expected vt_malware, got %s", files.ThreatLevel)
	}
}

func TestUpdateThreatLevelWithVT_AllClean(t *testing.T) {
	files := &TorrentFiles{
		ThreatLevel: "dangerous",
		Suspicious: []FileInfo{
			{Path: "safe.exe", VT: &VTFileReport{Status: "vt_clean", Detections: 0}},
			{Path: "safe2.dll", VT: &VTFileReport{Status: "vt_clean", Detections: 0}},
		},
	}
	updateThreatLevelWithVT(files)
	if files.ThreatLevel != "vt_clean" {
		t.Errorf("expected vt_clean, got %s", files.ThreatLevel)
	}
}

func TestUpdateThreatLevelWithVT_Unscanned(t *testing.T) {
	files := &TorrentFiles{
		ThreatLevel: "dangerous",
		Suspicious: []FileInfo{
			{Path: "big.exe", VT: &VTFileReport{Status: "suspicious_unscanned"}},
		},
	}
	updateThreatLevelWithVT(files)
	if files.ThreatLevel != "suspicious_unscanned" {
		t.Errorf("expected suspicious_unscanned, got %s", files.ThreatLevel)
	}
}

func TestUpdateThreatLevelWithVT_NoVT(t *testing.T) {
	files := &TorrentFiles{
		ThreatLevel: "dangerous",
		Suspicious: []FileInfo{
			{Path: "unknown.exe", VT: nil},
		},
	}
	updateThreatLevelWithVT(files)
	// Should keep original "dangerous" since no VT data
	if files.ThreatLevel != "dangerous" {
		t.Errorf("expected dangerous (unchanged), got %s", files.ThreatLevel)
	}
}

func TestEnrichWithVirusTotal_Disabled(t *testing.T) {
	files := &TorrentFiles{
		ThreatLevel: "dangerous",
		Suspicious: []FileInfo{
			{Path: "virus.exe"},
		},
	}

	// Should not panic or modify anything when disabled
	EnrichWithVirusTotal(nil, VTScanConfig{Enabled: false}, files, nil, "abc123")
	if files.Suspicious[0].VT != nil {
		t.Error("VT should be nil when disabled")
	}

	// Should not modify when no API key
	EnrichWithVirusTotal(nil, VTScanConfig{Enabled: true, APIKey: ""}, files, nil, "abc123")
	if files.Suspicious[0].VT != nil {
		t.Error("VT should be nil when no API key")
	}

	// Should not modify when no suspicious files
	emptyFiles := &TorrentFiles{ThreatLevel: "clean"}
	EnrichWithVirusTotal(nil, VTScanConfig{Enabled: true, APIKey: "key"}, emptyFiles, nil, "abc123")
}
