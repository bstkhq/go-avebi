package mobile

import (
	"fmt"
	"time"
)

const (
	defaultLogicalWidth  = 640
	defaultLogicalHeight = 360
	logicalShortSide     = 360

	controlMargin    = 16
	controlBottomGap = 8
	controlGap       = 8
	buttonHeight     = 40
	maxButtonWidth   = 104
	minButtonWidth   = 72

	timelineHeight    = 8
	timelineHitMargin = 9
)

type playbackAction uint8

const (
	actionNone playbackAction = iota
	actionOpen
	actionPlayPause
	actionStop
	actionSeekRelative
	actionSeekAbsolute
	actionToggleLoop
)

type controlInput struct {
	action    playbackAction
	seekDelta time.Duration
	seekRatio float64
}

type controlButton struct {
	x      int
	y      int
	width  int
	height int
}

func (b controlButton) contains(x, y int) bool {
	return x >= b.x && x < b.x+b.width &&
		y >= b.y && y < b.y+b.height
}

type controlLayout struct {
	width    int
	height   int
	panelY   int
	statusY  int
	timeline controlButton
	buttons  [6]controlButton
	stacked  bool
}

func logicalSize(outsideWidth, outsideHeight int) (int, int) {
	if outsideWidth <= 0 || outsideHeight <= 0 {
		return defaultLogicalWidth, defaultLogicalHeight
	}
	if outsideWidth >= outsideHeight {
		return roundedScale(outsideWidth, outsideHeight, logicalShortSide), logicalShortSide
	}
	return logicalShortSide, roundedScale(outsideHeight, outsideWidth, logicalShortSide)
}

func roundedScale(value, source, target int) int {
	return (value*target + source/2) / source
}

func newControlLayout(width, height int) controlLayout {
	if width <= 0 || height <= 0 {
		width, height = defaultLogicalWidth, defaultLogicalHeight
	}

	layout := controlLayout{width: width, height: height}
	rowWidth := width - 2*controlMargin
	singleRowWidth := 6*minButtonWidth + 5*controlGap
	layout.stacked = rowWidth < singleRowWidth

	columns := 6
	rows := 1
	if layout.stacked {
		columns = 3
		rows = 2
	}
	maximumRowWidth := columns*maxButtonWidth + (columns-1)*controlGap
	if rowWidth > maximumRowWidth {
		rowWidth = maximumRowWidth
	}
	buttonWidth := (rowWidth - (columns-1)*controlGap) / columns
	actualRowWidth := columns*buttonWidth + (columns-1)*controlGap
	rowX := (width - actualRowWidth) / 2
	bottomRowY := height - controlBottomGap - buttonHeight

	for i := range layout.buttons {
		row := i / columns
		column := i % columns
		layout.buttons[i] = controlButton{
			x:      rowX + column*(buttonWidth+controlGap),
			y:      bottomRowY - (rows-1-row)*(buttonHeight+controlGap),
			width:  buttonWidth,
			height: buttonHeight,
		}
	}

	topButtonY := layout.buttons[0].y
	layout.timeline = controlButton{
		x:      controlMargin,
		y:      topButtonY - timelineHeight - timelineHitMargin - 4,
		width:  width - 2*controlMargin,
		height: timelineHeight,
	}
	layout.statusY = layout.timeline.y - 19
	layout.panelY = layout.statusY - 8
	return layout
}

func controlAt(layout controlLayout, x, y int) controlInput {
	if x >= layout.timeline.x && x <= layout.timeline.x+layout.timeline.width &&
		y >= layout.timeline.y-timelineHitMargin && y < layout.timeline.y+layout.timeline.height+timelineHitMargin {
		return controlInput{
			action:    actionSeekAbsolute,
			seekRatio: float64(x-layout.timeline.x) / float64(layout.timeline.width),
		}
	}

	switch {
	case layout.buttons[0].contains(x, y):
		return controlInput{action: actionOpen}
	case layout.buttons[1].contains(x, y):
		return controlInput{action: actionPlayPause}
	case layout.buttons[2].contains(x, y):
		return controlInput{action: actionStop}
	case layout.buttons[3].contains(x, y):
		return controlInput{action: actionSeekRelative, seekDelta: -5 * time.Second}
	case layout.buttons[4].contains(x, y):
		return controlInput{action: actionSeekRelative, seekDelta: 5 * time.Second}
	case layout.buttons[5].contains(x, y):
		return controlInput{action: actionToggleLoop}
	default:
		return controlInput{}
	}
}

func playbackProgress(position, duration time.Duration) float64 {
	if duration <= 0 || position <= 0 {
		return 0
	}
	if position >= duration {
		return 1
	}
	return float64(position) / float64(duration)
}

func formatDiagnostics(tps, fps float64, width, height int, codec string) string {
	resolution := "--"
	if width > 0 && height > 0 {
		resolution = fmt.Sprintf("%dx%d", width, height)
	}
	if codec == "" {
		codec = "--"
	}
	return fmt.Sprintf("TPS %.1f | FPS %.1f | Video %s | Codec %s", tps, fps, resolution, codec)
}
