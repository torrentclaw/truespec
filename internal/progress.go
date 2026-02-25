package internal

import (
	"fmt"
	"io"
	"sync"
	"time"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// ProgressDisplay shows a compact live progress line on stderr during scans.
// It uses ANSI escape sequences to update a single line in-place.
type ProgressDisplay struct {
	mu        sync.Mutex
	w         io.Writer
	total     int
	completed int
	succeeded int
	failed    int
	frame     int
	ticker    *time.Ticker
	done      chan struct{}
	wg        sync.WaitGroup
	started   time.Time
	active    bool // false when stderr is not a terminal
}

// NewProgressDisplay creates a new progress display that writes to w.
// If isTTY is false the display is inactive (no output).
func NewProgressDisplay(w io.Writer, total int, isTTY bool) *ProgressDisplay {
	return &ProgressDisplay{
		w:       w,
		total:   total,
		done:    make(chan struct{}),
		started: time.Now(),
		active:  isTTY,
	}
}

// Start begins the spinner animation ticker. Call Stop when done.
func (p *ProgressDisplay) Start() {
	if !p.active {
		return
	}
	// Hide cursor
	fmt.Fprint(p.w, "\033[?25l")
	p.render()

	p.ticker = time.NewTicker(80 * time.Millisecond)
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			select {
			case <-p.done:
				return
			case <-p.ticker.C:
				p.render()
			}
		}
	}()
}

// RecordResult updates counters after a torrent finishes scanning.
func (p *ProgressDisplay) RecordResult(status string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.completed++
	if status == "success" {
		p.succeeded++
	} else {
		p.failed++
	}
}

// Stop halts the animation, clears the progress line and restores the cursor.
func (p *ProgressDisplay) Stop() {
	if !p.active {
		return
	}
	close(p.done)
	if p.ticker != nil {
		p.ticker.Stop()
	}
	p.wg.Wait()
	// Clear line and show cursor
	fmt.Fprint(p.w, "\r\033[K\033[?25u")
}

func (p *ProgressDisplay) render() {
	p.mu.Lock()
	completed := p.completed
	total := p.total
	succeeded := p.succeeded
	failed := p.failed
	frame := p.frame
	p.frame++
	p.mu.Unlock()

	spinner := spinnerFrames[frame%len(spinnerFrames)]
	elapsed := time.Since(p.started).Round(time.Second)

	// ⠹ Scanning [3/10]  ✓ 2  ✗ 1  (12s)   — or [3] when total is unknown
	var progress string
	if total > 0 {
		progress = fmt.Sprintf("[%d/%d]", completed, total)
	} else {
		progress = fmt.Sprintf("[%d]", completed)
	}
	line := fmt.Sprintf("\r\033[K%s Scanning %s  \033[32m✓ %d\033[0m  \033[31m✗ %d\033[0m  (%s)",
		spinner, progress, succeeded, failed, elapsed)

	fmt.Fprint(p.w, line)
}
