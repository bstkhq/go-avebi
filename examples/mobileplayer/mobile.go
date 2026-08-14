//go:build android || ios

// Package mobile demonstrates embedding avebi in an Ebitengine mobile
// application. It can be packaged as an Android APK or an iOS XCFramework.
package mobile

import (
	"errors"
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

const (
	logicalWidth  = 640
	logicalHeight = 360
	pickerX       = 16
	pickerY       = logicalHeight - 48
	pickerWidth   = 120
	pickerHeight  = 32
)

// FilePickerBridge is implemented by apk-ebiten-builder when the Android host
// discovers this optional gomobile contract.
type FilePickerBridge interface {
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
}

func (g *game) Update() error {
	pickerPressed := pickerButtonPressed()
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
	if g.player != nil {
		frame, err := g.player.CurrentFrame()
		if err != nil {
			g.recordError(err)
		} else {
			g.frame = frame
			g.recordError(g.player.Error())
		}
	}
	if pickerPressed && g.filePicker != nil && !g.pickerRequested {
		g.pickerRequested = true
		picker = g.filePicker
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
	if g.filePicker != nil {
		buttonColor := color.RGBA{R: 0x28, G: 0x69, B: 0xb8, A: 0xe8}
		label := "Open media"
		if g.pickerRequested {
			buttonColor = color.RGBA{R: 0x55, G: 0x55, B: 0x55, A: 0xe8}
			label = "Selecting..."
		}
		ebitenutil.DrawRect(screen, pickerX, pickerY, pickerWidth, pickerHeight, buttonColor)
		ebitenutil.DebugPrintAt(screen, label, pickerX+12, pickerY+10)
	}
	if g.lastError != "" {
		ebitenutil.DebugPrintAt(screen, g.lastError, 16, 16)
	}
}

func (*game) Layout(int, int) (int, int) {
	return logicalWidth, logicalHeight
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
	g.removeOwnedPathLocked()
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

func pickerButtonPressed() bool {
	if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
		x, y := ebiten.CursorPosition()
		if pickerButtonContains(x, y) {
			return true
		}
	}
	for _, touchID := range inpututil.AppendJustPressedTouchIDs(nil) {
		x, y := ebiten.TouchPosition(touchID)
		if pickerButtonContains(x, y) {
			return true
		}
	}
	return false
}

func pickerButtonContains(x, y int) bool {
	return x >= pickerX && x < pickerX+pickerWidth &&
		y >= pickerY && y < pickerY+pickerHeight
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
	currentGame.mutex.Lock()
	defer currentGame.mutex.Unlock()
	currentGame.filePicker = bridge
	currentGame.pickerRequested = false
}

// FilePickerResult receives a cache-backed local path, cancellation, or an
// asynchronous Android picker error from apk-ebiten-builder.
func FilePickerResult(path, message string) {
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
