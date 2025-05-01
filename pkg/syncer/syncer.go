// pkg/syncer/syncer.go
package syncer

import (
	"fmt"
	"sync"

	"github.com/jeepinbird/sync-dir/pkg/fileinfo"
	"github.com/jeepinbird/sync-dir/pkg/ignore"
)

// Syncer orchestrates the directory synchronization process.
type Syncer struct {
	SourceRoot    string
	TargetRoot    string
	CliExcludes   []string
	DryRun        bool
	ignoreMatcher *ignore.Matcher
	sourceFiles   map[string]*fileinfo.FileInfo
	targetFiles   map[string]*fileinfo.FileInfo
	plan          *SyncPlan
}

// NewSyncer creates a new Syncer instance.
func NewSyncer(sourceRoot, targetRoot string, cliExcludes []string, dryRun bool) *Syncer {
	return &Syncer{
		SourceRoot:  sourceRoot,
		TargetRoot:  targetRoot,
		CliExcludes: cliExcludes,
		DryRun:      dryRun,
	}
}

// Run executes the entire synchronization process: load ignores, scan, plan, execute.
func (s *Syncer) Run() error {
	var err error

	// 1. Load Ignore Rules
	s.ignoreMatcher, err = ignore.NewMatcher(s.SourceRoot, s.CliExcludes)
	if err != nil {
		return fmt.Errorf("failed to load ignore rules: %w", err)
	}

	// 2. Scan Source and Target Directories Concurrently
	var wg sync.WaitGroup
	var sourceErr, targetErr error // Separate error variables for concurrent scans

	wg.Add(2)

	go func() {
		defer wg.Done()
		// Pass the ignore matcher only when scanning the source
		s.sourceFiles, sourceErr = scanDirectory(s.SourceRoot, s.SourceRoot, s.ignoreMatcher, "source")
	}()

	go func() {
		defer wg.Done()
		// Do not pass the ignore matcher when scanning the target
		s.targetFiles, targetErr = scanDirectory(s.TargetRoot, s.TargetRoot, nil, "target")
	}()

	wg.Wait() // Wait for both scans to complete

	if sourceErr != nil {
		return fmt.Errorf("error scanning source directory: %w", sourceErr)
	}
	if targetErr != nil {
		// Target scan errors are often less critical (e.g., target doesn't exist yet)
		// But we should still report them. If targetFiles is nil, planning will handle it.
		fmt.Printf("Note: Error scanning target directory: %v\n", targetErr)
		// Ensure targetFiles is initialized even if scan failed partially or fully
		if s.targetFiles == nil {
			s.targetFiles = make(map[string]*fileinfo.FileInfo)
		}
	}

	// 3. Create Sync Plan
	s.plan, err = createSyncPlan(s.sourceFiles, s.targetFiles)
	if err != nil {
		return fmt.Errorf("failed to create sync plan: %w", err)
	}

	// 4. Execute Plan (includes confirmation)
	err = executePlan(s.plan, s.SourceRoot, s.TargetRoot, s.DryRun)
	if err != nil {
		return fmt.Errorf("failed to execute sync plan: %w", err)
	}

	return nil // Success
}
