package internal

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

// Public trackers for magnet resolution.
var defaultTrackers = []string{
	"udp://tracker.opentrackr.org:1337/announce",
	"udp://open.stealth.si:80/announce",
	"udp://tracker.torrent.eu.org:451/announce",
	"udp://open.demonii.com:1337/announce",
	"udp://exodus.desync.com:6969/announce",
}

var videoExtensions = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true,
	".m4v": true, ".wmv": true, ".ts": true,
}

// mp4Extensions are formats where the moov atom may be at the end of the file.
var mp4Extensions = map[string]bool{
	".mp4": true, ".m4v": true, ".mov": true,
}

// DownloadConfig holds settings for the BitTorrent downloader.
type DownloadConfig struct {
	TempDir      string
	StallTimeout time.Duration
	MaxTimeout   time.Duration
	Verbose      bool
	MinBytesMKV  int
	MinBytesMP4  int
}

// Downloader manages a BitTorrent client for partial torrent downloads.
type Downloader struct {
	client *torrent.Client
	cfg    DownloadConfig
}

// DownloadResult holds the outcome of a partial download.
type DownloadResult struct {
	FilePath string
	FileName string
	Ext      string
}

// NewDownloader creates a new BitTorrent downloader.
func NewDownloader(cfg DownloadConfig) (*Downloader, error) {
	if err := os.MkdirAll(cfg.TempDir, 0o755); err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}

	tcfg := torrent.NewDefaultClientConfig()
	tcfg.DataDir = cfg.TempDir
	tcfg.Seed = false
	tcfg.NoUpload = true
	tcfg.ListenPort = 0 // random port

	client, err := torrent.NewClient(tcfg)
	if err != nil {
		return nil, fmt.Errorf("create torrent client: %w", err)
	}

	return &Downloader{client: client, cfg: cfg}, nil
}

// GetTorrentStats returns the download and upload bytes for a specific torrent.
// Returns (0, 0) if the torrent is not found.
func (d *Downloader) GetTorrentStats(infoHash string) (downloaded, uploaded int64) {
	hash := metainfo.NewHashFromHex(infoHash)
	t, ok := d.client.Torrent(hash)
	if !ok {
		return 0, 0
	}
	stats := t.Stats()
	return stats.ConnStats.BytesReadData.Int64(), stats.ConnStats.BytesWrittenData.Int64()
}

// GetClientStats returns the total download and upload bytes for the entire client.
func (d *Downloader) GetClientStats() (downloaded, uploaded int64) {
	stats := d.client.Stats()
	return stats.BytesRead.Int64(), stats.BytesWritten.Int64()
}

// GetFileList extracts the complete file listing from a torrent's metadata.
// Must be called after metadata has been resolved (after PartialDownload).
func (d *Downloader) GetFileList(infoHash string) []FileInfo {
	hash := metainfo.NewHashFromHex(infoHash)
	t, ok := d.client.Torrent(hash)
	if !ok {
		return nil
	}

	files := t.Files()
	result := make([]FileInfo, 0, len(files))

	for _, f := range files {
		path := f.DisplayPath()
		ext := strings.ToLower(filepath.Ext(path))
		result = append(result, FileInfo{
			Path: path,
			Size: f.Length(),
			Ext:  ext,
		})
	}

	return result
}

// GetSwarmInfo captures a snapshot of the torrent's swarm health.
// Must be called while the torrent is still active (before Cleanup).
func (d *Downloader) GetSwarmInfo(infoHash string) *SwarmInfo {
	hash := metainfo.NewHashFromHex(infoHash)
	t, ok := d.client.Torrent(hash)
	if !ok {
		return nil
	}

	stats := t.Stats()

	// Count seeders: peers that have 100% of pieces
	seeds := 0
	for _, pc := range t.PeerConns() {
		if int(pc.PeerPieces().GetCardinality()) >= t.NumPieces() {
			seeds++
		}
	}

	return &SwarmInfo{
		ActivePeers:        stats.ActivePeers,
		TotalPeers:         stats.TotalPeers,
		Seeds:              seeds,
		DownloadBytesTotal: stats.ConnStats.BytesReadData.Int64(), // cumulative, caller can compute rate
		UploadBytesTotal:   stats.ConnStats.BytesWrittenData.Int64(),
	}
}

