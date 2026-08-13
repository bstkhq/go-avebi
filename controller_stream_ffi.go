//go:build !ios && !android && (amd64 || arm64)

package avebi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

type ffiStreamController struct {
	mutex sync.Mutex

	backend mediaBackend
	source  string
	opts    backendOpenOptions
	info    backendMediaInfo
	decoder mediaDecoder

	state  PlaybackState
	closed bool
	err    error

	lastVideo         *backendFrame
	pendingVideo      *backendFrame
	videoBuffers      backendVideoBufferPool
	referencePosition time.Duration
	referenceTime     time.Time
	havePTSBase       bool
	ptsBase           time.Duration
	wallBase          time.Time
	pausedAt          time.Time

	stopCh    chan struct{}
	decodedCh chan backendFrame
	wg        sync.WaitGroup
}

var _ playbackController = (*ffiStreamController)(nil)

func newFFmpegStreamController(backend mediaBackend, source string, opts backendOpenOptions, info backendMediaInfo, decoder mediaDecoder) *ffiStreamController {
	return &ffiStreamController{
		backend: backend,
		source:  source,
		opts:    opts,
		info:    info,
		decoder: decoder,
		state:   Stopped,
	}
}

func (c *ffiStreamController) State() (PlaybackState, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.state, c.err
}

func (c *ffiStreamController) Play() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.closed {
		return errors.New("avebi: player is closed")
	}
	if c.state == Playing {
		return nil
	}

	if c.state == Paused {
		if !c.pausedAt.IsZero() && c.havePTSBase {
			c.wallBase = c.wallBase.Add(time.Since(c.pausedAt))
		}
		c.referenceTime = time.Now()
		c.pausedAt = time.Time{}
		c.state = Playing
		return nil
	}

	decoder := c.decoder
	if decoder == nil {
		var err error
		decoder, err = c.backend.Open(context.Background(), c.source, c.opts)
		if err != nil {
			return err
		}
	}
	c.decoder = decoder
	c.err = nil
	c.noLockRecycleVideoFrames()
	c.referencePosition = 0
	c.referenceTime = time.Now()
	c.havePTSBase = false
	c.stopCh = make(chan struct{})
	c.decodedCh = make(chan backendFrame, 64)
	c.state = Playing

	c.wg.Add(2)
	go c.decodeLoop(decoder, c.stopCh, c.decodedCh)
	go c.scheduleLoop(c.stopCh, c.decodedCh)
	return nil
}

func (c *ffiStreamController) Pause() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.state != Playing {
		return nil
	}
	c.referencePosition = c.noLockPosition(time.Now())
	c.referenceTime = time.Now()
	c.pausedAt = time.Now()
	c.state = Paused
	return nil
}

func (c *ffiStreamController) Stop() error {
	c.mutex.Lock()
	stopCh := c.stopCh
	decoder := c.decoder
	decodedCh := c.decodedCh
	c.stopCh = nil
	c.decoder = nil
	c.decodedCh = nil
	c.state = Stopped
	c.referencePosition = 0
	c.referenceTime = time.Time{}
	if stopCh != nil {
		close(stopCh)
	}
	c.mutex.Unlock()

	var closeErr error
	if decoder != nil {
		closeErr = decoder.Close()
	}
	c.wg.Wait()
	c.recycleDecodedFrames(decodedCh)

	c.mutex.Lock()
	c.noLockRecycleVideoFrames()
	c.havePTSBase = false
	c.mutex.Unlock()
	return closeErr
}

func (c *ffiStreamController) Close() error {
	err := c.Stop()
	c.mutex.Lock()
	c.closed = true
	c.videoBuffers.clear()
	c.mutex.Unlock()
	return err
}

func (*ffiStreamController) Seek(time.Duration) (*backendFrame, error) {
	return nil, fmt.Errorf("cannot seek in live stream")
}

func (c *ffiStreamController) Position() (time.Duration, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.noLockPosition(time.Now()), nil
}

func (c *ffiStreamController) noLockPosition(now time.Time) time.Duration {
	if c.state == Playing && !c.referenceTime.IsZero() {
		return c.referencePosition + now.Sub(c.referenceTime)
	}
	return c.referencePosition
}

