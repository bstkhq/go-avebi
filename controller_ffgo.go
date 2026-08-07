//go:build !ios && !android && (amd64 || arm64)

package avebi

import (
	"context"
	"errors"
	"io"
	"math"
	"sync"
	"time"

	"github.com/hajimehoshi/ebiten/v2/audio"
)

const ffgoPlayerBufferSize = 200 * time.Millisecond

type ffgoAudioPlayer interface {
	Play()
	Pause()
	IsPlaying() bool
	Position() time.Duration
	SetBufferSize(time.Duration)
	SetVolume(float64)
	Close() error
}

type ffgoAudioPlayerFactory func(io.Reader) (ffgoAudioPlayer, error)

type playbackController interface {
	State() (PlaybackState, error)
	Play() error
	Pause() error
	Stop() error
	Close() error
	Seek(time.Duration) (*backendFrame, error)
	Position() (time.Duration, error)
	Duration() time.Duration
	SetLooping(bool)
	GetLooping() bool
	HasEnded() bool
	HasAudio() bool
	GetVolume() float64
	SetVolume(float64)
	GetMuted() bool
	SetMuted(bool)
	CurrentVideoFrame() (*backendFrame, bool, error)
	Error() error
}

var _ playbackController = (*ffgoLocalController)(nil)

type ffgoLocalController struct {
	mutex   sync.Mutex
	decoder mediaDecoder
	info    backendMediaInfo

	state   PlaybackState
	looping bool
	ended   bool
	closed  bool

	referenceTime     time.Time
	referencePosition time.Duration
	staticPosition    time.Duration

	lastVideo    *backendFrame
	videoQueue   []backendFrame
	videoBuffers backendVideoBufferPool
	audioQueue   []byte

	hasAudio           bool
	audioPlayer        ffgoAudioPlayer
	newAudioPlayer     ffgoAudioPlayerFactory
	audioEOF           bool
	firstAudioPTS      time.Duration
	waitingForAudioPTS bool
	volume             float64
	muted              bool

	decodeErr error
}

func newFFGOLocalController(decoder mediaDecoder) *ffgoLocalController {
	info := decoder.Info()
	return &ffgoLocalController{
		decoder:            decoder,
		info:               info,
		state:              Stopped,
		hasAudio:           info.Audio != nil,
		newAudioPlayer:     newEbitengineAudioPlayer,
		waitingForAudioPTS: info.Audio != nil,
		volume:             1,
		videoQueue:         make([]backendFrame, 0, 8),
		audioQueue:         make([]byte, 0, 4096),
	}
}

func (c *ffgoLocalController) State() (PlaybackState, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if _, err := c.noLockPosition(time.Now()); err != nil {
		return invalidPlaybackState, err
	}
	return c.state, nil
}

func (c *ffgoLocalController) Play() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.closed {
		return errors.New("avebi: player is closed")
	}
	if c.state == Playing {
		return nil
	}
	if c.ended {
		if err := c.noLockCloseAudioPlayer(); err != nil {
			return err
		}
		if err := c.decoder.Seek(0); err != nil {
			return err
		}
		c.noLockResetPlayback(0)
	}

	if c.hasAudio {
		if c.audioPlayer == nil {
			if err := c.noLockCreateAudioPlayer(); err != nil {
				return err
			}
		}
		c.state = Playing
		c.audioPlayer.Play()
		return nil
	}

	c.referenceTime = time.Now()
	c.state = Playing
	return nil
}

func (c *ffgoLocalController) Pause() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.state != Playing {
		return nil
	}
	position, err := c.noLockPosition(time.Now())
	if err != nil {
		return err
	}
	if c.ended {
		return nil
	}
	c.staticPosition = position
	c.referencePosition = position
	c.referenceTime = time.Now()
	c.state = Paused
	if c.audioPlayer != nil {
		c.audioPlayer.Pause()
	}
	return nil
}

func (c *ffgoLocalController) Stop() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.closed {
		return nil
	}
	if err := c.noLockCloseAudioPlayer(); err != nil {
		return err
	}
	if err := c.decoder.Seek(0); err != nil {
		return err
	}
	c.noLockResetPlayback(0)
	c.state = Stopped
	return nil
}

