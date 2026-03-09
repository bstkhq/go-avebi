//go:build !android

package avebi

import (
	"errors"

	"github.com/erparts/reisen"
	"github.com/hajimehoshi/ebiten/v2/audio"
)

var ErrNoAudio error = errors.New("media contains no audio")
var ErrNonNilAudioContext = errors.New("audio context already initialized")

// Creates an ebitengine audio context based on the given media.
func CreateAudioContextForMedia(videoFilename string) error {
	if audio.CurrentContext() != nil {
		return ErrNonNilAudioContext
	}

	sampleRate, err := GetMediaAudioSampleRate(videoFilename)
	if err != nil {
		return err
	}
	_ = audio.NewContext(sampleRate)
	return nil
}

// If the media has no audio, [ErrNoAudio] will be returned.
func GetMediaAudioSampleRate(videoFilename string) (int, error) {
	container, err := reisen.NewMedia(videoFilename)
	if err != nil {
		return 0, err
	}

	audioStreams := container.AudioStreams()
	if len(audioStreams) == 0 {
		return 0, ErrNoAudio
	}

	return audioStreams[0].SampleRate(), nil
}
