// pkg/syncer/scanner.go
package syncer

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/jeepinbird/sync-dir/pkg/fileinfo"
	"github.com/jeepinbird/sync-dir/pkg/ignore"
	"github.com/schollz/progressbar/v3"
)

// scanDirectory concurrently scans a directory and returns a map of relative paths to FileInfo.
// It respects ignore patterns and shows a progress bar.
func scanDirectory(dirPath string, rootPath string, ignoreMatcher *ignore.Matcher, description string) (map[string]*fileinfo.FileInfo, error) {
	results := make(map[string]*fileinfo.FileInfo)
	var mu sync.Mutex // Mutex to protect access to the results map
	var wg sync.WaitGroup
	errChan := make(chan error, 1) // Buffered channel to report the first error

	// --- Progress Bar Setup ---
	// We don't know the total number of files beforehand easily without a full walk first.
	// We can use a spinner-style progress bar.
	bar := progressbar.NewOptions(-1, // Use -1 for an indeterminate progress bar (spinner)
		progressbar.OptionSetDescription(fmt.Sprintf("Scanning %s...", description)),
		progressbar.OptionSetWriter(os.Stderr), // Write progress to stderr
		progressbar.OptionSpinnerType(14),      // Choose a spinner type
		progressbar.OptionSetWidth(15),
		progressbar.OptionClearOnFinish(),
		progressbar.OptionShowCount(), // Show the count of items processed
	)
	// Ensure the bar is cleaned up and handle potential errors
	defer func() {
		if err := bar.Finish(); err != nil {
			fmt.Fprintf(os.Stderr, "Error finishing progress bar: %v\n", err)
		}
	}()

	// --- Walk the Directory ---
	walkErr := filepath.WalkDir(dirPath, func(absPath string, d fs.DirEntry, err error) error {
		// Handle potential errors during walk (e.g., permission denied)
		if err != nil {
			// Log the error but continue walking if possible
			fmt.Fprintf(os.Stderr, "\nWarning: Error accessing %s: %v\n", absPath, err)
			// If it's a directory we can't read, skip its contents
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil // Continue walking other parts
		}

		// Calculate relative path
		relPath, err := filepath.Rel(rootPath, absPath)
		if err != nil {
			// This should ideally not happen if rootPath is an ancestor of absPath
			select {
			case errChan <- fmt.Errorf("failed to get relative path for %s: %w", absPath, err):
			default: // Don't block if channel is full
			}
			return err // Stop walking this branch on critical error
		}

		// Skip the root directory itself (relPath == ".")
		if relPath == "." {
			return nil
		}

		// --- Check Ignore Rules ---
		// Always ignore the .sync-ignore file itself if scanning source
		if dirPath == rootPath && filepath.Base(absPath) == ignore.IgnoreFileName {
			return nil
		}
		// Check against compiled patterns
		if ignoreMatcher != nil && ignoreMatcher.Matches(relPath) {
			fmt.Fprintf(os.Stderr, "\nIgnoring: %s\n", relPath) // Log ignored paths
			// If it's a directory, skip its contents entirely
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil // Skip this file
		}

		// --- Process File/Directory ---
		wg.Add(1)
		go func(currentAbsPath string, currentRelPath string, entry fs.DirEntry) {
			defer wg.Done()
			err := bar.Add(1)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\nscanner: Error updating progress bar: %v\n", err)
			}

			info, err := entry.Info()
			if err != nil {
				// Log error getting file info, but continue
				fmt.Fprintf(os.Stderr, "\nWarning: Could not get info for %s: %v\n", currentAbsPath, err)
				return // Skip this item
			}

			// Create FileInfo struct
			fi := fileinfo.New(currentRelPath, currentAbsPath, info)

			// Store the result
			mu.Lock()
			results[currentRelPath] = fi
			mu.Unlock()

		}(absPath, relPath, d) // Pass copies of loop variables

		return nil // Continue walking
	})

	// Wait for all goroutines launched inside WalkDir to finish
	wg.Wait()
	close(errChan) // Close channel once walking and processing are done

	// Check for the first error reported during path calculation or walking
	if walkErr != nil {
		return nil, fmt.Errorf("error during directory walk for %s: %w", description, walkErr)
	}
	if err := <-errChan; err != nil {
		return nil, fmt.Errorf("error during file processing for %s: %w", description, err)
	}

	fmt.Fprintf(os.Stderr, "\nFinished scanning %s. Found %d items.\n", description, len(results))
	return results, nil
}
