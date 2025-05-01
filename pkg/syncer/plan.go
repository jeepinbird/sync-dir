// pkg/syncer/plan.go
package syncer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jeepinbird/sync-dir/pkg/fileinfo"
)

// SyncActionType defines the type of action to be taken.
type SyncActionType int

const (
	Add    SyncActionType = iota // Add source file/dir to target
	Update                       // Update target file from source
	Delete                       // Delete target file/dir
	None                         // No action needed (for internal tracking)
)

func (t SyncActionType) String() string {
	switch t {
	case Add:
		return "Add"
	case Update:
		return "Update"
	case Delete:
		return "Delete"
	case None:
		return "None"
	default:
		return "Unknown"
	}
}

// SyncAction represents a single file operation in the sync plan.
type SyncAction struct {
	Type       SyncActionType
	SourceInfo *fileinfo.FileInfo // Info from source (nil for Delete)
	TargetInfo *fileinfo.FileInfo // Info from target (nil for Add)
	RelPath    string             // Relative path of the item
}

// SyncPlan contains the list of actions to perform.
type SyncPlan struct {
	Actions []SyncAction
	Adds    int
	Updates int
	Deletes int
}

// createSyncPlan compares source and target file maps and generates the plan.
func createSyncPlan(sourceFiles, targetFiles map[string]*fileinfo.FileInfo) (*SyncPlan, error) {
	plan := &SyncPlan{
		Actions: make([]SyncAction, 0),
	}
	processedTargetFiles := make(map[string]bool) // Keep track of targets we've handled

	fmt.Println("Comparing source and target...")

	// --- Iterate through Source Files ---
	for relPath, sourceFi := range sourceFiles {
		targetFi, existsInTarget := targetFiles[relPath]
		processedTargetFiles[relPath] = true // Mark as processed

		action := SyncAction{RelPath: relPath, SourceInfo: sourceFi}

		if !existsInTarget {
			// Source item doesn't exist in target -> Add
			action.Type = Add
			plan.Actions = append(plan.Actions, action)
			plan.Adds++
		} else {
			// Item exists in both source and target -> Compare for Update
			action.TargetInfo = targetFi

			// Check if types differ (file vs dir) - requires delete then add
			if sourceFi.IsDir != targetFi.IsDir {
				// Treat as Delete target then Add source
				// Add Delete action first
				plan.Actions = append(plan.Actions, SyncAction{
					Type:       Delete,
					TargetInfo: targetFi, // Need target info for deletion
					RelPath:    relPath,
				})
				plan.Deletes++
				// Add Add action
				action.Type = Add
				plan.Actions = append(plan.Actions, action)
				plan.Adds++
				continue // Move to next source item
			}

			// Types match, compare content if it's a file
			if !sourceFi.IsDir {
				needsUpdate, err := sourceFi.NeedsUpdate(targetFi, calculateSHA256)
				if err != nil {
					// Log error during comparison, maybe skip this file?
					// For now, let's return the error to halt the process.
					fmt.Fprintf(os.Stderr, "\nError comparing %s: %v\n", relPath, err)
					// Optionally, treat as update needed on error? Or skip?
					// Let's treat as update needed to be safe, but log it clearly.
					fmt.Fprintf(os.Stderr, "Assuming update needed for %s due to comparison error.\n", relPath)
					needsUpdate = true
					// return nil, fmt.Errorf("comparison failed for %s: %w", relPath, err)
				}

				if needsUpdate {
					action.Type = Update
					plan.Actions = append(plan.Actions, action)
					plan.Updates++
				}
				// If no update needed, do nothing for this item
			}
			// Directories: No update action needed based on content/time/size
			// Their existence and type matching is handled above.
			// Content differences are handled by actions on files within them.
		}
	}

	// --- Iterate through Target Files ---
	// Identify target items that were NOT in the source (and thus need deletion)
	for relPath, targetFi := range targetFiles {
		if _, processed := processedTargetFiles[relPath]; !processed {
			// This target item was not found in the source -> Delete
			plan.Actions = append(plan.Actions, SyncAction{
				Type:       Delete,
				TargetInfo: targetFi, // Need target info for deletion
				RelPath:    relPath,
			})
			plan.Deletes++
		}
	}

	// --- Sort Actions ---
	// Sort deletes first, then updates, then adds.
	// Within deletes, sort by path depth (deepest first) to avoid deleting a parent dir before its contents.
	// Within adds/updates, sort alphabetically by path.
	sort.SliceStable(plan.Actions, func(i, j int) bool {
		actionI := plan.Actions[i]
		actionJ := plan.Actions[j]

		// Prioritize Deletes
		if actionI.Type == Delete && actionJ.Type != Delete {
			return true
		}
		if actionI.Type != Delete && actionJ.Type == Delete {
			return false
		}
		if actionI.Type == Delete && actionJ.Type == Delete {
			// For deletes, sort deepest path first
			depthI := strings.Count(filepath.Clean(actionI.RelPath), string(os.PathSeparator))
			depthJ := strings.Count(filepath.Clean(actionJ.RelPath), string(os.PathSeparator))
			if depthI != depthJ {
				return depthI > depthJ // > for descending depth
			}
			// If depth is same, sort alphabetically (for deterministic order)
			return actionI.RelPath < actionJ.RelPath
		}

		// Prioritize Updates over Adds (though order often doesn't matter between them)
		if actionI.Type == Update && actionJ.Type == Add {
			return true
		}
		if actionI.Type == Add && actionJ.Type == Update {
			return false
		}

		// For Adds and Updates, sort alphabetically by path
		return actionI.RelPath < actionJ.RelPath
	})

	fmt.Printf("Comparison complete. Plan: %d Adds, %d Updates, %d Deletes.\n", plan.Adds, plan.Updates, plan.Deletes)
	return plan, nil
}
