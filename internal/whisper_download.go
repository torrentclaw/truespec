package internal

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	whisperReleasesAPI = "https://api.github.com/repos/ggml-org/whisper.cpp/releases/latest"
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

// errBuildFromSource signals that no prebuilt binary exists and the platform
// requires building whisper-cli from source.
var errBuildFromSource = errors.New("no prebuilt binary available")

type ghRelease struct {
	TagName    string    `json:"tag_name"`
	TarballURL string    `json:"tarball_url"`
	Assets     []ghAsset `json:"assets"`
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
// Returns errBuildFromSource for platforms without prebuilt binaries (Linux, macOS).
func whisperAssetPattern() (string, error) {
	if runtime.GOOS == "windows" {
		switch runtime.GOARCH {
		case "amd64":
			return "whisper-bin-x64", nil
		case "386":
			return "whisper-bin-win32", nil
		}
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		return "", errBuildFromSource
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
		fmt.Fprintf(os.Stderr, "Installing whisper-cli...\n")
		if err := downloadWhisperBinary(whisperBin); err != nil {
			return "", "", fmt.Errorf("install whisper-cli: %w", err)
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

	// Check if we have a prebuilt binary for this platform
	pattern, err := whisperAssetPattern()
	if errors.Is(err, errBuildFromSource) {
		return buildWhisperFromSource(release, destPath)
	}
	if err != nil {
		return err
	}

	// Find matching prebuilt asset (Windows)
	assetURL := findWhisperAsset(release.Assets, pattern)
	if assetURL == "" {
		// Fallback to source build even on Windows
		fmt.Fprintf(os.Stderr, "No prebuilt binary found for %s — building from source...\n", pattern)
		return buildWhisperFromSource(release, destPath)
	}

	fmt.Fprintf(os.Stderr, "Downloading from %s...\n", release.TagName)

	dlResp, err := dlClient.Get(assetURL)
	if err != nil {
		return fmt.Errorf("download asset: %w", err)
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", dlResp.StatusCode)
	}

	if strings.HasSuffix(strings.ToLower(assetURL), ".zip") {
		zipData, err := io.ReadAll(io.LimitReader(dlResp.Body, maxExtractSize))
		if err != nil {
			return fmt.Errorf("read zip data: %w", err)
		}
		return extractWhisperFromZip(zipData, destPath)
	}

	return extractWhisperFromTarGz(dlResp.Body, destPath)
}

// findWhisperAsset searches release assets for a matching platform binary.
// Prefers non-CUDA/BLAS builds for CPU-only operation. Supports both .zip and .tar.gz.
func findWhisperAsset(assets []ghAsset, pattern string) string {
	lowerPattern := strings.ToLower(pattern)

	// First pass: non-CUDA/BLAS archives
	for _, a := range assets {
		name := strings.ToLower(a.Name)
		if !strings.Contains(name, lowerPattern) {
			continue
		}
		if strings.Contains(name, "cuda") || strings.Contains(name, "cublas") || strings.Contains(name, "blas") {
			continue
		}
		if strings.HasSuffix(name, ".zip") || strings.HasSuffix(name, ".tar.gz") {
			return a.BrowserDownloadURL
		}
	}

	// Second pass: any matching archive (including CUDA/BLAS)
	for _, a := range assets {
		name := strings.ToLower(a.Name)
		if !strings.Contains(name, lowerPattern) {
			continue
		}
		if strings.HasSuffix(name, ".zip") || strings.HasSuffix(name, ".tar.gz") {
			return a.BrowserDownloadURL
		}
	}

	return ""
}

// extractWhisperFromZip extracts the whisper-cli binary from a zip archive.
func extractWhisperFromZip(zipData []byte, destPath string) error {
	r, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}

	targetNames := map[string]bool{
		"whisper-cli":     true,
		"whisper-cli.exe": true,
		"main":            true,
		"main.exe":        true,
	}

	for _, f := range r.File {
		base := filepath.Base(f.Name)
		if !targetNames[base] {
			continue
		}

		if f.UncompressedSize64 > uint64(maxExtractSize) {
			return fmt.Errorf("archive entry %q too large (%d bytes, max %d)", f.Name, f.UncompressedSize64, maxExtractSize)
		}

		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open zip entry: %w", err)
		}
		defer rc.Close()

		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return fmt.Errorf("create bin dir: %w", err)
		}

		out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return fmt.Errorf("create binary: %w", err)
		}

		limited := io.LimitReader(rc, maxExtractSize)
		if _, err := io.Copy(out, limited); err != nil {
			out.Close()
			os.Remove(destPath)
			return fmt.Errorf("extract binary: %w", err)
		}
		out.Close()
		return nil
	}

	return fmt.Errorf("whisper-cli binary not found in zip archive")
}

