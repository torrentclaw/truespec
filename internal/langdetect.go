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
	MaxTracks   int    // max audio tracks to detect per torrent (0 = use DefaultWhisperMaxTracks)
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
func DetectAudioLanguage(ctx context.Context, cfg LangDetectConfig, videoPath string, audioStreamIndex int) (*LangDetectResult, error) {
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
		"-map", fmt.Sprintf("0:a:%d", audioStreamIndex), // select specific audio stream
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
	cfg.MaxTracks = ucfg.WhisperMaxTracks
	return cfg
}

// isUnknownLang returns true if a language tag is undefined/empty.
func isUnknownLang(lang string) bool {
	l := strings.ToLower(lang)
	return l == "und" || l == "" || l == "undefined" || l == "unknown"
}

// ShouldDetectLanguage checks if language detection should run for a scan result.
// Triggers when ALL audio tracks have unknown language. If any track has a known
// language, we skip detection entirely (the known tags are trustworthy enough).
func ShouldDetectLanguage(result *ScanResult) bool {
	if result == nil || result.Status != "success" {
		return false
	}
	if len(result.Audio) == 0 {
		return false
	}
	for _, track := range result.Audio {
		if !isUnknownLang(track.Lang) {
			return false // at least one track has a known language - skip
		}
	}
	return true
}

// effectiveMaxTracks returns the max tracks limit, falling back to DefaultWhisperMaxTracks.
func effectiveMaxTracks(cfg LangDetectConfig) int {
	if cfg.MaxTracks > 0 {
		return cfg.MaxTracks
	}
	return DefaultWhisperMaxTracks
}

// undefinedTrackIndices returns the indices of audio tracks with unknown language.
func undefinedTrackIndices(audio []AudioTrack) []int {
	var indices []int
	for i, track := range audio {
		if isUnknownLang(track.Lang) {
			indices = append(indices, i)
		}
	}
	return indices
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
// Analyzes all audio tracks with unknown language using Whisper.
// Modifies the result in-place: updates audio track lang and adds detection info.
func ApplyLangDetection(ctx context.Context, cfg LangDetectConfig, result *ScanResult, videoPath string) {
	if !ShouldDetectLanguage(result) {
		return
	}

	indices := undefinedTrackIndices(result.Audio)
	maxT := effectiveMaxTracks(cfg)
	if len(indices) > maxT {
		log.Printf("  [%s] %d undefined audio tracks, capping to %d", truncHash(result.InfoHash), len(indices), maxT)
		indices = indices[:maxT]
	}

	log.Printf("  [%s] %d audio track(s) with unknown language, attempting whisper detection...",
		truncHash(result.InfoHash), len(indices))

	for _, i := range indices {
		detected, err := DetectAudioLanguage(ctx, cfg, videoPath, i)
		if err != nil {
			log.Printf("  [%s] language detection failed for track %d: %v", truncHash(result.InfoHash), i, err)
			continue
		}

		if detected == nil || detected.Language == "" {
			continue
		}

		normalized := NormalizeLang(detected.Language)

		log.Printf("  [%s] track %d: detected language: %s (confidence: %.1f%%, took %dms)",
			truncHash(result.InfoHash), i, normalized, detected.Confidence*100, detected.ElapsedMs)

		result.Audio[i].Lang = normalized

		confPct := int(detected.Confidence * 100)
		detectionNote := fmt.Sprintf("detected:%s(%d%%)", normalized, confPct)
		if result.Audio[i].Title != "" {
			result.Audio[i].Title = result.Audio[i].Title + " [" + detectionNote + "]"
		} else {
			result.Audio[i].Title = "[" + detectionNote + "]"
		}
	}

	result.Languages = ComputeLanguages(nil, result.Audio)
}
