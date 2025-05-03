// pkg/syncer/executor.go
package syncer

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jeepinbird/sync-dir/pkg/progress"
)

const maxConcurrentOps = 10 // Max number of parallel file operations

// executePlan performs the actions defined in the SyncPlan.
func executePlan(plan *SyncPlan, sourceRoot, targetRoot string, dryRun bool) error {
	logger := slog.Default()

	if len(plan.Actions) == 0 {
		logger.Info("No actions needed. Source and target are already in sync.")
		return nil
	}

	// --- Display Plan and Ask for Confirmation ---
	logger.Info("\n--- Sync Plan ---")
	logger.Info(fmt.Sprintf("Adds: %d, Updates: %d, Deletes: %d", plan.Adds, plan.Updates, plan.Deletes))
	logger.Info("-----------------")

	// Show sample actions (up to 20)
	limit := 20
	if len(plan.Actions) < limit {
		limit = len(plan.Actions)
	}
	if limit > 0 {
		logger.Info("Sample actions:")
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
			logger.Info(fmt.Sprintf("  %s %s", actionType, action.RelPath))
		}
		if len(plan.Actions) > limit {
			logger.Info(fmt.Sprintf("  ... and %d more actions", len(plan.Actions)-limit))
		}
		logger.Info("-----------------")
	}

	if dryRun {
		logger.Info("Dry run: No changes will be made.")
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
		logger.Info("Synchronization aborted by user.")
		return nil // User cancelled
	}

	logger.Info("Starting synchronization...")

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

	// Create our enhanced progress bar
	bar := progress.New(
		totalSize,
		"Syncing files",
		progress.Sync,
		&progress.Options{
			ShowBytes:       true,
			Detailed:        true,
			RateInterval:    250 * time.Millisecond,
			RateWindowSize:  10,
			RefreshInterval: 100 * time.Millisecond,
			Output:          os.Stderr,
		},
	)
	bar.Start()

	// Make sure to finish the progress bar when done
	defer func() {
		if err := bar.Finish(); err != nil {
			logger.Error(fmt.Sprintf("Failed to finish progress bar: %v", err))
		}
	}()

	// Create a channel to signal when files are ready for progress updates
	// This helps prevent the "hanging" issue you described
	readyChan := make(chan struct{}, 1)
	readyChan <- struct{}{} // Signal that we're ready to start

	for _, action := range plan.Actions {
		wg.Add(1)
		sem <- struct{}{} // Acquire semaphore slot

		go func(act SyncAction) {
			defer wg.Done()
			defer func() { <-sem }() // Release semaphore slot

			var execErr error
			targetPath := filepath.Join(targetRoot, act.RelPath)

			// Update progress display with current file
			bar.SetCurrentFile(act.RelPath)

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
					execErr = copyFile(act.SourceInfo.AbsPath, targetPath, act.SourceInfo.Mode.Perm(), act.SourceInfo.ModTime, bar)
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
					logger.Warn(fmt.Sprintf("Unexpected 'Update' action for directory: %s", act.RelPath))
				} else {
					execErr = copyFile(act.SourceInfo.AbsPath, targetPath, act.SourceInfo.Mode.Perm(), act.SourceInfo.ModTime, bar)
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
			}

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

	logger.Info("Synchronization finished successfully.")
	return nil
}

// copyFile copies a file from src to dst, sets permissions and mod time, and updates progress bar.
func copyFile(src, dst string, perm os.FileMode, modTime time.Time, bar *progress.Progress) error {
	logger := slog.Default()

	// Update progress with current file
	bar.SetCurrentFile(filepath.Base(src))

	sourceFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("could not open source %s: %w", src, err)
	}
	defer func() {
		if err := sourceFile.Close(); err != nil {
			logger.Warn("Error closing %s: %v", src, err)
		}
	}()

	// Create or truncate destination file
	destFile, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("could not create/open destination %s: %w", dst, err)
	}
	defer func() {
		if err := destFile.Close(); err != nil {
			logger.Warn("Error closing %s: %v", dst, err)
		}
	}()

	// Create a buffer for copying
	buf := make([]byte, 1024*1024) // 1MB buffer

	// Wrap the reader with our progress reader
	progressReader := progress.NewReader(sourceFile, bar)

	// Use io.CopyBuffer for efficient copying
	_, err = io.CopyBuffer(destFile, progressReader, buf)
	if err != nil {
		return fmt.Errorf("could not copy data from %s to %s: %w", src, dst, err)
	}

	// Set modification time
	if err := os.Chtimes(dst, modTime, modTime); err != nil {
		// Log warning, as setting time might fail on some systems/filesystems
		logger.Warn("Failed to set modification time for %s: %v", dst, err)
	}

	return nil
}
