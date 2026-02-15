package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LangDetectConfig holds configuration for audio language detection via Whisper.
type LangDetectConfig struct {
	WhisperPath string // path to whisper-cli binary
	ModelPath   string // path to ggml-tiny.bin model
	FFmpegPath  string // path to ffmpeg binary
	Enabled     bool   // whether language detection is enabled
}

// LangDetectResult holds the result of a language detection attempt.
type LangDetectResult struct {
	Language   string  `json:"language"`   // ISO 639-1 code (e.g., "es", "en")
	Confidence float64 `json:"confidence"` // 0.0 - 1.0
	ElapsedMs  int64   `json:"elapsed_ms"`
}

// whisperJSON matches the output JSON from whisper-cli --output-json.
type whisperJSON struct {
	Result struct {
		Language string `json:"language"`
	} `json:"result"`
}

// confidenceRe extracts confidence from whisper stderr:
// "auto-detected language: en (p = 0.409680)"
var confidenceRe = regexp.MustCompile(`auto-detected language:\s*(\S+)\s*\(p\s*=\s*([\d.]+)\)`)

// Cached language detection config (resolved once per process).
var (
	langDetectOnce   sync.Once
	langDetectCached LangDetectConfig
)

// DetectAudioLanguage extracts a short audio clip from the video file and uses
// whisper.cpp to detect the spoken language. Returns nil if detection fails
// or is not applicable.
func DetectAudioLanguage(ctx context.Context, cfg LangDetectConfig, videoPath string) (*LangDetectResult, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	start := time.Now()

	// Create temp wav file
	tmpDir := os.TempDir()
	wavPath := filepath.Join(tmpDir, fmt.Sprintf("truespec-lang-%d.wav", time.Now().UnixNano()))
	defer os.Remove(wavPath)

	// Extract 30 seconds of audio, mono 16kHz (whisper requirement)
	ffmpegCtx, ffmpegCancel := context.WithTimeout(ctx, 30*time.Second)
	defer ffmpegCancel()

	ffmpegCmd := exec.CommandContext(ffmpegCtx, cfg.FFmpegPath,
		"-i", videoPath,
		"-t", "30", // 30 seconds
		"-vn",          // no video
		"-ar", "16000", // 16kHz sample rate
		"-ac", "1", // mono
		"-f", "wav", // wav format
		"-y", // overwrite
		wavPath,
	)
	ffmpegCmd.Stderr = nil // suppress ffmpeg output
	ffmpegCmd.Stdout = nil

	if err := ffmpegCmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg audio extract failed: %w", err)
	}

	// Verify wav was created and has content
	info, err := os.Stat(wavPath)
	if err != nil || info.Size() < 1000 {
		return nil, fmt.Errorf("extracted audio too small or missing")
	}

	// Run whisper-cli --detect-language
	whisperCtx, whisperCancel := context.WithTimeout(ctx, 30*time.Second)
	defer whisperCancel()

	// Output JSON to temp file
	jsonOutPath := wavPath + "-out"
	defer os.Remove(jsonOutPath + ".json")

	whisperCmd := exec.CommandContext(whisperCtx, cfg.WhisperPath,
		"--model", cfg.ModelPath,
		"--detect-language",
		"--output-json",
		"--no-prints",
		"-of", jsonOutPath,
		"-f", wavPath,
	)

	// Capture stderr for confidence parsing
	var stderrBuf strings.Builder
	whisperCmd.Stderr = &stderrBuf
	whisperCmd.Stdout = nil

	if err := whisperCmd.Run(); err != nil {
		return nil, fmt.Errorf("whisper detect-language failed: %w", err)
	}

	// Parse JSON output
	jsonData, err := os.ReadFile(jsonOutPath + ".json")
	if err != nil {
		return nil, fmt.Errorf("read whisper JSON output: %w", err)
	}

	var wResult whisperJSON
	if err := json.Unmarshal(jsonData, &wResult); err != nil {
		return nil, fmt.Errorf("parse whisper JSON: %w", err)
	}

	lang := wResult.Result.Language
	if lang == "" {
		return nil, fmt.Errorf("whisper returned empty language")
	}

	// Try to extract confidence from stderr
	confidence := 0.0
	if matches := confidenceRe.FindStringSubmatch(stderrBuf.String()); len(matches) == 3 {
		if p, err := strconv.ParseFloat(matches[2], 64); err == nil {
			confidence = p
		}
	}

	elapsed := time.Since(start).Milliseconds()

	return &LangDetectResult{
		Language:   lang,
		Confidence: confidence,
		ElapsedMs:  elapsed,
	}, nil
}