// PartialDownload downloads the first (and last for MP4) bytes of the largest video file.
// Returns the download result with file path and metadata.
// The minBytes parameter controls how many bytes from the start to download.
// For MP4 files, it also downloads the last minBytes to catch the moov atom.
func (d *Downloader) PartialDownload(ctx context.Context, infoHash string, minBytes int) (*DownloadResult, error) {
	magnet := buildMagnet(infoHash)

	t, err := d.client.AddMagnet(magnet)
	if err != nil {
		return nil, fmt.Errorf("add magnet: %w", err)
	}

	// Wait for metadata with timeout
	metaCtx, metaCancel := context.WithTimeout(ctx, d.cfg.StallTimeout)
	defer metaCancel()

	select {
	case <-t.GotInfo():
		// Metadata resolved
	case <-metaCtx.Done():
		t.Drop()
		return nil, fmt.Errorf("metadata timeout for %s", infoHash[:8])
	}

	// Find largest video file
	videoFile, err := findLargestVideo(t.Files())
	if err != nil {
		t.Drop()
		return nil, err
	}

	ext := strings.ToLower(filepath.Ext(videoFile.DisplayPath()))

	if d.cfg.Verbose {
		log.Printf("  [%s] found video: %s (%d MB, %s)", infoHash[:8], videoFile.DisplayPath(), videoFile.Length()/1024/1024, ext)
	}

	// Calculate required pieces
	pieceLength := int(t.Info().PieceLength)
	fileStartPiece := videoFile.BeginPieceIndex()
	fileEndPiece := videoFile.EndPieceIndex() // exclusive

	// Pieces from the start of the file
	startPiecesNeeded := (minBytes + pieceLength - 1) / pieceLength
	startEnd := fileStartPiece + startPiecesNeeded
	if startEnd > fileEndPiece {
		startEnd = fileEndPiece
	}

	// Build list of required piece indices
	required := make(map[int]bool)
	for i := fileStartPiece; i < startEnd; i++ {
		required[i] = true
	}

	// For MP4/M4V: also download pieces from the END of the file (moov atom)
	if mp4Extensions[ext] {
		endPiecesNeeded := (minBytes + pieceLength - 1) / pieceLength
		endStart := fileEndPiece - endPiecesNeeded
		if endStart < startEnd {
			endStart = startEnd // avoid overlap
		}
		for i := endStart; i < fileEndPiece; i++ {
			required[i] = true
		}
		if d.cfg.Verbose {
			log.Printf("  [%s] MP4 detected: also requesting last %d pieces (moov atom)",
				infoHash[:8], fileEndPiece-endStart)
		}
	}

	if d.cfg.Verbose {
		log.Printf("  [%s] need %d pieces (%dKB each) for %dKB",
			infoHash[:8], len(required), pieceLength/1024, minBytes/1024)
	}

	// Set priority on required pieces
	for i := range required {
		t.Piece(i).SetPriority(torrent.PiecePriorityNow)
	}

	// Poll for piece completion with stall detection
	err = d.waitForPieces(ctx, t, infoHash, required)
	if err != nil {
		t.Drop()
		return nil, err
	}

	// Build the local file path.
	// anacrolix/torrent stores multi-file torrents under DataDir/<torrent_name>/<file_path>.
	// IMPORTANT: Incomplete files get a ".part" suffix from anacrolix/torrent.
	// File.Path() returns path components within the torrent — for multi-file torrents
	// it sometimes includes t.Name() as a prefix, causing duplicated directories when
	// combined with t.Name() again.
	tName := t.Name()
	vPath := videoFile.Path()
	vDisplay := videoFile.DisplayPath()
	vBase := filepath.Base(vDisplay)

	basePaths := []string{
		filepath.Join(d.cfg.TempDir, tName, vPath),
		filepath.Join(d.cfg.TempDir, tName, vDisplay),
		filepath.Join(d.cfg.TempDir, vPath),
		filepath.Join(d.cfg.TempDir, vDisplay),
		filepath.Join(d.cfg.TempDir, tName, vBase), // basename only, no internal path components
		filepath.Join(d.cfg.TempDir, tName),
	}

	// If Path()/DisplayPath() include t.Name() as prefix, add deduplicated candidates
	// first to avoid TempDir/name/name/file double-nesting.
	if strings.HasPrefix(vPath, tName+"/") {
		rel := vPath[len(tName)+1:]
		basePaths = append([]string{filepath.Join(d.cfg.TempDir, tName, rel)}, basePaths...)
	}
	if vDisplay != vPath && strings.HasPrefix(vDisplay, tName+"/") {
		rel := vDisplay[len(tName)+1:]
		basePaths = append([]string{filepath.Join(d.cfg.TempDir, tName, rel)}, basePaths...)
	}

	// For each base path, also try with ".part" suffix (incomplete files)
	var candidates []string
	for _, p := range basePaths {
		candidates = append(candidates, p, p+".part")
	}

	filePath := ""
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			filePath = c
			break
		}
	}

	// Fallback: walk the torrent directory to find the video file by name.
	// Handles torrents with wrapper directories (e.g. "www.UIndex.org    -    <name>/...")
	// where the static candidate paths don't match the actual nested structure.
	if filePath == "" {
		targetBase := filepath.Base(videoFile.DisplayPath())
		torrentDir := filepath.Join(d.cfg.TempDir, t.Name())
		_ = filepath.WalkDir(torrentDir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return nil
			}
			base := filepath.Base(path)
			if base == targetBase || base == targetBase+".part" {
				filePath = path
				return filepath.SkipAll
			}
			return nil
		})
	}

	if filePath == "" {
		log.Printf("  [%s] file not found", infoHash[:8])
		log.Printf("    torrent name: %s", tName)
		log.Printf("    video Path(): %s", vPath)
		log.Printf("    video DisplayPath(): %s", vDisplay)
		log.Printf("    temp dir: %s", d.cfg.TempDir)
		if d.cfg.Verbose {
			torrentDir := filepath.Join(d.cfg.TempDir, tName)
			if entries, err := os.ReadDir(torrentDir); err == nil {
				log.Printf("    files in %s:", torrentDir)
				for _, e := range entries {
					log.Printf("      %s", e.Name())
				}
			} else {
				log.Printf("    cannot list %s: %v", torrentDir, err)
			}
		}
		t.Drop()
		return nil, fmt.Errorf("downloaded file not found on disk for %s", infoHash[:8])
	}

	return &DownloadResult{
		FilePath: filePath,
		FileName: filepath.Base(videoFile.DisplayPath()),
		Ext:      ext,
	}, nil
}

