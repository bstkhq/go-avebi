//go:build !android

package avebi

import (
	"fmt"
	"sync"
	"time"

	"github.com/erparts/reisen"
)

var _ videoController = (*streamVideoController)(nil)

// Tunables for live playback behavior.
const (
	// defaultJitter allows small PTS vs. wall-clock slippage without delaying.
	defaultJitter = 15 * time.Millisecond
	// decodeErrSleepLive is the backoff used when the decoder encounters
	// transient errors or starvation on a live source.
	decodeErrSleepLive = 10 * time.Millisecond
)

// streamVideoController manages live-only playback using PTS-based scheduling.
//
// Design overview
//
//   - Decoding: a dedicated goroutine reads packets/frames from the reisen
//     Media/VideoStream and pushes decoded frames into a buffered channel.
//   - Scheduling: a second goroutine consumes decoded frames and delays their
//     presentation until the wall-clock time corresponding to each frame’s PTS.
//   - Timebase: when the first frame is observed, its PTS is recorded as ptsBase
//     and the current wall-clock as wallBase. All subsequent frames are aligned
//     to wallBase + (PTS - ptsBase).
//   - State model: Playing, Paused, Stopped. Seek and Looping are intentionally
//     unsupported for live sources.
//   - Concurrency: the public API acquires c.mutex. The decoding and scheduling
//     goroutines avoid holding c.mutex while blocking on I/O or timers.
//
// Notes
//
//   - Duration() returns 0 for live content.
//   - Seek() returns an error for live content.
//   - CurrentVideoFrame() returns the last frame “released” by the scheduler.
//   - Position() is a logical clock (wall-clock derived), not a file position.
type streamVideoController struct {
	mutex  sync.Mutex
	media  *reisen.Media
	stream *reisen.VideoStream
	opts   *StreamOptions

	state             PlaybackState
	referenceTime     time.Time
	referencePosition time.Duration

	lastReadFrame *reisen.VideoFrame

	havePTSBase bool
	ptsBase     time.Duration
	wallBase    time.Time
	jitter      time.Duration

	stopCh    chan struct{}
	wg        sync.WaitGroup
	decodedCh chan *reisen.VideoFrame
	errCh     chan error
}

// newStreamVideoController constructs a controller for a live video stream.
// The provided media and video stream must be non-nil and unopened. opts must
// also be non-nil. The controller is created in Stopped state; call Play() to
// start.
func newStreamVideoController(media *reisen.Media, s *reisen.VideoStream, opts *StreamOptions) (videoController, error) {
	if opts == nil {
		panic("precondition violation")
	}
	if media == nil || s == nil {
		return nil, fmt.Errorf("nil media or video stream")
	}
	return &streamVideoController{
		media:  media,
		stream: s,
		opts:   opts,
		state:  Stopped,
		jitter: defaultJitter,
	}, nil
}

// Play opens the decoder/stream (if needed) and starts the decode and schedule
// goroutines. If already Playing, Play is a no-op. On first Play after Stop,
// PTS and reference clocks are reset.
func (c *streamVideoController) Play() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if err := reisen.NetworkInitialize(); err != nil {
		return err
	}

	if c.state == Playing {
		return nil
	}

	if c.state == Stopped {
		// Reset live state and open decoder/stream.
		c.lastReadFrame = nil
		c.referencePosition = 0
		c.havePTSBase = false

		if err := c.media.OpenDecode(); err != nil {
			return err
		}
		if err := c.stream.Open(); err != nil {
			_ = c.media.CloseDecode()
			return err
		}

		// Start background pipelines.
		c.stopCh = make(chan struct{})
		c.decodedCh = make(chan *reisen.VideoFrame, 64)
		c.errCh = make(chan error, 1)

		c.wg.Add(1)
		go c.decodeLoop()

		c.wg.Add(1)
		go c.scheduleLoop()
	}

	c.referenceTime = time.Now()
	c.state = Playing
	return nil
}

// State returns the current playback state. It also updates the internal logical
// clock using the current wall-clock to keep Position() fresh for UI consumers.
func (c *streamVideoController) State() (PlaybackState, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	_, _, _ = c.noLockPosition(time.Now())
	return c.state, nil
}

// Pause transitions from Playing to Paused and captures the current logical
// position based on wall-clock. Pausing does not stop decoding; frames continue
// to be processed but the scheduler will not delay for Paused state here—UI
// should gate rendering on c.state if needed.
func (c *streamVideoController) Pause() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.state != Playing {
		return nil
	}
	now := time.Now()
	pos, _, _ := c.noLockPosition(now)
	c.state = Paused
	c.referenceTime = now
	c.referencePosition = pos
	return nil
}

// Stop terminates background goroutines, closes the decoder/stream, and resets
// the reference clock. It is safe to call multiple times.
func (c *streamVideoController) Stop() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.noLockStop(stopModeManual)
}

// Close stops playback (if needed), tears down reisen network state, and closes
// the underlying media handle.
func (c *streamVideoController) Close() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	defer reisen.NetworkDeinitialize()

	if err := c.noLockStop(stopModeManual); err != nil {
		return err
	}
	c.media.Close()
	return nil
}

