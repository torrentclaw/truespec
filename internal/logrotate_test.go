package internal

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRotatingLogWriter_BasicWrite(t *testing.T) {
	dir := t.TempDir()
	w, err := NewRotatingLogWriter(dir, 1024, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	msg := "hello log\n"
	n, err := w.Write([]byte(msg))
	if err != nil {
		t.Fatal(err)
	}
	if n != len(msg) {
		t.Fatalf("wrote %d, want %d", n, len(msg))
	}

	w.Close()

	data, err := os.ReadFile(filepath.Join(dir, "truespec.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != msg {
		t.Fatalf("got %q, want %q", data, msg)
	}
}

func TestRotatingLogWriter_Rotation(t *testing.T) {
	dir := t.TempDir()
	// Max 100 bytes per file, keep 3 rotated
	w, err := NewRotatingLogWriter(dir, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Write 120 bytes to trigger rotation
	chunk := strings.Repeat("A", 60) + "\n" // 61 bytes
	w.Write([]byte(chunk))
	w.Write([]byte(chunk)) // total 122 > 100 → rotation

	w.Close()

	// Current file should exist with the second chunk
	if _, err := os.Stat(filepath.Join(dir, "truespec.log")); err != nil {
		t.Fatal("current log file missing after rotation")
	}
	// Rotated .1 should exist with the first chunk
	if _, err := os.Stat(filepath.Join(dir, "truespec.1.log")); err != nil {
		t.Fatal("rotated .1 log file missing after rotation")
	}
}

func TestRotatingLogWriter_MaxFiles(t *testing.T) {
	dir := t.TempDir()
	// Max 50 bytes, keep 2 rotated files
	w, err := NewRotatingLogWriter(dir, 50, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	chunk := strings.Repeat("B", 55) + "\n" // 56 bytes, exceeds 50
	// Write 4 chunks → should create .1 and .2, but not .3
	for i := 0; i < 4; i++ {
		w.Write([]byte(chunk))
	}

	w.Close()

	if _, err := os.Stat(filepath.Join(dir, "truespec.log")); err != nil {
		t.Fatal("current log missing")
	}
	if _, err := os.Stat(filepath.Join(dir, "truespec.1.log")); err != nil {
		t.Fatal("truespec.1.log missing")
	}
	if _, err := os.Stat(filepath.Join(dir, "truespec.2.log")); err != nil {
		t.Fatal("truespec.2.log missing")
	}
	// .3 should not exist (maxFiles=2)
	if _, err := os.Stat(filepath.Join(dir, "truespec.3.log")); err == nil {
		t.Fatal("truespec.3.log should not exist with maxFiles=2")
	}
}

func TestRotatingLogWriter_ConcurrentWrite(t *testing.T) {
	dir := t.TempDir()
	w, err := NewRotatingLogWriter(dir, 10*1024, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				w.Write([]byte("concurrent write line\n"))
			}
		}()
	}
	wg.Wait()

	// Should not panic or corrupt; just verify file exists
	if _, err := os.Stat(filepath.Join(dir, "truespec.log")); err != nil {
		t.Fatal("log file missing after concurrent writes")
	}
}
