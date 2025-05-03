// pkg/progress/progress.go
package progress

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mitchellh/colorstring"
)

// ProgressType defines different kinds of progress indicators
type ProgressType int

const (
	// Scan progress for directory scanning
	Scan ProgressType = iota
	// Sync progress for file synchronization
	Sync
	// Checksum progress for file verification
	Checksum
)

// Progress is a custom progress tracking system
type Progress struct {
	// Basic properties
	total       int64
	current     int64
	description string
	progType    ProgressType
	startTime   time.Time
	lastUpdate  time.Time
	isActive    bool
	showBytes   bool
	detailed    bool
	nowFunc     func() time.Time // For testing

	// For transfer rate calculation
	rateWindow         []ratePoint
	rateWindowSize     int
	lastRateCalc       time.Time
	calculatedRate     float64
	estimatedRemaining time.Duration

	// Current file being processed (for detailed view)
	currentFile string

	// For concurrency safety
	mu sync.Mutex

	// Writer
	out io.Writer
}

// ratePoint represents a data point for rate calculation
type ratePoint struct {
	timestamp time.Time
	bytes     int64
}

// DefaultOptions returns the default options for a progress bar
func DefaultOptions() *Options {
	return &Options{
		ShowBytes:       true,
		Detailed:        true,
		RateInterval:    250 * time.Millisecond,
		RateWindowSize:  20,
		RefreshInterval: 100 * time.Millisecond,
		Output:          os.Stderr,
	}
}

// Options configures a progress bar
type Options struct {
	ShowBytes       bool
	Detailed        bool
	RateInterval    time.Duration
	RateWindowSize  int
	RefreshInterval time.Duration
	Output          io.Writer
}

// New creates a new progress tracker
func New(total int64, description string, progType ProgressType, opts *Options) *Progress {
	if opts == nil {
		opts = DefaultOptions()
	}

	now := time.Now()
	p := &Progress{
		total:          total,
		current:        0,
		description:    description,
		progType:       progType,
		startTime:      now,
		lastUpdate:     now,
		lastRateCalc:   now,
		isActive:       false,
		showBytes:      opts.ShowBytes,
		detailed:       opts.Detailed,
		nowFunc:        time.Now,
		rateWindowSize: opts.RateWindowSize,
		rateWindow:     make([]ratePoint, 0, opts.RateWindowSize),
		out:            opts.Output,
	}

	// Start the refresh goroutine
	go p.refreshLoop(opts.RefreshInterval)

	return p
}

// refreshLoop periodically updates the progress display
func (p *Progress) refreshLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		p.mu.Lock()
		if !p.isActive {
			p.mu.Unlock()
			return
		}
		err := p.update(0) // Update the display without adding progress
		if err != nil {
			slog.Error("Error updating progress", "error", err)
		}
		p.mu.Unlock()
	}
}

// Start begins the progress tracking
func (p *Progress) Start() *Progress {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.isActive = true
	p.startTime = p.nowFunc()
	err := p.render()
	if err != nil {
		slog.Error("Error rendering progress", "error", err)
	}

	return p
}

// Add adds n units to the progress
func (p *Progress) Add(n int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.isActive {
		return fmt.Errorf("progress tracker is not active")
	}

	return p.update(n)
}

// SetCurrentFile updates the currently processing file name (for detailed view)
func (p *Progress) SetCurrentFile(filename string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.currentFile = filename
	// Update display immediately when file changes
	err := p.render()
	if err != nil {
		slog.Error("Error rendering progress", "error", err)
	}
}

// update adds progress and updates the rate calculation
func (p *Progress) update(n int64) error {
	now := p.nowFunc()
	p.current += n

	// Only recalculate rate at intervals to avoid too much CPU usage
	if now.Sub(p.lastRateCalc) >= 250*time.Millisecond {
		p.calculateRate(now)
		p.lastRateCalc = now
	}

	// Render the progress bar
	return p.render()
}

// calculateRate updates the transfer rate and ETA
func (p *Progress) calculateRate(now time.Time) {
	// Add current point to the window
	currentPoint := ratePoint{
		timestamp: now,
		bytes:     p.current,
	}

	// Manage the sliding window
	if len(p.rateWindow) == 0 {
		// First point
		p.rateWindow = append(p.rateWindow, currentPoint)
		return
	}

	// Add to window
	p.rateWindow = append(p.rateWindow, currentPoint)

	// Trim window if it's too large
	if len(p.rateWindow) > p.rateWindowSize {
		p.rateWindow = p.rateWindow[1:]
	}

	// Need at least 2 points to calculate rate
	if len(p.rateWindow) < 2 {
		return
	}

	// Get oldest point in window
	oldestPoint := p.rateWindow[0]

	// Calculate time difference
	timeDiff := now.Sub(oldestPoint.timestamp).Seconds()
	if timeDiff < 0.001 {
		return // Avoid division by zero or very small numbers
	}

	// Calculate byte difference
	byteDiff := float64(p.current - oldestPoint.bytes)

	// Calculate rate: bytes per second
	rate := byteDiff / timeDiff

	// Update the stored rate
	p.calculatedRate = rate

	// Calculate ETA if we have a total
	if p.total > 0 {
		remaining := float64(p.total - p.current)
		if rate > 0 {
			etaSeconds := remaining / rate
			p.estimatedRemaining = time.Duration(etaSeconds * float64(time.Second))
		}
	}
}

