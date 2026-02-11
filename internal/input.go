package internal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anacrolix/torrent/metainfo"
)

// NormalizeInput takes a raw input string (info hash, magnet link, .torrent path,
// or directory of .torrent files) and returns the extracted info hashes.
func NormalizeInput(input string) ([]string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, nil
	}

	// Magnet link
	if strings.HasPrefix(input, "magnet:") {
		m, err := metainfo.ParseMagnetUri(input)
		if err != nil {
			return nil, fmt.Errorf("invalid magnet link: %w", err)
		}
		return []string{m.InfoHash.HexString()}, nil
	}

	// Check if it's a file or directory path
	info, err := os.Stat(input)
	if err == nil {
		// It's a directory → collect all .torrent files
		if info.IsDir() {
			return hashesFromTorrentDir(input)
		}
		// It's a .torrent file
		if strings.HasSuffix(strings.ToLower(input), ".torrent") {
			h, err := hashFromTorrentFile(input)
			if err != nil {
				return nil, err
			}
			return []string{h}, nil
		}
	}

	// Assume raw info hash
	return []string{strings.ToLower(input)}, nil
}

func hashFromTorrentFile(path string) (string, error) {
	mi, err := metainfo.LoadFromFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", filepath.Base(path), err)
	}
	return mi.HashInfoBytes().HexString(), nil
}

func hashesFromTorrentDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	var hashes []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".torrent") {
			continue
		}
		h, err := hashFromTorrentFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		hashes = append(hashes, h)
	}

	if len(hashes) == 0 {
		return nil, fmt.Errorf("no .torrent files found in %s", dir)
	}
	return hashes, nil
}
