package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ANSI color codes for terminal output
const (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
)

// FileInfo stores relevant details for comparison
type FileInfo struct {
	Path    string // Relative path
	Size    int64
	ModTime time.Time
	IsDir   bool
}

// ActionType defines the type of synchronization action
type ActionType int

const (
	ActionAdd ActionType = iota
	ActionDelete
	ActionUpdate
)

// SyncAction represents a single action to be performed
type SyncAction struct {
	Type     ActionType
	RelPath  string // Relative path of the file/dir
	SourcePath string // Full path in source (if applicable)
	TargetPath string // Full path in target (if applicable)
	IsDir    bool
}

func main() {
	// --- 1. Define and Parse Command Line Flags ---
	sourceDir := flag.String("source", "", "Source directory path (required)")
	targetDir := flag.String("target", "", "Target directory path (required)")
	dryRun := flag.Bool("dry-run", false, "Only show the plan, don't execute actions") // Added dry-run flag
	flag.Parse()

	// --- 2. Validate Input ---
	if *sourceDir == "" || *targetDir == "" {
		fmt.Println("Error: Both --source and --target flags are required.")
		flag.Usage()
		os.Exit(1)
	}

	absSource, err := filepath.Abs(*sourceDir)
	if err != nil {
		log.Fatalf("Error getting absolute path for source: %v", err)
	}
	absTarget, err := filepath.Abs(*targetDir)
	if err != nil {
		log.Fatalf("Error getting absolute path for target: %v", err)
	}

	if absSource == absTarget {
		log.Fatalf("Error: Source and target directories cannot be the same.")
	}

	sourceInfo, err := os.Stat(absSource)
	if err != nil {
		log.Fatalf("Error accessing source directory '%s': %v", absSource, err)
	}
	if !sourceInfo.IsDir() {
		log.Fatalf("Error: Source path '%s' is not a directory.", absSource)
	}

	targetInfo, err := os.Stat(absTarget)
	if err != nil {
		// If target doesn't exist, we might create it later
		if !os.IsNotExist(err) {
			log.Fatalf("Error accessing target directory '%s': %v", absTarget, err)
		}
		fmt.Printf("Target directory '%s' does not exist. It will be created if sync proceeds.\n", absTarget)
	} else if !targetInfo.IsDir() {
		log.Fatalf("Error: Target path '%s' exists but is not a directory.", absTarget)
	}

	fmt.Printf("Source: %s\n", absSource)
	fmt.Printf("Target: %s\n", absTarget)
	fmt.Println("Scanning directories...")

	// --- 3. Scan Directories ---
	sourceFiles, err := walkDir(absSource)
	if err != nil {
		log.Fatalf("Error scanning source directory: %v", err)
	}
	targetFiles, err := walkDir(absTarget)
	if err != nil {
		// Ignore "not exist" error for target if it wasn't there initially
		if !os.IsNotExist(err) {
			log.Fatalf("Error scanning target directory: %v", err)
		}
		// Target doesn't exist, so targetFiles map will be empty, which is correct
	}

	fmt.Println("Comparing directories...")

	// --- 4. Compare and Build Action Plan ---
	actions := compareDirs(sourceFiles, targetFiles, absSource, absTarget)

	// --- 5. Display Plan ---
	displayPlan(actions)

	if len(actions) == 0 {
		fmt.Println("Directories are already in sync. No actions needed.")
		os.Exit(0)
	}

	if *dryRun {
		fmt.Println("\nDry run complete. No changes were made.")
		os.Exit(0)
	}

	// --- 6. Get Confirmation ---
	if !confirmExecution() {
		fmt.Println("Aborted by user.")
		os.Exit(0)
	}

	// --- 7. Execute Plan ---
	fmt.Println("\nExecuting synchronization plan...")
	err = executePlan(actions)
	if err != nil {
		log.Fatalf("Error during synchronization: %v", err)
	}

	fmt.Println(ColorGreen + "\nSynchronization complete." + ColorReset)
}

// walkDir recursively scans a directory and returns a map of relative paths to FileInfo
func walkDir(rootDir string) (map[string]FileInfo, error) {
	files := make(map[string]FileInfo)
	err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Don't stop the walk for single file errors, just log them
			fmt.Printf("Warning: Error accessing path %q: %v\n", path, err)
			return nil // Continue walking
		}

		// Skip the root directory itself in the map
		if path == rootDir {
			return nil
		}

		relPath, err := filepath.Rel(rootDir, path)
		if err != nil {
			// Should not happen if path is within rootDir
			return fmt.Errorf("failed to get relative path for %s: %w", path, err)
		}

		// Use filepath.ToSlash to ensure consistent path separators (/)
		relPath = filepath.ToSlash(relPath)

		files[relPath] = FileInfo{
			Path:    relPath,
			Size:    info.Size(),
			ModTime: info.ModTime(),
			IsDir:   info.IsDir(),
		}
		return nil
	})

	if err != nil {
		// Check if the error is because the root directory doesn't exist
		if os.IsNotExist(err) {
			// If the root doesn't exist, return an empty map and no error *for this function*
			// The caller (main) should handle the non-existence if needed.
			return make(map[string]FileInfo), nil
		}
		return nil, fmt.Errorf("error walking directory %s: %w", rootDir, err)
	}
	return files, nil
}

