// DirSync is a command-line tool for synchronizing directories
// It takes a source and target directory, compares them recursively,
// and makes the target match the source by adding, updating, or deleting files
package main

import (
	"bufio"
	"crypto/md5"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Action represents a single sync operation to be performed
type Action struct {
	Type       string // "add", "delete", "update"
	Path       string // Relative path to the file
	SourcePath string // Full path in source
	TargetPath string // Full path in target
}

// Maximum number of files to display in the action plan
const maxFilesToDisplay = 20

// buildFileList creates a map of all files in a directory (recursive)
// The keys are relative paths, and the values are file info objects
func buildFileList(root string) (map[string]os.FileInfo, error) {
	files := make(map[string]os.FileInfo)
	
	// Walk through the directory recursively
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		
		// Skip directories themselves
		if info.IsDir() {
			return nil
		}
		
		// Get relative path from root
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		
		files[relPath] = info
		return nil
	})
	
	return files, err
}

// calculateMD5 returns the MD5 hash of a file
func calculateMD5(filePath string) ([]byte, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	
	hash := md5.New()
	if _, err := io.Copy(hash, file); err != nil {
		return nil, err
	}
	
	return hash.Sum(nil), nil
}

// filesAreDifferent compares two files by their MD5 hash
func filesAreDifferent(sourcePath, targetPath string) (bool, error) {
	sourceHash, err := calculateMD5(sourcePath)
	if err != nil {
		return false, err
	}
	
	targetHash, err := calculateMD5(targetPath)
	if err != nil {
		return false, err
	}
	
	// Compare the hashes
	return string(sourceHash) != string(targetHash), nil
}

// copyFile copies a file from source to target
func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	
	// Create or truncate the destination file
	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()
	
	// Copy contents
	_, err = io.Copy(destFile, sourceFile)
	if err != nil {
		return err
	}
	
	// Sync to ensure write is complete
	return destFile.Sync()
}

// buildActionPlan compares source and target directories to create a list of actions
func buildActionPlan(sourceFiles map[string]os.FileInfo, targetFiles map[string]os.FileInfo, sourceDir, targetDir string) ([]Action, error) {
	var actions []Action
	
	// Find files to add or update
	for relPath := range sourceFiles {
		sourcePath := filepath.Join(sourceDir, relPath)
		targetPath := filepath.Join(targetDir, relPath)
		
		if _, exists := targetFiles[relPath]; exists {
			// File exists in both - check if different
			different, err := filesAreDifferent(sourcePath, targetPath)
			if err != nil {
				return nil, err
			}
			
			if different {
				actions = append(actions, Action{
					Type:       "update",
					Path:       relPath,
					SourcePath: sourcePath,
					TargetPath: targetPath,
				})
			}
		} else {
			// File exists only in source - add to target
			actions = append(actions, Action{
				Type:       "add",
				Path:       relPath,
				SourcePath: sourcePath,
				TargetPath: targetPath,
			})
		}
	}
	
	// Find files to delete
	for relPath := range targetFiles {
		if _, exists := sourceFiles[relPath]; !exists {
			targetPath := filepath.Join(targetDir, relPath)
			
			actions = append(actions, Action{
				Type:       "delete",
				Path:       relPath,
				TargetPath: targetPath,
			})
		}
	}
	
	return actions, nil
}

// displayActionPlan shows the action plan with colors
func displayActionPlan(actions []Action, useColors bool) {
	var addCount, updateCount, deleteCount int
	displayCount := 0
	
	// Set up color codes
	green := "\033[32m"
	yellow := "\033[33m"
	red := "\033[31m"
	reset := "\033[0m"
	
	// Disable colors if needed
	if !useColors {
		green = ""
		yellow = ""
		red = ""
		reset = ""
	}
	
	fmt.Println("\nSync Action Plan:")
	fmt.Println("================")
	
	// Display file actions (limited by maxFilesToDisplay)
	for _, action := range actions {
		// Only display up to maxFilesToDisplay
		if displayCount < maxFilesToDisplay {
			switch action.Type {
			case "add":
				fmt.Printf("%s+ Add: %s%s\n", green, action.Path, reset)
			case "update":
				fmt.Printf("%s~ Update: %s%s\n", yellow, action.Path, reset)
			case "delete":
				fmt.Printf("%s- Delete: %s%s\n", red, action.Path, reset)
			}
			displayCount++
		}
		
		// Count by type
		switch action.Type {
		case "add":
			addCount++
		case "update":
			updateCount++
		case "delete":
			deleteCount++
		}
	}
	
	// If there are more files than we displayed
	if len(actions) > maxFilesToDisplay {
		fmt.Printf("... and %d more actions\n", len(actions) - maxFilesToDisplay)
	}
	
	// Display summary
	fmt.Println("\nSummary:")
	fmt.Printf("  %sAdd: %d%s, %sUpdate: %d%s, %sDelete: %d%s\n", 
		green, addCount, reset, 
		yellow, updateCount, reset, 
		red, deleteCount, reset)
	fmt.Printf("  Total actions: %d\n", len(actions))
}

