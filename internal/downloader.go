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

	alog "github.com/anacrolix/log"
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
	".mov": true,
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

	// Remove stale piece-completion database from previous runs.
	// The default file storage uses SQLite to track which pieces have been
	// downloaded. If data files were cleaned up but the DB persists, the
	// client may believe pieces are already available, causing file_not_found.
	for _, f := range []string{".torrent.db", ".torrent.db-wal", ".torrent.db-shm"} {
		os.Remove(filepath.Join(cfg.TempDir, f))
	}

	tcfg := torrent.NewDefaultClientConfig()
	tcfg.DataDir = cfg.TempDir
	tcfg.Seed = false
	tcfg.NoUpload = true
	tcfg.ListenPort = 0 // random port
	tcfg.Logger = alog.Default.FilterLevel(alog.Disabled)

	client, err := torrent.NewClient(tcfg)
	if err != nil {
		return nil, fmt.Errorf("create torrent client: %w", err)
	}

	return &Downloader{client: client, cfg: cfg}, nil
}

// GetTorrentStats returns the download and upload bytes for a specific torrent.
// Returns (0, 0) if the torrent is not found or the handle is stale.
func (d *Downloader) GetTorrentStats(infoHash string) (downloaded, uploaded int64) {
	hash := metainfo.NewHashFromHex(infoHash)
	t, ok := d.client.Torrent(hash)
	if !ok {
		return 0, 0
	}
	// The torrent handle may reference a dropped/closed torrent, causing
	// nil-pointer panics when accessing internal state. Recover gracefully.
	defer func() {
		if r := recover(); r != nil {
			downloaded, uploaded = 0, 0
		}
	}()
	stats := t.Stats()
	return stats.ConnStats.BytesReadData.Int64(), stats.ConnStats.BytesWrittenData.Int64()
}

// GetFileList extracts the complete file listing from a torrent's metadata.
// Must be called after metadata has been resolved (after PartialDownload).
// Returns nil if the torrent is not found or the handle is stale.
func (d *Downloader) GetFileList(infoHash string) (result []FileInfo) {
	hash := metainfo.NewHashFromHex(infoHash)
	t, ok := d.client.Torrent(hash)
	if !ok {
		return nil
	}

	// The torrent handle may reference a dropped/closed torrent, causing
	// nil-pointer panics when accessing internal state. Recover gracefully.
	defer func() {
		if r := recover(); r != nil {
			result = nil
		}
	}()

	files := t.Files()
	result = make([]FileInfo, 0, len(files))

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
// Returns nil if the torrent is not found or the handle is stale.
func (d *Downloader) GetSwarmInfo(infoHash string) (result *SwarmInfo) {
	hash := metainfo.NewHashFromHex(infoHash)
	t, ok := d.client.Torrent(hash)
	if !ok {
		return nil
	}

	// The torrent handle may reference a dropped/closed torrent, causing
	// nil-pointer panics when accessing internal state. Recover gracefully.
	defer func() {
		if r := recover(); r != nil {
			result = nil
		}
	}()

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
		DownloadBytesTotal: stats.ConnStats.BytesReadData.Int64(),
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
		return nil, fmt.Errorf("metadata timeout for %s", TruncHash(infoHash))
	}

	// Find largest video file
	videoFile, err := findLargestVideo(t.Files())
	if err != nil {
		return nil, err
	}

	ext := strings.ToLower(filepath.Ext(videoFile.DisplayPath()))

	if d.cfg.Verbose {
		log.Printf("  [%s] found video: %s (%d MB, %s)", TruncHash(infoHash), videoFile.DisplayPath(), videoFile.Length()/1024/1024, ext)
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
				TruncHash(infoHash), fileEndPiece-endStart)
		}
	}

	if d.cfg.Verbose {
		log.Printf("  [%s] need %d pieces (%dKB each) for %dKB",
			TruncHash(infoHash), len(required), pieceLength/1024, minBytes/1024)
	}

	// Set priority on required pieces
	for i := range required {
		t.Piece(i).SetPriority(torrent.PiecePriorityNow)
	}

	// Poll for piece completion with stall detection
	err = d.waitForPieces(ctx, t, infoHash, required)
	if err != nil {
		return nil, err
	}

	filePath, err := d.resolveFilePath(t, videoFile, infoHash)
	if err != nil {
		return nil, err
	}

	return &DownloadResult{
		FilePath: filePath,
		FileName: filepath.Base(videoFile.DisplayPath()),
		Ext:      ext,
	}, nil
}

