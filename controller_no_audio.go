//go:build !android

package avebi

import (
	"sync"
	"time"

	"github.com/erparts/reisen"
)

// TODO: looping logic not implemented

var _ videoController = (*videoOnlyController)(nil)

type videoOnlyController struct {
	// mutex and underlying reisen objects
	mutex  sync.Mutex // TODO: change to RWMutex and switch to RLock()/RUnlock() where possible
	media  *reisen.Media
	stream *reisen.VideoStream

	// static data
	duration      time.Duration // complete video duration
	frameDuration time.Duration

	// state variables
	referenceTime     time.Time
	referencePosition time.Duration
	looping           bool
	videoPendingLoop  bool
	state             PlaybackState
	lastReadFrame     *reisen.VideoFrame
}

func newVideoOnlyController(media *reisen.Media, videoStream *reisen.VideoStream) (videoController, error) {
	if media == nil || videoStream == nil {
		panic("nil media or video stream")
	}

	frNum, frDenom := videoStream.FrameRate()
	frameDuration := (time.Second * time.Duration(frDenom)) / time.Duration(frNum)
	duration, err := videoStream.Duration()
	if err != nil {
		return nil, err
	}

	controller := &videoOnlyController{
		// underlying reisen objects
		media:  media,
		stream: videoStream,

		// static values
		duration:      duration,
		frameDuration: frameDuration,

		// state variables
		referenceTime: time.Now(),
		state:         Stopped,
	}
	return controller, nil
}

func (c *videoOnlyController) Play() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.state != Playing {
		if c.state == Stopped {
			c.lastReadFrame = nil
			c.referencePosition = 0 // necessary if we had a natural end-of-video stop
			err := c.media.OpenDecode()
			if err != nil {
				return err
			}
			err = c.stream.Open()
			if err != nil {
				return err
			}
		}

		c.referenceTime = time.Now()
		c.state = Playing
	}
	return nil
}

func (c *videoOnlyController) State() (PlaybackState, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	// we call c.noLockPosition for its side-effects: if the
	// video has reached the end, that will be detected and
	// reflected on c.state
	if _, _, err := c.noLockPosition(time.Now()); err != nil {
		return invalidPlaybackState, err
	}
	return c.state, nil
}

func (c *videoOnlyController) Pause() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.state != Playing {
		return nil
	}

	now := time.Now()
	position, endedAsSideEffect, err := c.noLockPosition(now)
	if err != nil {
		return err
	}
	if !endedAsSideEffect {
		c.state = Paused
		c.referenceTime = now
		c.referencePosition = position
	}
	return nil
}

// same as Position(), but without locking. this is "unsafe", but can
// be used internally in cases where the lock is already acquired for
// another reason.
// the returned bool will be true if the video ending is handled as
// a side effect of calling this function, due to the time exceeding
// the duration of the video
func (c *videoOnlyController) noLockPosition(now time.Time) (time.Duration, bool, error) {
	if c.referenceTime.After(now) {
		pkgLogger.Printf("WARNING: time inconsistency, video reference time after time.Now()")
		now = c.referenceTime
	}

	if c.state == Playing {
		position := c.referencePosition + now.Sub(c.referenceTime)
		if position < c.duration {
			return position, false, nil
		}

		// consider looping case
		if c.looping {
			err := c.stream.Rewind(0)
			if err != nil {
				return position, false, err
			}
			c.referenceTime = now
			c.referencePosition = position - c.duration
			c.videoPendingLoop = true
			return c.referencePosition, false, nil
		}

		// here exhausting video frames to fetch the latest one could be reasonable,
		// but it also comes with some risks, and in practice should be limited to
		// a maximum decoding of only a few frames. we are avoiding this whole process
		// at the moment for simplicity
		// c.exhaustVideoFrames()

		err := c.noLockStop(stopModeEndOfVideo)
		c.referencePosition = c.duration
		return c.referencePosition, true, err
	} else {
		return c.referencePosition, false, nil
	}
}

func (c *videoOnlyController) Stop() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.noLockStop(stopModeManual)
}

func (c *videoOnlyController) Close() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	err := c.noLockStop(stopModeManual)
	if err != nil {
		return err
	}
	c.media.Close()
	return nil
}

