package internal

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	whisperReleasesAPI = "https://api.github.com/repos/ggerganov/whisper.cpp/releases/latest"
	whisperModelURL    = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-tiny.bin"
	whisperModelName   = "ggml-tiny.bin"

	// Safety limits
	maxExtractSize = 500 * 1024 * 1024 // 500MB max for extracted binary
	maxModelSize   = 200 * 1024 * 1024 // 200MB max for model file

	// Known SHA256 hash of ggml-tiny.bin (v1.5.x)
	whisperModelSHA256 = "be07e048e1e599ad46341c8d2a135645097a538221678b7acdd1b1919c6e1b21"
)

// HTTP clients with timeouts (never use http.DefaultClient for downloads)
var (
	apiClient = &http.Client{Timeout: 30 * time.Second}
	dlClient  = &http.Client{Timeout: 10 * time.Minute}
)

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// whisperBinaryName returns the correct binary name for the current OS.
func whisperBinaryName() string {
	if runtime.GOOS == "windows" {
		return "whisper-cli.exe"
	}
	return "whisper-cli"
}

// whisperAssetPattern returns the expected asset name pattern for the current platform.
func whisperAssetPattern() (string, error) {
	switch runtime.GOOS {
	case "linux":
		switch runtime.GOARCH {
		case "amd64":
			return "linux-x86_64", nil
		case "arm64":
			return "linux-aarch64", nil
		}
	case "darwin":
		switch runtime.GOARCH {
		case "amd64":
			return "darwin-x86_64", nil
		case "arm64":
			return "darwin-arm64", nil
		}
	case "windows":
		switch runtime.GOARCH {
		case "amd64":
			return "win-x86_64", nil
		case "arm64":
			return "win-arm64", nil
		}
	}
	return "", fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
}

// DownloadWhisper downloads the whisper-cli binary and the tiny model.
// Returns (whisperPath, modelPath, error).
func DownloadWhisper() (string, string, error) {
	binDir := WhisperBinDir()
	modelDir := WhisperModelDir()

	whisperBin := filepath.Join(binDir, whisperBinaryName())
	modelPath := filepath.Join(modelDir, whisperModelName)

	// Download binary if not exists
	if _, err := os.Stat(whisperBin); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Downloading whisper-cli...\n")
		if err := downloadWhisperBinary(whisperBin); err != nil {
			return "", "", fmt.Errorf("download whisper-cli: %w", err)
		}
		fmt.Fprintf(os.Stderr, "whisper-cli installed to %s\n", whisperBin)
	} else {
		fmt.Fprintf(os.Stderr, "whisper-cli already installed at %s\n", whisperBin)
	}

	// Download model if not exists
	if _, err := os.Stat(modelPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Downloading whisper model (tiny, ~75MB)...\n")
		if err := downloadFile(whisperModelURL, modelPath, maxModelSize); err != nil {
			return "", "", fmt.Errorf("download whisper model: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Model installed to %s\n", modelPath)
	} else {
		fmt.Fprintf(os.Stderr, "Model already installed at %s\n", modelPath)
	}

	return whisperBin, modelPath, nil
}

func downloadWhisperBinary(destPath string) error {
	pattern, err := whisperAssetPattern()
	if err != nil {
		return err
	}

	// Get latest release (with timeout)
	resp, err := apiClient.Get(whisperReleasesAPI)
	if err != nil {
		return fmt.Errorf("fetch releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var release ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return fmt.Errorf("parse release: %w", err)
	}

	// Find matching asset
	assetURL := findWhisperAsset(release.Assets, pattern)
	if assetURL == "" {
		return fmt.Errorf("no whisper-cli release found for %s (release %s)", pattern, release.TagName)
	}

	fmt.Fprintf(os.Stderr, "Downloading from %s...\n", release.TagName)

	// Download tar.gz (with timeout)
	dlResp, err := dlClient.Get(assetURL)
	if err != nil {
		return fmt.Errorf("download asset: %w", err)
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", dlResp.StatusCode)
	}

	// Windows releases may be .zip, Unix are .tar.gz
	if strings.HasSuffix(strings.ToLower(assetURL), ".zip") {
		// For now, only tar.gz is supported
		return fmt.Errorf("zip archives not yet supported; please install whisper-cli manually")
	}

	if err := extractWhisperFromTarGz(dlResp.Body, destPath); err != nil {
		return err
	}

	return nil
}

// findWhisperAsset searches release assets for a matching platform binary.
// Prefers non-CUDA builds for CPU-only operation.
func findWhisperAsset(assets []ghAsset, pattern string) string {
	// First pass: non-CUDA tar.gz
	for _, a := range assets {
		name := strings.ToLower(a.Name)
		if strings.Contains(name, pattern) && strings.HasSuffix(name, ".tar.gz") {
			if strings.Contains(name, "cuda") || strings.Contains(name, "cublas") {
				continue
			}
			return a.BrowserDownloadURL
		}
	}

	// Second pass: any matching tar.gz (including CUDA)
	for _, a := range assets {
		name := strings.ToLower(a.Name)
		if strings.Contains(name, pattern) && strings.HasSuffix(name, ".tar.gz") {
			return a.BrowserDownloadURL
		}
	}

	return ""
}

func extractWhisperFromTarGz(r io.Reader, destPath string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)

	// Target binary names to search for in the archive
	targetNames := map[string]bool{
		"whisper-cli":     true,
		"whisper-cli.exe": true,
		"main":            true,
		"main.exe":        true,
	}

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}

		// Safety: reject entries larger than maxExtractSize
		if header.Size > maxExtractSize {
			return fmt.Errorf("archive entry %q too large (%d bytes, max %d)", header.Name, header.Size, maxExtractSize)
		}

		base := filepath.Base(header.Name)
		if !targetNames[base] {
			continue
		}

		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return fmt.Errorf("create bin dir: %w", err)
		}

		out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return fmt.Errorf("create binary: %w", err)
		}

		// Use LimitReader to enforce size limit even if header.Size is spoofed
		limited := io.LimitReader(tr, maxExtractSize)
		if _, err := io.Copy(out, limited); err != nil {
			out.Close()
			os.Remove(destPath)
			return fmt.Errorf("extract binary: %w", err)
		}
		out.Close()
		return nil
	}

	return fmt.Errorf("whisper-cli binary not found in archive")
}

func downloadFile(url, destPath string, maxSize int64) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	resp, err := dlClient.Get(url)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}

	tmpPath := destPath + ".tmp"
	out, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}

	// Enforce max download size
	limited := io.LimitReader(resp.Body, maxSize)
	written, err := io.Copy(out, limited)
	out.Close()
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("write file: %w", err)
	}

	if written < 1000 {
		os.Remove(tmpPath)
		return fmt.Errorf("downloaded file too small (%d bytes)", written)
	}

	if err := atomicRename(tmpPath, destPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

// verifyModelChecksum checks the SHA256 hash of the downloaded model file.
// Returns nil if the hash matches or if the known hash is empty (skip verification).
func verifyModelChecksum(path string) error {
	if whisperModelSHA256 == "" {
		return nil
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open model for checksum: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("read model for checksum: %w", err)
	}

	got := hex.EncodeToString(h.Sum(nil))
	if got != whisperModelSHA256 {
		return fmt.Errorf("model checksum mismatch: got %s, want %s", got, whisperModelSHA256)
	}
	return nil
}

