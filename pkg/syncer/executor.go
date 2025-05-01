// pkg/syncer/executor.go
package syncer

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/schollz/progressbar/v3"
)

const maxConcurrentOps = 10 // Max number of parallel file operations

// executePlan performs the actions defined in the SyncPlan.
func executePlan(plan *SyncPlan, sourceRoot, targetRoot string, dryRun bool) error {
	if len(plan.Actions) == 0 {
		fmt.Println("No actions needed. Source and target are already in sync.")
		return nil
	}

	// --- Display Plan and Ask for Confirmation ---
	fmt.Println("\n--- Sync Plan ---")
	fmt.Printf("Adds: %d, Updates: %d, Deletes: %d\n", plan.Adds, plan.Updates, plan.Deletes)
	fmt.Println("-----------------")

	// Show sample actions (up to 20)
	limit := 20
	if len(plan.Actions) < limit {
		limit = len(plan.Actions)
	}
	if limit > 0 {
		fmt.Println("Sample actions:")
		for i := 0; i < limit; i++ {
			action := plan.Actions[i]
			actionType := ""
			switch action.Type {
			case Add:
				actionType = "[ADD   ]"
			case Update:
				actionType = "[UPDATE]"
			case Delete:
				actionType = "[DELETE]"
			}
			fmt.Printf("  %s %s\n", actionType, action.RelPath)
		}
		if len(plan.Actions) > limit {
			fmt.Printf("  ... and %d more actions\n", len(plan.Actions)-limit)
		}
		fmt.Println("-----------------")
	}

	if dryRun {
		fmt.Println("Dry run: No changes will be made.")
		return nil // Stop here for dry run
	}

	// Confirmation prompt
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Proceed with synchronization? [Y/n]: ")
	response, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read confirmation: %w", err)
	}
	response = strings.TrimSpace(strings.ToLower(response))

	if response != "" && response != "y" && response != "yes" {
		fmt.Println("Synchronization aborted by user.")
		return nil // User cancelled
	}

	fmt.Println("Starting synchronization...")

	// --- Execute Actions Concurrently ---
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrentOps)   // Semaphore to limit concurrency
	errChan := make(chan error, len(plan.Actions)) // Channel to collect errors

	// Calculate total size for progress bar (approximated for adds/updates)
	var totalSize int64
	for _, action := range plan.Actions {
		if (action.Type == Add || action.Type == Update) && action.SourceInfo != nil && !action.SourceInfo.IsDir {
			totalSize += action.SourceInfo.Size
		}
	}

	bar := progressbar.NewOptions64(totalSize,
		progressbar.OptionSetDescription("Syncing files..."),
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionSetWidth(15),
		progressbar.OptionShowBytes(true),
		progressbar.OptionClearOnFinish(),
		progressbar.OptionThrottle(100*time.Millisecond), // Refresh rate
	)
	defer func() {
		if err := bar.Clear(); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to clear progress bar: %v\n", err)
		}
	}()

	var copyMu sync.Mutex // Mutex for progress bar updates during copy

	for _, action := range plan.Actions {
		wg.Add(1)
		sem <- struct{}{} // Acquire semaphore slot

		go func(act SyncAction) {
			defer wg.Done()
			defer func() { <-sem }() // Release semaphore slot

			var execErr error
			targetPath := filepath.Join(targetRoot, act.RelPath)

			switch act.Type {
			case Add:
				// Ensure parent directory exists in target
				parentDir := filepath.Dir(targetPath)
				if err := os.MkdirAll(parentDir, 0755); err != nil { // Use appropriate permissions
					execErr = fmt.Errorf("failed to create parent directory %s for adding %s: %w", parentDir, act.RelPath, err)
					break
				}
				// Add directory or file
				if act.SourceInfo.IsDir {
					if err := os.Mkdir(targetPath, act.SourceInfo.Mode.Perm()); err != nil { // Use source permissions
						// Ignore error if dir already exists (might happen with concurrent adds)
						if !os.IsExist(err) {
							execErr = fmt.Errorf("failed to create directory %s: %w", act.RelPath, err)
						}
					}
				} else {
					// Add file (copy from source)
					execErr = copyFile(act.SourceInfo.AbsPath, targetPath, act.SourceInfo.Mode.Perm(), act.SourceInfo.ModTime, bar, &copyMu)
					if execErr != nil {
						execErr = fmt.Errorf("failed to copy file for add %s: %w", act.RelPath, execErr)
					}
				}

			case Update:
				// Update file (copy from source, overwriting target)
				// Parent directory should already exist if target file exists
				if act.SourceInfo.IsDir {
					// This case should ideally be handled by delete+add if type changes
					// If types match (both dirs), no action needed here.
					fmt.Fprintf(os.Stderr, "\nWarning: Unexpected 'Update' action for directory: %s\n", act.RelPath)
				} else {
					execErr = copyFile(act.SourceInfo.AbsPath, targetPath, act.SourceInfo.Mode.Perm(), act.SourceInfo.ModTime, bar, &copyMu)
					if execErr != nil {
						execErr = fmt.Errorf("failed to copy file for update %s: %w", act.RelPath, execErr)
					}
				}

			case Delete:
				// Delete file or directory recursively
				// Check if it still exists before attempting deletion
				if _, statErr := os.Lstat(targetPath); statErr == nil {
					if act.TargetInfo != nil && act.TargetInfo.IsDir {
						// Use RemoveAll for directories
						if err := os.RemoveAll(targetPath); err != nil {
							execErr = fmt.Errorf("failed to delete directory %s: %w", act.RelPath, err)
						}
					} else {
						// Use Remove for files or symlinks
						if err := os.Remove(targetPath); err != nil {
							execErr = fmt.Errorf("failed to delete file %s: %w", act.RelPath, err)
						}
					}
				} else if !os.IsNotExist(statErr) {
					// Error stating the file other than not existing
					execErr = fmt.Errorf("failed to stat item for deletion %s: %w", act.RelPath, statErr)
				}
				// If os.IsNotExist(statErr), item is already gone, no error.

			} // end switch

			if execErr != nil {
				errChan <- execErr // Send error to the channel
			}

		}(action) // Pass action by value to the goroutine
	}

	// Wait for all operations to complete
	wg.Wait()
	close(errChan) // Close error channel

	// Check for errors
	var errors []string
	for err := range errChan {
		errors = append(errors, err.Error())
	}

	if len(errors) > 0 {
		// Optionally rollback or provide more detailed error report
		return fmt.Errorf("synchronization finished with %d error(s):\n- %s", len(errors), strings.Join(errors, "\n- "))
	}

	fmt.Println("\nSynchronization finished successfully.")
	return nil
}