// noLockStop stops goroutines and closes decode/stream without holding the lock
// during potentially blocking Wait(), preventing self-deadlock.
func (c *streamVideoController) noLockStop(_ stopMode) error {
	if c.stopCh != nil {
		close(c.stopCh)
	}

	// Release the mutex while waiting for goroutines to terminate.
	c.mutex.Unlock()
	c.wg.Wait()
	c.mutex.Lock()
	c.stopCh = nil

	if c.decodedCh != nil {
		close(c.decodedCh)
		c.decodedCh = nil
	}
	if c.errCh != nil {
		close(c.errCh)
		c.errCh = nil
	}

	c.referencePosition = 0
	c.lastReadFrame = nil
	if c.state == Stopped {
		return nil
	}

	c.state = Stopped
	c.referenceTime = time.Time{}

	// In live mode there is no rewind/seekable resource—just close.
	if err := c.stream.Close(); err != nil {
		return err
	}
	return c.media.CloseDecode()
}

// Position returns the current logical playback time relative to the live
// session start (wall-clock derived). For live content this is not a file
// offset and may drift if the source clock is unstable.
func (c *streamVideoController) Position() (time.Duration, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	pos, _, err := c.noLockPosition(time.Now())
	return pos, err
}

// Duration returns 0 because live streams have no defined end.
func (c *streamVideoController) Duration() time.Duration {
	return 0
}

// Seek is unsupported for live streams and returns an error.
func (c *streamVideoController) Seek(_ time.Duration) (*reisen.VideoFrame, error) {
	return nil, fmt.Errorf("cannot seek in live stream")
}

// GetLooping always returns false for live streams.
func (*streamVideoController) GetLooping() bool {
	return false
}

// SetLooping is a no-op for live streams.
func (*streamVideoController) SetLooping(bool) {}

// CurrentVideoFrame returns the most recently scheduled frame. The boolean
// return value is unused here and remains false for compatibility with other
// controllers that might include “new frame available” semantics.
func (c *streamVideoController) CurrentVideoFrame() (*reisen.VideoFrame, bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.lastReadFrame, false, nil
}

// noLockPosition computes the logical position at time now without side effects
// on external state. If Playing, it advances from referenceTime by wall time;
// otherwise it returns the last captured referencePosition.
func (c *streamVideoController) noLockPosition(now time.Time) (time.Duration, bool, error) {
	if c.referenceTime.After(now) {
		now = c.referenceTime
	}
	if c.state == Playing {
		return c.referencePosition + now.Sub(c.referenceTime), false, nil
	}
	return c.referencePosition, false, nil
}

// decodeLoop continuously pulls packets and decodes video frames from the live
// source. EOF is not final in live mode; on transient errors/starvation it
// sleeps briefly and continues until stopCh is closed.
func (c *streamVideoController) decodeLoop() {
	defer c.wg.Done()
	for {
		select {
		case <-c.stopCh:
			return
		default:
		}

		packet, ok, err := c.media.ReadPacketWithTimeout(c.opts.ReadTimeout)
		if err != nil {
			// Report error if a consumer is listening, but do not block.
			select {
			case <-c.stopCh:
				return
			case c.errCh <- err:
			default:
			}
			time.Sleep(decodeErrSleepLive)
			continue
		}
		if !ok {
			// No packet available yet (live starvation): try again shortly.
			time.Sleep(decodeErrSleepLive)
			continue
		}
		if packet.Type() != reisen.StreamVideo || packet.StreamIndex() != c.stream.Index() {
			continue
		}

		frame, got, err := c.stream.ReadVideoFrame()
		if err != nil {
			// Non-fatal on live inputs: report and keep going.
			select {
			case <-c.stopCh:
				return
			case c.errCh <- err:
			default:
			}
			continue
		}
		if !got || frame == nil {
			continue
		}

		select {
		case <-c.stopCh:
			return
		case c.decodedCh <- frame:
		}
	}
}

// scheduleLoop aligns frames to wall-clock based on PTS. For the first frame,
// it captures ptsBase and wallBase. For each subsequent frame, it computes the
// due time as wallBase + (PTS - ptsBase). If Playing and due is sufficiently
// in the future (beyond jitter), it sleeps until due; otherwise it publishes
// immediately. After publishing, it updates the logical reference clock.
func (c *streamVideoController) scheduleLoop() {
	defer c.wg.Done()

	for {
		select {
		case <-c.stopCh:
			return
		case f, ok := <-c.decodedCh:
			if !ok {
				return
			}
			pts, err := f.PresentationOffset()
			if err != nil {
				// If PTS is unavailable, drop the frame; live sync requires PTS.
				continue
			}

			c.mutex.Lock()
			if !c.havePTSBase {
				c.ptsBase = pts
				c.wallBase = time.Now()
				c.havePTSBase = true
			}
			due := c.wallBase.Add(pts - c.ptsBase)
			j := c.jitter
			st := c.state
			c.mutex.Unlock()

			now := time.Now()
			if st == Playing && due.After(now.Add(j)) {
				select {
				case <-c.stopCh:
					return
				case <-time.After(due.Sub(now)):
				}
			}

			c.mutex.Lock()
			c.lastReadFrame = f
			c.referencePosition = pts - c.ptsBase
			c.referenceTime = time.Now()
			c.mutex.Unlock()
		}
	}
}

func (*streamVideoController) Error() error {
	return nil
}