func (c *ffgoLocalController) Close() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	playerErr := c.noLockCloseAudioPlayer()
	c.noLockRecycleVideoFrames()
	decoderErr := c.decoder.Close()
	c.videoBuffers.clear()
	return errors.Join(playerErr, decoderErr)
}

func (c *ffgoLocalController) Seek(position time.Duration) (*backendFrame, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.closed {
		return nil, errors.New("avebi: player is closed")
	}

	position = max(position, 0)
	if c.info.Duration > 0 {
		position = min(position, c.info.Duration)
	}
	previousState := c.state
	if err := c.noLockCloseAudioPlayer(); err != nil {
		return nil, err
	}
	if err := c.decoder.Seek(position); err != nil {
		return nil, err
	}
	c.noLockResetPlayback(position)

	if c.info.Duration > 0 && position >= c.info.Duration {
		c.staticPosition = c.info.Duration
		c.referencePosition = c.info.Duration
		c.ended = true
		c.state = Stopped
		return nil, nil
	}

	frame, err := c.noLockPrimeAfterSeek()
	if err != nil {
		return nil, err
	}
	if frame != nil {
		c.staticPosition = frame.PTS
		c.referencePosition = frame.PTS
	}
	c.referenceTime = time.Now()

	switch previousState {
	case Playing:
		c.state = Playing
		if c.hasAudio {
			if err := c.noLockCreateAudioPlayer(); err != nil {
				return nil, err
			}
			c.audioPlayer.Play()
		}
	case Paused:
		c.state = Paused
	default:
		if position == 0 {
			c.state = Stopped
		} else {
			c.state = Paused
		}
	}

	return frame, nil
}

// noLockPrimeAfterSeek buffers accepted audio until the first accepted video
// frame. The decoder's Seek contract has already filtered both streams to the
// requested target.
func (c *ffgoLocalController) noLockPrimeAfterSeek() (*backendFrame, error) {
	for {
		frame, err := c.decoder.ReadFrame(context.Background(), &c.videoBuffers)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return c.lastVideo, nil
			}
			return nil, err
		}
		switch frame.Kind {
		case backendFrameVideo:
			c.noLockReplaceLastVideo(frame)
			return c.lastVideo, nil
		case backendFrameAudio:
			if c.hasAudio {
				if c.waitingForAudioPTS {
					c.firstAudioPTS = frame.PTS
					c.waitingForAudioPTS = false
				}
				c.audioQueue = append(c.audioQueue, frame.Audio.PCM...)
			}
		}
	}
}

func (c *ffgoLocalController) Position() (time.Duration, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.noLockPosition(time.Now())
}

func (c *ffgoLocalController) noLockPosition(now time.Time) (time.Duration, error) {
	if c.ended {
		return c.info.Duration, nil
	}

	var position time.Duration
	switch {
	case c.state != Playing:
		position = c.staticPosition
	case c.hasAudio && c.audioPlayer != nil && !c.waitingForAudioPTS:
		position = c.firstAudioPTS + c.audioPlayer.Position()
	case c.hasAudio:
		position = c.staticPosition
	default:
		if c.referenceTime.IsZero() {
			c.referenceTime = now
		}
		position = c.referencePosition + now.Sub(c.referenceTime)
	}
	if c.hasAudio && c.audioEOF && c.state == Playing && c.audioPlayer != nil && !c.audioPlayer.IsPlaying() && c.info.Duration > 0 {
		position = c.info.Duration
	}

	if c.info.Duration <= 0 || position < c.info.Duration {
		return max(position, 0), nil
	}
	if c.looping {
		if c.hasAudio {
			if !c.audioEOF {
				return c.info.Duration, nil
			}
			if err := c.noLockCloseAudioPlayer(); err != nil {
				return 0, err
			}
			if err := c.decoder.Seek(0); err != nil {
				return 0, err
			}
			c.noLockResetPlayback(0)
			c.state = Playing
			if err := c.noLockCreateAudioPlayer(); err != nil {
				c.state = Stopped
				return 0, err
			}
			c.audioPlayer.Play()
			return 0, nil
		}
		position %= c.info.Duration
		if err := c.decoder.Seek(position); err != nil {
			return 0, err
		}
		c.noLockRecycleVideoFrames()
		c.referencePosition = position
		c.referenceTime = now
		return position, nil
	}

	c.ended = true
	c.state = Stopped
	c.staticPosition = c.info.Duration
	c.referencePosition = c.info.Duration
	if c.audioPlayer != nil {
		c.audioPlayer.Pause()
	}
	return c.info.Duration, nil
}

