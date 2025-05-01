// pkg/fileinfo/fileinfo.go
package fileinfo

import (
	"fmt"
	"io/fs"
	"os"
	//"path/filepath"
	"time"
)

// FileInfo holds metadata about a file or directory relevant for syncing.
type FileInfo struct {
	RelPath  string      // Path relative to the source or target root
	AbsPath  string      // Absolute path on the filesystem
	Size     int64       // File size in bytes
	Mode     fs.FileMode // File mode (permissions, type)
	ModTime  time.Time   // Modification time
	IsDir    bool        // True if it's a directory
	Checksum string      // SHA256 checksum (calculated on demand)
}

// New creates a FileInfo struct from fs.FileInfo and paths.
func New(relPath, absPath string, info fs.FileInfo) *FileInfo {
	return &FileInfo{
		RelPath: relPath,
		AbsPath: absPath,
		Size:    info.Size(),
		Mode:    info.Mode(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}
}

// GetInfo retrieves fs.FileInfo for a given absolute path.
// Returns nil, nil if the file does not exist.
func GetInfo(absPath string) (fs.FileInfo, error) {
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // Not an error condition for our purposes, just doesn't exist
		}
		return nil, err // Actual error reading stats
	}
	return info, nil
}

// Exists checks if the file/directory exists.
func (fi *FileInfo) Exists() bool {
	// Check if the underlying file info exists
	_, err := os.Stat(fi.AbsPath)
	return err == nil || !os.IsNotExist(err) // True if stat succeeds or error is something other than NotExist
}

// IsSymlink checks if the file is a symbolic link.
func (fi *FileInfo) IsSymlink() bool {
	return fi.Mode&fs.ModeSymlink != 0
}

// NeedsUpdate checks if the target file needs to be updated from the source file.
// It compares ModTime, Size, and optionally Checksum.
func (fi *FileInfo) NeedsUpdate(targetFi *FileInfo, calculateChecksum func(path string) (string, error)) (bool, error) {
	if fi.IsDir != targetFi.IsDir {
		return true, nil // Type mismatch always needs update (will involve delete + add)
	}
	if fi.IsDir {
		return false, nil // Directories themselves don't "update" based on content time/size
	}

	// Compare ModTime and Size first (common cases)
	// Use a tolerance for ModTime comparison across different filesystems/clocks
	// Note: Some systems have low-resolution timestamps. A small tolerance helps.
	// Go's time comparison is exact, so we check if they are *not* equal.
	// We truncate to second precision as sub-second precision varies wildly.
	timeDiffers := fi.ModTime.Truncate(time.Second) != targetFi.ModTime.Truncate(time.Second)
	sizeDiffers := fi.Size != targetFi.Size

	if sizeDiffers {
		return true, nil // Different size always means update
	}

	if timeDiffers {
		// Same size, different time: Need checksum verification
		sourceChecksum, err := calculateChecksum(fi.AbsPath)
		if err != nil {
			return false, fmt.Errorf("failed to calculate checksum for source %s: %w", fi.RelPath, err)
		}
		targetChecksum, err := calculateChecksum(targetFi.AbsPath)
		if err != nil {
			// If target checksum fails (e.g., file gone missing), assume update needed
			if os.IsNotExist(err) {
				return true, nil
			}
			return false, fmt.Errorf("failed to calculate checksum for target %s: %w", targetFi.RelPath, err)
		}
		return sourceChecksum != targetChecksum, nil
	}

	// Same size, same time (within tolerance): Assume no update needed
	return false, nil
}
