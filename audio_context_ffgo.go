//go:build avebi_ffgo && !ios && !android && (amd64 || arm64)

package avebi

import (
	"context"
	"errors"

	"github.com/hajimehoshi/ebiten/v2/audio"
)

var ErrNoAudio = errors.New("media contains no audio")
var ErrNonNilAudioContext = errors.New("audio context already initialized")

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

func GetMediaAudioSampleRate(videoFilename string) (int, error) {
	info, err := newMediaBackend().Probe(context.Background(), videoFilename, backendOpenOptions{})
	if err != nil {
		return 0, err
	}
	if info.Audio == nil || info.Audio.SampleRate <= 0 {
		return 0, ErrNoAudio
	}
	return info.Audio.SampleRate, nil
}
