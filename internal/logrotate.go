package internal

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	DefaultLogMaxBytes int64 = 10 * 1024 * 1024 // 10 MB per log file
	DefaultLogMaxFiles       = 5                // keep up to 5 rotated files
	logDirName               = "logs"
	logFileName              = "truespec.log"
)

// LogDirPath returns the directory for log files (~/.truespec/logs/).
func LogDirPath() string {
	return filepath.Join(TrueSpecDir(), logDirName)
}

// RotatingLogWriter is an io.Writer that writes to a file with size-based rotation.
// It is safe for concurrent use.
type RotatingLogWriter struct {
	mu       sync.Mutex
	dir      string
	maxBytes int64
	maxFiles int
	file     *os.File
	size     int64
}

// NewRotatingLogWriter creates a rotating log writer in dir.
// The directory is created if it does not exist.
func NewRotatingLogWriter(dir string, maxBytes int64, maxFiles int) (*RotatingLogWriter, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	w := &RotatingLogWriter{
		dir:      dir,
		maxBytes: maxBytes,
		maxFiles: maxFiles,
	}
	if err := w.openOrCreate(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *RotatingLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			if w.file != nil {
				return w.file.Write(p)
			}
			return 0, fmt.Errorf("log rotate: %w", err)
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// Close closes the underlying log file.
func (w *RotatingLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		return w.file.Close()
	}
	return nil
}

func (w *RotatingLogWriter) openOrCreate() error {
	path := filepath.Join(w.dir, logFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("stat log file: %w", err)
	}
	w.file = f
	w.size = info.Size()
	return nil
}

func (w *RotatingLogWriter) rotate() error {
	w.file.Close()
	w.file = nil

	// Shift existing rotated files: N→N+1, ..., 1→2
	for i := w.maxFiles - 1; i >= 1; i-- {
		os.Rename(w.rotatedName(i), w.rotatedName(i+1))
	}

	// Current → .1
	current := filepath.Join(w.dir, logFileName)
	os.Rename(current, w.rotatedName(1))

	// Remove excess
	os.Remove(w.rotatedName(w.maxFiles + 1))

	return w.openOrCreate()
}

func (w *RotatingLogWriter) rotatedName(n int) string {
	return filepath.Join(w.dir, fmt.Sprintf("truespec.%d.log", n))
}
