package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// WorkerInput is sent via stdin to the worker subprocess.
type WorkerInput struct {
	InfoHash       string `json:"info_hash"`
	Index          int    `json:"index"` // for logging "[idx/total]"
	Total          int    `json:"total"` // for logging
	FFprobePath    string `json:"ffprobe_path"`
	TempDir        string `json:"temp_dir"`
	StallTimeout   int    `json:"stall_timeout_s"`
	MaxTimeout     int    `json:"max_timeout_s"`
	TimeoutSeconds int    `json:"timeout_seconds"` // absolute timeout for this worker
	MinBytesMKV    int    `json:"min_bytes_mkv"`
	MinBytesMP4    int    `json:"min_bytes_mp4"`
	MaxRetries     int    `json:"max_retries"`
	Verbose        bool   `json:"verbose"`
}

// WorkerOutput is written to the original stdout file descriptor.
type WorkerOutput struct {
	Result     ScanResult `json:"result"`
	Downloaded int64      `json:"downloaded"`
	Uploaded   int64      `json:"uploaded"`
}

// RunWorker is the main worker function, executed inside the subprocess.
// It creates its own Downloader in an isolated subdirectory, processes the torrent,
// captures stats and does cleanup. Returns the serializable result.
func RunWorker(input WorkerInput) WorkerOutput {
	start := time.Now()

	// Subdirectorio aislado para este worker
	subdir := filepath.Join(input.TempDir, fmt.Sprintf("worker-%s", input.InfoHash[:8]))
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		return WorkerOutput{
			Result: errorResult(input.InfoHash, fmt.Errorf("create worker dir: %w", err), start),
		}
	}
	defer os.RemoveAll(subdir) // cleanup del subdirectorio

	// Limpiar base de datos de piece completion del directorio anterior
	for _, f := range []string{".torrent.db", ".torrent.db-wal", ".torrent.db-shm"} {
		os.Remove(filepath.Join(subdir, f))
	}

	// Crear Downloader para este worker
	dl, err := NewDownloader(DownloadConfig{
		TempDir:      subdir,
		StallTimeout: time.Duration(input.StallTimeout) * time.Second,
		MaxTimeout:   time.Duration(input.MaxTimeout) * time.Second,
		Verbose:      input.Verbose,
		MinBytesMKV:  input.MinBytesMKV,
		MinBytesMP4:  input.MinBytesMP4,
	})
	if err != nil {
		return WorkerOutput{
			Result: errorResult(input.InfoHash, fmt.Errorf("create downloader: %w", err), start),
		}
	}
	defer dl.Close()

	// Configurar logging con prefijo si verbose
	if input.Verbose {
		prefix := fmt.Sprintf("[worker:%s] ", input.InfoHash[:8])
		log.SetPrefix(prefix)
		log.Printf("[%d/%d] starting worker", input.Index, input.Total)
	}

	// Construir Config para processOne
	cfg := Config{
		FFprobePath:       input.FFprobePath,
		TempDir:           subdir,
		Verbose:           input.Verbose,
		MinBytesMKV:       input.MinBytesMKV,
		MinBytesMP4:       input.MinBytesMP4,
		MaxFFprobeRetries: input.MaxRetries,
	}

	// Create context with timeout to respect parent cancellation
	ctx := context.Background()
	if input.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(input.TimeoutSeconds)*time.Second)
		defer cancel()
	}
	result := processOne(ctx, dl, cfg, input.InfoHash)

	// Capturar stats ANTES de cleanup
	downloaded, uploaded := dl.GetTorrentStats(input.InfoHash)

	// Cleanup del torrent
	dl.Cleanup(input.InfoHash)

	if input.Verbose {
		log.Printf("[%d/%d] worker done: status=%s dl=%d up=%d",
			input.Index, input.Total, result.Status, downloaded, uploaded)
	}

	return WorkerOutput{
		Result:     result,
		Downloaded: downloaded,
		Uploaded:   uploaded,
	}
}

