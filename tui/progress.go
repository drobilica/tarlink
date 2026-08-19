package tui

import (
	"fmt"
	"math"
	"time"
)

const (
	speedWindow = 8 * time.Second
	etaWarmup   = 2 * time.Second
	// progressBarWidth is the standard display width of the Bubbles progress
	// bar (excluding the trailing percentage), used for normal and wide
	// terminals.
	progressBarWidth = 24
)

// progressBarWidthFor returns the deterministic progress bar width for a given
// terminal width. It holds the standard width stable across wide and normal
// resize and only shrinks on genuinely narrow terminals so the bar still
// renders gracefully.
func progressBarWidthFor(terminalWidth int) int {
	switch {
	case terminalWidth < 40:
		return 14
	case terminalWidth < 60:
		return 18
	default:
		return progressBarWidth
	}
}

type progressSample struct {
	at    time.Time
	bytes int64
}
type speedEstimator struct{ samples []progressSample }

func (e *speedEstimator) Reset() { e.samples = nil }
func (e *speedEstimator) Add(at time.Time, bytes int64) float64 {
	if bytes < 0 {
		bytes = 0
	}
	if len(e.samples) > 0 && (at.Before(e.samples[len(e.samples)-1].at) || bytes < e.samples[len(e.samples)-1].bytes || (bytes == 0 && e.samples[len(e.samples)-1].bytes > 0)) {
		e.Reset()
	}
	if len(e.samples) > 0 && at.Equal(e.samples[len(e.samples)-1].at) {
		e.samples[len(e.samples)-1].bytes = bytes
		return 0
	}
	e.samples = append(e.samples, progressSample{at, bytes})
	cutoff := at.Add(-speedWindow)
	first := 0
	for first < len(e.samples)-1 && e.samples[first].at.Before(cutoff) {
		first++
	}
	e.samples = e.samples[first:]
	if len(e.samples) < 2 {
		return 0
	}
	d := e.samples[len(e.samples)-1].bytes - e.samples[0].bytes
	dt := e.samples[len(e.samples)-1].at.Sub(e.samples[0].at)
	if d <= 0 || dt <= 0 {
		return 0
	}
	return float64(d) / dt.Seconds()
}

func (e speedEstimator) Ready() bool {
	if len(e.samples) < 2 {
		return false
	}
	d := e.samples[len(e.samples)-1].bytes - e.samples[0].bytes
	dt := e.samples[len(e.samples)-1].at.Sub(e.samples[0].at)
	return d > 0 && dt >= etaWarmup
}

func formatRate(rate float64) string {
	if rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return ""
	}
	units := []string{"B/s", "KiB/s", "MiB/s", "GiB/s"}
	i := 0
	for rate >= 1024 && i < len(units)-1 {
		rate /= 1024
		i++
	}
	if rate >= 10 || i == 0 {
		return fmt.Sprintf("%.0f %s", rate, units[i])
	}
	return fmt.Sprintf("%.1f %s", rate, units[i])
}

func formatDuration(seconds float64) string {
	if seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return ""
	}
	seconds = math.Ceil(seconds)
	if seconds < 60 {
		return fmt.Sprintf("%ds", int64(seconds))
	}
	minutes := int64(seconds) / 60
	if minutes < 60 {
		return fmt.Sprintf("%dm %02ds", minutes, int64(seconds)%60)
	}
	return fmt.Sprintf("%dh %02dm", minutes/60, minutes%60)
}
