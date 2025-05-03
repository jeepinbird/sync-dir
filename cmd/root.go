// cmd/root.go
package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/jeepinbird/sync-dir/pkg/syncer" // Import the syncer package
	"github.com/spf13/cobra"
)

var (
	// Flags
	excludePatterns []string // Stores values from --exclude flags
	dryRun          bool     // Flag for dry run
	verbosity       string   // Verbosity level

	// rootCmd represents the base command when called without any subcommands
	rootCmd = &cobra.Command{
		Use:   "sync-dir <source> <target>",
		Short: "Synchronizes a source directory to a target directory.",
		Long: `Synchronizes files and directories from a source path to a target path.

The source is treated as the source of truth.
- Files/directories in the target that do not exist in the source will be deleted.
- Files that differ based on modification time and size will be updated from the source.
- A checksum is automatically used to verify differences when modification times or sizes alone are inconclusive (e.g., same size but different time).
- Exclusions can be specified via --exclude flags or a .sync-ignore file in the source directory.`,
		Args: cobra.ExactArgs(2), // Requires exactly two arguments: source and target
		RunE: func(cmd *cobra.Command, args []string) error {
			// Set up logging based on verbosity flag
			var logLevel slog.Leveler
			switch verbosity {
			case "debug":
				logLevel = slog.LevelDebug
			case "info":
				logLevel = slog.LevelInfo
			case "warn":
				logLevel = slog.LevelWarn
			case "error":
				logLevel = slog.LevelError
			default:
				logLevel = slog.LevelInfo
			}
			logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
			slog.SetDefault(logger)

			sourcePath, err := filepath.Abs(args[0])
			if err != nil {
				return fmt.Errorf("invalid source path '%s': %w", args[0], err)
			}
			targetPath, err := filepath.Abs(args[1])
			if err != nil {
				return fmt.Errorf("invalid target path '%s': %w", args[1], err)
			}

			// Basic validation: source must exist and be a directory
			sourceInfo, err := os.Stat(sourcePath)
			if err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("source path '%s' does not exist", sourcePath)
				}
				return fmt.Errorf("could not stat source path '%s': %w", sourcePath, err)
			}
			if !sourceInfo.IsDir() {
				return fmt.Errorf("source path '%s' is not a directory", sourcePath)
			}

			// Target validation: if it exists, must be a directory
			targetInfo, err := os.Stat(targetPath)
			if err != nil {
				if !os.IsNotExist(err) {
					return fmt.Errorf("could not stat target path '%s': %w", targetPath, err)
				}
				// Target doesn't exist, which is fine, it will be created
			} else if !targetInfo.IsDir() {
				return fmt.Errorf("target path '%s' exists but is not a directory", targetPath)
			}

			// Prevent syncing a directory to itself or a subdirectory of itself
			if sourcePath == targetPath {
				return fmt.Errorf("source and target paths cannot be the same")
			}
			rel, err := filepath.Rel(sourcePath, targetPath)
			if err == nil && !filepath.IsAbs(rel) && len(rel) > 0 && rel[0] != '.' {
				return fmt.Errorf("target path '%s' cannot be inside the source path '%s'", targetPath, sourcePath)
			}

			logger.Info(fmt.Sprintf("Source: %s", sourcePath))
			logger.Info(fmt.Sprintf("Target: %s", targetPath))
			if len(excludePatterns) > 0 {
				logger.Info(fmt.Sprintf("CLI Exclusions: %v", excludePatterns))
			}
			if dryRun {
				logger.Info("--- DRY RUN MODE ---")
			}

			// Create Syncer instance
			sync := syncer.NewSyncer(sourcePath, targetPath, excludePatterns, dryRun)
			sync.SetLogLevel(logLevel)

			// Run the synchronization process
			err = sync.Run()
			if err != nil {
				return fmt.Errorf("sync failed: %w", err) // Wrap error for context
			}

			logger.Info("\nSync completed successfully.")
			if dryRun {
				logger.Info("(Dry run - no changes were actually made)")
			}
			return nil // Return nil for successful execution
		},
	}
)

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	// Define flags
	rootCmd.Flags().StringSliceVarP(&excludePatterns, "exclude", "e", []string{}, "Patterns to exclude (can be specified multiple times)")
	rootCmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be done without actually performing any actions")
	rootCmd.Flags().StringVarP(&verbosity, "verbosity", "v", "info", "Log verbosity level (debug, info, warn, error)")
}
