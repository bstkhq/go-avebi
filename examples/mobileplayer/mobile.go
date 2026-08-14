//go:build android || ios

// Package mobile demonstrates embedding avebi in an Ebitengine mobile
// application. It can be packaged as an Android APK or an iOS XCFramework.
package mobile

import (
	"errors"
	"sync"
	"time"

	"github.com/erparts/go-avebi"
	"github.com/hajimehoshi/ebiten/v2"
	ebitenmobile "github.com/hajimehoshi/ebiten/v2/mobile"
)

const (
	logicalWidth  = 640
	logicalHeight = 360
)

type game struct {
	mutex          sync.Mutex
	player         *avebi.Player
	frame          *ebiten.Image
	pendingPath    string
	pendingSeek    time.Duration
	openRequested  bool
	seekRequested  bool
	closeRequested bool
	lastError      string
}

func (g *game) Update() error {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	if g.closeRequested {
		g.closeRequested = false
		g.closeLocked()
	}
	if g.openRequested {
		g.openRequested = false
		g.openLocked(g.pendingPath)
	}
	if g.seekRequested {
		g.seekRequested = false
		if g.player != nil {
			g.recordError(g.player.Seek(g.pendingSeek))
		}
	}
	if g.player == nil {
		return nil
	}
	frame, err := g.player.CurrentFrame()
	if err != nil {
		g.recordError(err)
		return nil
	}
	g.frame = frame
	g.recordError(g.player.Error())
	return nil
}

func (g *game) Draw(screen *ebiten.Image) {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	if g.frame != nil {
		avebi.Draw(screen, g.frame)
	}
}

func (*game) Layout(int, int) (int, int) {
	return logicalWidth, logicalHeight
}

func (g *game) openLocked(path string) {
	if g.player != nil {
		if err := g.player.Close(); err != nil {
			g.recordError(err)
			return
		}
		g.player = nil
		g.frame = nil
	}

	err := avebi.CreateAudioContextForMedia(path)
	if err != nil && !errors.Is(err, avebi.ErrNoAudio) && !errors.Is(err, avebi.ErrNonNilAudioContext) {
		g.recordError(err)
		return
	}
	player, err := avebi.NewPlayer(path)
	if err != nil {
		g.recordError(err)
		return
	}
	if err := player.Play(); err != nil {
		_ = player.Close()
		g.recordError(err)
		return
	}
	g.player = player
	g.lastError = ""
}

func (g *game) closeLocked() {
	if g.player != nil {
		g.recordError(g.player.Close())
	}
	g.player = nil
	g.frame = nil
}

func (g *game) recordError(err error) {
	if err != nil {
		g.lastError = err.Error()
	}
}

var currentGame = &game{}

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

// Open loads a local media file and starts synchronized playback.
func Open(path string) {
	currentGame.mutex.Lock()
	defer currentGame.mutex.Unlock()
	currentGame.pendingPath = path
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
