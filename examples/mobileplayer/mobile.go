//go:build android || ios

// Package mobile demonstrates embedding avebi in an Ebitengine mobile
// application. It can be packaged as an Android APK or an iOS XCFramework.
package mobile

import (
	"errors"
	"fmt"
	"image/color"
	"os"
	"sync"
	"time"

	"github.com/erparts/go-avebi"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	ebitenmobile "github.com/hajimehoshi/ebiten/v2/mobile"
)

// FilePickerBridge is implemented by apk-ebiten-builder when the Android host
// discovers this optional gomobile contract.
type FilePickerHandler interface {
	OnResult(path, message string)
}

type FilePickerBridge interface {
	SetHandler(FilePickerHandler)
	Open(mimeType string)
}

type game struct {
	mutex           sync.Mutex
	player          *avebi.Player
	frame           *ebiten.Image
	filePicker      FilePickerBridge
	pendingPath     string
	pendingSeek     time.Duration
	pendingOwned    bool
	ownedPath       string
	openRequested   bool
	seekRequested   bool
	closeRequested  bool
	pickerRequested bool
	lastError       string
	position        time.Duration
	duration        time.Duration
	state           avebi.PlaybackState
	looping         bool
	hasEnded        bool
	layoutWidth     int
	layoutHeight    int
}

func (g *game) Update() error {
	g.mutex.Lock()
	layout := newControlLayout(g.layoutWidth, g.layoutHeight)
	g.mutex.Unlock()
	input := readControlInput(layout)
	var picker FilePickerBridge

	g.mutex.Lock()
	if g.closeRequested {
		g.closeRequested = false
		g.closeLocked()
	}
	if g.openRequested {
		g.openRequested = false
		g.openLocked(g.pendingPath, g.pendingOwned)
		g.pendingOwned = false
	}
	if g.seekRequested {
		g.seekRequested = false
		if g.player != nil {
			g.recordError(g.player.Seek(g.pendingSeek))
		}
	}
	if input.action == actionOpen && g.filePicker != nil && !g.pickerRequested {
		g.pickerRequested = true
		picker = g.filePicker
	} else {
		g.applyPlaybackControlLocked(input)
	}
	if g.player != nil {
		g.refreshPlayerLocked()
	}
	g.mutex.Unlock()

	if picker != nil {
		picker.Open("*/*")
	}
	return nil
}

func (g *game) Draw(screen *ebiten.Image) {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	if g.frame != nil {
		avebi.Draw(screen, g.frame)
	} else {
		screen.Fill(color.Black)
	}
	layout := newControlLayout(screen.Bounds().Dx(), screen.Bounds().Dy())
	g.drawDiagnostics(screen, layout.width)
	g.drawControls(screen, layout)
	if g.lastError != "" {
		ebitenutil.DebugPrintAt(screen, g.lastError, controlMargin, 28)
	}
}

func (g *game) Layout(outsideWidth, outsideHeight int) (int, int) {
	width, height := logicalSize(outsideWidth, outsideHeight)
	g.mutex.Lock()
	g.layoutWidth = width
	g.layoutHeight = height
	g.mutex.Unlock()
	return width, height
}

func (g *game) openLocked(path string, owned bool) {
	if g.player != nil {
		if err := g.player.Close(); err != nil {
			g.recordError(err)
			if owned {
				_ = os.Remove(path)
			}
			return
		}
		g.player = nil
		g.frame = nil
		g.resetPlaybackStateLocked()
		g.removeOwnedPathLocked()
	}

	err := avebi.CreateAudioContextForMedia(path)
	if err != nil && !errors.Is(err, avebi.ErrNoAudio) && !errors.Is(err, avebi.ErrNonNilAudioContext) {
		g.recordError(err)
		if owned {
			_ = os.Remove(path)
		}
		return
	}
	player, err := avebi.NewPlayer(path)
	if err != nil {
		g.recordError(err)
		if owned {
			_ = os.Remove(path)
		}
		return
	}
	if err := player.Play(); err != nil {
		_ = player.Close()
		g.recordError(err)
		if owned {
			_ = os.Remove(path)
		}
		return
	}
	g.player = player
	g.position = 0
	g.duration = player.Duration()
	g.state = avebi.Playing
	g.looping = player.GetLooping()
	g.hasEnded = false
	if owned {
		g.ownedPath = path
	}
	g.lastError = ""
}

func (g *game) closeLocked() {
	if g.player != nil {
		g.recordError(g.player.Close())
	}
	g.player = nil
	g.frame = nil
	g.resetPlaybackStateLocked()
	g.removeOwnedPathLocked()
}

func (g *game) resetPlaybackStateLocked() {
	g.position = 0
	g.duration = 0
	g.state = avebi.Stopped
	g.looping = false
	g.hasEnded = false
}

func (g *game) removeOwnedPathLocked() {
	if g.ownedPath == "" {
		return
	}
	_ = os.Remove(g.ownedPath)
	g.ownedPath = ""
}

func (g *game) recordError(err error) {
	if err != nil {
		g.lastError = err.Error()
	}
}

var currentGame = &game{}

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

