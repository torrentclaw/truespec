package internal

import (
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Config holds all runtime configuration for truespec.
type Config struct {
	Concurrency  int
	StallTimeout time.Duration
	MaxTimeout   time.Duration
	FFprobePath  string
	TempDir      string
	Verbose      bool
	OutputFile   string // empty = stdout

	// Download thresholds
	MinBytesMKV int // bytes to download for MKV (headers at start)
	MinBytesMP4 int // bytes to download for MP4 (moov can be at end)

	// Retry
	MaxFFprobeRetries int

	// Stats
	StatsFile string // path to persistent stats JSON file
}

// DefaultConfig returns a Config with sensible defaults, overridden by env vars.
func DefaultConfig() Config {
	return Config{
		Concurrency:       envInt("TRUESPEC_CONCURRENCY", 5),
		StallTimeout:      time.Duration(envInt("TRUESPEC_STALL_TIMEOUT", 90)) * time.Second,
		MaxTimeout:        time.Duration(envInt("TRUESPEC_MAX_TIMEOUT", 600)) * time.Second,
		FFprobePath:       os.Getenv("FFPROBE_PATH"),
		TempDir:           envString("TRUESPEC_TEMP_DIR", os.TempDir()+"/truespec"),
		MinBytesMKV:       envInt("TRUESPEC_MIN_BYTES_MKV", 10*1024*1024), // 10MB
		MinBytesMP4:       envInt("TRUESPEC_MIN_BYTES_MP4", 20*1024*1024), // 20MB
		MaxFFprobeRetries: 3,
		StatsFile:         envString("TRUESPEC_STATS_FILE", defaultStatsPath()),
	}
}

func defaultStatsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/truespec-stats.json"
	}
	return filepath.Join(home, ".truespec", "stats.json")
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ToWorkerInput creates a WorkerInput from Config for a specific torrent.
// This is used when spawning worker subprocesses for isolated torrent processing.
func (c Config) ToWorkerInput(infoHash string, index, total int) WorkerInput {
	return WorkerInput{
		InfoHash:       infoHash,
		Index:          index,
		Total:          total,
		FFprobePath:    c.FFprobePath,
		TempDir:        c.TempDir,
		StallTimeout:   int(c.StallTimeout / time.Second),
		MaxTimeout:     int(c.MaxTimeout / time.Second),
		TimeoutSeconds: int(c.MaxTimeout / time.Second), // absolute timeout for worker
		MinBytesMKV:    c.MinBytesMKV,
		MinBytesMP4:    c.MinBytesMP4,
		MaxRetries:     c.MaxFFprobeRetries,
		Verbose:        c.Verbose,
	}
}
