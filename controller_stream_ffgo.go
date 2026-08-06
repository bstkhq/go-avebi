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

type ffgoStreamController struct {
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

var _ playbackController = (*ffgoStreamController)(nil)

func newFFGOStreamController(backend mediaBackend, source string, opts backendOpenOptions, info backendMediaInfo, decoder mediaDecoder) *ffgoStreamController {
	return &ffgoStreamController{
		backend: backend,
		source:  source,
		opts:    opts,
		info:    info,
		decoder: decoder,
		state:   Stopped,
	}
}

func (c *ffgoStreamController) State() (PlaybackState, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.state, c.err
}

func (c *ffgoStreamController) Play() error {
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
	c.lastVideo = nil
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

func (c *ffgoStreamController) Pause() error {
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

func (c *ffgoStreamController) Stop() error {
	c.mutex.Lock()
	if c.state == Stopped && c.decoder == nil {
		c.mutex.Unlock()
		return nil
	}
	stopCh := c.stopCh
	decoder := c.decoder
	c.stopCh = nil
	c.decoder = nil
	c.state = Stopped
	c.lastVideo = nil
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

	c.mutex.Lock()
	c.decodedCh = nil
	c.havePTSBase = false
	c.mutex.Unlock()
	return closeErr
}

func (c *ffgoStreamController) Close() error {
	err := c.Stop()
	c.mutex.Lock()
	c.closed = true
	c.mutex.Unlock()
	return err
}

func (*ffgoStreamController) Seek(time.Duration) (*backendFrame, error) {
	return nil, fmt.Errorf("cannot seek in live stream")
}

func (c *ffgoStreamController) Position() (time.Duration, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.noLockPosition(time.Now()), nil
}

func (c *ffgoStreamController) noLockPosition(now time.Time) time.Duration {
	if c.state == Playing && !c.referenceTime.IsZero() {
		return c.referencePosition + now.Sub(c.referenceTime)
	}
	return c.referencePosition
}

func (*ffgoStreamController) Duration() time.Duration { return 0 }
func (*ffgoStreamController) SetLooping(bool)         {}
func (*ffgoStreamController) GetLooping() bool        { return false }
func (*ffgoStreamController) HasEnded() bool          { return false }
func (*ffgoStreamController) HasAudio() bool          { return false }
func (*ffgoStreamController) GetVolume() float64      { return 0 }
func (*ffgoStreamController) SetVolume(float64)       {}
func (*ffgoStreamController) GetMuted() bool          { return true }
func (*ffgoStreamController) SetMuted(bool)           {}

func (c *ffgoStreamController) CurrentVideoFrame() (*backendFrame, bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.lastVideo, false, c.err
}

func (c *ffgoStreamController) Error() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.err
}

func (c *ffgoStreamController) decodeLoop(decoder mediaDecoder, stop chan struct{}, decoded chan<- backendFrame) {
	defer c.wg.Done()
	for {
		select {
		case <-stop:
			return
		default:
		}

		frame, err := decoder.ReadFrame(context.Background())
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
			return
		case decoded <- frame:
		}
	}
}

func (c *ffgoStreamController) scheduleLoop(stop <-chan struct{}, decoded <-chan backendFrame) {
	defer c.wg.Done()
	for {
		select {
		case <-stop:
			return
		case frame := <-decoded:
			if !c.waitUntilPlaying(stop) {
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
					return
				case <-timer.C:
				}
			}

			c.mutex.Lock()
			if c.state == Playing {
				copyFrame := frame
				c.lastVideo = &copyFrame
				c.referencePosition = frame.PTS - c.ptsBase
				c.referenceTime = time.Now()
			}
			c.mutex.Unlock()
		}
	}
}

func (c *ffgoStreamController) waitUntilPlaying(stop <-chan struct{}) bool {
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