// similar to Stop(), but without locking. this is "unsafe", but can
// be used internally in cases where the lock is already acquired for
// another reason.
// There are only two ways in which a video can be stopped: manually or
// due to reaching the end of the video. For manual stops, the lastReadFrame
// will be changed to nil. For end-of-video stops, the reference position
// will be set to c.duration.
func (c *videoOnlyController) noLockStop(videoStopMode stopMode) error {
	// maybe not strictly necessary, but probably safer to reset
	c.videoPendingLoop = false

	// manual stops need to be handled even if already stopped due to end-of-video
	if videoStopMode == stopModeManual {
		c.referencePosition = 0
		c.lastReadFrame = nil
	}

	// already stopped
	if c.state == Stopped {
		return nil
	}

	// stopping logic
	c.state = Stopped
	c.referenceTime = time.Time{}
	if videoStopMode == stopModeEndOfVideo {
		c.referencePosition = c.duration
		// we don't clear lastReadFrame. in fact, we might
		// want to exhaust the frames to reach the last one,
		// but for the time being we are avoiding this for
		// simplicity
	}
	err := c.stream.Rewind(0)
	if err != nil {
		return err
	}

	err = c.stream.Close()
	if err != nil {
		return err
	}
	return c.media.CloseDecode()
}

func (c *videoOnlyController) Position() (time.Duration, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	position, _, err := c.noLockPosition(time.Now())
	return position, err
}

func (c *videoOnlyController) Duration() time.Duration {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.duration
}

func (c *videoOnlyController) Seek(position time.Duration) (*reisen.VideoFrame, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if position >= c.duration {
		// while end of video sounds more logical, it introduces issues
		// with the lastReadFrame, position and so on. for the moment this
		// should be decent enough
		err := c.noLockStop(stopModeManual)
		return nil, err
	} else {
		position = max(position, 0)
		err := c.stream.Rewind(position)
		if err != nil {
			return nil, err
		}
		c.lastReadFrame, err = c.internalReadVideoFrame()
		if err != nil {
			return c.lastReadFrame, err
		}
		c.referencePosition = position
		c.referenceTime = time.Now()
		return c.lastReadFrame, nil
	}
}

func (c *videoOnlyController) GetLooping() bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.looping
}

func (c *videoOnlyController) SetLooping(loop bool) {
	c.mutex.Lock()
	c.looping = loop
	c.mutex.Unlock()
}

func (c *videoOnlyController) CurrentVideoFrame() (*reisen.VideoFrame, bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.state == Stopped {
		// TODO: in the case of end-of-video, we should probably allow to
		//       keep reading... but the streams have already been closed.
		//       It's tricky, unclear what's the best way to deal with it.
		return c.lastReadFrame, c.referencePosition == c.duration, nil
	}

	// get target position
	now := time.Now()
	position, endedAsSideEffect, err := c.noLockPosition(now)
	if err != nil {
		return nil, false, err
	}
	if endedAsSideEffect {
		// frames exhaustion would depend on noLockPosition behavior
		return c.lastReadFrame, true, nil
	}

	// get current presentation offset
	var prevPresOffset, presOffset time.Duration
	if c.lastReadFrame != nil {
		presOffset, err = c.lastReadFrame.PresentationOffset()
		if err != nil {
			return nil, false, err
		}
		prevPresOffset = presOffset
	}

	// read frames until we reach the target position
	for presOffset+c.frameDuration < position || c.videoPendingLoop {
		if c.videoPendingLoop && presOffset < prevPresOffset {
			c.videoPendingLoop = false
		}

		frame, err := c.internalReadVideoFrame()
		if err != nil {
			return nil, false, err
		}

		// check whether the video is stopping
		if frame == nil {
			if c.looping {
				err := c.stream.Rewind(0)
				if err != nil {
					return nil, false, err
				}
				c.referenceTime = now
				c.referencePosition = 0
				c.videoPendingLoop = true
				return c.lastReadFrame, false, nil
			}

			err = c.noLockStop(stopModeEndOfVideo)
			return c.lastReadFrame, true, err
		}

		// otherwise, update presentation offset
		prevPresOffset = presOffset
		presOffset, err = frame.PresentationOffset()
		if err != nil {
			return nil, false, err
		}
		c.lastReadFrame = frame
	}

	return c.lastReadFrame, false, nil
}

func (c *videoOnlyController) internalReadVideoFrame() (*reisen.VideoFrame, error) {
	// read packets until we come across the next video frame packet
	for {
		packet, packetFound, err := c.media.ReadPacket()
		if err != nil {
			return nil, err
		}

		if !packetFound {
			if packet != nil {
				panic("broken code")
			}
			return nil, nil
		}

		if packet.Type() == reisen.StreamVideo && packet.StreamIndex() == c.stream.Index() {
			frame, frameFound, err := c.stream.ReadVideoFrame()
			if err != nil {
				return nil, err
			}
			_ = frameFound // frameFound can be true while frame is nil: that's a frame skip
			if frame != nil {
				return frame, nil
			}
		}
	}
}

func (*videoOnlyController) Error() error {
	return nil
}
