//go:build android

package avebi

import "errors"

var ErrNoAudio error = errors.New("media contains no audio")
var ErrNonNilAudioContext = errors.New("audio context already initialized")

// Creates an ebitengine audio context based on the given media.
//
// On Android this package provides a mock implementation intended to keep
// dependent tools buildable without requiring the desktop video backend.
func CreateAudioContextForMedia(videoFilename string) error {
	return nil
}

// If the media has no audio, [ErrNoAudio] will be returned.
//
// On Android this mock implementation doesn't inspect the media and reports
// that audio metadata is unavailable.
func GetMediaAudioSampleRate(videoFilename string) (int, error) {
	return 0, ErrNoAudio
}
