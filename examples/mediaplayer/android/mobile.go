//go:build android

// Package mobile binds the shared media player for an Android native host.
package mobile

import (
	"errors"
	"time"

	mediaplayer "github.com/erparts/go-avebi/examples/mediaplayer"
	ebitenmobile "github.com/hajimehoshi/ebiten/v2/mobile"
)

// FilePickerHandler receives the result of apk-ebiten-builder's optional
// Android document picker.
type FilePickerHandler interface {
	OnResult(path, message string)
}

// FilePickerBridge is implemented by apk-ebiten-builder when the Android host
// discovers this optional gomobile contract.
type FilePickerBridge interface {
	SetHandler(FilePickerHandler)
	Open(mimeType string)
}

var currentGame = mediaplayer.New(mediaplayer.Options{})

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
// document picker.
func RegisterFilePickerBridge(bridge FilePickerBridge) {
	bridge.SetHandler(filePickerHandler{})
	currentGame.SetFilePicker(func() {
		bridge.Open("*/*")
	})
}

type filePickerHandler struct{}

// OnResult receives a cache-backed local path, cancellation, or an asynchronous
// Android picker error from apk-ebiten-builder.
func (filePickerHandler) OnResult(path, message string) {
	var err error
	if message != "" {
		err = errors.New(message)
	}
	currentGame.CompleteFilePicker(path, err)
}

// Open loads a local media file and starts synchronized playback.
func Open(path string) {
	currentGame.QueueOpen(path, false)
}

// Seek moves playback to the requested position in milliseconds.
func Seek(milliseconds int64) {
	currentGame.QueueSeek(time.Duration(milliseconds) * time.Millisecond)
}

// Close releases the current player.
func Close() {
	currentGame.QueueClose()
}

// Error returns the latest asynchronous playback error.
func Error() string {
	return currentGame.Error()
}
