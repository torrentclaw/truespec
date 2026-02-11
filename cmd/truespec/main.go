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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/torrentclaw/truespec/internal"
	"golang.org/x/term"
)

var version = "dev"

func main() {
	// Subcommand dispatch
	if len(os.Args) < 2 {
		// No subcommand: launch interactive mode if terminal, else show usage
		if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd())) {
			runInteractive()
			return
		}
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
  truespec                               (interactive mode)
  truespec scan [flags] <input> [input...]
  truespec scan [flags] -f <file>
  truespec scan [flags] --stdin
  truespec version

Inputs can be info hashes, magnet links, .torrent files, or directories
containing .torrent files.

The scan command partially downloads torrent files and extracts verified
media metadata (audio tracks, subtitles, video codec/resolution/HDR) using
ffprobe. Results are saved to a JSON file (default: results_<timestamp>.json).

Examples:
  truespec scan abc123def456...
  truespec scan "magnet:?xt=urn:btih:abc123..."
  truespec scan movie.torrent
  truespec scan ./torrents/
  truespec scan -f hashes.txt -o my-results.json
  cat hashes.txt | truespec scan --stdin --verbose

Run 'truespec scan --help' for scan-specific flags.
`, version)
}

func runInteractive() {
	fmt.Fprintf(os.Stderr, "truespec %s — Interactive Mode\n\n", version)

	cfg := internal.DefaultConfig()

	var source string
	var pasteInput string
	var filePath string
	var concurrencyStr string
	var verbose bool
	var outputFile string

	concurrencyStr = strconv.Itoa(cfg.Concurrency)

	// Step 1: Source selection
	sourceForm := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("How do you want to provide torrents?").
				Options(
					huh.NewOption("Paste hashes or magnet links", "paste"),
					huh.NewOption("Load from file (.txt list or .torrent)", "file"),
					huh.NewOption("Load all .torrent files from a folder", "folder"),
				).
				Value(&source),
		),
	)

	if err := sourceForm.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Cancelled.\n")
		os.Exit(0)
	}

	// Step 2: Collect input based on source
	var inputForm *huh.Form
	switch source {
	case "paste":
		inputForm = huh.NewForm(
			huh.NewGroup(
				huh.NewText().
					Title("Paste info hashes or magnet links (one per line)").
					Value(&pasteInput).
					Validate(func(s string) error {
						if strings.TrimSpace(s) == "" {
							return fmt.Errorf("at least one hash or magnet link is required")
						}
						return nil
					}),
			),
		)
	case "file":
		inputForm = huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title("File path (.txt with hashes/magnets, or .torrent)").
					Placeholder("./hashes.txt").
					Value(&filePath).
					Validate(func(s string) error {
						if strings.TrimSpace(s) == "" {
							return fmt.Errorf("file path is required")
						}
						if _, err := os.Stat(s); err != nil {
							return fmt.Errorf("file not found: %s", s)
						}
						return nil
					}),
			),
		)
	case "folder":
		inputForm = huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title("Folder path containing .torrent files").
					Placeholder("./torrents/").
					Value(&filePath).
					Validate(func(s string) error {
						if strings.TrimSpace(s) == "" {
							return fmt.Errorf("folder path is required")
						}
						info, err := os.Stat(s)
						if err != nil {
							return fmt.Errorf("path not found: %s", s)
						}
						if !info.IsDir() {
							return fmt.Errorf("%s is not a directory", s)
						}
						return nil
					}),
			),
		)
	}

	if err := inputForm.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Cancelled.\n")
		os.Exit(0)
	}

	// Step 3: Scan options
	optionsForm := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Concurrency (max parallel downloads)").
				Value(&concurrencyStr).
				Validate(func(s string) error {
					n, err := strconv.Atoi(s)
					if err != nil || n < 1 {
						return fmt.Errorf("must be a positive number")
					}
					return nil
				}),
			huh.NewConfirm().
				Title("Enable verbose output?").
				Value(&verbose),
			huh.NewInput().
				Title("Output file (leave empty for auto-generated)").
				Placeholder("results.json").
				Value(&outputFile),
		),
	)

	if err := optionsForm.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Cancelled.\n")
		os.Exit(0)
	}

	// Apply options
	cfg.Concurrency, _ = strconv.Atoi(concurrencyStr)
	cfg.Verbose = verbose
	cfg.OutputFile = outputFile

	// Collect hashes from interactive input
	var hashes []string
	var err error

	switch source {
	case "paste":
		for _, line := range strings.Split(pasteInput, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			resolved, err := internal.NormalizeInput(line)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			hashes = append(hashes, resolved...)
		}
	case "file":
		if strings.HasSuffix(strings.ToLower(filePath), ".torrent") {
			hashes, err = internal.NormalizeInput(filePath)
		} else {
			hashes, err = readAndNormalizeFile(filePath)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "folder":
		hashes, err = internal.NormalizeInput(filePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}

	if len(hashes) == 0 {
		fmt.Fprintln(os.Stderr, "Error: no valid info hashes found")
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "\nFound %d torrent(s). Starting scan...\n\n", len(hashes))
	executeScan(cfg, hashes)
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
	fs.StringVar(&fromFile, "f", "", "Read info hashes/magnets from file (one per line)")
	fs.BoolVar(&fromStdin, "stdin", false, "Read info hashes/magnets from stdin")

	fs.Parse(args)

	// Apply parsed durations
	cfg.StallTimeout = time.Duration(stallSec) * time.Second
	cfg.MaxTimeout = time.Duration(maxSec) * time.Second

	// Collect info hashes from all sources
	var hashes []string

	// From positional args (support hashes, magnets, .torrent files, directories)
	for _, arg := range fs.Args() {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		resolved, err := internal.NormalizeInput(arg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error processing %q: %v\n", arg, err)
			os.Exit(1)
		}
		hashes = append(hashes, resolved...)
	}

	// From file
	if fromFile != "" {
		fileHashes, err := readAndNormalizeFile(fromFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading file %s: %v\n", fromFile, err)
			os.Exit(1)
		}
		hashes = append(hashes, fileHashes...)
	}

	// From stdin
	if fromStdin {
		stdinHashes, err := readAndNormalizeReader(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading stdin: %v\n", err)
			os.Exit(1)
		}
		hashes = append(hashes, stdinHashes...)
	}

	if len(hashes) == 0 {
		// If terminal, offer interactive mode
		if !fromStdin && fromFile == "" && term.IsTerminal(int(os.Stdin.Fd())) {
			fmt.Fprintln(os.Stderr, "No inputs provided. Launching interactive mode...")
			runInteractive()
			return
		}
		fmt.Fprintln(os.Stderr, "Error: no info hashes provided")
		fmt.Fprintln(os.Stderr, "")
		fs.Usage()
		os.Exit(1)
	}

	executeScan(cfg, hashes)
}

func executeScan(cfg internal.Config, hashes []string) {
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

// readAndNormalizeFile reads lines from a file and normalizes each one
// (supports hashes, magnet links, mixed content).
func readAndNormalizeFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readAndNormalizeReader(f)
}

// readAndNormalizeReader reads lines from a reader and normalizes each one.
func readAndNormalizeReader(r *os.File) ([]string, error) {
	var hashes []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		resolved, err := internal.NormalizeInput(line)
		if err != nil {
			return nil, fmt.Errorf("line %q: %w", line, err)
		}
		hashes = append(hashes, resolved...)
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
