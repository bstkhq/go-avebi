package mediaplayer

import (
	"fmt"
	"image/color"
	"time"

	"github.com/erparts/go-avebi"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
)

func (g *Game) Draw(screen *ebiten.Image) {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	screen.Fill(color.Black)
	if g.frame != nil {
		avebi.Draw(screen, g.frame)
	}
	layout := newControlLayout(screen.Bounds().Dx(), screen.Bounds().Dy(), g.filePicker != nil)
	g.drawDiagnostics(screen, layout.width)
	g.drawControls(screen, layout)
	if g.lastError != nil {
		ebitenutil.DebugPrintAt(screen, g.lastError.Error(), controlMargin, 28)
	}
}

func (g *Game) Layout(outsideWidth, outsideHeight int) (int, int) {
	width, height := logicalSize(outsideWidth, outsideHeight)
	g.mutex.Lock()
	g.layoutWidth = width
	g.layoutHeight = height
	g.mutex.Unlock()
	return width, height
}

func escapePressed() bool {
	return inpututil.IsKeyJustPressed(ebiten.KeyEscape)
}

func readControlInput(layout controlLayout) controlInput {
	switch {
	case inpututil.IsKeyJustPressed(ebiten.KeyP), inpututil.IsKeyJustPressed(ebiten.KeySpace):
		return controlInput{action: actionPlayPause}
	case inpututil.IsKeyJustPressed(ebiten.KeyS):
		return controlInput{action: actionStop}
	case inpututil.IsKeyJustPressed(ebiten.KeyL):
		return controlInput{action: actionToggleLoop}
	case inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft):
		return controlInput{action: actionSeekRelative, seekDelta: -time.Second}
	case inpututil.IsKeyJustPressed(ebiten.KeyArrowRight):
		return controlInput{action: actionSeekRelative, seekDelta: time.Second}
	}

	if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
		x, y := ebiten.CursorPosition()
		return controlAt(layout, x, y)
	}
	for _, touchID := range inpututil.AppendJustPressedTouchIDs(nil) {
		x, y := ebiten.TouchPosition(touchID)
		return controlAt(layout, x, y)
	}
	return controlInput{}
}

func (g *Game) drawControls(screen *ebiten.Image, layout controlLayout) {
	ebitenutil.DrawRect(screen, 0, float64(layout.panelY), float64(layout.width), float64(layout.height-layout.panelY), color.RGBA{A: 0xc0})

	status := "No media selected"
	if g.player != nil {
		status = fmt.Sprintf("%s  %s / %s", g.state, formatDuration(g.position), formatDuration(g.duration))
		if g.looping {
			status += "  Loop"
		}
		if g.hasEnded {
			status += "  Ended"
		}
	}
	ebitenutil.DebugPrintAt(screen, status, layout.timeline.x, layout.statusY)

	drawTimeline(screen, layout.timeline, playbackProgress(g.position, g.duration), g.player != nil)
	for _, button := range layout.buttons[:layout.buttonCount] {
		label, enabled, active := g.controlAppearance(button.input)
		drawControlButton(screen, button, label, enabled, active)
	}
}

func (g *Game) controlAppearance(input controlInput) (label string, enabled, active bool) {
	hasPlayer := g.player != nil
	switch input.action {
	case actionOpen:
		if g.pickerPending {
			return "Selecting", false, true
		}
		return "Open", g.filePicker != nil, false
	case actionPlayPause:
		if g.state == avebi.Playing {
			return "Pause", hasPlayer, false
		}
		return "Play", hasPlayer, false
	case actionStop:
		return "Stop", hasPlayer, false
	case actionSeekRelative:
		if input.seekDelta < 0 {
			return "-5s", hasPlayer, false
		}
		return "+5s", hasPlayer, false
	case actionToggleLoop:
		return "Loop", hasPlayer, g.looping
	}
	return "", false, false
}

func (g *Game) drawDiagnostics(screen *ebiten.Image, viewportWidth int) {
	ebitenutil.DrawRect(screen, 0, 0, float64(viewportWidth), 20, color.RGBA{A: 0xc0})
	ebitenutil.DebugPrintAt(
		screen,
		formatDiagnostics(ebiten.ActualTPS(), ebiten.ActualFPS(), g.videoWidth, g.videoHeight, g.videoCodec),
		8,
		6,
	)
}

func drawTimeline(screen *ebiten.Image, timeline controlButton, progress float64, enabled bool) {
	trackColor := color.RGBA{R: 0x55, G: 0x55, B: 0x55, A: 0xff}
	if enabled {
		trackColor = color.RGBA{R: 0xaa, G: 0xaa, B: 0xaa, A: 0xff}
	}
	ebitenutil.DrawRect(screen, float64(timeline.x), float64(timeline.y), float64(timeline.width), float64(timeline.height), trackColor)
	if progress > 0 {
		ebitenutil.DrawRect(screen, float64(timeline.x), float64(timeline.y), float64(timeline.width)*progress, float64(timeline.height), color.RGBA{R: 0x38, G: 0x87, B: 0xd7, A: 0xff})
	}
}

func drawControlButton(screen *ebiten.Image, button controlButton, label string, enabled, active bool) {
	buttonColor := color.RGBA{R: 0x36, G: 0x36, B: 0x36, A: 0xe8}
	if !enabled {
		buttonColor = color.RGBA{R: 0x55, G: 0x55, B: 0x55, A: 0xe8}
	} else if active {
		buttonColor = color.RGBA{R: 0x28, G: 0x69, B: 0xb8, A: 0xe8}
	}
	ebitenutil.DrawRect(screen, float64(button.x), float64(button.y), float64(button.width), float64(button.height), buttonColor)
	textX := button.x + (button.width-len(label)*6)/2
	textY := button.y + (button.height-12)/2
	ebitenutil.DebugPrintAt(screen, label, textX, textY)
}

func formatDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	totalSeconds := duration.Milliseconds() / 1000
	return fmt.Sprintf("%02d:%02d", totalSeconds/60, totalSeconds%60)
}
