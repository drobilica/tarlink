package tui

import (
	"fmt"
	"math"
	"time"
)

const speedWindow = 5 * time.Second

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
	if len(e.samples) > 0 && bytes < e.samples[len(e.samples)-1].bytes {
		e.Reset()
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
