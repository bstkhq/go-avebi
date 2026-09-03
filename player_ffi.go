//go:build amd64 || arm64

package avebi

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/audio"
)

var (
	ErrNoVideo         = errors.New("file doesn't include any video stream")
	ErrNilAudioContext = errors.New("file has audio stream but audio.Context is not initialized")
	ErrBadSampleRate   = errors.New("file audio stream and audio context sample rates don't match")
)

// Player is a video player backed by go-ffmpeg-ffi.
type Player struct {
	controller        playbackController
	currentFrame      *ebiten.Image
	currentPresOffset time.Duration
	videoCodec        string
	videoFrameRateNum int
	videoFrameRateDen int
	onBlackFrame      bool
	yuvShader         *ebiten.Shader
	yuvImage          *ebiten.Image
}

func NewPlayer(videoFilename string) (*Player, error) {
	return NewPlayerWithOptions(videoFilename, nil)
}

// PlayerOptions configures local media playback.
type PlayerOptions struct {
	// DisableAudio ignores the media's audio streams and does not require an
	// Ebitengine audio context.
	DisableAudio bool

	// RejectSampleRateMismatch makes opening fail with ErrBadSampleRate when the
	// media and Ebitengine audio context use different sample rates. By default,
	// NewPlayer converts the media sample rate and reports the mismatch through
	// the package Logger.
	RejectSampleRateMismatch bool

	// UseYUVShader keeps supported 8-bit 4:2:0 video frames in YUV and performs
	// color conversion on the GPU. Unsupported formats still use the RGBA path.
	// This is opt-in because chroma upsampling is currently nearest-neighbor;
	// leave it disabled when matching the bilinear RGBA output is more important.
	UseYUVShader bool
}

func NewPlayerWithOptions(videoFilename string, options *PlayerOptions) (*Player, error) {
	if options == nil {
		options = &PlayerOptions{}
	}
	return newFFmpegPlayer(videoFilename, options.DisableAudio, options, nil)
}

func NewStreamPlayer(videoFilename string) (*Player, error) {
	return NewStreamPlayerWithOptions(videoFilename, nil)
}

type StreamOptions struct {
	ConnTimeout time.Duration
	ReadTimeout time.Duration
	// RTSPTransport selects the RTP transport for rtsp:// sources ("tcp",
	// "udp", "udp_multicast", "http"). Empty keeps FFmpeg's default, UDP with
	// TCP fallback. TCP avoids the corrupted frames that packet loss causes on
	// long-haul or VPN links, at the cost of some extra latency. The option is
	// ignored for non-RTSP sources.
	RTSPTransport string
	// UseYUVShader keeps supported 8-bit 4:2:0 video frames in YUV and performs
	// color conversion on the GPU. Unsupported formats still use the RGBA path.
	// This is opt-in because chroma upsampling is currently nearest-neighbor;
	// leave it disabled when matching the bilinear RGBA output is more important.
	UseYUVShader bool
}

func NewStreamPlayerWithOptions(videoFilename string, options *StreamOptions) (*Player, error) {
	if options == nil {
		options = &StreamOptions{}
	}
	if options.ReadTimeout == 0 {
		options.ReadTimeout = 200 * time.Millisecond
	}
	return newFFmpegPlayer(videoFilename, true, nil, options)
}

func newFFmpegPlayer(source string, ignoreAudio bool, playerOptions *PlayerOptions, streamOptions *StreamOptions) (*Player, error) {
	backend := newMediaBackend()
	opts := backendOpenOptions{DisableAudio: ignoreAudio}
	if playerOptions != nil {
		opts.UseYUVShader = playerOptions.UseYUVShader
	} else if streamOptions != nil {
		opts.UseYUVShader = streamOptions.UseYUVShader
	}
	if ctx := audio.CurrentContext(); ctx != nil {
		opts.OutputSampleRate = ctx.SampleRate()
	}
	if streamOptions != nil {
		opts.Live = true
		opts.ConnTimeout = streamOptions.ConnTimeout
		opts.ReadTimeout = streamOptions.ReadTimeout
		opts.RTSPTransport = streamOptions.RTSPTransport
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
		controller = newFFmpegStreamController(backend, source, opts, info, decoder)
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
		if !ignoreAudio {
			if err := checkSampleRateMismatch(info, opts.OutputSampleRate, playerOptions); err != nil {
				_ = decoder.Close()
				return nil, err
			}
		}
		controller = newFFmpegLocalController(decoder)
	}

	if info.Video == nil || info.Video.Width <= 0 || info.Video.Height <= 0 {
		_ = controller.Close()
		return nil, ErrNoVideo
	}
	frameImage := ebiten.NewImageWithOptions(
		image.Rect(0, 0, info.Video.Width, info.Video.Height),
		&ebiten.NewImageOptions{Unmanaged: opts.UseYUVShader},
	)
	frameImage.Fill(color.Black)
	player := &Player{
		controller:        controller,
		currentFrame:      frameImage,
		videoCodec:        info.Video.Codec,
		videoFrameRateNum: info.Video.FrameRateNum,
		videoFrameRateDen: info.Video.FrameRateDen,
		onBlackFrame:      true,
	}
	if opts.UseYUVShader {
		shader, err := loadYUV420Shader()
		if err != nil {
			frameImage.Deallocate()
			_ = controller.Close()
			return nil, fmt.Errorf("compile YUV video shader: %w", err)
		}
		player.yuvShader = shader
		textureWidth, textureHeight := packedYUVTextureSize(info.Video.Width, info.Video.Height)
		player.yuvImage = ebiten.NewImageWithOptions(
			image.Rect(0, 0, textureWidth, textureHeight),
			&ebiten.NewImageOptions{Unmanaged: true},
		)
	}
	return player, nil
}

