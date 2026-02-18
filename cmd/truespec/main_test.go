package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestEnsureClassicFileIO_ClassicReturnsImmediately(t *testing.T) {
	t.Setenv("TORRENT_STORAGE_DEFAULT_FILE_IO", "classic")
	// Must return without calling syscall.Exec — if it tried to re-exec,
	// the test binary would restart and eventually fail or hang.
	ensureClassicFileIO()
}

func TestEnsureClassicFileIO_MmapReturnsImmediately(t *testing.T) {
	t.Setenv("TORRENT_STORAGE_DEFAULT_FILE_IO", "mmap")
	ensureClassicFileIO()
}

func TestEnsureClassicFileIO_UnsetSetsClassic(t *testing.T) {
	if os.Getenv("GO_TEST_SUBPROCESS") == "1" {
		ensureClassicFileIO()
		// After ensureClassicFileIO, the env var must be "classic" regardless
		// of whether re-exec succeeded (env inherited) or failed (os.Setenv).
		if v := os.Getenv("TORRENT_STORAGE_DEFAULT_FILE_IO"); v != "classic" {
			t.Fatalf("expected env var 'classic' after ensureClassicFileIO, got %q", v)
		}
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestEnsureClassicFileIO_UnsetSetsClassic$")
	cmd.Env = append(filterEnv(os.Environ(), "TORRENT_STORAGE_DEFAULT_FILE_IO"),
		"GO_TEST_SUBPROCESS=1",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess failed: %v\noutput:\n%s", err, output)
	}
}

// TestEnsureClassicFileIO_InvalidValuePanicsInLibrary documents that invalid
// values for TORRENT_STORAGE_DEFAULT_FILE_IO cause the library's init() to
// panic before main() runs. Our code cannot validate this — the library
// enforces it at package initialization time.
func TestEnsureClassicFileIO_InvalidValuePanicsInLibrary(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestEnsureClassicFileIO_ClassicReturnsImmediately$")
	cmd.Env = append(filterEnv(os.Environ(), "TORRENT_STORAGE_DEFAULT_FILE_IO"),
		"TORRENT_STORAGE_DEFAULT_FILE_IO=bogus",
	)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected subprocess to fail with panic, but it succeeded")
	}
	if !strings.Contains(string(output), "panic: bogus") {
		t.Errorf("expected library panic for invalid value, got:\n%s", output)
	}
}

func filterEnv(env []string, key string) []string {
	prefix := key + "="
	filtered := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			filtered = append(filtered, e)
		}
	}
	return filtered
}