// compareDirs compares source and target file maps and returns a list of actions
func compareDirs(sourceFiles, targetFiles map[string]FileInfo, sourceRoot, targetRoot string) []SyncAction {
	var actions []SyncAction

	// Check for Adds and Updates
	for relPath, sInfo := range sourceFiles {
		tInfo, exists := targetFiles[relPath]

		actionPathSource := filepath.Join(sourceRoot, relPath)
		actionPathTarget := filepath.Join(targetRoot, relPath)

		if !exists {
			// Add: Exists in source, not in target
			actions = append(actions, SyncAction{
				Type:     ActionAdd,
				RelPath:  relPath,
				SourcePath: actionPathSource,
				TargetPath: actionPathTarget,
				IsDir:    sInfo.IsDir,
			})
		} else {
			// Exists in both: Check for Update
			// If types differ (file vs dir), treat as delete + add
			if sInfo.IsDir != tInfo.IsDir {
				actions = append(actions, SyncAction{ // Delete target first
					Type:     ActionDelete,
					RelPath:  relPath,
					TargetPath: actionPathTarget,
					IsDir:    tInfo.IsDir, // Delete what's currently there
				})
				actions = append(actions, SyncAction{ // Then add source item
					Type:     ActionAdd,
					RelPath:  relPath,
					SourcePath: actionPathSource,
					TargetPath: actionPathTarget,
					IsDir:    sInfo.IsDir,
				})
			} else if !sInfo.IsDir {
				// If both are files, compare ModTime and Size
				// Use Truncate(time.Second) to avoid issues with sub-second precision differences
				// across filesystems. Add size check for robustness.
				if !sInfo.ModTime.Truncate(time.Second).Equal(tInfo.ModTime.Truncate(time.Second)) || sInfo.Size != tInfo.Size {
					actions = append(actions, SyncAction{
						Type:     ActionUpdate,
						RelPath:  relPath,
						SourcePath: actionPathSource,
						TargetPath: actionPathTarget,
						IsDir:    false,
					})
				}
			}
			// If both are directories, no specific update action needed here.
			// Content differences are handled by file add/delete/update actions.
		}
	}

	// Check for Deletes
	for relPath, tInfo := range targetFiles {
		_, exists := sourceFiles[relPath]
		if !exists {
			// Delete: Exists in target, not in source
			actions = append(actions, SyncAction{
				Type:     ActionDelete,
				RelPath:  relPath,
				TargetPath: filepath.Join(targetRoot, relPath),
				IsDir:    tInfo.IsDir,
			})
		}
	}

	// --- Important: Sort actions ---
	// We need to ensure directories are created before files inside them,
	// and files are deleted before their parent directories.
	// Simple sort: Deletes first (in reverse path order), then Adds/Updates (in path order).
	// This isn't perfectly robust for complex cases but handles common scenarios.
	// A more robust approach might involve building a dependency graph.

	// Separate deletes from adds/updates
	var deletes, addsUpdates []SyncAction
	for _, action := range actions {
		if action.Type == ActionDelete {
			deletes = append(deletes, action)
		} else {
			addsUpdates = append(addsUpdates, action)
		}
	}

	// Sort deletes: deeper paths first (reverse alphabetical should generally work)
	// so files are deleted before parent dirs
	sort.SliceStable(deletes, func(i, j int) bool {
		return deletes[i].RelPath > deletes[j].RelPath
	})

	// Sort adds/updates: shallower paths first (alphabetical)
	// so parent dirs are created before files inside them
	sort.SliceStable(addsUpdates, func(i, j int) bool {
		return addsUpdates[i].RelPath < addsUpdates[j].RelPath
	})

	// Combine sorted lists: Deletes first, then Adds/Updates
	return append(deletes, addsUpdates...)
}


// displayPlan prints the planned actions to the console with colors
func displayPlan(actions []SyncAction) {
	if len(actions) == 0 {
		return // Nothing to display
	}

	fmt.Println("\n--- Synchronization Plan ---")
	counts := make(map[ActionType]int)
	for _, action := range actions {
		counts[action.Type]++
		prefix := ""
		color := ColorReset
		actionStr := ""
		pathStr := action.RelPath
		if action.IsDir {
			pathStr += "/" // Indicate directory
		}

		switch action.Type {
		case ActionAdd:
			prefix = "[+] ADD   "
			color = ColorGreen
			actionStr = fmt.Sprintf("%s %s", prefix, pathStr)
		case ActionDelete:
			prefix = "[-] DELETE"
			color = ColorRed
			actionStr = fmt.Sprintf("%s %s", prefix, pathStr)
		case ActionUpdate:
			prefix = "[~] UPDATE"
			color = ColorYellow
			actionStr = fmt.Sprintf("%s %s", prefix, pathStr)
		}
		fmt.Println(color + actionStr + ColorReset)
	}

	fmt.Println("\n--- Summary ---")
	fmt.Printf("%sAdds:    %d%s\n", ColorGreen, counts[ActionAdd], ColorReset)
	fmt.Printf("%sUpdates: %d%s\n", ColorYellow, counts[ActionUpdate], ColorReset)
	fmt.Printf("%sDeletes: %d%s\n", ColorRed, counts[ActionDelete], ColorReset)
	fmt.Printf("Total actions: %d\n", len(actions))
}

