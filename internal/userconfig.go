package internal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DefaultWhisperMaxTracks is the default maximum number of audio tracks to
// run language detection on per torrent.
const DefaultWhisperMaxTracks = 3

// UserConfig is the persistent user configuration saved to ~/.truespec/config.json.
// It controls which features are enabled. CLI flags override these values at runtime.
type UserConfig struct {
	// Stats
	StatsEnabled bool `json:"stats_enabled"` // track scan statistics

	// Community
	ShareAnonymous bool `json:"share_anonymous"` // share anonymous scan results with community

	// Language detection
	WhisperEnabled   bool   `json:"whisper_enabled"`    // detect language for "und" audio tracks
	WhisperPath      string `json:"whisper_path"`       // path to whisper-cli binary (auto-set on install)
	WhisperModel     string `json:"whisper_model"`      // path to ggml model file (auto-set on install)
	WhisperMaxTracks int    `json:"whisper_max_tracks"` // max audio tracks to detect per torrent (0 = default 3)

	// Threat detection
	ThreatScanEnabled bool   `json:"threat_scan_enabled"` // analyze torrent files for threats
	VirusTotalAPIKey  string `json:"virustotal_api_key"`  // VirusTotal API key for suspicious files

	// Scan defaults
	Concurrency  int `json:"concurrency"`   // default concurrent downloads
	StallTimeout int `json:"stall_timeout"` // seconds before killing stalled torrent
	MaxTimeout   int `json:"max_timeout"`   // absolute max seconds per torrent

	// Output
	VerboseLevel int `json:"verbose_level"` // 0=normal (progress+logfile), 1=verbose (all to stderr)

	// Meta
	Configured bool `json:"configured"` // true after first run of `truespec config`
}

// DefaultUserConfig returns a UserConfig with sensible defaults.
func DefaultUserConfig() UserConfig {
	return UserConfig{
		StatsEnabled:      true,
		ShareAnonymous:    false,
		WhisperEnabled:    false,
		ThreatScanEnabled: true,
		Concurrency:       5,
		StallTimeout:      90,
		MaxTimeout:        600,
		VerboseLevel:      VerboseNormal,
		Configured:        false,
	}
}

// UserConfigPath returns the path to the user config file.
func UserConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "truespec-config.json")
	}
	return filepath.Join(home, ".truespec", "config.json")
}

// TrueSpecDir returns the base directory for truespec data (~/.truespec/).
func TrueSpecDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".truespec")
	}
	return filepath.Join(home, ".truespec")
}

// WhisperBinDir returns the directory for whisper binaries (~/.truespec/bin/).
func WhisperBinDir() string {
	return filepath.Join(TrueSpecDir(), "bin")
}

// WhisperModelDir returns the directory for whisper models (~/.truespec/models/).
func WhisperModelDir() string {
	return filepath.Join(TrueSpecDir(), "models")
}

// LoadUserConfig loads the user config from disk. Returns defaults if not found.
func LoadUserConfig() UserConfig {
	path := UserConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return DefaultUserConfig()
	}

	cfg := DefaultUserConfig()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return DefaultUserConfig()
	}
	return cfg
}

// Save writes the user config to disk atomically.
// On Windows, removes the destination first since os.Rename cannot overwrite.
func (c *UserConfig) Save() error {
	path := UserConfigPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	tmpFile := path + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0o644); err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}

	if err := atomicRename(tmpFile, path); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("rename config file: %w", err)
	}

	return nil
}

// ApplyToConfig merges user config into a runtime Config.
// Only applies if the user has run `truespec config` at least once.
func (c *UserConfig) ApplyToConfig(cfg *Config) {
	if !c.Configured {
		return
	}

	if !c.StatsEnabled {
		cfg.StatsFile = ""
	}

	if c.Concurrency > 0 {
		cfg.Concurrency = c.Concurrency
	}
	if c.StallTimeout > 0 {
		cfg.StallTimeout = time.Duration(c.StallTimeout) * time.Second
	}
	if c.MaxTimeout > 0 {
		cfg.MaxTimeout = time.Duration(c.MaxTimeout) * time.Second
	}

	cfg.VerboseLevel = c.VerboseLevel
}

// ShowConfig returns a human-readable summary of the current configuration.
func (c *UserConfig) ShowConfig() string {
	yn := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}

	s := "TrueSpec Configuration\n"
	s += "══════════════════════════════════════\n\n"
	s += fmt.Sprintf("  Stats tracking:       %s\n", yn(c.StatsEnabled))
	s += fmt.Sprintf("  Share anonymous data: %s\n", yn(c.ShareAnonymous))
	s += fmt.Sprintf("  Threat detection:     %s\n", yn(c.ThreatScanEnabled))
	s += fmt.Sprintf("  Whisper lang detect:  %s\n", yn(c.WhisperEnabled))
	if c.WhisperEnabled {
		s += fmt.Sprintf("    Binary:             %s\n", valueOrNA(c.WhisperPath))
		s += fmt.Sprintf("    Model:              %s\n", valueOrNA(c.WhisperModel))
		maxT := c.WhisperMaxTracks
		if maxT <= 0 {
			maxT = DefaultWhisperMaxTracks
		}
		s += fmt.Sprintf("    Max tracks/torrent: %d\n", maxT)
	}
	s += fmt.Sprintf("  VirusTotal API key:   %s\n", maskAPIKey(c.VirusTotalAPIKey))
	s += fmt.Sprintf("\n  Concurrency:          %d\n", c.Concurrency)
	s += fmt.Sprintf("  Stall timeout:        %ds\n", c.StallTimeout)
	s += fmt.Sprintf("  Max timeout:          %ds\n", c.MaxTimeout)
	s += fmt.Sprintf("\n  Output mode:          %s\n", VerboseLevelLabel(c.VerboseLevel))
	if c.VerboseLevel == VerboseNormal {
		s += fmt.Sprintf("  Log directory:        %s\n", LogDirPath())
	}
	s += fmt.Sprintf("\n  Config file:          %s\n", UserConfigPath())
	s += fmt.Sprintf("  Configured:           %s\n", yn(c.Configured))

	return s
}

// maskAPIKey safely masks an API key for display.
// Shows first 4 and last 4 characters if long enough, otherwise masks entirely.
func maskAPIKey(key string) string {
	if key == "" {
		return "not set"
	}
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

func valueOrNA(s string) string {
	if s == "" {
		return "n/a"
	}
	return s
}

// atomicRename is defined in fileutil.go (shared between stats.go and userconfig.go)
