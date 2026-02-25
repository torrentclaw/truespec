package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
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
	// Must be the first call — may re-exec the process via syscall.Exec,
	// which requires no goroutines or resources to have been initialized.
	ensureClassicFileIO()

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
	case "stats":
		runStatsCmd(os.Args[2:])
	case "config":
		runConfigCmd(os.Args[2:])
	case "version":
		fmt.Printf("truespec %s\n", version)
	case "--help", "-h", "help":
		printUsage()
	case "_worker":
		runWorker()
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
  truespec scan [flags] --pipe
  truespec stats [--json] [--reset]
  truespec config [--show] [--json] [--reset]
  truespec version

Inputs can be info hashes, magnet links, .torrent files, or directories
containing .torrent files.

Commands:
  scan     Partially download torrents and extract verified media metadata
  stats    Display accumulated scan statistics
  config   Configure TrueSpec features (interactive wizard)
  version  Show version

Examples:
  truespec scan abc123def456...
  truespec scan "magnet:?xt=urn:btih:abc123..."
  truespec scan movie.torrent
  truespec scan ./torrents/
  truespec scan -f hashes.txt -o my-results.json
  cat hashes.txt | truespec scan --stdin --verbose
  cat hashes.txt | truespec scan --pipe
  truespec stats
  truespec stats --json
  truespec config
  truespec config --show

