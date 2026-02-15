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

func TestShowConfig_MaskedVTKey(t *testing.T) {
	cfg := DefaultUserConfig()
	cfg.VirusTotalAPIKey = "abcdefghijklmnop"
	output := cfg.ShowConfig()

	if !strings.Contains(output, "abcd") {
		t.Error("should show first 4 chars")
	}
	if strings.Contains(output, "abcdefghijklmnop") {
		t.Error("should NOT show full key")
	}
}