func (g *game) applyPlaybackControlLocked(input controlInput) {
	if g.player == nil {
		return
	}

	switch input.action {
	case actionPlayPause:
		state, err := g.player.State()
		if err != nil {
			g.recordError(err)
			return
		}
		if state == avebi.Playing {
			g.recordError(g.player.Pause())
		} else {
			g.recordError(g.player.Play())
		}
	case actionStop:
		g.recordError(g.player.Stop())
	case actionSeekRelative:
		g.seekLocked(g.position + input.seekDelta)
	case actionSeekAbsolute:
		g.seekLocked(time.Duration(float64(g.duration) * input.seekRatio))
	case actionToggleLoop:
		g.player.SetLooping(!g.player.GetLooping())
	}
}

func (g *game) seekLocked(position time.Duration) {
	if position < 0 {
		position = 0
	}
	if g.duration > 0 && position > g.duration {
		position = g.duration
	}
	g.recordError(g.player.Seek(position))
}

func (g *game) refreshPlayerLocked() {
	frame, err := g.player.CurrentFrame()
	if err != nil {
		g.recordError(err)
	} else {
		g.frame = frame
	}
	if position, err := g.player.Position(); err != nil {
		g.recordError(err)
	} else {
		g.position = position
	}
	if state, err := g.player.State(); err != nil {
		g.recordError(err)
	} else {
		g.state = state
	}
	g.duration = g.player.Duration()
	g.looping = g.player.GetLooping()
	g.hasEnded = g.player.HasEnded()
	g.recordError(g.player.Error())
}

func (g *game) drawControls(screen *ebiten.Image, layout controlLayout) {
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

	openLabel := "Open"
	if g.pickerRequested {
		openLabel = "Selecting"
	}
	drawControlButton(screen, layout.buttons[0], openLabel, g.filePicker != nil && !g.pickerRequested, true)

	playLabel := "Play"
	if g.state == avebi.Playing {
		playLabel = "Pause"
	}
	hasPlayer := g.player != nil
	drawControlButton(screen, layout.buttons[1], playLabel, hasPlayer, false)
	drawControlButton(screen, layout.buttons[2], "Stop", hasPlayer, false)
	drawControlButton(screen, layout.buttons[3], "-5s", hasPlayer, false)
	drawControlButton(screen, layout.buttons[4], "+5s", hasPlayer, false)
	drawControlButton(screen, layout.buttons[5], "Loop", hasPlayer, g.looping)
}

func (g *game) drawDiagnostics(screen *ebiten.Image, viewportWidth int) {
	videoWidth, videoHeight := 0, 0
	codec := ""
	if g.player != nil {
		videoWidth, videoHeight = g.player.Resolution()
		codec = g.player.VideoCodec()
	}

	ebitenutil.DrawRect(screen, 0, 0, float64(viewportWidth), 20, color.RGBA{A: 0xc0})
	ebitenutil.DebugPrintAt(
		screen,
		formatDiagnostics(ebiten.ActualTPS(), ebiten.ActualFPS(), videoWidth, videoHeight, codec),
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

func init() {
	ebitenmobile.SetGame(currentGame)
}

// IMEBridge matches the Android input bridge provided by apk-ebiten-builder.
type IMEBridge interface {
	Show(inputType, imeOptions int32)
	Hide()
	Composing() string
}

// RegisterIMEBridge receives apk-ebiten-builder's Android input bridge. This
// player example does not currently display text input controls.
func RegisterIMEBridge(IMEBridge) {}

// SetAndroidID receives apk-ebiten-builder's stable application identifier.
// Playback does not require it, so the example intentionally ignores it.
func SetAndroidID(int64) {}

// SetTimezone is an optional apk-ebiten-builder hook. Media timestamps are
// relative durations, so the example does not need the device timezone.
func SetTimezone(string) {}

// RegisterFilePickerBridge receives apk-ebiten-builder's optional Android
// document picker. iOS hosts can continue to call Open with a local path.
func RegisterFilePickerBridge(bridge FilePickerBridge) {
	bridge.SetHandler(filePickerHandler{})
	currentGame.mutex.Lock()
	defer currentGame.mutex.Unlock()
	currentGame.filePicker = bridge
	currentGame.pickerRequested = false
}

type filePickerHandler struct{}

// OnResult receives a cache-backed local path, cancellation, or an asynchronous
// Android picker error from apk-ebiten-builder.
func (filePickerHandler) OnResult(path, message string) {
	currentGame.mutex.Lock()
	defer currentGame.mutex.Unlock()
	currentGame.pickerRequested = false
	if message != "" {
		currentGame.lastError = message
		return
	}
	if path == "" {
		return
	}
	currentGame.pendingPath = path
	currentGame.pendingOwned = true
	currentGame.openRequested = true
}

// Open loads a local media file and starts synchronized playback.
func Open(path string) {
	currentGame.mutex.Lock()
	defer currentGame.mutex.Unlock()
	currentGame.pendingPath = path
	currentGame.pendingOwned = false
	currentGame.openRequested = true
}

// Seek moves playback to the requested position in milliseconds.
func Seek(milliseconds int64) {
	currentGame.mutex.Lock()
	defer currentGame.mutex.Unlock()
	currentGame.pendingSeek = time.Duration(milliseconds) * time.Millisecond
	currentGame.seekRequested = true
}

// Close releases the current player.
func Close() {
	currentGame.mutex.Lock()
	defer currentGame.mutex.Unlock()
	currentGame.closeRequested = true
}

// Error returns the latest asynchronous playback error.
func Error() string {
	currentGame.mutex.Lock()
	defer currentGame.mutex.Unlock()
	return currentGame.lastError
}
