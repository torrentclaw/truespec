package internal

import (
	"testing"
)

func TestAnalyzeFiles_Clean(t *testing.T) {
	files := []FileInfo{
		{Path: "Movie (2024)/Movie.mkv", Size: 5_000_000_000, Ext: ".mkv"},
		{Path: "Movie (2024)/Movie.srt", Size: 50_000, Ext: ".srt"},
		{Path: "Movie (2024)/Movie.nfo", Size: 1_000, Ext: ".nfo"},
		{Path: "Movie (2024)/poster.jpg", Size: 200_000, Ext: ".jpg"},
	}

	result := AnalyzeFiles(files)

	if result.ThreatLevel != "clean" {
		t.Errorf("expected clean, got %s", result.ThreatLevel)
	}
	if len(result.Suspicious) != 0 {
		t.Errorf("expected 0 suspicious, got %d", len(result.Suspicious))
	}
	if len(result.VideoFiles) != 1 {
		t.Errorf("expected 1 video, got %d", len(result.VideoFiles))
	}
	if len(result.SubFiles) != 1 {
		t.Errorf("expected 1 sub, got %d", len(result.SubFiles))
	}
	if len(result.ImageFiles) != 1 {
		t.Errorf("expected 1 image, got %d", len(result.ImageFiles))
	}
	if result.Total != 4 {
		t.Errorf("expected total=4, got %d", result.Total)
	}
	if result.TotalSize != 5_000_251_000 {
		t.Errorf("expected total_size=5000251000, got %d", result.TotalSize)
	}
}

func TestAnalyzeFiles_Dangerous(t *testing.T) {
	files := []FileInfo{
		{Path: "Movie/Movie.mkv", Size: 1_000_000_000, Ext: ".mkv"},
		{Path: "Movie/setup.exe", Size: 500_000, Ext: ".exe"},
		{Path: "Movie/crack.bat", Size: 200, Ext: ".bat"},
	}

	result := AnalyzeFiles(files)

	if result.ThreatLevel != "dangerous" {
		t.Errorf("expected dangerous, got %s", result.ThreatLevel)
	}
	if len(result.Suspicious) != 2 {
		t.Errorf("expected 2 suspicious, got %d", len(result.Suspicious))
	}
	if result.Suspicious[0].Reason != "Windows executable" {
		t.Errorf("expected 'Windows executable', got %q", result.Suspicious[0].Reason)
	}
	if result.Suspicious[1].Reason != "Windows batch script" {
		t.Errorf("expected 'Windows batch script', got %q", result.Suspicious[1].Reason)
	}
}

func TestAnalyzeFiles_Warning(t *testing.T) {
	files := []FileInfo{
		{Path: "Movie/Movie.mp4", Size: 2_000_000_000, Ext: ".mp4"},
		{Path: "Movie/extras.zip", Size: 10_000_000, Ext: ".zip"},
	}

	result := AnalyzeFiles(files)

	if result.ThreatLevel != "warning" {
		t.Errorf("expected warning, got %s", result.ThreatLevel)
	}
	if len(result.Suspicious) != 1 {
		t.Errorf("expected 1 suspicious, got %d", len(result.Suspicious))
	}
}

func TestAnalyzeFiles_ScriptExtensions(t *testing.T) {
	tests := []struct {
		ext      string
		expected string
	}{
		{".vbs", "dangerous"},
		{".ps1", "dangerous"},
		{".hta", "dangerous"},
		{".lnk", "dangerous"},
		{".scr", "dangerous"},
		{".msi", "dangerous"},
		{".dll", "dangerous"},
		{".reg", "dangerous"},
		{".iso", "warning"},
		{".apk", "warning"},
	}

	for _, tt := range tests {
		files := []FileInfo{
			{Path: "test/file" + tt.ext, Size: 1000, Ext: tt.ext},
		}
		result := AnalyzeFiles(files)
		if result.ThreatLevel != tt.expected {
			t.Errorf("ext %s: expected %s, got %s", tt.ext, tt.expected, result.ThreatLevel)
		}
	}
}

func TestAnalyzeFiles_Empty(t *testing.T) {
	result := AnalyzeFiles([]FileInfo{})
	if result.ThreatLevel != "clean" {
		t.Errorf("expected clean for empty, got %s", result.ThreatLevel)
	}
	if result.Total != 0 {
		t.Errorf("expected total=0, got %d", result.Total)
	}
}

func TestAnalyzeFiles_AudioFiles(t *testing.T) {
	files := []FileInfo{
		{Path: "Album/01-track.flac", Size: 30_000_000, Ext: ".flac"},
		{Path: "Album/02-track.flac", Size: 28_000_000, Ext: ".flac"},
		{Path: "Album/cover.jpg", Size: 500_000, Ext: ".jpg"},
		{Path: "Album/album.cue", Size: 2_000, Ext: ".cue"},
	}

	result := AnalyzeFiles(files)

	if result.ThreatLevel != "clean" {
		t.Errorf("expected clean, got %s", result.ThreatLevel)
	}
	if len(result.AudioFiles) != 2 {
		t.Errorf("expected 2 audio, got %d", len(result.AudioFiles))
	}
}

func TestHasSuspiciousPattern(t *testing.T) {
	tests := []struct {
		name     string
		expected bool
	}{
		{"movie.mkv", false},
		{"movie.srt", false},
		{"movie.nfo", false},
		{"movie.avi.xyz", true},       // media ext + unknown ext
		{"movie.mp4.abc", true},       // media ext + unknown ext
		{"movie.mkv.srt", false},      // media ext + safe ext
		{"normal_file.txt", false},
	}

	for _, tt := range tests {
		result := hasSuspiciousPattern(tt.name)
		if result != tt.expected {
			t.Errorf("hasSuspiciousPattern(%q) = %v, want %v", tt.name, result, tt.expected)
		}
	}
}

func TestAnalyzeFiles_DangerousOverridesWarning(t *testing.T) {
	files := []FileInfo{
		{Path: "Movie/Movie.mkv", Size: 1_000_000_000, Ext: ".mkv"},
		{Path: "Movie/extras.zip", Size: 10_000_000, Ext: ".zip"},      // warning
		{Path: "Movie/keygen.exe", Size: 500_000, Ext: ".exe"},          // dangerous
	}

	result := AnalyzeFiles(files)

	// Dangerous should take precedence over warning
	if result.ThreatLevel != "dangerous" {
		t.Errorf("expected dangerous (overrides warning), got %s", result.ThreatLevel)
	}
	if len(result.Suspicious) != 2 {
		t.Errorf("expected 2 suspicious, got %d", len(result.Suspicious))
	}
}
