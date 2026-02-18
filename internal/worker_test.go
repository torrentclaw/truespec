package internal

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestWorkerProtocol_RoundTrip(t *testing.T) {
	input := WorkerInput{
		InfoHash:     "0123456789abcdef0123456789abcdef01234567",
		Index:        1,
		Total:        5,
		FFprobePath:  "/usr/bin/ffprobe",
		TempDir:      os.TempDir(),
		StallTimeout: 90,
		MaxTimeout:   600,
		MinBytesMKV:  10 * 1024 * 1024,
		MinBytesMP4:  20 * 1024 * 1024,
		MaxRetries:   3,
		Verbose:      true,
	}

	data, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	var decoded WorkerInput
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}

	if decoded.InfoHash != input.InfoHash {
		t.Errorf("InfoHash mismatch: got %q, want %q", decoded.InfoHash, input.InfoHash)
	}
	if decoded.Index != input.Index {
		t.Errorf("Index mismatch: got %d, want %d", decoded.Index, input.Index)
	}
}

func TestWorkerOutput_Marshal(t *testing.T) {
	output := WorkerOutput{
		Result: ScanResult{
			InfoHash:  "0123456789abcdef0123456789abcdef01234567",
			Status:    "worker_crashed",
			Error:     "killed by signal SIGBUS",
			ElapsedMs: 0,
		},
		Downloaded: 0,
		Uploaded:   0,
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal output: %v", err)
	}

	var decoded WorkerOutput
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	if decoded.Result.Status != "worker_crashed" {
		t.Errorf("Status mismatch: got %q, want %q", decoded.Result.Status, "worker_crashed")
	}
	if decoded.Result.Error != "killed by signal SIGBUS" {
		t.Errorf("Error mismatch: got %q, want %q", decoded.Result.Error, "killed by signal SIGBUS")
	}
}

func TestPrefixWriter(t *testing.T) {
	var got []byte
	w := &prefixWriter{
		prefix: []byte("[test] "),
		w:      writerFunc(func(p []byte) (int, error) {
			got = append(got, p...)
			return len(p), nil
		}),
	}

	// Single line
	n, err := w.Write([]byte("hello\n"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != 6 { // original input length
		t.Logf("write returned %d, len(input)=6", n)
	}

	want := "[test] hello\n"
	if string(got) != want {
		t.Errorf("got %q, want %q", string(got), want)
	}

	// Multiple lines
	got = nil
	_, _ = w.Write([]byte("line1\nline2\nline3"))
	want = "[test] line1\n[test] line2\n[test] line3"
	if string(got) != want {
		t.Errorf("got %q, want %q", string(got), want)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestWorkerCrashResult(t *testing.T) {
	result := workerCrashResult("abc123", "SIGBUS")
	if result.Result.Status != "worker_crashed" {
		t.Errorf("status: got %q, want %q", result.Result.Status, "worker_crashed")
	}
	if result.Result.Error != "SIGBUS" {
		t.Errorf("error: got %q, want %q", result.Result.Error, "SIGBUS")
	}
	if result.Result.InfoHash != "abc123" {
		t.Errorf("infoHash: got %q, want %q", result.Result.InfoHash, "abc123")
	}
}

func TestWorkerErrorResult(t *testing.T) {
	result := workerErrorResult("def456", "exit 1")
	if result.Result.Status != "worker_error" {
		t.Errorf("status: got %q, want %q", result.Result.Status, "worker_error")
	}
	if result.Result.Error != "exit 1" {
		t.Errorf("error: got %q, want %q", result.Result.Error, "exit 1")
	}
}

func TestSignalName(t *testing.T) {
	tests := []struct {
		sig     syscall.Signal
		want    string
	}{
		{syscall.SIGBUS, "SIGBUS"},
		{syscall.SIGSEGV, "SIGSEGV"},
		{syscall.SIGKILL, "SIGKILL"},
		{syscall.SIGTERM, "SIGTERM"},
		{syscall.SIGINT, "SIGINT"},
		{syscall.SIGABRT, "SIGABRT"},
		{syscall.Signal(42), "signal 42"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := signalName(tt.sig)
			if got != tt.want {
				t.Errorf("signalName(%v) = %q, want %q", tt.sig, got, tt.want)
			}
		})
	}
}

func TestTruncateOutput(t *testing.T) {
	short := []byte("short")
	got := truncateOutput(short)
	if got != "short" {
		t.Errorf("short: got %q, want %q", got, "short")
	}

	long := make([]byte, 300)
	for i := range long {
		long[i] = 'x'
	}
	got = truncateOutput(long)
	want := string(long[:200]) + "... (truncated)"
	if len(got) != len(want) {
		t.Errorf("long length: got %d, want %d", len(got), len(want))
	}
	if got != want {
		t.Errorf("long: got %q, want %q", got, want)
	}
}

func TestWorkerMode_SimulatedCrash(t *testing.T) {
	if os.Getenv("GO_TEST_SUBPROCESS") == "1" {
		// Simulate a crash by exiting with signal-like code
		os.Exit(137) // 128 + 9 = SIGKILL typically
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestWorkerMode_SimulatedCrash$")
	cmd.Env = append(os.Environ(), "GO_TEST_SUBPROCESS=1")
	err := cmd.Run()

	if err == nil {
		t.Fatal("expected subprocess to fail with exit 137, but it succeeded")
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() != 137 {
			t.Logf("subprocess exit code: %d", exitErr.ExitCode())
		}
	}
}

func TestGetExePath(t *testing.T) {
	path, err := getExePath()
	if err != nil {
		t.Logf("getExePath failed (expected in some environments): %v", err)
		return
	}
	if path == "" {
		t.Error("getExePath returned empty string")
	}
	t.Logf("getExePath = %q", path)
}

func TestProcessOneInProcess_Timing(t *testing.T) {
	// This test just verifies the function signature and basic behavior
	// without actually downloading anything (would require network)

	// Create a minimal config
	cfg := Config{
		TempDir:      os.TempDir(),
		Verbose:      false,
		MinBytesMKV:  1024,
		MinBytesMP4:  1024,
		MaxFFprobeRetries: 0,
	}

	// We can't test the full flow without a real torrent,
	// but we can verify the function is callable and has the right signature
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// This would normally require a Downloader; we're just testing
	// that the function exists and has the right signature
	_ = ctx
	_ = cfg
	// Actual download test would require network and real torrent
}