func (c *ffgoLocalController) Duration() time.Duration { return c.info.Duration }

func (c *ffgoLocalController) SetLooping(looping bool) {
	c.mutex.Lock()
	c.looping = looping
	c.mutex.Unlock()
}

func (c *ffgoLocalController) GetLooping() bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.looping
}

func (c *ffgoLocalController) HasEnded() bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if !c.ended && !c.looping {
		if _, err := c.noLockPosition(time.Now()); err != nil && c.decodeErr == nil {
			c.decodeErr = err
		}
	}
	return c.ended && !c.looping
}

func (c *ffgoLocalController) HasAudio() bool { return c.hasAudio }

func (c *ffgoLocalController) GetVolume() float64 {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.volume
}

func (c *ffgoLocalController) SetVolume(volume float64) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.volume = volume
	if c.audioPlayer != nil {
		c.audioPlayer.SetVolume(c.noLockEffectiveVolume())
	}
}

func (c *ffgoLocalController) GetMuted() bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.muted
}

func (c *ffgoLocalController) SetMuted(muted bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.muted = muted
	if c.audioPlayer != nil {
		c.audioPlayer.SetVolume(c.noLockEffectiveVolume())
	}
}

func (c *ffgoLocalController) noLockEffectiveVolume() float64 {
	if c.muted {
		return 0
	}
	return c.volume
}

func (c *ffgoLocalController) CurrentVideoFrame() (*backendFrame, bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.state == Stopped {
		return c.lastVideo, c.ended, nil
	}
	position, err := c.noLockPosition(time.Now())
	if err != nil {
		return nil, false, err
	}

	if c.hasAudio {
		c.noLockConsumeVideoQueue(position)
		return c.lastVideo, c.ended, nil
	}
	if c.ended {
		return c.lastVideo, true, nil
	}

	for c.lastVideo == nil || c.lastVideo.PTS+c.frameDuration() < position {
		frame, err := c.decoder.ReadFrame(context.Background(), &c.videoBuffers)
		if err != nil {
			if errors.Is(err, io.EOF) {
				if c.looping {
					if err := c.decoder.Seek(0); err != nil {
						return nil, false, err
					}
					c.noLockRecycleVideoFrames()
					c.referencePosition = 0
					c.referenceTime = time.Now()
					return nil, false, nil
				}
				c.ended = true
				c.state = Stopped
				c.staticPosition = c.info.Duration
				c.referencePosition = c.info.Duration
				return c.lastVideo, true, nil
			}
			c.decodeErr = err
			return nil, false, err
		}
		if frame.Kind == backendFrameVideo {
			c.noLockReplaceLastVideo(frame)
		}
	}
	return c.lastVideo, false, nil
}

func (c *ffgoLocalController) noLockConsumeVideoQueue(position time.Duration) {
	consumed := 0
	for consumed < len(c.videoQueue) {
		frame := &c.videoQueue[consumed]
		if c.lastVideo != nil && frame.PTS+frame.Duration > position {
			break
		}
		next := *frame
		*frame = backendFrame{}
		c.noLockReplaceLastVideo(next)
		consumed++
	}
	if consumed > 0 {
		copy(c.videoQueue, c.videoQueue[consumed:])
		clear(c.videoQueue[len(c.videoQueue)-consumed:])
		c.videoQueue = c.videoQueue[:len(c.videoQueue)-consumed]
	}
}

