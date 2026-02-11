package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/torrentclaw/truespec/internal"
)

var version = "dev"

func main() {
	// Subcommand dispatch
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "scan":
		runScan(os.Args[2:])
	case "version":
		fmt.Printf("truespec %s\n", version)
	case "--help", "-h", "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `truespec %s — Verify real media specs from torrent data

Usage:
  truespec scan [flags] <info_hash> [info_hash...]
  truespec scan [flags] -f <file>
  truespec scan [flags] --stdin
  truespec version

The scan command partially downloads torrent files and extracts verified
media metadata (audio tracks, subtitles, video codec/resolution/HDR) using
ffprobe. Results are saved to a JSON file (default: results.json).

Examples:
  truespec scan abc123def456...
  truespec scan --concurrency=3 hash1 hash2 hash3
  truespec scan -f hashes.txt -o my-results.json
  cat hashes.txt | truespec scan --stdin --verbose

Run 'truespec scan --help' for scan-specific flags.
`, version)
}

func runScan(args []string) {
	cfg := internal.DefaultConfig()

	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	fs.IntVar(&cfg.Concurrency, "concurrency", cfg.Concurrency, "Maximum concurrent torrent downloads")
	fs.IntVar(&cfg.Concurrency, "c", cfg.Concurrency, "Maximum concurrent torrent downloads (shorthand)")

	stallSec := int(cfg.StallTimeout / time.Second)
	fs.IntVar(&stallSec, "stall-timeout", stallSec, "Seconds of no progress before killing a torrent")

	maxSec := int(cfg.MaxTimeout / time.Second)
	fs.IntVar(&maxSec, "max-timeout", maxSec, "Absolute maximum seconds per torrent")

	fs.StringVar(&cfg.FFprobePath, "ffprobe", cfg.FFprobePath, "Path to ffprobe binary (auto-detect if empty)")
	fs.StringVar(&cfg.TempDir, "temp-dir", cfg.TempDir, "Temporary directory for downloads")
	fs.BoolVar(&cfg.Verbose, "verbose", false, "Print progress logs to stderr")
	fs.BoolVar(&cfg.Verbose, "v", false, "Print progress logs to stderr (shorthand)")
	fs.StringVar(&cfg.OutputFile, "output", "", "Output JSON file path (default: results_<timestamp>.json)")
	fs.StringVar(&cfg.OutputFile, "o", "", "Output JSON file path (default: results_<timestamp>.json)")

	var fromFile string
	var fromStdin bool
	fs.StringVar(&fromFile, "f", "", "Read info hashes from file (one per line)")
	fs.BoolVar(&fromStdin, "stdin", false, "Read info hashes from stdin")

	fs.Parse(args)

	// Apply parsed durations
	cfg.StallTimeout = time.Duration(stallSec) * time.Second
	cfg.MaxTimeout = time.Duration(maxSec) * time.Second

	// Default output filename with timestamp (never overwrites previous runs)
	if cfg.OutputFile == "" {
		cfg.OutputFile = fmt.Sprintf("results_%s.json", time.Now().Format("2006-01-02_150405"))
	}

	// Configure logging: verbose logs go to stderr
	log.SetOutput(os.Stderr)
	log.SetFlags(log.Ltime)
	if !cfg.Verbose {
		log.SetOutput(devNull{})
	}

	// Collect info hashes from all sources
	var hashes []string

	// From positional args
	for _, arg := range fs.Args() {
		h := strings.TrimSpace(arg)
		if h != "" {
			hashes = append(hashes, strings.ToLower(h))
		}
	}

	// From file
	if fromFile != "" {
		fileHashes, err := readHashesFromFile(fromFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading file %s: %v\n", fromFile, err)
			os.Exit(1)
		}
		hashes = append(hashes, fileHashes...)
	}

	// From stdin
	if fromStdin {
		stdinHashes, err := readHashesFromReader(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading stdin: %v\n", err)
			os.Exit(1)
		}
		hashes = append(hashes, stdinHashes...)
	}

	if len(hashes) == 0 {
		fmt.Fprintln(os.Stderr, "Error: no info hashes provided")
		fmt.Fprintln(os.Stderr, "")
		fs.Usage()
		os.Exit(1)
	}

	// Validate hashes (should be 40-char hex strings)
	for i, h := range hashes {
		if len(h) != 40 {
			fmt.Fprintf(os.Stderr, "Warning: hash #%d (%q) is not 40 characters, may be invalid\n", i+1, h)
		}
	}

	// Resolve ffprobe early so we fail fast
	ffprobePath, err := internal.ResolveFFprobe(cfg.FFprobePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	cfg.FFprobePath = ffprobePath

	log.Printf("truespec %s — scanning %d hash(es)", version, len(hashes))
	log.Printf("  concurrency: %d", cfg.Concurrency)
	log.Printf("  stall timeout: %s", cfg.StallTimeout)
	log.Printf("  max timeout: %s", cfg.MaxTimeout)
	log.Printf("  ffprobe: %s", cfg.FFprobePath)
	log.Printf("  temp dir: %s", cfg.TempDir)
	log.Printf("  output: %s", cfg.OutputFile)

	// Startup cleanup: remove leftover files from previous runs (crashes, OOM kills, etc.)
	// Partial downloads are never resumable, so there's zero value in keeping them.
	cleanTempDir(cfg.TempDir)

	// Context with signal handling for graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Run scan and collect results
	start := time.Now()
	results := internal.Scan(ctx, cfg, hashes)

	stats := map[string]int{}
	var collected []internal.ScanResult

	for result := range results {
		// Ensure slices are never nil (always [] in JSON, not null)
		if result.Audio == nil {
			result.Audio = []internal.AudioTrack{}
		}
		if result.Subtitles == nil {
			result.Subtitles = []internal.SubtitleTrack{}
		}
		if result.Languages == nil {
			result.Languages = []string{}
		}

		collected = append(collected, result)
		stats[result.Status]++

		if cfg.Verbose {
			log.Printf("  [%d/%d] %s → %s (%dms)",
				len(collected), len(hashes), result.InfoHash[:8], result.Status, result.ElapsedMs)
		}
	}

	elapsed := time.Since(start)

	// Build report
	report := internal.ScanReport{
		Version:   version,
		ScannedAt: time.Now().UTC().Format(time.RFC3339),
		ElapsedMs: elapsed.Milliseconds(),
		Total:     len(collected),
		Stats:     stats,
		Results:   collected,
	}

	// Write JSON file
	outFile, err := os.Create(cfg.OutputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating output file: %v\n", err)
		os.Exit(1)
	}
	defer outFile.Close()

	encoder := json.NewEncoder(outFile)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing JSON: %v\n", err)
		os.Exit(1)
	}

	// Post-scan cleanup: remove all temp files (partial downloads are never resumable)
	cleanTempDir(cfg.TempDir)

	// Print summary to stderr (always visible)
	fmt.Fprintf(os.Stderr, "\nScan complete in %s\n", elapsed.Round(time.Millisecond))
	fmt.Fprintf(os.Stderr, "  Total: %d\n", len(collected))
	for status, count := range stats {
		fmt.Fprintf(os.Stderr, "  %s: %d\n", status, count)
	}
	fmt.Fprintf(os.Stderr, "  Results saved to: %s\n", cfg.OutputFile)
}

func readHashesFromFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readHashesFromReader(f)
}

func readHashesFromReader(r *os.File) ([]string, error) {
	var hashes []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		h := strings.TrimSpace(scanner.Text())
		if h != "" && !strings.HasPrefix(h, "#") {
			hashes = append(hashes, strings.ToLower(h))
		}
	}
	return hashes, scanner.Err()
}

// cleanTempDir removes the temp directory and all its contents.
// Errors are logged but not fatal — best-effort cleanup.
func cleanTempDir(dir string) {
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("warning: failed to clean temp dir %s: %v", dir, err)
	}
}

// devNull is an io.Writer that discards all output.
type devNull struct{}

func (devNull) Write(p []byte) (int, error) { return len(p), nil }
