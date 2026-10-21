package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
)

func (m model) dockVisible() bool {
	return m.busy != "" || m.operationTitle != ""
}

// Reserve a fixed budget for every phase and its outcome. Only resizing or
// dismissing the result changes this geometry; idle screens reserve no space.
func (m model) operationDockHeight() int {
	if !m.dockVisible() || m.height < 10 || m.width > 0 && m.width < 30 {
		return 0
	}
	shellRows := 3 + strings.Count(m.tabsLine(), "\n") + 1
	return min(6, max(4, (m.height-shellRows)/3))
}

func (m model) operationResultViewport() viewport.Model {
	v := viewport.New(viewport.WithWidth(viewWidth(m.width)), viewport.WithHeight(max(1, m.operationDockHeight()-3)))
	content := m.status
	if m.err != nil {
		if content != "" {
			content += "\n"
		}
		content += m.err.Error()
	}
	v.SetContent(strings.Join(wrappedLines(content, viewWidth(m.width)), "\n"))
	v.SetYOffset(m.dockScroll)
	return v
}

func (m model) dockScrollable() bool {
	return m.operationDockHeight() > 0 && m.operationTitle != "" && m.busy == "" && m.operationResultViewport().TotalLineCount() > max(1, m.operationDockHeight()-3)
}

func (m model) operationDockLines() []string {
	height := m.operationDockHeight()
	if height == 0 {
		return nil
	}
	title := m.busy
	if title == "" {
		title = m.operationTitle
	}
	lines := []string{m.separator()}
	if m.busy == "" {
		result := "COMPLETED"
		style := m.theme.success
		if m.status == "Operation cancelled" {
			result, style = "CANCELLED", m.theme.muted
		}
		if m.feedbackPartial {
			result, style = "COMPLETED WITH FAILURES", m.theme.warning
		}
		if m.err != nil {
			result, style = "FAILED", m.theme.danger
		}
		lines = append(lines, style.Render(fit(result+" · "+title, m.width)))
		lines = append(lines, strings.Split(style.Render(m.operationResultViewport().View()), "\n")...)
	} else {
		progress := m.operationProgressLines()
		if height == 4 && progress[0] != "" {
			// Let the observed phase and bar own the compact dock; operation
			// identity also remains in the detail status and selected action.
			lines = append(lines, progress[:2]...)
		} else {
			lines = append(lines, m.theme.panel.Render(fit(title, m.width)))
			lines = append(lines, progress...)
		}
	}
	// Lip Gloss/Bubbles own visual widths; the shell owns only the row budget.
	lines = lines[:min(len(lines), height-1)]
	for len(lines) < height-1 {
		lines = append(lines, "")
	}
	return append(lines, m.separator())
}

// Render only observed counters and the existing speed estimator. Unknown
// totals leave the bar empty rather than implying overall operation progress.
func (m model) operationProgressLines() []string {
	phase := title(string(m.progress.Stage))
	if m.progress.Description != "" {
		phase = m.progress.Description
	}
	if m.progress.Item > 0 && m.progress.Total > 0 {
		phase += fmt.Sprintf(" %d/%d", m.progress.Item, m.progress.Total)
	}
	if m.progress.AppID != "" {
		phase += " · " + m.progress.AppID
	}
	barLine, metrics := "", ""
	if m.progress.BytesTotal > 0 {
		done := m.progress.BytesDone
		if done < 0 {
			done = 0
		}
		if done > m.progress.BytesTotal {
			done = m.progress.BytesTotal
		}
		bar := m.progressBar
		if bar.Width() == 0 {
			bar = newProgress(m.color)
		}
		bar.SetWidth(min(progressBarWidthFor(m.width), max(1, viewWidth(m.width)-8)))
		barLine = bar.ViewAs(float64(done) / float64(m.progress.BytesTotal))
		metrics = bytesLabel(m.progress.BytesDone) + " / " + bytesLabel(m.progress.BytesTotal)
	} else if m.progress.BytesDone > 0 {
		metrics = bytesLabel(m.progress.BytesDone)
	}
	if m.progressSpeed > 0 {
		metrics += "  " + formatRate(m.progressSpeed)
		if m.estimator.Ready() && m.progress.BytesTotal > m.progress.BytesDone && m.progress.BytesTotal > 0 {
			metrics += " · ETA ~" + formatDuration(float64(m.progress.BytesTotal-m.progress.BytesDone)/m.progressSpeed)
		}
	}
	return []string{fit(phase, m.width), fit(barLine, m.width), fit(metrics, m.width)}
}
