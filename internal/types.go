package internal

// ScanReport is the top-level output written to the results JSON file.
type ScanReport struct {
	Version   string         `json:"version"`
	ScannedAt string         `json:"scanned_at"` // ISO 8601
	ElapsedMs int64          `json:"elapsed_ms"`
	Total     int            `json:"total"`
	Stats     map[string]int `json:"stats"`
	Results   []ScanResult   `json:"results"`
}

// ScanResult is the output for a single torrent scan.
// All fields are always present (null/empty for missing data, never omitted).
type ScanResult struct {
	InfoHash  string          `json:"info_hash"`
	Status    string          `json:"status"` // success, stall_metadata, stall_download, no_video, ffprobe_failed, timeout, error
	File      string          `json:"file"`
	Audio     []AudioTrack    `json:"audio"`
	Subtitles []SubtitleTrack `json:"subtitles"`
	Video     *VideoInfo      `json:"video"`
	Languages []string        `json:"languages"`
	ElapsedMs int64           `json:"elapsed_ms"`
	Error     string          `json:"error"`

	// File listing & threat analysis
	Files *TorrentFiles `json:"files,omitempty"`

	// Swarm health at time of scan
	Swarm *SwarmInfo `json:"swarm,omitempty"`
}

// AudioTrack represents a single audio stream extracted by ffprobe.
type AudioTrack struct {
	Lang     string `json:"lang"`
	Codec    string `json:"codec"`
	Channels int    `json:"channels"`
	Title    string `json:"title"`
	Default  bool   `json:"default"`
}

// SubtitleTrack represents a single subtitle stream extracted by ffprobe.
type SubtitleTrack struct {
	Lang    string `json:"lang"`
	Codec   string `json:"codec"`
	Title   string `json:"title"`
	Forced  bool   `json:"forced"`
	Default bool   `json:"default"`
}

// VideoInfo represents the primary video stream metadata.
type VideoInfo struct {
	Codec     string  `json:"codec"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	BitDepth  int     `json:"bitDepth"`
	HDR       string  `json:"hdr"`       // HDR10, HLG, DV, DV+HDR10, "" if SDR
	FrameRate float64 `json:"frameRate"` // e.g. 23.976
	Profile   string  `json:"profile"`   // e.g. "Main 10", "High"
}

// TorrentFiles contains the complete file listing of a torrent with threat analysis.
type TorrentFiles struct {
	Total       int        `json:"total"`
	TotalSize   int64      `json:"total_size"`
	VideoFiles  []FileInfo `json:"video_files"`
	AudioFiles  []FileInfo `json:"audio_files"`
	SubFiles    []FileInfo `json:"sub_files"`
	ImageFiles  []FileInfo `json:"image_files"`
	OtherFiles  []FileInfo `json:"other_files"`
	Suspicious  []FileInfo `json:"suspicious"`
	ThreatLevel string     `json:"threat_level"` // clean, warning, dangerous
}

// FileInfo represents a single file within a torrent.
type FileInfo struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Ext    string `json:"ext"`
	Reason string `json:"reason,omitempty"` // why it's suspicious
}

// SwarmInfo contains live peer/seeder data from the BitTorrent swarm.
type SwarmInfo struct {
	ActivePeers int   `json:"active_peers"`
	TotalPeers  int   `json:"total_peers"`
	Seeds       int   `json:"seeds"`
	DownloadBps int64 `json:"download_bps"` // bytes per second at snapshot
	UploadBps   int64 `json:"upload_bps"`
}
