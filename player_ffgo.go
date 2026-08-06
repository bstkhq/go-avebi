//go:build !ios && !android && (amd64 || arm64)

package avebi

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/audio"
)

var (
	ErrNoVideo         = errors.New("file doesn't include any video stream")
	ErrNilAudioContext = errors.New("file has audio stream but audio.Context is not initialized")
	ErrBadSampleRate   = errors.New("file audio stream and audio context sample rates don't match")
	ErrTooManyChannels = errors.New("file audio streams with more than 2 channels are not supported")
)

// Player is a video player backed by ffgo.
type Player struct {
	controller        playbackController
	currentFrame      *ebiten.Image
	currentPresOffset time.Duration
	onBlackFrame      bool
}

func NewPlayerWithoutAudio(videoFilename string) (*Player, error) {
	return newFFGOPlayer(videoFilename, true, nil)
}

func NewPlayer(videoFilename string) (*Player, error) {
	return newFFGOPlayer(videoFilename, false, nil)
}

func NewStreamPlayer(videoFilename string) (*Player, error) {
	return NewStreamPlayerWithOptions(videoFilename, nil)
}

type StreamOptions struct {
	ConnTimeout time.Duration
	ReadTimeout time.Duration
}

func NewStreamPlayerWithOptions(videoFilename string, options *StreamOptions) (*Player, error) {
	if options == nil {
		options = &StreamOptions{}
	}
	if options.ReadTimeout == 0 {
		options.ReadTimeout = 200 * time.Millisecond
	}
	return newFFGOPlayer(videoFilename, true, options)
}

func newFFGOPlayer(source string, ignoreAudio bool, streamOptions *StreamOptions) (*Player, error) {
	backend := newMediaBackend()
	opts := backendOpenOptions{DisableAudio: ignoreAudio}
	if ctx := audio.CurrentContext(); ctx != nil {
		opts.OutputSampleRate = ctx.SampleRate()
	}
	if streamOptions != nil {
		opts.Live = true
		opts.ConnTimeout = streamOptions.ConnTimeout
		opts.ReadTimeout = streamOptions.ReadTimeout
		opts.DisableAudio = true
	}

	var controller playbackController
	var info backendMediaInfo
	if opts.Live {
		decoder, err := backend.Open(context.Background(), source, opts)
		if err != nil {
			return nil, err
		}
		info = decoder.Info()
		controller = newFFGOStreamController(backend, source, opts, info, decoder)
	} else {
		decoder, err := backend.Open(context.Background(), source, opts)
		if err != nil {
			return nil, err
		}
		info = decoder.Info()
		if info.Audio != nil && !ignoreAudio && audio.CurrentContext() == nil {
			_ = decoder.Close()
			return nil, ErrNilAudioContext
		}
		controller = newFFGOLocalController(decoder)
	}

	if info.Video == nil || info.Video.Width <= 0 || info.Video.Height <= 0 {
		_ = controller.Close()
		return nil, ErrNoVideo
	}
	image := ebiten.NewImage(info.Video.Width, info.Video.Height)
	image.Fill(color.Black)
	return &Player{
		controller:   controller,
		currentFrame: image,
		onBlackFrame: true,
	}, nil
}

func (p *Player) CurrentFrame() (*ebiten.Image, error) {
	frame, _, err := p.controller.CurrentVideoFrame()
	if err != nil {
		return nil, err
	}
	if frame == nil {
		return p.currentFrame, nil
	}
	if frame.PTS != p.currentPresOffset || p.onBlackFrame {
		expected := 4 * p.currentFrame.Bounds().Dx() * p.currentFrame.Bounds().Dy()
		if len(frame.Video.RGBA) != expected {
			return nil, fmt.Errorf("avebi: decoded RGBA frame has %d bytes, expected %d", len(frame.Video.RGBA), expected)
		}
		p.currentFrame.WritePixels(frame.Video.RGBA)
		p.currentPresOffset = frame.PTS
		p.onBlackFrame = false
	}
	return p.currentFrame, nil
}

func (p *Player) LastPresentationOffset() time.Duration { return p.currentPresOffset }

func (p *Player) NextVideoFrame() (*ebiten.Image, error) { panic("unimplemented") }

func (p *Player) Resolution() (int, int) {
	bounds := p.currentFrame.Bounds()
	return bounds.Dx(), bounds.Dy()
}

func (p *Player) State() (PlaybackState, error) { return p.controller.State() }
func (p *Player) HasEnded() bool                { return p.controller.HasEnded() }

func (p *Player) Play() error {
	if p.controller.HasEnded() {
		p.copyFrame(nil)
		p.currentPresOffset = 0
	}
	return p.controller.Play()
}

func (p *Player) Pause() error { return p.controller.Pause() }

func (p *Player) Stop() error {
	p.currentPresOffset = 0
	p.copyFrame(nil)
	return p.controller.Stop()
}

func (p *Player) Position() (time.Duration, error) { return p.controller.Position() }
func (p *Player) Duration() time.Duration          { return p.controller.Duration() }
func (p *Player) HasAudio() bool                   { return p.controller.HasAudio() }
func (p *Player) GetVolume() float64               { return p.controller.GetVolume() }
func (p *Player) SetVolume(volume float64)         { p.controller.SetVolume(volume) }
func (p *Player) GetMuted() bool                   { return p.controller.GetMuted() }
func (p *Player) SetMuted(muted bool)              { p.controller.SetMuted(muted) }
func (p *Player) SetLooping(looping bool)          { p.controller.SetLooping(looping) }
func (p *Player) GetLooping() bool                 { return p.controller.GetLooping() }
func (p *Player) Error() error                     { return p.controller.Error() }
func (p *Player) Close() error                     { return p.controller.Close() }

func (p *Player) Seek(position time.Duration) error {
	frame, err := p.controller.Seek(position)
	if err != nil {
		return err
	}
	if frame == nil {
		p.copyFrame(nil)
		p.currentPresOffset, err = p.controller.Position()
		return err
	}
	p.copyFrame(frame)
	p.currentPresOffset = frame.PTS
	return nil
}

func (p *Player) copyFrame(frame *backendFrame) {
	if frame == nil {
		if !p.onBlackFrame {
			p.currentFrame.Fill(color.Black)
			p.onBlackFrame = true
		}
		return
	}
	p.currentFrame.WritePixels(frame.Video.RGBA)
	p.onBlackFrame = false
}