Run 'truespec scan --help' for scan-specific flags.
`, version)
}

// ═══════════════════════════════════════════════════════════════════
// CONFIG COMMAND
// ═══════════════════════════════════════════════════════════════════

func runConfigCmd(args []string) {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	var showOnly bool
	var jsonOutput bool
	var reset bool

	fs.BoolVar(&showOnly, "show", false, "Show current configuration without modifying")
	fs.BoolVar(&jsonOutput, "json", false, "Output current configuration as JSON")
	fs.BoolVar(&reset, "reset", false, "Reset configuration to defaults")

	fs.Parse(args)

	if reset {
		cfg := internal.DefaultUserConfig()
		if err := cfg.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "Error resetting config: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "Configuration reset to defaults.")
		return
	}

	if jsonOutput {
		cfg := internal.LoadUserConfig()
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		encoder.Encode(cfg)
		return
	}

	if showOnly {
		cfg := internal.LoadUserConfig()
		fmt.Print(cfg.ShowConfig())
		return
	}

	// Interactive wizard
	runConfigWizard()
}

func runConfigWizard() {
	fmt.Fprintf(os.Stderr, "truespec %s — Configuration\n\n", version)

	// Load existing config (or defaults)
	cfg := internal.LoadUserConfig()

	// ── Section 1: Core features ──
	coreForm := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Track scan statistics?").
				Description("Saves stats (traffic, quality distributions, performance) to ~/.truespec/stats.json").
				Value(&cfg.StatsEnabled),

			huh.NewConfirm().
				Title("Analyze torrent files for threats?").
				Description("Detects dangerous files (.exe, .bat, .dll, etc.) in torrent contents").
				Value(&cfg.ThreatScanEnabled),

			huh.NewConfirm().
				Title("Share anonymous scan results with the community?").
				Description("Helps improve quality data for everyone. No personal info is shared.").
				Value(&cfg.ShareAnonymous),
		).Title("Core Features"),
	)

	if err := coreForm.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Cancelled.\n")
		os.Exit(0)
	}

	// ── Section 2: Language detection ──
	whisperForm := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Detect audio language with Whisper?").
				Description("When audio tracks are marked as 'und' (undefined),\nuse whisper.cpp to detect the spoken language (~2s per track, CPU only).\nAnalyzes up to 3 tracks per torrent (configurable via whisper_max_tracks).\nRequires ~75MB download for the model.").
				Value(&cfg.WhisperEnabled),
		).Title("Language Detection"),
	)

	if err := whisperForm.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Cancelled.\n")
		os.Exit(0)
	}

	// If whisper enabled, download it
	if cfg.WhisperEnabled {
		fmt.Fprintln(os.Stderr, "")
		whisperPath, modelPath, err := internal.DownloadWhisper()
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nWarning: Could not install whisper: %v\n", err)
			fmt.Fprintf(os.Stderr, "Language detection will be disabled. Run 'truespec config' again to retry.\n")
			cfg.WhisperEnabled = false
		} else {
			cfg.WhisperPath = whisperPath
			cfg.WhisperModel = modelPath
			fmt.Fprintf(os.Stderr, "\nWhisper installed successfully!\n")
		}
	}

	// ── Section 3: VirusTotal (optional) ──
	var vtKey string
	if cfg.VirusTotalAPIKey != "" {
		vtKey = cfg.VirusTotalAPIKey
	}

	vtForm := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("VirusTotal API key (optional, press Enter to skip)").
				Description("Used to scan suspicious files found in torrents.\nFree key: https://www.virustotal.com/gui/sign-in → API key").
				Placeholder("paste your API key here").
				Value(&vtKey),
		).Title("VirusTotal Integration"),
	)

	if err := vtForm.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Cancelled.\n")
		os.Exit(0)
	}
	cfg.VirusTotalAPIKey = strings.TrimSpace(vtKey)

	// ── Section 4: Scan defaults ──
	concurrencyStr := strconv.Itoa(cfg.Concurrency)
	stallStr := strconv.Itoa(cfg.StallTimeout)
	maxStr := strconv.Itoa(cfg.MaxTimeout)

	scanForm := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Default concurrency (parallel downloads)").
				Value(&concurrencyStr).
				Validate(func(s string) error {
					n, err := strconv.Atoi(s)
					if err != nil || n < 1 || n > 50 {
						return fmt.Errorf("must be between 1 and 50")
					}
					return nil
				}),
			huh.NewInput().
				Title("Stall timeout (seconds with no progress)").
				Value(&stallStr).
				Validate(func(s string) error {
					n, err := strconv.Atoi(s)
					if err != nil || n < 10 {
						return fmt.Errorf("must be at least 10 seconds")
					}
					return nil
				}),
			huh.NewInput().
				Title("Max timeout per torrent (seconds)").
				Value(&maxStr).
				Validate(func(s string) error {
					n, err := strconv.Atoi(s)
					if err != nil || n < 60 {
						return fmt.Errorf("must be at least 60 seconds")
					}
					return nil
				}),
		).Title("Scan Defaults"),
	)

	if err := scanForm.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Cancelled.\n")
		os.Exit(0)
	}

	cfg.Concurrency, _ = strconv.Atoi(concurrencyStr)
	cfg.StallTimeout, _ = strconv.Atoi(stallStr)
	cfg.MaxTimeout, _ = strconv.Atoi(maxStr)

	// ── Section 5: Output & Logging ──
	verboseLevelStr := strconv.Itoa(cfg.VerboseLevel)
	outputForm := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Default output mode").
				Description("Controls what you see during scans.\n"+
					"Normal: compact progress bar, detailed logs saved to file.\n"+
					"Verbose: all logs printed to terminal.").
				Options(
					huh.NewOption("Normal (progress bar, logs to file)", "0"),
					huh.NewOption("Verbose (all logs to terminal)", "1"),
				).
				Value(&verboseLevelStr),
		).Title("Output & Logging"),
	)

	if err := outputForm.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Cancelled.\n")
		os.Exit(0)
	}
	cfg.VerboseLevel, _ = strconv.Atoi(verboseLevelStr)

	// Mark as configured
	cfg.Configured = true

	// Save
	if err := cfg.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Configuration saved!")
	fmt.Fprintln(os.Stderr, "")
	fmt.Print(cfg.ShowConfig())
}

// ═══════════════════════════════════════════════════════════════════
// STATS COMMAND
// ═══════════════════════════════════════════════════════════════════

func runStatsCmd(args []string) {
	cfg := internal.DefaultConfig()

	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	var jsonOutput bool
	var reset bool
	var statsFile string

	fs.BoolVar(&jsonOutput, "json", false, "Output raw JSON")
	fs.BoolVar(&reset, "reset", false, "Reset all stats")
	fs.StringVar(&statsFile, "file", cfg.StatsFile, "Path to stats file")

	fs.Parse(args)

	if reset {
		s := internal.NewStats()
		if err := s.Save(statsFile); err != nil {
			fmt.Fprintf(os.Stderr, "Error resetting stats: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "Stats reset successfully.")
		return
	}

	s, err := internal.LoadStats(statsFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading stats: %v\n", err)
		os.Exit(1)
	}

	s.Compute()

	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(s); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding stats: %v\n", err)
			os.Exit(1)
		}
		return
	}

	fmt.Print(internal.FormatStats(s))
}

// ═══════════════════════════════════════════════════════════════════
// INTERACTIVE MODE
// ═══════════════════════════════════════════════════════════════════

func runInteractive() {
	fmt.Fprintf(os.Stderr, "truespec %s — Interactive Mode\n\n", version)

	cfg := internal.DefaultConfig()

	// Apply user config
	ucfg := internal.LoadUserConfig()
	ucfg.ApplyToConfig(&cfg)

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
				Description("Shows all logs on terminal instead of saving to file.").
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
	if verbose {
		cfg.VerboseLevel = internal.VerboseVerbose
	}
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

// ═══════════════════════════════════════════════════════════════════
// SCAN COMMAND
// ═══════════════════════════════════════════════════════════════════

func runScan(args []string) {
	cfg := internal.DefaultConfig()

	// Apply user config as base defaults
	ucfg := internal.LoadUserConfig()
	ucfg.ApplyToConfig(&cfg)

	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	fs.IntVar(&cfg.Concurrency, "concurrency", cfg.Concurrency, "Maximum concurrent torrent downloads")
	fs.IntVar(&cfg.Concurrency, "c", cfg.Concurrency, "Maximum concurrent torrent downloads (shorthand)")

	stallSec := int(cfg.StallTimeout / time.Second)
	fs.IntVar(&stallSec, "stall-timeout", stallSec, "Seconds of no progress before killing a torrent")

	maxSec := int(cfg.MaxTimeout / time.Second)
	fs.IntVar(&maxSec, "max-timeout", maxSec, "Absolute maximum seconds per torrent")

	fs.StringVar(&cfg.FFprobePath, "ffprobe", cfg.FFprobePath, "Path to ffprobe binary (auto-detect if empty)")
	fs.StringVar(&cfg.TempDir, "temp-dir", cfg.TempDir, "Temporary directory for downloads")
	var verbose bool
	fs.BoolVar(&verbose, "verbose", false, "Print all logs to stderr (overrides config verbose level)")
	fs.BoolVar(&verbose, "v", false, "Print all logs to stderr (shorthand)")
	fs.StringVar(&cfg.OutputFile, "output", "", "Output JSON file path (default: results_<timestamp>.json)")
	fs.StringVar(&cfg.OutputFile, "o", "", "Output JSON file path (default: results_<timestamp>.json)")
	fs.StringVar(&cfg.StatsFile, "stats-file", cfg.StatsFile, "Path to stats file")

	var fromFile string
	var fromStdin bool
	var pipeMode bool
	var noStats bool
	fs.StringVar(&fromFile, "f", "", "Read info hashes/magnets from file (one per line)")
	fs.BoolVar(&fromStdin, "stdin", false, "Read info hashes/magnets from stdin")
	fs.BoolVar(&pipeMode, "pipe", false, "Pipe mode: read hashes from stdin continuously, emit JSONL results to stdout")
	fs.BoolVar(&noStats, "no-stats", false, "Disable stats tracking for this scan")

	fs.Parse(args)

	// Apply parsed durations (CLI flags override user config)
	cfg.StallTimeout = time.Duration(stallSec) * time.Second
	cfg.MaxTimeout = time.Duration(maxSec) * time.Second

	if noStats {
		cfg.StatsFile = ""
	}

	if verbose {
		cfg.VerboseLevel = internal.VerboseVerbose
	}

	// Validate mutually exclusive flags
	if pipeMode && fromStdin {
		fmt.Fprintln(os.Stderr, "Error: --pipe and --stdin are mutually exclusive")
		os.Exit(1)
	}
	if pipeMode && (fromFile != "" || len(fs.Args()) > 0) {
		fmt.Fprintln(os.Stderr, "Error: --pipe cannot be combined with positional arguments or -f")
		os.Exit(1)
	}

	// Pipe mode: continuous stdin → JSONL stdout (no upfront hash collection needed)
	if pipeMode {
		executePipe(cfg)
		return
	}

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

	logCloser := setupLogging(&cfg)
	if logCloser != nil {
		defer logCloser.Close()
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

	stats := loadStats(cfg.StatsFile)

	// Context with signal handling for graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Progress display for normal mode (started before scan so no results are missed)
	var progress *internal.ProgressDisplay
	if !cfg.IsVerbose() {
		isTTY := term.IsTerminal(int(os.Stderr.Fd()))
		progress = internal.NewProgressDisplay(os.Stderr, len(hashes), isTTY)
		progress.Start()
	}

	// Run scan and collect results (with stats tracking)
	start := time.Now()
	results := internal.ScanWithStats(ctx, cfg, hashes, stats)

	scanStats := map[string]int{}
	var collected []internal.ScanResult

	for result := range results {
		collected = append(collected, result)
		scanStats[result.Status]++

		if progress != nil {
			progress.RecordResult(result.Status)
		}

		log.Printf("  [%d/%d] %s → %s (%dms)",
			len(collected), len(hashes), internal.TruncHash(result.InfoHash), result.Status, result.ElapsedMs)
	}

	if progress != nil {
		progress.Stop()
	}

	elapsed := time.Since(start)

	// Build report
	report := internal.ScanReport{
		Version:   version,
		ScannedAt: time.Now().UTC().Format(time.RFC3339),
		ElapsedMs: elapsed.Milliseconds(),
		Total:     len(collected),
		Stats:     scanStats,
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

	saveStats(stats, cfg.StatsFile)

	// Print summary to stderr (always visible)
	fmt.Fprintf(os.Stderr, "\nScan complete in %s\n", elapsed.Round(time.Millisecond))
	fmt.Fprintf(os.Stderr, "  Total: %d\n", len(collected))
	for status, count := range scanStats {
		fmt.Fprintf(os.Stderr, "  %s: %d\n", status, count)
	}
	fmt.Fprintf(os.Stderr, "  Results saved to: %s\n", cfg.OutputFile)
	if cfg.StatsFile != "" {
		fmt.Fprintf(os.Stderr, "  Stats saved to: %s\n", cfg.StatsFile)
	}
}

// executePipe runs in pipe mode: reads hashes from stdin continuously,
// scans them with the configured concurrency, and emits each ScanResult
// as a JSONL line on stdout as soon as it completes.
// Closing stdin (EOF) signals "no more hashes"; the process finishes
// remaining in-flight workers and exits cleanly.
func executePipe(cfg internal.Config) {
	logCloser := setupLogging(&cfg)
	if logCloser != nil {
		defer logCloser.Close()
	}

	// Resolve ffprobe early so we fail fast
	ffprobePath, err := internal.ResolveFFprobe(cfg.FFprobePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	cfg.FFprobePath = ffprobePath

	log.Printf("truespec %s — pipe mode (concurrency=%d)", version, cfg.Concurrency)
	log.Printf("  stall timeout: %s", cfg.StallTimeout)
	log.Printf("  max timeout: %s", cfg.MaxTimeout)
	log.Printf("  ffprobe: %s", cfg.FFprobePath)
	log.Printf("  temp dir: %s", cfg.TempDir)

	// Startup cleanup
	cleanTempDir(cfg.TempDir)

	stats := loadStats(cfg.StatsFile)

	// Context with signal handling for graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Read hashes from stdin continuously into a channel
	hashes := make(chan string, cfg.Concurrency)
	go func() {
		defer close(hashes)
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			resolved, resolveErr := internal.NormalizeInput(line)
			if resolveErr != nil {
				log.Printf("pipe: skipping invalid input %q: %v", line, resolveErr)
				continue
			}
			for _, h := range resolved {
				select {
				case hashes <- h:
				case <-ctx.Done():
					return
				}
			}
		}
		if scanErr := scanner.Err(); scanErr != nil {
			log.Printf("pipe: stdin read error: %v", scanErr)
		}
	}()

	// Progress display (stderr only, stdout is for JSONL)
	var progress *internal.ProgressDisplay
	if !cfg.IsVerbose() {
		isTTY := term.IsTerminal(int(os.Stderr.Fd()))
		progress = internal.NewProgressDisplay(os.Stderr, 0, isTTY)
		progress.Start()
	}

	// Run scan from channel
	start := time.Now()
	results := internal.ScanFromChannel(ctx, cfg, hashes, stats, 0)

	scanStats := map[string]int{}
	encoder := json.NewEncoder(os.Stdout)
	var total int

	for result := range results {
		total++
		scanStats[result.Status]++

		if progress != nil {
			progress.RecordResult(result.Status)
		}

		// Emit JSONL line to stdout
		if err := encoder.Encode(result); err != nil {
			log.Printf("pipe: failed to encode result for %s: %v", internal.TruncHash(result.InfoHash), err)
		}

		log.Printf("  [%d] %s → %s (%dms)",
			total, internal.TruncHash(result.InfoHash), result.Status, result.ElapsedMs)
	}

	if progress != nil {
		progress.Stop()
	}

	elapsed := time.Since(start)

	// Post-scan cleanup
	cleanTempDir(cfg.TempDir)

	saveStats(stats, cfg.StatsFile)

	// Print summary to stderr
	fmt.Fprintf(os.Stderr, "\nPipe session complete in %s\n", elapsed.Round(time.Millisecond))
	fmt.Fprintf(os.Stderr, "  Total: %d\n", total)
	for status, count := range scanStats {
		fmt.Fprintf(os.Stderr, "  %s: %d\n", status, count)
	}
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
func readAndNormalizeReader(r io.Reader) ([]string, error) {
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

// setupLogging configures log output based on verbose mode.
// Returns a closer for the log file (nil if logging to stderr).
func setupLogging(cfg *internal.Config) io.Closer {
	log.SetFlags(log.Ltime)
	if cfg.IsVerbose() {
		log.SetOutput(os.Stderr)
		cfg.LogWriter = os.Stderr
		return nil
	}
	rlw, err := internal.NewRotatingLogWriter(
		internal.LogDirPath(),
		internal.DefaultLogMaxBytes,
		internal.DefaultLogMaxFiles,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not create log file: %v\n", err)
		log.SetOutput(os.Stderr)
		cfg.LogWriter = os.Stderr
		return nil
	}
	log.SetOutput(rlw)
	cfg.LogWriter = rlw
	return rlw
}

// loadStats loads scan statistics from disk, creating new stats if loading fails.
// Returns nil if statsFile is empty (stats disabled).
func loadStats(statsFile string) *internal.Stats {
	if statsFile == "" {
		return nil
	}
	stats, err := internal.LoadStats(statsFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not load stats: %v\n", err)
		stats = internal.NewStats()
	}
	stats.Version = version
	stats.RecordSession()
	return stats
}

// saveStats persists scan statistics to disk.
func saveStats(stats *internal.Stats, statsFile string) {
	if stats == nil || statsFile == "" {
		return
	}
	stats.PruneOldBuckets()
	stats.Compute()
	if err := stats.Save(statsFile); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not save stats: %v\n", err)
	}
}

// cleanTempDir removes the temp directory and all its contents.
// Errors are logged but not fatal — best-effort cleanup.
func cleanTempDir(dir string) {
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("warning: failed to clean temp dir %s: %v", dir, err)
	}
}

// ensureClassicFileIO prevents SIGBUS crashes from mmap-based piece hashing in
// the anacrolix/torrent storage layer. The library defaults to mmap file I/O
// on Linux, which causes fatal bus errors when files are truncated during
// concurrent piece verification (race between openForWrite truncation and
// pieceHasher reads). Setting TORRENT_STORAGE_DEFAULT_FILE_IO=classic switches
// to os.File-based I/O that handles truncation gracefully (EOF, not SIGBUS).
//
// The env var is read during the storage package's init(), which runs before
// main(), so we must re-exec to ensure it's set in time.
func ensureClassicFileIO() {
	const envKey = "TORRENT_STORAGE_DEFAULT_FILE_IO"

	// If already set (either by user or by a previous re-exec), nothing to do.
	// Invalid values are caught by the library's init() which panics before
	// main() runs, so we only need to check for presence here.
	if _, ok := os.LookupEnv(envKey); ok {
		return
	}

	os.Setenv(envKey, "classic")
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: cannot determine executable path: %v — mmap storage active, SIGBUS risk\n", err)
		return
	}
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: re-exec failed (%v) — mmap storage active, SIGBUS risk\n", err)
	}
}

// runWorker is the entry point for worker subprocesses.
// It reads WorkerInput from stdin, runs the scan, and writes WorkerOutput to stdout.
func runWorker() {
	// Protect stdout from any stray prints by dependencies:
	// save the original fd and redirect os.Stdout to os.Stderr.
	// The result JSON will be written directly to the saved fd.
	originalStdout := os.Stdout
	os.Stdout = os.Stderr

	infoHash := "unknown" // default until input decode; used by panic handler

	// Ensure we write a result even if we panic
	defer func() {
		if r := recover(); r != nil {
			output := internal.WorkerOutput{
				Result: internal.ScanResult{
					InfoHash: infoHash,
					Status:   "worker_error",
					Error:    fmt.Sprintf("panic: %v", r),
				},
			}
			_ = json.NewEncoder(originalStdout).Encode(output)
		}
	}()

	// Read WorkerInput from stdin
	var input internal.WorkerInput
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		// Can't even read input — write minimal error result
		output := internal.WorkerOutput{
			Result: internal.ScanResult{
				InfoHash: "unknown",
				Status:   "worker_error",
				Error:    fmt.Sprintf("decode input: %v", err),
			},
		}
		_ = json.NewEncoder(originalStdout).Encode(output)
		os.Exit(1)
	}
	infoHash = input.InfoHash

	// Workers always log to stderr — the parent process routes their stderr
	// to the appropriate destination (terminal or rotating log file).
	log.SetOutput(os.Stderr)
	log.SetFlags(log.Ltime)

	// Run the worker
	output := internal.RunWorker(input)

	// Write result to the original stdout fd
	if err := json.NewEncoder(originalStdout).Encode(output); err != nil {
		// Can't write output — this is fatal, exit with error
		fmt.Fprintf(os.Stderr, "Error encoding worker output: %v\n", err)
		os.Exit(1)
	}
}