// confirmExecution asks the user for confirmation
func confirmExecution() bool {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("\nProceed with synchronization? (y/N): ")
	input, err := reader.ReadString('\n')
	if err != nil {
		fmt.Println("Error reading input:", err)
		return false
	}
	input = strings.TrimSpace(strings.ToLower(input))
	return input == "y" || input == "yes"
}

// executePlan performs the file operations based on the action list
func executePlan(actions []SyncAction) error {
	var errors []string

	for i, action := range actions {
		fmt.Printf("Action %d/%d: ", i+1, len(actions)) // Progress indicator
		var err error
		switch action.Type {
		case ActionAdd:
			fmt.Printf("%sAdding %s%s%s\n", ColorGreen, getActionTypeIndicator(action.IsDir), action.RelPath, ColorReset)
			err = addOrUpdateItem(action.SourcePath, action.TargetPath, action.IsDir)
		case ActionUpdate:
			fmt.Printf("%sUpdating %s%s%s\n", ColorYellow, getActionTypeIndicator(action.IsDir), action.RelPath, ColorReset)
			// Update is essentially the same as add for files (overwrite)
			// For directories, it's a no-op at this stage (content handled separately)
			if !action.IsDir {
				err = addOrUpdateItem(action.SourcePath, action.TargetPath, action.IsDir)
			}
		case ActionDelete:
			fmt.Printf("%sDeleting %s%s%s\n", ColorRed, getActionTypeIndicator(action.IsDir), action.RelPath, ColorReset)
			err = deleteItem(action.TargetPath, action.IsDir)
		}

		if err != nil {
			errorMsg := fmt.Sprintf("Failed action [%s %s]: %v", actionTypeToString(action.Type), action.RelPath, err)
			fmt.Printf("%sError: %s%s\n", ColorRed, errorMsg, ColorReset)
			errors = append(errors, errorMsg)
			// Decide whether to continue or stop on error.
			// For now, let's continue but report all errors at the end.
			// Consider adding a flag to stop on first error later.
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("encountered %d error(s) during sync:\n- %s", len(errors), strings.Join(errors, "\n- "))
	}
	return nil
}

// Helper to get (dir) or (file) indicator
func getActionTypeIndicator(isDir bool) string {
	if isDir {
		return "(dir)  "
	}
	return "(file) "
}

// Helper to convert action type to string for error messages
func actionTypeToString(actionType ActionType) string {
	switch actionType {
	case ActionAdd: return "ADD"
	case ActionDelete: return "DELETE"
	case ActionUpdate: return "UPDATE"
	default: return "UNKNOWN"
	}
}


// addOrUpdateItem handles creating directories or copying files
func addOrUpdateItem(sourcePath, targetPath string, isDir bool) error {
	if isDir {
		// Create directory if it doesn't exist
		// MkdirAll is safe to call even if the dir exists
		err := os.MkdirAll(targetPath, 0755) // Use appropriate permissions
		if err != nil {
			return fmt.Errorf("failed to create directory %s: %w", targetPath, err)
		}
	} else {
		// Ensure parent directory exists for the file
		targetDir := filepath.Dir(targetPath)
		err := os.MkdirAll(targetDir, 0755)
		if err != nil {
			return fmt.Errorf("failed to create parent directory %s for file %s: %w", targetDir, targetPath, err)
		}

		// Copy file content
		err = copyFile(sourcePath, targetPath)
		if err != nil {
			return fmt.Errorf("failed to copy file from %s to %s: %w", sourcePath, targetPath, err)
		}
	}
	return nil
}

// deleteItem handles deleting files or directories (recursively for dirs)
func deleteItem(targetPath string, isDir bool) error {
	var err error
	if isDir {
		// Remove directory and all its contents
		err = os.RemoveAll(targetPath)
	} else {
		// Remove single file
		err = os.Remove(targetPath)
	}

	if err != nil && !os.IsNotExist(err) { // Don't report error if it was already gone
		return fmt.Errorf("failed to delete %s: %w", targetPath, err)
	}
	return nil // Return nil if successful or if it didn't exist
}

// copyFile copies file content from src to dst
func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	// Create truncates if exists, otherwise creates new
	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	if err != nil {
		return err
	}

	// Preserve modification time (optional but good for sync tools)
	srcInfo, err := os.Stat(src)
	if err == nil { // If stat fails, we just skip preserving time
		os.Chtimes(dst, srcInfo.ModTime(), srcInfo.ModTime())
	}


	// Ensure contents are flushed to disk
	return destFile.Sync()
}