// RequestMorePieces requests additional pieces for a torrent that's already active.
// Used for ffprobe retry — instead of re-downloading, just request more bytes.
func (d *Downloader) RequestMorePieces(ctx context.Context, infoHash string, minBytes int) error {
	hash := metainfo.NewHashFromHex(infoHash)
	t, ok := d.client.Torrent(hash)
	if !ok {
		return fmt.Errorf("torrent %s not found in client", infoHash[:8])
	}

	videoFile, err := findLargestVideo(t.Files())
	if err != nil {
		return err
	}

	ext := strings.ToLower(filepath.Ext(videoFile.DisplayPath()))
	pieceLength := int(t.Info().PieceLength)
	fileStartPiece := videoFile.BeginPieceIndex()
	fileEndPiece := videoFile.EndPieceIndex()

	startPiecesNeeded := (minBytes + pieceLength - 1) / pieceLength
	startEnd := fileStartPiece + startPiecesNeeded
	if startEnd > fileEndPiece {
		startEnd = fileEndPiece
	}

	required := make(map[int]bool)
	for i := fileStartPiece; i < startEnd; i++ {
		required[i] = true
	}

	// Also request end pieces for MP4
	if mp4Extensions[ext] {
		endPiecesNeeded := (minBytes + pieceLength - 1) / pieceLength
		endStart := fileEndPiece - endPiecesNeeded
		if endStart < startEnd {
			endStart = startEnd
		}
		for i := endStart; i < fileEndPiece; i++ {
			required[i] = true
		}
	}

	if d.cfg.Verbose {
		log.Printf("  [%s] requesting %d more pieces for %dKB retry",
			infoHash[:8], len(required), minBytes/1024)
	}

	for i := range required {
		t.Piece(i).SetPriority(torrent.PiecePriorityNow)
	}

	return d.waitForPieces(ctx, t, infoHash, required)
}