func (*ffiStreamController) Duration() time.Duration { return 0 }
func (*ffiStreamController) SetLooping(bool)         {}
func (*ffiStreamController) GetLooping() bool        { return false }
func (*ffiStreamController) HasEnded() bool          { return false }
func (*ffiStreamController) HasAudio() bool          { return false }
func (*ffiStreamController) GetVolume() float64      { return 0 }
func (*ffiStreamController) SetVolume(float64)       {}
func (*ffiStreamController) GetMuted() bool          { return true }
func (*ffiStreamController) SetMuted(bool)           {}

func (c *ffiStreamController) CurrentVideoFrame() (*backendFrame, bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.pendingVideo != nil {
		recycleBackendFrame(&c.videoBuffers, c.lastVideo)
		c.lastVideo = c.pendingVideo
		c.pendingVideo = nil
	}
	return c.lastVideo, false, c.err
}

func (c *ffiStreamController) Error() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.err
}

func (c *ffiStreamController) decodeLoop(decoder mediaDecoder, stop chan struct{}, decoded chan<- backendFrame) {
	defer c.wg.Done()
	for {
		select {
		case <-stop:
			return
		default:
		}

		frame, err := decoder.ReadFrame(context.Background(), &c.videoBuffers)
		if err != nil {
			select {
			case <-stop:
				return
			default:
			}

			c.mutex.Lock()
			ownsSession := c.decoder == decoder && c.stopCh == stop
			if ownsSession {
				if errors.Is(err, io.EOF) {
					c.err = io.ErrUnexpectedEOF
				} else {
					c.err = err
				}
				c.state = Stopped
				c.decoder = nil
				c.stopCh = nil
				c.decodedCh = nil
				close(stop)
			}
			c.mutex.Unlock()

			if ownsSession {
				_ = decoder.Close()
			}
			return
		}
		if frame.Kind != backendFrameVideo {
			continue
		}
		select {
		case <-stop:
			recycleBackendFrame(&c.videoBuffers, &frame)
			return
		case decoded <- frame:
		}
	}
}

func (c *ffiStreamController) scheduleLoop(stop <-chan struct{}, decoded <-chan backendFrame) {
	defer c.wg.Done()
	defer c.recycleDecodedFrames(decoded)
	for {
		select {
		case <-stop:
			return
		case frame, ok := <-decoded:
			if !ok {
				return
			}
			if !c.waitUntilPlaying(stop) {
				recycleBackendFrame(&c.videoBuffers, &frame)
				return
			}

			c.mutex.Lock()
			if !c.havePTSBase {
				c.ptsBase = frame.PTS
				c.wallBase = time.Now()
				c.havePTSBase = true
			}
			due := c.wallBase.Add(frame.PTS - c.ptsBase)
			c.mutex.Unlock()

			delay := time.Until(due)
			if delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-stop:
					if !timer.Stop() {
						<-timer.C
					}
					recycleBackendFrame(&c.videoBuffers, &frame)
					return
				case <-timer.C:
				}
			}

			c.mutex.Lock()
			if c.state == Playing {
				recycleBackendFrame(&c.videoBuffers, c.pendingVideo)
				copyFrame := frame
				c.pendingVideo = &copyFrame
				c.referencePosition = frame.PTS - c.ptsBase
				c.referenceTime = time.Now()
				frame = backendFrame{}
			}
			c.mutex.Unlock()
			recycleBackendFrame(&c.videoBuffers, &frame)
		}
	}
}

func (c *ffiStreamController) noLockRecycleVideoFrames() {
	recycleBackendFrame(&c.videoBuffers, c.lastVideo)
	recycleBackendFrame(&c.videoBuffers, c.pendingVideo)
	c.lastVideo = nil
	c.pendingVideo = nil
}

func (c *ffiStreamController) recycleDecodedFrames(decoded <-chan backendFrame) {
	if decoded == nil {
		return
	}
	for {
		select {
		case frame, ok := <-decoded:
			if !ok {
				return
			}
			recycleBackendFrame(&c.videoBuffers, &frame)
		default:
			return
		}
	}
}

func (c *ffiStreamController) waitUntilPlaying(stop <-chan struct{}) bool {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		c.mutex.Lock()
		state := c.state
		c.mutex.Unlock()
		switch state {
		case Playing:
			return true
		case Stopped:
			return false
		}
		select {
		case <-stop:
			return false
		case <-ticker.C:
		}
	}
}