// ResolveLangDetect finds whisper-cli and model, returns a configured LangDetectConfig.
// It checks: 1) UserConfig paths, 2) env vars, 3) known install locations, 4) PATH.
// Returns with Enabled=false if whisper is not available (not an error).
func ResolveLangDetect() LangDetectConfig {
	langDetectOnce.Do(func() {
		langDetectCached = resolveLangDetectInner()
	})
	return langDetectCached
}

// resolveLangDetectInner does the actual resolution (called once via sync.Once).
func resolveLangDetectInner() LangDetectConfig {
	cfg := LangDetectConfig{}

	// Check user config first — if whisper is explicitly disabled, skip
	ucfg := LoadUserConfig()
	if ucfg.Configured && !ucfg.WhisperEnabled {
		return cfg
	}

	// Find whisper-cli: UserConfig path → env → ~/.truespec/bin → ~/local/bin → PATH
	cfg.WhisperPath = findBinary("whisper-cli",
		ucfg.WhisperPath,
		os.Getenv("WHISPER_PATH"),
		filepath.Join(WhisperBinDir(), "whisper-cli"),
		filepath.Join(homeDir(), "local", "bin", "whisper-cli"),
	)
	if cfg.WhisperPath == "" {
		return cfg
	}

	// Find model: UserConfig path → env → ~/.truespec/models → ~/local/whisper-models → cache
	cfg.ModelPath = findFile(
		ucfg.WhisperModel,
		os.Getenv("WHISPER_MODEL"),
		filepath.Join(WhisperModelDir(), "ggml-tiny.bin"),
		filepath.Join(homeDir(), "local", "whisper-models", "ggml-tiny.bin"),
		filepath.Join(homeDir(), ".cache", "whisper", "ggml-tiny.bin"),
	)
	if cfg.ModelPath == "" {
		return cfg
	}

	// Find ffmpeg
	cfg.FFmpegPath = findBinary("ffmpeg",
		os.Getenv("FFMPEG_PATH"),
	)
	if cfg.FFmpegPath == "" {
		return cfg
	}

	cfg.Enabled = true
	return cfg
}

// ShouldDetectLanguage checks if language detection should run for a scan result.
// Only triggers when there's exactly one audio track and its language is "und".
func ShouldDetectLanguage(result *ScanResult) bool {
	if result == nil || result.Status != "success" {
		return false
	}
	if len(result.Audio) != 1 {
		return false
	}
	lang := strings.ToLower(result.Audio[0].Lang)
	return lang == "und" || lang == "" || lang == "undefined" || lang == "unknown"
}

func findBinary(name string, candidates ...string) string {
	for _, c := range candidates {
		if c != "" {
			if _, err := os.Stat(c); err == nil {
				return c
			}
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func findFile(candidates ...string) string {
	for _, c := range candidates {
		if c != "" {
			if _, err := os.Stat(c); err == nil {
				return c
			}
		}
	}
	return ""
}

func homeDir() string {
	if runtime.GOOS == "windows" {
		return os.Getenv("USERPROFILE")
	}
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

// ApplyLangDetection runs language detection on a scan result if applicable.
// Modifies the result in-place: updates audio track lang and adds detection info.
func ApplyLangDetection(ctx context.Context, cfg LangDetectConfig, result *ScanResult, videoPath string) {
	if !ShouldDetectLanguage(result) {
		return
	}

	log.Printf("  [%s] audio language is 'und', attempting whisper detection...", truncHash(result.InfoHash))

	detected, err := DetectAudioLanguage(ctx, cfg, videoPath)
	if err != nil {
		log.Printf("  [%s] language detection failed: %v", truncHash(result.InfoHash), err)
		return
	}

	if detected == nil || detected.Language == "" {
		return
	}

	normalized := NormalizeLang(detected.Language)

	log.Printf("  [%s] detected language: %s (confidence: %.1f%%, took %dms)",
		truncHash(result.InfoHash), normalized, detected.Confidence*100, detected.ElapsedMs)

	result.Audio[0].Lang = normalized

	confPct := int(detected.Confidence * 100)
	detectionNote := fmt.Sprintf("detected:%s(%d%%)", normalized, confPct)
	if result.Audio[0].Title != "" {
		result.Audio[0].Title = result.Audio[0].Title + " [" + detectionNote + "]"
	} else {
		result.Audio[0].Title = "[" + detectionNote + "]"
	}

	result.Languages = ComputeLanguages(nil, result.Audio)
}
