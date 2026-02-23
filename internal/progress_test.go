package internal

import (
	"bytes"
	"testing"
)

func TestProgressDisplay_RecordResult(t *testing.T) {
	var buf bytes.Buffer
	p := NewProgressDisplay(&buf, 5, false) // isTTY=false → inactive
	p.RecordResult("success")
	p.RecordResult("success")
	p.RecordResult("stall_download")

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.completed != 3 {
		t.Fatalf("completed=%d, want 3", p.completed)
	}
	if p.succeeded != 2 {
		t.Fatalf("succeeded=%d, want 2", p.succeeded)
	}
	if p.failed != 1 {
		t.Fatalf("failed=%d, want 1", p.failed)
	}
}

func TestProgressDisplay_NonTTY(t *testing.T) {
	var buf bytes.Buffer
	p := NewProgressDisplay(&buf, 3, false)
	p.Start()
	p.RecordResult("success")
	p.Stop()

	// Non-TTY mode should produce no output
	if buf.Len() != 0 {
		t.Fatalf("expected no output for non-TTY, got %d bytes: %q", buf.Len(), buf.String())
	}
}
