package tui

import "github.com/drobilica/tarlink/internal/app"

// rect is terminal-cell geometry. Coordinates are relative to the complete
// Bubble Tea view and are also used for input hit testing.
type rect struct {
	x, y, width, height int
}

func (r rect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.width && y >= r.y && y < r.y+r.height
}

type layoutMode uint8

const (
	modeNarrow layoutMode = iota
	modeWide
)

// Layout is the sole geometry contract for the TUI shell. Renderers and
// input handlers must consume these rectangles instead of deriving offsets.
type Layout struct {
	Width, Height int
	Mode          layoutMode
	Header        rect
	Navigation    rect
	Workspace     rect
	Applications  rect
	Details       rect
	Activity      rect
	Footer        rect
}

const (
	headerRows     = 1
	navigationRows = 2
	activityRows   = 2
	footerRows     = 1
)

func layoutFor(width, height int) Layout {
	width = viewWidth(width)
	if height <= 0 {
		height = 12
	}
	contentHeight := max(0, height-headerRows-navigationRows-activityRows-footerRows)
	workspace := rect{x: 0, y: headerRows + navigationRows, width: width, height: contentHeight}
	wide := width >= 72
	result := Layout{Width: width, Height: height, Mode: modeNarrow,
		Header:     rect{width: width, height: headerRows},
		Navigation: rect{y: headerRows, width: width, height: navigationRows},
		Workspace:  workspace,
		Activity:   rect{y: workspace.y + workspace.height, width: width, height: activityRows},
		Footer:     rect{y: workspace.y + workspace.height + activityRows, width: width, height: footerRows}}
	if wide {
		result.Mode = modeWide
		left := max(1, (width-1)/2)
		result.Applications = rect{x: 0, y: workspace.y, width: left, height: workspace.height}
		result.Details = rect{x: left + 1, y: workspace.y, width: max(1, width-left-1), height: workspace.height}
	} else {
		result.Applications = workspace
		result.Details = workspace
	}
	return result
}

func (m model) layout() Layout { return layoutFor(m.width, m.height) }

func (m model) applicationValues() []app.Application { return m.visibleApplications() }
