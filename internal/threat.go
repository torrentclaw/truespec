package internal

import (
	"path/filepath"
	"strings"
)

// Dangerous extensions: direct executables and scripts.
var dangerousExts = map[string]string{
	".exe": "Windows executable",
	".msi": "Windows installer",
	".bat": "Windows batch script",
	".cmd": "Windows command script",
	".com": "DOS executable",
	".scr": "Windows screensaver (executable)",
	".pif": "Program Information File (executable)",
	".lnk": "Windows shortcut (can execute commands)",
	".vbs": "VBScript",
	".vbe": "Encoded VBScript",
	".jse": "Encoded JScript",
	".wsf": "Windows Script File",
	".wsh": "Windows Script Host settings",
	".ps1": "PowerShell script",
	".psm1": "PowerShell module",
	".psd1": "PowerShell data file",
	".reg": "Windows Registry file",
	".inf": "Setup Information file",
	".cpl": "Control Panel extension",
	".hta": "HTML Application (executable)",
	".dll": "Dynamic Link Library",
	".sys": "System driver file",
	".drv": "Device driver",
	".ocx": "ActiveX control",
}

// Warning extensions: archives that could contain executables.
var warningExts = map[string]string{
	".zip":  "Archive (may contain executables)",
	".rar":  "Archive (may contain executables)",
	".7z":   "Archive (may contain executables)",
	".cab":  "Windows Cabinet archive",
	".iso":  "Disk image (may auto-run)",
	".img":  "Disk image",
	".dmg":  "macOS disk image",
	".apk":  "Android package",
	".deb":  "Debian package",
	".rpm":  "RPM package",
	".appimage": "Linux AppImage",
	".js":       "JavaScript file (review if unexpected)",
}

// Known safe extensions for media torrents.
var videoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".m4v": true,
	".wmv": true, ".ts": true, ".mov": true, ".flv": true,
	".webm": true, ".mpg": true, ".mpeg": true, ".m2ts": true,
	".vob": true, ".ogv": true, ".divx": true, ".3gp": true,
}

var audioExts = map[string]bool{
	".mp3": true, ".flac": true, ".aac": true, ".ogg": true,
	".wav": true, ".wma": true, ".m4a": true, ".opus": true,
	".ac3": true, ".dts": true, ".eac3": true, ".mka": true,
	".ape": true, ".alac": true, ".aiff": true,
}

var subtitleExts = map[string]bool{
	".srt": true, ".ass": true, ".ssa": true, ".sub": true,
	".idx": true, ".sup": true, ".vtt": true, ".smi": true,
}

var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
	".bmp": true, ".webp": true, ".tiff": true, ".ico": true,
}

// Safe non-media files commonly found in torrents.
var safeExts = map[string]bool{
	".nfo": true, ".txt": true, ".nzb": true, ".sfv": true,
	".md5": true, ".sha1": true, ".sha256": true, ".log": true,
	".cue": true, ".url": true, ".html": true, ".htm": true,
	".pdf": true, ".md": true, ".rtf": true, ".xml": true,
}

// AnalyzeFiles categorizes torrent files and detects threats.
func AnalyzeFiles(files []FileInfo) *TorrentFiles {
	tf := &TorrentFiles{
		Total:      len(files),
		VideoFiles: []FileInfo{},
		AudioFiles: []FileInfo{},
		SubFiles:   []FileInfo{},
		ImageFiles: []FileInfo{},
		OtherFiles: []FileInfo{},
		Suspicious: []FileInfo{},
	}

	hasDangerous := false
	hasWarning := false

	for _, f := range files {
		tf.TotalSize += f.Size
		ext := strings.ToLower(f.Ext)

		// Check dangerous first
		if reason, ok := dangerousExts[ext]; ok {
			f.Reason = reason
			tf.Suspicious = append(tf.Suspicious, f)
			hasDangerous = true
			continue
		}

		// Check warning
		if reason, ok := warningExts[ext]; ok {
			f.Reason = reason
			tf.Suspicious = append(tf.Suspicious, f)
			hasWarning = true
			continue
		}

		// Check for double extensions (e.g., "movie.avi.exe" → already caught above)
		// But also check patterns like "movie.mp4.lnk" where outer ext is dangerous
		baseName := strings.ToLower(filepath.Base(f.Path))
		if hasSuspiciousPattern(baseName) && f.Reason == "" {
			f.Reason = "Suspicious filename pattern"
			tf.Suspicious = append(tf.Suspicious, f)
			hasDangerous = true
			continue
		}

		// Categorize safe files
		switch {
		case videoExts[ext]:
			tf.VideoFiles = append(tf.VideoFiles, f)
		case audioExts[ext]:
			tf.AudioFiles = append(tf.AudioFiles, f)
		case subtitleExts[ext]:
			tf.SubFiles = append(tf.SubFiles, f)
		case imageExts[ext]:
			tf.ImageFiles = append(tf.ImageFiles, f)
		default:
			tf.OtherFiles = append(tf.OtherFiles, f)
		}
	}

	// Determine threat level
	switch {
	case hasDangerous:
		tf.ThreatLevel = "dangerous"
	case hasWarning:
		tf.ThreatLevel = "warning"
	default:
		tf.ThreatLevel = "clean"
	}

	return tf
}

// hasSuspiciousPattern checks for known malicious naming patterns.
func hasSuspiciousPattern(name string) bool {
	// Double extension trick: "video.mp4.exe" (but .exe already caught)
	// Check for hidden executables with Unicode tricks or unusual patterns
	parts := strings.Split(name, ".")
	if len(parts) >= 3 {
		// e.g., "movie.avi.scr" — the final extension would be caught by dangerousExts
		// But check if there's a media extension followed by another extension
		secondToLast := "." + parts[len(parts)-2]
		if videoExts[secondToLast] || audioExts[secondToLast] {
			// Has a media extension before the final extension — suspicious
			lastExt := "." + parts[len(parts)-1]
			if !safeExts[lastExt] && !videoExts[lastExt] && !audioExts[lastExt] &&
				!subtitleExts[lastExt] && !imageExts[lastExt] {
				return true
			}
		}
	}

	// Extremely long filenames (buffer overflow attempts)
	if len(name) > 255 {
		return true
	}

	return false
}
