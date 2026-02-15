package internal

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	whisperReleasesAPI = "https://api.github.com/repos/ggerganov/whisper.cpp/releases/latest"
	whisperModelURL    = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-tiny.bin"
	whisperModelName   = "ggml-tiny.bin"
)

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
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
	}
	return "", fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
}

// DownloadWhisper downloads the whisper-cli binary and the tiny model.
// Returns (whisperPath, modelPath, error).
func DownloadWhisper() (string, string, error) {
	binDir := WhisperBinDir()
	modelDir := WhisperModelDir()

	whisperBin := filepath.Join(binDir, "whisper-cli")
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
		if err := downloadFile(whisperModelURL, modelPath); err != nil {
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

	// Get latest release
	resp, err := http.Get(whisperReleasesAPI)
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

	// Find matching asset (look for tar.gz with platform pattern, prefer non-cuda)
	var assetURL string
	for _, a := range release.Assets {
		name := strings.ToLower(a.Name)
		if strings.Contains(name, pattern) && strings.HasSuffix(name, ".tar.gz") {
			// Skip CUDA builds — we want CPU-only
			if strings.Contains(name, "cuda") || strings.Contains(name, "cublas") {
				continue
			}
			assetURL = a.BrowserDownloadURL
			break
		}
	}

	if assetURL == "" {
		// Fallback: try any matching asset
		for _, a := range release.Assets {
			name := strings.ToLower(a.Name)
			if strings.Contains(name, pattern) && strings.HasSuffix(name, ".tar.gz") {
				assetURL = a.BrowserDownloadURL
				break
			}
		}
	}

	if assetURL == "" {
		return fmt.Errorf("no whisper-cli release found for %s (release %s)", pattern, release.TagName)
	}

	fmt.Fprintf(os.Stderr, "Downloading from %s...\n", release.TagName)

	// Download tar.gz
	dlResp, err := http.Get(assetURL)
	if err != nil {
		return fmt.Errorf("download asset: %w", err)
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", dlResp.StatusCode)
	}

	// Extract whisper-cli from tar.gz
	if err := extractWhisperFromTarGz(dlResp.Body, destPath); err != nil {
		return err
	}

	return nil
}

func extractWhisperFromTarGz(r io.Reader, destPath string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}

		base := filepath.Base(header.Name)
		// Look for whisper-cli or main binary
		if base == "whisper-cli" || base == "main" {
			if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
				return fmt.Errorf("create bin dir: %w", err)
			}

			out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
			if err != nil {
				return fmt.Errorf("create binary: %w", err)
			}

			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				os.Remove(destPath)
				return fmt.Errorf("extract binary: %w", err)
			}
			out.Close()
			return nil
		}
	}

	return fmt.Errorf("whisper-cli binary not found in archive")
}

func downloadFile(url, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	resp, err := http.Get(url)
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

	written, err := io.Copy(out, resp.Body)
	out.Close()
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("write file: %w", err)
	}

	if written < 1000 {
		os.Remove(tmpPath)
		return fmt.Errorf("downloaded file too small (%d bytes)", written)
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}