// render draws the progress bar to the terminal
func (p *Progress) render() error {
	if p.out == nil {
		return nil
	}

	// Calculate percentage
	percent := 0.0
	if p.total > 0 {
		percent = float64(p.current) / float64(p.total) * 100
	}

	// Format progress elements
	var elements []string

	// Description and counter
	if p.total > 0 {
		if p.showBytes {
			elements = append(elements, fmt.Sprintf("[light_blue]%s[reset]: [white]%s/%s[reset] [green]%.1f%%[reset]",
				p.description,
				formatSize(p.current),
				formatSize(p.total),
				percent))
		} else {
			elements = append(elements, fmt.Sprintf("[light_blue]%s[reset]: [white]%d/%d[reset] [green]%.1f%%[reset]",
				p.description,
				p.current,
				p.total,
				percent))
		}
	} else {
		// Indeterminate progress
		if p.showBytes {
			elements = append(elements, fmt.Sprintf("[light_blue]%s[reset]: [white]%s[reset]",
				p.description,
				formatSize(p.current)))
		} else {
			elements = append(elements, fmt.Sprintf("[light_blue]%s[reset]: [white]%d[reset]",
				p.description,
				p.current))
		}
	}

	// Transfer rate
	if p.showBytes && p.calculatedRate > 0 {
		elements = append(elements, fmt.Sprintf("[yellow]%s/s[reset]", formatSize(int64(p.calculatedRate))))
	}

	// Elapsed time
	elapsed := p.nowFunc().Sub(p.startTime)
	elements = append(elements, fmt.Sprintf("[cyan]%s[reset]", formatDuration(elapsed)))

	// ETA
	if p.total > 0 && p.estimatedRemaining > 0 {
		elements = append(elements, fmt.Sprintf("[magenta]ETA: %s[reset]", formatDuration(p.estimatedRemaining)))
	}

	// Current file (in detailed mode)
	if p.detailed && p.currentFile != "" {
		// Truncate filename if too long
		filename := p.currentFile
		maxLen := 30
		if len(filename) > maxLen {
			filename = "..." + filename[len(filename)-maxLen+3:]
		}
		elements = append(elements, fmt.Sprintf("[light_green]%s[reset]", filename))
	}

	// Build progress bar (20 characters wide)
	if p.total > 0 {
		barWidth := 20
		filled := int(float64(barWidth) * float64(p.current) / float64(p.total))
		if filled > barWidth {
			filled = barWidth
		}

		bar := fmt.Sprintf("[blue][%s%s][reset]",
			strings.Repeat("=", filled),
			strings.Repeat(" ", barWidth-filled))
		elements = append(elements, bar)
	} else {
		// Spinner for indeterminate progress
		spinner := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		idx := int(p.nowFunc().UnixNano()/100000000) % len(spinner)
		elements = append(elements, fmt.Sprintf("[blue]%s[reset]", spinner[idx]))
	}

	// Join all elements and colorize
	statusLine := colorstring.Color(strings.Join(elements, " "))

	// Clear the line and print the status
	_, err := fmt.Fprintf(p.out, "\r\033[K%s", statusLine)
	if err != nil {
		return err
	}

	return nil
}

// Finish completes the progress tracking
func (p *Progress) Finish() error {
	logger := slog.Default()

	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.isActive {
		return nil
	}

	p.isActive = false

	// Print final progress and a newline
	err := p.render()
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintln(p.out); err != nil {
		logger.Error("Error printing newline", "error", err)
	}

	// Log completion statistics
	elapsed := p.nowFunc().Sub(p.startTime)
	if p.showBytes {
		logger.Info(fmt.Sprintf("%s completed: %s in %s", p.description, formatSize(p.current), formatDuration(elapsed)))
	} else {
		logger.Info(fmt.Sprintf("%s completed: %d operations in %s", p.description, p.current, formatDuration(elapsed)))
	}

	return nil
}

// Clear removes the progress display
func (p *Progress) Clear() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.isActive {
		return nil
	}

	_, err := fmt.Fprintf(p.out, "\r\033[K")
	if err != nil {
		return err
	}

	return nil
}

// formatSize formats a byte count as a human-readable string
func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// formatDuration formats a duration as a human-readable string
func formatDuration(d time.Duration) string {
	// For very short durations
	if d < time.Second {
		return "0s"
	}

	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	if hours > 0 {
		return fmt.Sprintf("%dh %dm %ds", hours, minutes, seconds)
	} else if minutes > 0 {
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}

// ---------- Reader implementation for io.Copy ----------

// ProgressReader wraps an io.Reader to track progress
type ProgressReader struct {
	Reader   io.Reader
	Progress *Progress
}

// Read reads data and updates progress
func (pr *ProgressReader) Read(p []byte) (int, error) {
	n, err := pr.Reader.Read(p)
	if n > 0 {
		_ = pr.Progress.Add(int64(n)) // Ignore errors from Progress.Add
	}
	return n, err
}

// NewReader creates a reader that updates progress
func NewReader(r io.Reader, progress *Progress) *ProgressReader {
	return &ProgressReader{
		Reader:   r,
		Progress: progress,
	}
}