// processOneIsolated executes a torrent scan in an isolated subprocess.
// It spawns the subprocess, communicates via stdin/stdout, and handles crashes.
func processOneIsolated(ctx context.Context, exePath string, input WorkerInput) (WorkerOutput, error) {
	// Serializar input
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return WorkerOutput{}, fmt.Errorf("marshal worker input: %w", err)
	}

	// Crear comando
	cmd := exec.CommandContext(ctx, exePath, "_worker")
	cmd.Stdin = strings.NewReader(string(inputJSON))

	// Capturar stdout (resultado JSON)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return WorkerOutput{}, fmt.Errorf("create stdout pipe: %w", err)
	}
	defer stdout.Close()

	// stderr del worker → prefixWriter
	cmd.Stderr = &prefixWriter{
		prefix: []byte(fmt.Sprintf("[worker:%s] ", input.InfoHash[:8])),
		w:      os.Stderr,
	}

	// Iniciar proceso
	if err := cmd.Start(); err != nil {
		// Si falla el lanzamiento, intentar fallback in-process
		if ctx.Err() == nil {
			return WorkerOutput{}, fmt.Errorf("start worker: %w", err)
		}
	}

	// Leer stdout completo
	stdoutBytes, err := io.ReadAll(stdout)
	if err != nil {
		// Proceso murió antes de escribir output
		_ = cmd.Wait() // limpiar zombie
		return workerCrashResult(input.InfoHash, "read stdout failed"), nil
	}

	// Esperar a que termine
	wErr := cmd.Wait()

	// Analizar resultado
	if wErr == nil {
		// Exit 0 → parsear JSON
		var output WorkerOutput
		if err := json.Unmarshal(stdoutBytes, &output); err != nil {
			// Exit 0 pero JSON inválido → bug
			return workerErrorResult(input.InfoHash,
				fmt.Sprintf("invalid worker output: %v (output: %s)", err, truncateOutput(stdoutBytes))), nil
		}
		return output, nil
	}

	// Exit no fue 0 → analizar causa
	if exitErr, ok := wErr.(*exec.ExitError); ok {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			// Murió por señal
			sigName := signalName(status.Signal())
			return workerCrashResult(input.InfoHash, fmt.Sprintf("killed by signal %s", sigName)), nil
		}
		// Exit code != 0 sin señal (panic, error fatal)
		return workerErrorResult(input.InfoHash,
			fmt.Sprintf("exit %d", exitErr.ExitCode())), nil
	}

	// Timeout del contexto u otro error
	return workerErrorResult(input.InfoHash, wErr.Error()), nil
}

// processOneInProcess is the fallback that processes a torrent in-process
// with the shared Downloader (original behavior).
func processOneInProcess(ctx context.Context, dl *Downloader, cfg Config, hash string, idx, total int) (ScanResult, int64, int64) {
	if cfg.Verbose {
		log.Printf("[%d/%d] scanning %s (in-process)", idx, total, truncHash(hash))
	}

	result := processOne(ctx, dl, cfg, hash)
	downloaded, uploaded := dl.GetTorrentStats(hash)
	dl.Cleanup(hash)

	return result, downloaded, uploaded
}

// prefixWriter prepends a prefix to each written line.
type prefixWriter struct {
	prefix []byte
	w      io.Writer
	buf    []byte // buffer for partial lines
}

func (p *prefixWriter) Write(data []byte) (int, error) {
	total := 0
	for _, b := range data {
		p.buf = append(p.buf, b)
		if b == '\n' {
			// Write line with prefix
			if _, err := p.w.Write(p.prefix); err != nil {
				return total, err
			}
			n, err := p.w.Write(p.buf)
			total += n
			if err != nil {
				return total, err
			}
			p.buf = p.buf[:0] // reset
		}
	}
	// Write any remaining without prefix (incomplete line)
	if len(p.buf) > 0 {
		n, err := p.w.Write(p.prefix)
		total += n
		if err != nil {
			return total, err
		}
		n, err = p.w.Write(p.buf)
		total += n
		if err != nil {
			return total, err
		}
		p.buf = p.buf[:0]
	}
	return total, nil
}

func workerCrashResult(infoHash, reason string) WorkerOutput {
	return WorkerOutput{
		Result: ScanResult{
			InfoHash:  infoHash,
			Status:    "worker_crashed",
			Error:     reason,
			ElapsedMs: 0,
		},
	}
}

func workerErrorResult(infoHash, reason string) WorkerOutput {
	return WorkerOutput{
		Result: ScanResult{
			InfoHash:  infoHash,
			Status:    "worker_error",
			Error:     reason,
			ElapsedMs: 0,
		},
	}
}

func truncateOutput(data []byte) string {
	if len(data) <= 200 {
		return string(data)
	}
	return string(data[:200]) + "... (truncated)"
}

func signalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGBUS:
		return "SIGBUS"
	case syscall.SIGSEGV:
		return "SIGSEGV"
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGABRT:
		return "SIGABRT"
	default:
		return fmt.Sprintf("signal %d", sig)
	}
}