// waitForPieces polls until all required pieces are complete or a timeout/stall occurs.
func (d *Downloader) waitForPieces(ctx context.Context, t *torrent.Torrent, infoHash string, required map[int]bool) error {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	deadline := time.After(d.cfg.MaxTimeout)
	lastProgressAt := time.Now()
	lastCompleted := 0

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("max timeout (%s) for %s", d.cfg.MaxTimeout, infoHash[:8])
		case <-ticker.C:
			// Check piece completion
			allComplete := true
			completed := 0
			for i := range required {
				if t.Piece(i).State().Complete {
					completed++
				} else {
					allComplete = false
				}
			}

			if allComplete {
				return nil
			}

			// Track progress
			now := time.Now()
			if completed > lastCompleted {
				lastProgressAt = now
				lastCompleted = completed
			}

			// Stall detection: no progress for StallTimeout and no peers
			stats := t.Stats()
			hasPeers := stats.ActivePeers > 0
			stallDuration := now.Sub(lastProgressAt)

			if stallDuration > d.cfg.StallTimeout && !hasPeers {
				return fmt.Errorf("stall: no progress for %s and no peers for %s",
					stallDuration.Round(time.Second), infoHash[:8])
			}

			if d.cfg.Verbose && completed > 0 {
				log.Printf("  [%s] pieces %d/%d peers=%d",
					infoHash[:8], completed, len(required), stats.ActivePeers)
			}
		}
	}
}

// Cleanup removes a torrent and its downloaded files.
func (d *Downloader) Cleanup(infoHash string) {
	hash := metainfo.NewHashFromHex(infoHash)
	if t, ok := d.client.Torrent(hash); ok {
		name := t.Name()
		t.Drop()
		// Remove downloaded files
		dir := filepath.Join(d.cfg.TempDir, name)
		os.RemoveAll(dir)
		// Also try the file directly (single-file torrents)
		os.Remove(filepath.Join(d.cfg.TempDir, name))
	}
}

// Close shuts down the BitTorrent client.
func (d *Downloader) Close() {
	d.client.Close()
}

func buildMagnet(infoHash string) string {
	params := []string{"xt=urn:btih:" + infoHash}
	for _, tracker := range defaultTrackers {
		params = append(params, "tr="+url.QueryEscape(tracker))
	}
	return "magnet:?" + strings.Join(params, "&")
}

func findLargestVideo(files []*torrent.File) (*torrent.File, error) {
	var best *torrent.File
	var bestSize int64

	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f.DisplayPath()))
		if videoExtensions[ext] && f.Length() > bestSize {
			best = f
			bestSize = f.Length()
		}
	}

	if best == nil {
		return nil, fmt.Errorf("no video file found in torrent")
	}
	return best, nil
}