// askForConfirmation prompts the user for confirmation
func askForConfirmation() bool {
	reader := bufio.NewReader(os.Stdin)
	
	fmt.Print("\nDo you want to proceed with these actions? (y/n): ")
	response, err := reader.ReadString('\n')
	if err != nil {
		fmt.Println("Error reading input:", err)
		return false
	}
	
	response = strings.ToLower(strings.TrimSpace(response))
	return response == "y" || response == "yes"
}

// executeActions performs all the actions in the plan
func executeActions(actions []Action) error {
	total := len(actions)
	
	for i, action := range actions {
		// Show progress
		fmt.Printf("[%d/%d] ", i+1, total)
		
		switch action.Type {
		case "add", "update":
			// Create parent directories if they don't exist
			targetDir := filepath.Dir(action.TargetPath)
			if err := os.MkdirAll(targetDir, 0755); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", targetDir, err)
			}
			
			// Copy file from source to target
			if err := copyFile(action.SourcePath, action.TargetPath); err != nil {
				return fmt.Errorf("failed to copy file %s: %w", action.Path, err)
			}
			
			fmt.Printf("Completed: %s %s\n", action.Type, action.Path)
			
		case "delete":
			// Delete file from target
			if err := os.Remove(action.TargetPath); err != nil {
				return fmt.Errorf("failed to delete file %s: %w", action.Path, err)
			}
			
			fmt.Printf("Completed: delete %s\n", action.Path)
		}
	}
	
	return nil
}

func main() {
	// Parse command line arguments
	sourceDir := flag.String("source", "", "Source directory to sync from")
	targetDir := flag.String("target", "", "Target directory to sync to")
	flag.Parse()
	
	// If not enough arguments, use positional ones
	args := flag.Args()
	if *sourceDir == "" && len(args) > 0 {
		*sourceDir = args[0]
	}
	if *targetDir == "" && len(args) > 1 {
		*targetDir = args[1]
	}
	
	// Check if source and target are provided
	if *sourceDir == "" || *targetDir == "" {
		fmt.Println("Please provide both source and target directories")
		fmt.Println("Usage: dirsync --source /path/to/source --target /path/to/target")
		fmt.Println("   or: dirsync /path/to/source /path/to/target")
		os.Exit(1)
	}
	
	// Resolve absolute paths for better display
	absSource, err := filepath.Abs(*sourceDir)
	if err == nil {
		*sourceDir = absSource
	}
	
	absTarget, err := filepath.Abs(*targetDir)
	if err == nil {
		*targetDir = absTarget
	}
	
	fmt.Printf("Source: %s\n", *sourceDir)
	fmt.Printf("Target: %s\n", *targetDir)
	
	// Check if source exists
	sourceInfo, err := os.Stat(*sourceDir)
	if err != nil || !sourceInfo.IsDir() {
		fmt.Printf("Source directory does not exist or is not a directory: %s\n", *sourceDir)
		os.Exit(1)
	}
	
	// Check if target exists, create it if it doesn't
	targetInfo, err := os.Stat(*targetDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("Target directory does not exist. Creating: %s\n", *targetDir)
			err = os.MkdirAll(*targetDir, 0755)
			if err != nil {
				fmt.Printf("Failed to create target directory: %s\n", err)
				os.Exit(1)
			}
		} else {
			fmt.Printf("Error accessing target directory: %s\n", err)
			os.Exit(1)
		}
	} else if !targetInfo.IsDir() {
		fmt.Printf("Target exists but is not a directory: %s\n", *targetDir)
		os.Exit(1)
	}
	
	// Check if running on Windows to determine color support
	useColors := true
	if os.Getenv("OS") == "Windows_NT" {
		// Windows command prompt doesn't support ANSI colors by default
		useColors = false
	}
	
	// Build file lists
	fmt.Println("Scanning directories...")
	sourceFiles, err := buildFileList(*sourceDir)
	if err != nil {
		fmt.Printf("Error scanning source directory: %s\n", err)
		os.Exit(1)
	}
	
	targetFiles, err := buildFileList(*targetDir)
	if err != nil {
		fmt.Printf("Error scanning target directory: %s\n", err)
		os.Exit(1)
	}
	
	fmt.Printf("Found %d files in source, %d files in target\n", 
		len(sourceFiles), len(targetFiles))
	
	// Compare and build action plan
	fmt.Println("Building action plan...")
	actions, err := buildActionPlan(sourceFiles, targetFiles, *sourceDir, *targetDir)
	if err != nil {
		fmt.Printf("Error building action plan: %s\n", err)
		os.Exit(1)
	}
	
	if len(actions) == 0 {
		fmt.Println("Directories are already in sync. No actions needed.")
		os.Exit(0)
	}
	
	// Display action plan
	displayActionPlan(actions, useColors)
	
	// Ask for confirmation
	if askForConfirmation() {
		fmt.Println("\nExecuting sync actions...")
		if err := executeActions(actions); err != nil {
			fmt.Printf("Error during execution: %s\n", err)
			os.Exit(1)
		}
		fmt.Println("\nSync completed successfully!")
	} else {
		fmt.Println("\nSync cancelled.")
	}
}