func checkSampleRateMismatch(info backendMediaInfo, outputSampleRate int, options *PlayerOptions) error {
	if info.Audio == nil || info.Audio.SampleRate <= 0 || outputSampleRate <= 0 || info.Audio.SampleRate == outputSampleRate {
		return nil
	}
	if options != nil && options.RejectSampleRateMismatch {
		return fmt.Errorf("%w: media=%d Hz, audio context=%d Hz", ErrBadSampleRate, info.Audio.SampleRate, outputSampleRate)
	}
	pkgLogger.Printf(
		"WARNING: media audio sample rate = %d Hz, audio context sample rate = %d Hz; converting audio sample rate",
		info.Audio.SampleRate,
		outputSampleRate,
	)
	return nil
}

// CurrentFrame returns the current decoded video frame. It returns nil before
// the first frame is decoded and after Stop is called.
func (p *Player) CurrentFrame() (*ebiten.Image, error) {
	frame, _, err := p.controller.CurrentVideoFrame()
	if err != nil {
		return nil, err
	}
	if frame == nil {
		if p.onBlackFrame {
			return nil, nil
		}
		return p.currentFrame, nil
	}
	if frame.PTS != p.currentPresOffset || p.onBlackFrame {
		if err := p.copyFrame(frame); err != nil {
			return nil, err
		}
		p.currentPresOffset = frame.PTS
	}
	return p.currentFrame, nil
}

func (p *Player) LastPresentationOffset() time.Duration { return p.currentPresOffset }

func (p *Player) Resolution() (int, int) {
	bounds := p.currentFrame.Bounds()
	return bounds.Dx(), bounds.Dy()
}

// VideoCodec returns the short FFmpeg codec name for the selected video
// stream, such as "h264", "hevc", or "av1".
func (p *Player) VideoCodec() string { return p.videoCodec }

// VideoFrameRate returns the selected video stream's average frame rate as a
// rational number. Both values are zero when the media does not report one.
func (p *Player) VideoFrameRate() (numerator, denominator int) {
	return p.videoFrameRateNum, p.videoFrameRateDen
}

func (p *Player) State() (PlaybackState, error) { return p.controller.State() }
func (p *Player) HasEnded() bool                { return p.controller.HasEnded() }

func (p *Player) Play() error {
	if p.controller.HasEnded() {
		_ = p.copyFrame(nil)
		p.currentPresOffset = 0
	}
	return p.controller.Play()
}

func (p *Player) Pause() error { return p.controller.Pause() }

func (p *Player) Stop() error {
	p.currentPresOffset = 0
	_ = p.copyFrame(nil)
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
func (p *Player) Close() error {
	err := p.controller.Close()
	p.currentFrame.Deallocate()
	if p.yuvImage != nil {
		p.yuvImage.Deallocate()
		p.yuvImage = nil
	}
	return err
}

func (p *Player) Seek(position time.Duration) error {
	frame, err := p.controller.Seek(position)
	if err != nil {
		return err
	}
	if frame == nil {
		_ = p.copyFrame(nil)
		p.currentPresOffset, err = p.controller.Position()
		return err
	}
	if err := p.copyFrame(frame); err != nil {
		return err
	}
	p.currentPresOffset = frame.PTS
	return nil
}

func (p *Player) copyFrame(frame *backendFrame) error {
	if frame == nil {
		if !p.onBlackFrame {
			p.currentFrame.Fill(color.Black)
			p.onBlackFrame = true
		}
		return nil
	}
	if frame.Video.Format != backendVideoFormatRGBA && len(frame.Video.YUV) > 0 {
		if err := p.drawYUVFrame(&frame.Video); err != nil {
			return err
		}
		p.onBlackFrame = false
		return nil
	}
	expected := 4 * p.currentFrame.Bounds().Dx() * p.currentFrame.Bounds().Dy()
	if len(frame.Video.RGBA) != expected {
		return fmt.Errorf("avebi: decoded RGBA frame has %d bytes, expected %d", len(frame.Video.RGBA), expected)
	}
	p.currentFrame.WritePixels(frame.Video.RGBA)
	p.onBlackFrame = false
	return nil
}
