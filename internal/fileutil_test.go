package internal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicRename_NewFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")

	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := atomicRename(src, dst); err != nil {
		t.Fatalf("atomicRename failed: %v", err)
	}

	// src should be gone
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("source file should not exist after rename")
	}

	// dst should have content
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("expected 'hello', got %q", string(data))
	}
}

func TestAtomicRename_OverwriteExisting(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")

	// Create existing destination
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create source with new content
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := atomicRename(src, dst); err != nil {
		t.Fatalf("atomicRename overwrite failed: %v", err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(data) != "new" {
		t.Errorf("expected 'new', got %q", string(data))
	}
}