// copyFile copies a file from src to dst, sets permissions and mod time, and updates progress bar.
func copyFile(src, dst string, perm os.FileMode, modTime time.Time, bar *progressbar.ProgressBar, barMu *sync.Mutex) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("could not open source %s: %w", src, err)
	}
	defer func() {
		if err := sourceFile.Close(); err != nil {
			fmt.Printf("Error closing %s: %v\n", src, err)
		}
	}()

	// Create or truncate destination file
	destFile, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("could not create/open destination %s: %w", dst, err)
	}
	defer func() {
		if err := destFile.Close(); err != nil {
			fmt.Printf("Error closing %s: %v\n", dst, err)
		}
	}()

	// Create a buffer for copying
	buf := make([]byte, 32*1024) // 32KB buffer

	// Use io.CopyBuffer with progress tracking
	_, err = io.CopyBuffer(destFile, io.TeeReader(sourceFile, &progressWriter{bar: bar, mu: barMu}), buf)
	if err != nil {
		return fmt.Errorf("could not copy data from %s to %s: %w", src, dst, err)
	}

	// Sync file contents to disk
	if err := destFile.Sync(); err != nil {
		// Log warning, but don't necessarily fail the whole operation
		fmt.Fprintf(os.Stderr, "\nWarning: Failed to sync file %s: %v\n", dst, err)
	}

	// Set modification time
	if err := os.Chtimes(dst, modTime, modTime); err != nil {
		// Log warning, as setting time might fail on some systems/filesystems
		fmt.Fprintf(os.Stderr, "\nWarning: Failed to set modification time for %s: %v\n", dst, err)
	}

	// Note: Setting exact permissions after creation might be needed on some OS
	// if os.Chmod(dst, perm) != nil { ... }

	return nil
}

// progressWriter is a helper to update the progress bar during io.Copy
type progressWriter struct {
	bar *progressbar.ProgressBar
	mu  *sync.Mutex // Mutex to protect concurrent bar updates
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	pw.mu.Lock()
	err := pw.bar.Add(n)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nexecutor: Error updating progress bar: %v\n", err)
	}
	pw.mu.Unlock()
	return n, nil
}