// resolveFilePath locates the downloaded video file on disk.
// anacrolix/torrent stores files under DataDir using the torrent name and file path,
// but the exact layout varies (single-file vs multi-file, wrapper dirs, .part suffix).
// This method tries multiple candidate paths and falls back to a recursive walk.
func (d *Downloader) resolveFilePath(t *torrent.Torrent, videoFile *torrent.File, infoHash string) (string, error) {
	tName := t.Name()
	vPath := videoFile.Path()
	vDisplay := videoFile.DisplayPath()
	vBase := filepath.Base(vDisplay)

	basePaths := []string{
		filepath.Join(d.cfg.TempDir, tName, vPath),
		filepath.Join(d.cfg.TempDir, tName, vDisplay),
		filepath.Join(d.cfg.TempDir, vPath),
		filepath.Join(d.cfg.TempDir, vDisplay),
		filepath.Join(d.cfg.TempDir, tName, vBase),
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

	// Retry a few times — the storage backend may not have flushed to disk yet.
	filePath := ""
	for attempt := 0; attempt < 3; attempt++ {
		for _, c := range candidates {
			if info, err := os.Stat(c); err == nil && !info.IsDir() {
				filePath = c
				break
			}
		}
		if filePath != "" {
			break
		}

		// Fallback: walk the torrent directory to find the video file by name.
		// Handles wrapper directories (e.g. "www.UIndex.org - <name>/...").
		targetBase := filepath.Base(vDisplay)
		torrentDir := filepath.Join(d.cfg.TempDir, tName)
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

		// For single-file torrents, torrentDir is the file itself — walk TempDir.
		if filePath == "" {
			_ = filepath.WalkDir(d.cfg.TempDir, func(path string, entry fs.DirEntry, err error) error {
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

		if filePath != "" {
			break
		}
		if attempt < 2 {
			time.Sleep(1 * time.Second)
			if d.cfg.Verbose {
				log.Printf("  [%s] file not found on disk, retrying (%d/3)...", TruncHash(infoHash), attempt+2)
			}
		}
	}

	if filePath == "" {
		d.logFileNotFound(infoHash, tName, vPath, vDisplay)
		return "", fmt.Errorf("downloaded file not found on disk for %s", TruncHash(infoHash))
	}

	return filePath, nil
}

// logFileNotFound prints diagnostic info when a downloaded file can't be located.
func (d *Downloader) logFileNotFound(infoHash, tName, vPath, vDisplay string) {
	log.Printf("  [%s] file not found", TruncHash(infoHash))
	log.Printf("    torrent name: %s", tName)
	log.Printf("    video Path(): %s", vPath)
	log.Printf("    video DisplayPath(): %s", vDisplay)
	log.Printf("    temp dir: %s", d.cfg.TempDir)

	torrentDir := filepath.Join(d.cfg.TempDir, tName)
	if entries, err := os.ReadDir(torrentDir); err == nil {
		log.Printf("    files in %s:", torrentDir)
		for _, e := range entries {
			log.Printf("      %s", e.Name())
		}
	} else {
		log.Printf("    cannot list %s: %v", torrentDir, err)
		if entries, err := os.ReadDir(d.cfg.TempDir); err == nil {
			log.Printf("    files in %s:", d.cfg.TempDir)
			for _, e := range entries {
				log.Printf("      %s", e.Name())
			}
		}
	}
}

// RequestMorePieces requests additional pieces for a torrent that's already active.
// Used for ffprobe retry — instead of re-downloading, just request more bytes.
func (d *Downloader) RequestMorePieces(ctx context.Context, infoHash string, minBytes int) error {
	hash := metainfo.NewHashFromHex(infoHash)
	t, ok := d.client.Torrent(hash)
	if !ok {
		return fmt.Errorf("torrent %s not found in client", TruncHash(infoHash))
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
			TruncHash(infoHash), len(required), minBytes/1024)
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
	now := time.Now()
	lastPieceAt := now
	lastBytesAt := now
	lastCompleted := 0
	lastBytes := int64(0)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("max timeout (%s) for %s", d.cfg.MaxTimeout, TruncHash(infoHash))
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

			// Track progress at piece and byte level
			now = time.Now()
			stats := t.Stats()
			bytesNow := stats.ConnStats.BytesReadData.Int64()

			if completed > lastCompleted {
				lastPieceAt = now
				lastCompleted = completed
			}
			if bytesNow > lastBytes {
				lastBytesAt = now
				lastBytes = bytesNow
			}

			// Stall detection: no progress at either level for StallTimeout.
			// This catches both no-peer stalls AND leecher-only swarms where
			// peers are connected but nobody sends data.
			pieceStall := now.Sub(lastPieceAt) > d.cfg.StallTimeout
			byteStall := now.Sub(lastBytesAt) > d.cfg.StallTimeout

			if pieceStall && byteStall {
				return fmt.Errorf("stall: no progress for %s for %s",
					now.Sub(lastPieceAt).Round(time.Second), TruncHash(infoHash))
			}

			if d.cfg.Verbose && completed > 0 {
				log.Printf("  [%s] pieces %d/%d peers=%d",
					TruncHash(infoHash), completed, len(required), stats.ActivePeers)
			}
		}
	}
}

// Cleanup removes a torrent and its downloaded files.
func (d *Downloader) Cleanup(infoHash string) {
	defer func() { recover() }()

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

// FindLocalFile tries to locate a torrent file on disk in the temp directory.
// Returns the local path if found, or empty string if not.
func (d *Downloader) FindLocalFile(infoHash string, filePath string) (result string) {
	hash := metainfo.NewHashFromHex(infoHash)
	t, ok := d.client.Torrent(hash)
	if !ok {
		return ""
	}

	defer func() {
		if r := recover(); r != nil {
			result = ""
		}
	}()

	candidates := []string{
		filepath.Join(d.cfg.TempDir, t.Name(), filePath),
		filepath.Join(d.cfg.TempDir, filePath),
	}

	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
		if info, err := os.Stat(c + ".part"); err == nil && !info.IsDir() {
			return c + ".part"
		}
	}

	return ""
}

// DownloadFullFile downloads a specific file completely from a torrent.
// Returns the local path to the fully downloaded file.
func (d *Downloader) DownloadFullFile(ctx context.Context, infoHash string, filePath string) (localPath string, err error) {
	hash := metainfo.NewHashFromHex(infoHash)
	t, ok := d.client.Torrent(hash)
	if !ok {
		return "", fmt.Errorf("torrent %s not found", TruncHash(infoHash))
	}

	defer func() {
		if r := recover(); r != nil {
			localPath = ""
			err = fmt.Errorf("torrent handle invalid: %v", r)
		}
	}()

	for _, f := range t.Files() {
		dp := f.DisplayPath()
		if dp == filePath || f.Path() == filePath || strings.HasSuffix(dp, filePath) {
			f.Download()

			dlCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()

			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()

			for {
				select {
				case <-dlCtx.Done():
					return "", fmt.Errorf("timeout downloading %s", filePath)
				case <-ticker.C:
					if f.BytesCompleted() >= f.Length() {
						lp := d.FindLocalFile(infoHash, filePath)
						if lp != "" {
							return lp, nil
						}
						return "", fmt.Errorf("file completed but not found on disk")
					}
				}
			}
		}
	}

	return "", fmt.Errorf("file %s not found in torrent", filePath)
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