// buildWhisperFromSource downloads the source tarball and compiles whisper-cli with cmake.
func buildWhisperFromSource(release ghRelease, destPath string) error {
	cmakePath, err := exec.LookPath("cmake")
	if err != nil {
		return fmt.Errorf("cmake not found: install cmake and a C++ compiler to build whisper-cli from source")
	}

	cxxFound := false
	for _, cxx := range []string{"c++", "g++", "clang++"} {
		if _, err := exec.LookPath(cxx); err == nil {
			cxxFound = true
			break
		}
	}
	if !cxxFound {
		return fmt.Errorf("C++ compiler not found: install g++ or clang++ to build whisper-cli")
	}

	if release.TarballURL == "" {
		return fmt.Errorf("no source tarball URL in release %s", release.TagName)
	}

	fmt.Fprintf(os.Stderr, "No prebuilt binary for %s/%s — building from source (%s)...\n",
		runtime.GOOS, runtime.GOARCH, release.TagName)

	// Download source tarball
	fmt.Fprintf(os.Stderr, "  Downloading source...\n")
	srcResp, err := dlClient.Get(release.TarballURL)
	if err != nil {
		return fmt.Errorf("download source: %w", err)
	}
	defer srcResp.Body.Close()

	if srcResp.StatusCode != http.StatusOK {
		return fmt.Errorf("download source returned HTTP %d", srcResp.StatusCode)
	}

	// Extract to temp directory
	tmpDir, err := os.MkdirTemp("", "whisper-build-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	fmt.Fprintf(os.Stderr, "  Extracting source...\n")
	srcDir, err := extractSourceTarball(srcResp.Body, tmpDir)
	if err != nil {
		return fmt.Errorf("extract source: %w", err)
	}

	// Configure with cmake
	fmt.Fprintf(os.Stderr, "  Configuring (cmake)...\n")
	buildDir := filepath.Join(srcDir, "build")
	configCmd := exec.Command(cmakePath, "-B", buildDir, "-DCMAKE_BUILD_TYPE=Release", "-DBUILD_SHARED_LIBS=OFF")
	configCmd.Dir = srcDir
	configCmd.Stderr = os.Stderr
	if err := configCmd.Run(); err != nil {
		return fmt.Errorf("cmake configure failed: %w", err)
	}

	// Build whisper-cli
	fmt.Fprintf(os.Stderr, "  Building whisper-cli (this may take a few minutes)...\n")
	buildCmd := exec.Command(cmakePath, "--build", buildDir, "-j", "--config", "Release", "--target", "whisper-cli")
	buildCmd.Dir = srcDir
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("cmake build failed: %w", err)
	}

	// Find the built binary
	builtBinary := filepath.Join(buildDir, "bin", "whisper-cli")
	if runtime.GOOS == "windows" {
		builtBinary += ".exe"
	}
	if _, err := os.Stat(builtBinary); err != nil {
		return fmt.Errorf("built binary not found at %s: %w", builtBinary, err)
	}

	// Copy binary to destination (can't os.Rename across filesystems)
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("create bin dir: %w", err)
	}

	data, err := os.ReadFile(builtBinary)
	if err != nil {
		return fmt.Errorf("read built binary: %w", err)
	}

	if err := os.WriteFile(destPath, data, 0o755); err != nil {
		return fmt.Errorf("write binary: %w", err)
	}

	fmt.Fprintf(os.Stderr, "  Build complete!\n")
	return nil
}

// extractSourceTarball extracts a GitHub source tarball and returns the path to the root directory.
func extractSourceTarball(r io.Reader, destDir string) (string, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var rootDir string
	var totalSize int64
	cleanDest := filepath.Clean(destDir) + string(os.PathSeparator)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read tar: %w", err)
		}

		// Skip PAX global headers (GitHub tarballs include pax_global_header)
		if header.Typeflag == tar.TypeXGlobalHeader || header.Typeflag == tar.TypeXHeader {
			continue
		}

		// Prevent zip-slip
		target := filepath.Join(destDir, header.Name)
		if !strings.HasPrefix(filepath.Clean(target)+string(os.PathSeparator), cleanDest) &&
			filepath.Clean(target) != filepath.Clean(destDir) {
			return "", fmt.Errorf("tar entry %q escapes destination", header.Name)
		}

		// Track root directory (first real directory entry)
		if rootDir == "" {
			parts := strings.SplitN(header.Name, "/", 2)
			if len(parts) > 0 && parts[0] != "" {
				rootDir = parts[0]
			}
		}

		totalSize += header.Size
		if totalSize > maxExtractSize {
			return "", fmt.Errorf("source archive too large (exceeds %d bytes)", maxExtractSize)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", fmt.Errorf("create dir: %w", err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return "", fmt.Errorf("create parent dir: %w", err)
			}
			mode := os.FileMode(header.Mode) & 0o777
			if mode == 0 {
				mode = 0o644
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return "", fmt.Errorf("create file: %w", err)
			}
			limited := io.LimitReader(tr, header.Size+1)
			if _, err := io.Copy(out, limited); err != nil {
				out.Close()
				return "", fmt.Errorf("write file: %w", err)
			}
			out.Close()
		}
	}

	if rootDir == "" {
		return "", fmt.Errorf("empty or invalid source archive")
	}

	return filepath.Join(destDir, rootDir), nil
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
