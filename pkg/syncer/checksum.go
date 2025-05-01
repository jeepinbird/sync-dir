// pkg/syncer/checksum.go
package syncer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// calculateSHA256 computes the SHA256 checksum of a file.
func calculateSHA256(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err // Return error directly, including os.IsNotExist
	}
	defer func() {
		if err := file.Close(); err != nil {
			fmt.Printf("Error closing %s: %v\n", filePath, err)
		}
	}()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}
