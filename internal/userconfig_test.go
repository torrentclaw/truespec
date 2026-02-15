package internal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultUserConfig(t *testing.T) {
	cfg := DefaultUserConfig()

	if cfg.Configured {
		t.Error("default config should not be configured")
	}
	if !cfg.StatsEnabled {
		t.Error("stats should be enabled by default")
	}
	if cfg.ShareAnonymous {
		t.Error("share anonymous should be disabled by default")
	}
	if cfg.WhisperEnabled {
		t.Error("whisper should be disabled by default")
	}
	if !cfg.ThreatScanEnabled {
		t.Error("threat scan should be enabled by default")
	}
	if cfg.Concurrency != 5 {
		t.Errorf("expected concurrency=5, got %d", cfg.Concurrency)
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg := DefaultUserConfig()
	cfg.Configured = true
	cfg.StatsEnabled = false
	cfg.WhisperEnabled = true
	cfg.WhisperPath = "/usr/bin/whisper-cli"
	cfg.Concurrency = 10
	cfg.VirusTotalAPIKey = "abc123def456"

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	readData, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var loaded UserConfig
	if err := json.Unmarshal(readData, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !loaded.Configured {
		t.Error("should be configured")
	}
	if loaded.StatsEnabled {
		t.Error("stats should be disabled")
	}
	if !loaded.WhisperEnabled {
		t.Error("whisper should be enabled")
	}
	if loaded.Concurrency != 10 {
		t.Errorf("expected concurrency=10, got %d", loaded.Concurrency)
	}
	if loaded.VirusTotalAPIKey != "abc123def456" {
		t.Errorf("expected VT key, got %s", loaded.VirusTotalAPIKey)
	}
}

func TestApplyToConfig_Configured(t *testing.T) {
	ucfg := DefaultUserConfig()
	ucfg.Configured = true
	ucfg.StatsEnabled = false
	ucfg.Concurrency = 8
	ucfg.StallTimeout = 120
	ucfg.MaxTimeout = 300

	cfg := DefaultConfig()
	ucfg.ApplyToConfig(&cfg)

	if cfg.StatsFile != "" {
		t.Error("stats file should be empty when stats disabled")
	}
	if cfg.Concurrency != 8 {
		t.Errorf("expected concurrency=8, got %d", cfg.Concurrency)
	}
	if cfg.StallTimeout != 120*time.Second {
		t.Errorf("expected stall=120s, got %s", cfg.StallTimeout)
	}
	if cfg.MaxTimeout != 300*time.Second {
		t.Errorf("expected max=300s, got %s", cfg.MaxTimeout)
	}
}

func TestApplyToConfig_NotConfigured(t *testing.T) {
	ucfg := DefaultUserConfig()
	ucfg.Configured = false
	ucfg.Concurrency = 99

	cfg := DefaultConfig()
	original := cfg.Concurrency
	ucfg.ApplyToConfig(&cfg)

	if cfg.Concurrency != original {
		t.Errorf("should not apply unconfigured, got %d", cfg.Concurrency)
	}
}

func TestShowConfig(t *testing.T) {
	cfg := DefaultUserConfig()
	cfg.Configured = true
	output := cfg.ShowConfig()

	if !strings.Contains(output, "Stats tracking") {
		t.Error("should contain 'Stats tracking'")
	}
	if !strings.Contains(output, "Whisper lang detect") {
		t.Error("should contain 'Whisper lang detect'")
	}
}

// Fix 1: Test that ShowConfig doesn't panic with short VT keys
func TestShowConfig_VTKeyLengths(t *testing.T) {
	tests := []struct {
		key      string
		expected string
	}{
		{"", "not set"},
		{"a", "****"},
		{"abc", "****"},
		{"abcd1234", "****"},                // exactly 8 → masked
		{"abcd12345", "abcd...2345"},        // 9 chars → shows first4...last4
		{"abcdefghijklmnop", "abcd...mnop"}, // 16 chars
	}

	for _, tt := range tests {
		cfg := DefaultUserConfig()
		cfg.VirusTotalAPIKey = tt.key
		output := cfg.ShowConfig() // must not panic
		if !strings.Contains(output, tt.expected) {
			t.Errorf("key=%q: expected output to contain %q, got:\n%s", tt.key, tt.expected, output)
		}
	}
}

func TestMaskAPIKey(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "not set"},
		{"a", "****"},
		{"ab", "****"},
		{"abcdefgh", "****"},                // 8 chars → fully masked
		{"abcdefghi", "abcd...fghi"},        // 9 chars
		{"0123456789abcdef", "0123...cdef"}, // 16 chars
	}

	for _, tt := range tests {
		result := maskAPIKey(tt.input)
		if result != tt.expected {
			t.Errorf("maskAPIKey(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestSave_CreatesDirectory(t *testing.T) {
	// Override HOME to use temp dir
	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", origHome)

	cfg := DefaultUserConfig()
	cfg.Configured = true
	cfg.Concurrency = 42

	err := cfg.Save()
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify file exists
	path := filepath.Join(dir, ".truespec", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config file not created: %v", err)
	}

	var loaded UserConfig
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("parse saved config: %v", err)
	}

	if loaded.Concurrency != 42 {
		t.Errorf("expected concurrency=42, got %d", loaded.Concurrency)
	}
}

func TestSave_OverwriteExisting(t *testing.T) {
	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", origHome)

	// Save first version
	cfg1 := DefaultUserConfig()
	cfg1.Configured = true
	cfg1.Concurrency = 1
	if err := cfg1.Save(); err != nil {
		t.Fatalf("first Save failed: %v", err)
	}

	// Overwrite with second version (tests Windows rename fix)
	cfg2 := DefaultUserConfig()
	cfg2.Configured = true
	cfg2.Concurrency = 2
	if err := cfg2.Save(); err != nil {
		t.Fatalf("second Save failed: %v", err)
	}

	// Verify second version persisted
	loaded := LoadUserConfig()
	if loaded.Concurrency != 2 {
		t.Errorf("expected concurrency=2 after overwrite, got %d", loaded.Concurrency)
	}
}
