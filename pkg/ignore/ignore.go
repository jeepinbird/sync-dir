// pkg/ignore/ignore.go
package ignore

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sabhiram/go-gitignore" // Using this library for pattern matching
)

const IgnoreFileName = ".sync-ignore"

// Matcher holds the ignore patterns.
type Matcher struct {
	ignoreMatcher *ignore.GitIgnore
	cliPatterns   []string // Store raw CLI patterns for potential logging/debugging
}

// NewMatcher creates a Matcher by reading .sync-ignore from the source directory
// and combining it with CLI exclude patterns.
func NewMatcher(sourceDir string, cliExcludes []string) (*Matcher, error) {
	ignoreFilePath := filepath.Join(sourceDir, IgnoreFileName)
	var patterns []string

	// Add CLI patterns first
	patterns = append(patterns, cliExcludes...)

	// Read .sync-ignore if it exists
	if _, err := os.Stat(ignoreFilePath); err == nil {
		file, err := os.Open(ignoreFilePath)
		if err != nil {
			return nil, fmt.Errorf("failed to open %s: %w", IgnoreFileName, err)
		}
		defer func() {
			if err := file.Close(); err != nil {
				fmt.Printf("Error closing %s: %v\n", ignoreFilePath, err)
			}
		}()

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			// Ignore empty lines and comments
			if line != "" && !strings.HasPrefix(line, "#") {
				patterns = append(patterns, line)
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", IgnoreFileName, err)
		}
		fmt.Printf("Loaded %d patterns from %s\n", len(patterns)-len(cliExcludes), IgnoreFileName)
	} else if !os.IsNotExist(err) {
		// Error other than file not existing
		return nil, fmt.Errorf("failed to stat %s: %w", IgnoreFileName, err)
	}

	// Compile patterns using go-gitignore
	// Note: go-gitignore expects patterns relative to the base directory (sourceDir)
	matcher := ignore.CompileIgnoreLines(patterns...)

	return &Matcher{
		ignoreMatcher: matcher,
		cliPatterns:   cliExcludes, // Keep original CLI patterns if needed
	}, nil
}

// Matches checks if a given path (relative to the source directory) should be ignored.
func (m *Matcher) Matches(relPath string) bool {
	if m.ignoreMatcher == nil {
		return false // No patterns loaded
	}
	// go-gitignore expects paths with OS-specific separators, but internally
	// often works better with '/'. Let's normalize for safety.
	unixPath := filepath.ToSlash(relPath)
	return m.ignoreMatcher.MatchesPath(unixPath)
}