func (c *ffgoLocalController) noLockReplaceLastVideo(frame backendFrame) {
	recycleBackendFrame(&c.videoBuffers, c.lastVideo)
	copyFrame := frame
	c.lastVideo = &copyFrame
}

func (c *ffgoLocalController) noLockRecycleVideoFrames() {
	recycleBackendFrame(&c.videoBuffers, c.lastVideo)
	c.lastVideo = nil
	for i := range c.videoQueue {
		recycleBackendFrame(&c.videoBuffers, &c.videoQueue[i])
	}
	c.videoQueue = c.videoQueue[:0]
}

func (c *ffgoLocalController) frameDuration() time.Duration {
	if c.info.Video == nil {
		return 0
	}
	return c.info.Video.FrameDuration()
}

func (c *ffgoLocalController) Error() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.decodeErr
}

func (c *ffgoLocalController) Read(buffer []byte) (int, error) {
	if len(buffer)&3 != 0 {
		buffer = buffer[:len(buffer)&(math.MaxInt-3)]
	}
	c.mutex.Lock()
	defer c.mutex.Unlock()

	served := c.noLockCopyAudio(buffer)
	buffer = buffer[served:]
	for len(buffer) > 0 {
		frame, err := c.decoder.ReadFrame(context.Background(), &c.videoBuffers)
		if err != nil {
			if errors.Is(err, io.EOF) {
				// The decoder can reach EOF while Oto still has buffered PCM to
				// play. The playback clock, not this read-ahead boundary, decides
				// when media ended.
				c.audioEOF = true
				return served, io.EOF
			}
			if c.decodeErr == nil {
				c.decodeErr = err
			}
			c.state = Stopped
			return served, io.EOF
		}

		switch frame.Kind {
		case backendFrameVideo:
			c.videoQueue = append(c.videoQueue, frame)
		case backendFrameAudio:
			if c.waitingForAudioPTS {
				c.firstAudioPTS = frame.PTS
				c.staticPosition = frame.PTS
				c.waitingForAudioPTS = false
			}
			c.audioQueue = append(c.audioQueue, frame.Audio.PCM...)
		}

		copied := c.noLockCopyAudio(buffer)
		served += copied
		buffer = buffer[copied:]
	}
	return served, nil
}

func (c *ffgoLocalController) noLockCopyAudio(buffer []byte) int {
	copied := copy(buffer, c.audioQueue)
	if copied == len(c.audioQueue) {
		c.audioQueue = c.audioQueue[:0]
	} else if copied > 0 {
		remaining := copy(c.audioQueue, c.audioQueue[copied:])
		c.audioQueue = c.audioQueue[:remaining]
	}
	return copied
}

func (c *ffgoLocalController) noLockCreateAudioPlayer() error {
	factory := c.newAudioPlayer
	if factory == nil {
		factory = newEbitengineAudioPlayer
	}
	player, err := factory(&struct{ io.Reader }{c})
	if err != nil {
		return err
	}
	player.SetBufferSize(ffgoPlayerBufferSize)
	player.SetVolume(c.noLockEffectiveVolume())
	c.audioPlayer = player
	c.waitingForAudioPTS = len(c.audioQueue) == 0
	return nil
}

func newEbitengineAudioPlayer(source io.Reader) (ffgoAudioPlayer, error) {
	ctx := audio.CurrentContext()
	if ctx == nil {
		return nil, ErrNilAudioContext
	}
	return ctx.NewPlayer(source)
}

func (c *ffgoLocalController) noLockCloseAudioPlayer() error {
	if c.audioPlayer == nil {
		return nil
	}
	c.audioPlayer.Pause()
	err := c.audioPlayer.Close()
	c.audioPlayer = nil
	c.audioEOF = false
	return err
}

func (c *ffgoLocalController) noLockResetPlayback(position time.Duration) {
	c.noLockRecycleVideoFrames()
	c.audioQueue = c.audioQueue[:0]
	c.firstAudioPTS = position
	c.audioEOF = false
	c.waitingForAudioPTS = c.hasAudio
	c.staticPosition = position
	c.referencePosition = position
	c.referenceTime = time.Now()
	c.ended = false
	c.decodeErr = nil
}
