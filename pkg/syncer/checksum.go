// pkg/syncer/checksum.go
package syncer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
)

// checksumWorkerCount defines how many concurrent checksum calculations to perform
const checksumWorkerCount = 4

// checksumJob represents a file to calculate checksum for
type checksumJob struct {
	filePath string
	result   chan<- checksumResult
}

// checksumResult contains the result of a checksum calculation
type checksumResult struct {
	filePath string
	checksum string
	err      error
}

// Global pool of checksum workers
var (
	checksumJobChan        chan checksumJob
	checksumWorkerWg       sync.WaitGroup
	checksumWorkersStarted bool
	checksumWorkersMu      sync.Mutex
)

// startChecksumWorkers initializes the worker pool for checksums
func startChecksumWorkers() {
	checksumWorkersMu.Lock()
	defer checksumWorkersMu.Unlock()

	if checksumWorkersStarted {
		return // Already started
	}

	checksumJobChan = make(chan checksumJob, checksumWorkerCount*2)
	checksumWorkersStarted = true

	// Start worker goroutines
	for i := 0; i < checksumWorkerCount; i++ {
		checksumWorkerWg.Add(1)
		go func() {
			defer checksumWorkerWg.Done()

			for job := range checksumJobChan {
				checksum, err := calculateSHA256Internal(job.filePath)
				job.result <- checksumResult{
					filePath: job.filePath,
					checksum: checksum,
					err:      err,
				}
			}
		}()
	}

	slog.Info(fmt.Sprintf("Started %d checksum worker goroutines", checksumWorkerCount))
}

// stopChecksumWorkers shuts down the worker pool
func stopChecksumWorkers() {
	checksumWorkersMu.Lock()
	defer checksumWorkersMu.Unlock()

	if !checksumWorkersStarted {
		return // Not started
	}

	close(checksumJobChan)
	checksumWorkerWg.Wait()
	checksumWorkersStarted = false

	slog.Info("Stopped checksum worker goroutines")
}

// calculateSHA256 computes the SHA256 checksum of a file using the worker pool.
func calculateSHA256(filePath string) (string, error) {
	// Start workers if not already started
	startChecksumWorkers()

	// Create a channel for the result
	resultChan := make(chan checksumResult, 1)

	// Submit the job
	checksumJobChan <- checksumJob{
		filePath: filePath,
		result:   resultChan,
	}

	// Wait for the result
	result := <-resultChan
	return result.checksum, result.err
}

// calculateSHA256Internal is the internal function that actually calculates the checksum
func calculateSHA256Internal(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer func() {
		if err := file.Close(); err != nil {
			slog.Error("Error closing %s: %v\n", filePath, err)
		}
	}()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}
