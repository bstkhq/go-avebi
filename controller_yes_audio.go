package avebi

import (
	"fmt"
	"io"
	"math"
	"sync"
	"time"

	"github.com/erparts/reisen"
	"github.com/hajimehoshi/ebiten/v2/audio"
)

// TODO: the current limitation is that audio and video tracks must have the
//       same length. otherwise audio can't lead video. this can be fixed but
//       it's a bit annoying to do right, and right now we don't have the need
// TODO: from reisen, using pools for data could help reduce memory usage for
//       both audio and video frames (considerably)
// TODO: from reisen, hardware acceleration is necessary, h264_vaapi I think
//       in particular (set up the codec context (AVCodecContext) to use the
//       VAAPI hardware accelerator)
// TODO: mono audio is untested

// player buffer size of 40ms should be ok on desktops. 70ms should be
// ok on wasm/web. for microcontrollers, you might have to experiment.
const playerBufferSize time.Duration = 200 * time.Millisecond

const panicOnPartialSampleReads = false // set to true if you want to ensure ebitengine doesn't ask you for partial samples

// NOTICE: for documentation, reading controller_no_audio.go first
// is recommended. most comments there are not repeated here, but do
// typically still apply

var _ videoController = (*videoWithAudioController)(nil)

type videoWithAudioController struct {
	// mutex and underlying reisen objects
	mutex sync.RWMutex
	media *reisen.Media
	video *reisen.VideoStream
	audio *reisen.AudioStream

	// static data
	duration      time.Duration // complete video duration
	frameDuration time.Duration

	// state variables
	looping          bool
	videoPendingLoop bool
	muted            bool
	state            PlaybackState
	volume           float64
	lastReadFrame    *reisen.VideoFrame
	leftoverVideo    []*reisen.VideoFrame

	// audio-specific internal management
	audioPlayer                 *audio.Player
	leftoverAudio               []byte
	firstAudioFrameOffsetOnPlay time.Duration
	needsFirstAudioFrameOffset  bool
	staticPosition              time.Duration // set manually and used when video is paused or stopped

	// last fatal decode/playback error (if any). this is kept internal and
	// never propagated directly to ebitengine; Read only returns nil or io.EOF.
	decodeErr error
}

func newVideoWithAudioController(media *reisen.Media, videoStream *reisen.VideoStream, audioStream *reisen.AudioStream) (videoController, error) {
	// basic safety assertions and checks
	if media == nil || videoStream == nil || audioStream == nil {
		panic("nil media or video or audio stream")
	}
	audioSampleRate := audioStream.SampleRate()
	audioContext := audio.CurrentContext()
	if audioContext == nil {
		return nil, ErrNilAudioContext
	}
	if audioContext.SampleRate() != audioSampleRate {
		pkgLogger.Printf("WARNING: context sample rate = %d, video audio sample rate = %d\n", audioContext.SampleRate(), audioSampleRate)
		return nil, ErrBadSampleRate
	}

	// get media duration
	frNum, frDenom := videoStream.FrameRate()
	frameDuration := (time.Second * time.Duration(frDenom)) / time.Duration(frNum)
	videoDuration, err := videoStream.Duration()
	if err != nil {
		return nil, err
	}
	audioDuration, err := audioStream.Duration()
	if err != nil {
		return nil, err
	}
	duration := max(videoDuration, audioDuration)
	// TODO: video and audio durations can indeed be different, and we definitely
	// need to account for it with the internal clocks

	return &videoWithAudioController{
		// underlying reisen objects
		media: media,
		video: videoStream,
		audio: audioStream,

		// static values
		duration:      duration,
		frameDuration: frameDuration,

		// state variables
		state:         Stopped,
		volume:        1.0,
		leftoverVideo: make([]*reisen.VideoFrame, 0, 8),

		// audio-related internal state
		leftoverAudio: make([]byte, 0, 1024),
	}, err
}

// --- audio-specific methods ---

func (c *videoWithAudioController) GetVolume() float64 {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.volume
}

func (c *videoWithAudioController) SetVolume(volume float64) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.volume = volume
	if c.audioPlayer != nil {
		c.audioPlayer.SetVolume(volume)
	}
}

func (c *videoWithAudioController) SetMuted(muted bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.muted = muted
}

func (c *videoWithAudioController) GetMuted() bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.muted
}

func (c *videoWithAudioController) Error() error {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.decodeErr
}

// --- videoController implementation ---

func (c *videoWithAudioController) Play() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.noLockPlay()
}

func (c *videoWithAudioController) noLockPlay() error {
	if c.state == Playing {
		return nil
	}

	if err := c.noLockGetReadyToPlay(); err != nil {
		return err
	}

	c.state = Playing
	c.audioPlayer.Play()
	return nil
}

func (c *videoWithAudioController) noLockPrepareReset() {
	c.leftoverAudio = c.leftoverAudio[:0]
	c.leftoverVideo = c.leftoverVideo[:0]
	c.lastReadFrame = nil
	c.firstAudioFrameOffsetOnPlay = 0
	c.staticPosition = 0
	c.decodeErr = nil
}

func (c *videoWithAudioController) noLockGetReadyToPlay() error {
	if c.state == Stopped {
		err := c.media.OpenDecode()
		if err != nil {
			return err
		}
		err = c.video.Open()
		if err != nil {
			return err
		}
		err = c.audio.Open()
		if err != nil {
			return err
		}

		// necessary if we had a natural end-of-video stop
		c.noLockPrepareReset()
	}

	if c.audioPlayer == nil {
		err := c.noLockCreateAudioPlayer()
		if err != nil {
			return err
		}
	}

	return nil
}

func (c *videoWithAudioController) Pause() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.state != Playing {
		return nil
	}

	position, endedAsSideEffect, err := c.noLockPosition()
	if err != nil {
		return err
	}
	if !endedAsSideEffect {
		c.state = Paused

		err := c.noLockEnsureAudioHalt()
		if err != nil {
			return err
		}
		c.firstAudioFrameOffsetOnPlay = position
		c.staticPosition = position
	}
	return nil
}

func (c *videoWithAudioController) Stop() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.noLockStop(stopModeManual)
}

func (c *videoWithAudioController) Close() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	err := c.noLockStop(stopModeManual)
	if err != nil {
		return err
	}
	c.media.Close()
	return nil
}

func (c *videoWithAudioController) State() (PlaybackState, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	// we call c.noLockPosition for its side-effects: if the
	// video has reached the end, that will be detected and
	// reflected on c.state
	if _, _, err := c.noLockPosition(); err != nil {
		return invalidPlaybackState, err
	}
	return c.state, nil
}

func (c *videoWithAudioController) Seek(offset time.Duration) (*reisen.VideoFrame, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	preState := c.state

	// into stop states
	if offset <= 0 || offset >= c.duration {
		if err := c.noLockStop(stopModeManual); err != nil {
			return nil, err
		}

		if offset >= c.duration {
			c.staticPosition = c.duration
		}

		if offset <= 0 {
			if preState != Playing {
				return nil, nil
			}
			return nil, c.noLockPlay()
		}
	}

	// from stop states
	if c.state == Stopped {
		if err := c.noLockGetReadyToPlay(); err != nil {
			return nil, err
		}
		c.state = Paused
	}

	// normal offsets
	if err := c.video.Rewind(offset); err != nil {
		return nil, err
	}
	// if err := c.audio.Rewind(offset); err != nil {
	// 	return nil, err
	// }

	c.noLockPrepareReset()
	if err := c.internalReadFrame(false); err != nil {
		return nil, err
	}
	if len(c.leftoverVideo) > 0 {
		c.lastReadFrame = c.leftoverVideo[0]
		pos, _ := c.lastReadFrame.PresentationOffset()
		fmt.Printf("last read frame: %.02f\n", pos.Seconds())
		copy(c.leftoverVideo, c.leftoverVideo[1:])
		c.leftoverVideo = c.leftoverVideo[:len(c.leftoverVideo)-1]
	}
	c.staticPosition, _ = c.lastReadFrame.PresentationOffset()

	if preState == Playing {
		if err := c.noLockHackyAudioReset(); err != nil {
			return nil, err
		}
	} else { // assume Paused
		if err := c.noLockEnsureAudioHalt(); err != nil {
			return nil, err
		}
	}

	return c.lastReadFrame, nil
}

func (c *videoWithAudioController) Position() (time.Duration, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	position, _, err := c.noLockPosition()
	return position, err
}

func (c *videoWithAudioController) Duration() time.Duration {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.duration
}

func (c *videoWithAudioController) SetLooping(looping bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.looping = looping
}

func (c *videoWithAudioController) GetLooping() bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.looping
}

func (c *videoWithAudioController) CurrentVideoFrame() (*reisen.VideoFrame, bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.state == Stopped {
		// NOTICE: here we don't touch c.leftoverVideo because stopping can
		// happen in different ways and we already set up the lastReadFrame
		// properly then
		return c.lastReadFrame, c.staticPosition == c.duration, nil
	}

	// get target position
	position, endedAsSideEffect, err := c.noLockPosition()
	if err != nil {
		return nil, false, err
	}

	if endedAsSideEffect {
		// natural end of video, take any remaining leftover video frames we can
		if len(c.leftoverVideo) > 0 {
			c.lastReadFrame = c.leftoverVideo[len(c.leftoverVideo)-1]
			c.leftoverVideo = c.leftoverVideo[:0]
		}
		// further frame exhaustion would depend on noLockPosition behavior
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

	// consume leftover video frames until we reach the target position
	var leftoverIndex int
	for len(c.leftoverVideo) > leftoverIndex && (presOffset+c.frameDuration < position || c.videoPendingLoop) {
		if c.videoPendingLoop && presOffset < prevPresOffset {
			c.videoPendingLoop = false
		}

		c.lastReadFrame = c.leftoverVideo[leftoverIndex]
		leftoverIndex += 1

		// otherwise, update presentation offset
		prevPresOffset = presOffset
		presOffset, err = c.lastReadFrame.PresentationOffset()
		if err != nil {
			return nil, false, err
		}
	}

	// update c.leftoverVideo to skip the used frames
	switch leftoverIndex {
	case 0:
		// nothing
	case len(c.leftoverVideo):
		c.leftoverVideo = c.leftoverVideo[:0]
	default:
		movedFrames := copy(c.leftoverVideo, c.leftoverVideo[leftoverIndex:])
		c.leftoverVideo = c.leftoverVideo[:movedFrames]
	}

	// return the most recent video frame
	return c.lastReadFrame, false, nil
}

// --- internal ---

func (c *videoWithAudioController) getEffectiveVolume() float64 {
	if c.muted {
		return 0.0
	}
	return c.volume
}

// the returned bool will be true if the video ending is handled as
// a side effect of calling this function, due to the time exceeding
// the duration of the video
//
// preconditions: c.mutex is locked, can't be called from c.Read()
func (c *videoWithAudioController) noLockPosition() (time.Duration, bool, error) {
	if c.audioPlayer == nil || c.needsFirstAudioFrameOffset {
		return c.staticPosition, false, nil
	}

	position := c.firstAudioFrameOffsetOnPlay + c.audioPlayer.Position()
	if position < c.duration {
		return position, false, nil
	}

	// here exhausting video frames to fetch the latest one could be reasonable,
	// but it also comes with some risks, and in practice should be limited to
	// a maximum decoding of only a few frames. we are avoiding this whole process
	// at the moment for simplicity
	// c.exhaustVideoFrames()

	err := c.noLockStop(stopModeEndOfVideo)
	return c.duration, true, err
}

// preconditions: c.mutex is locked, can't be called from c.Read() if c.audioPlayer != nil
func (c *videoWithAudioController) noLockStop(videoStopMode stopMode) error {
	// manual stops need to be handled even if already stopped due to end-of-video
	if videoStopMode == stopModeManual {
		err := c.noLockEnsureAudioHalt()
		if err != nil {
			return err
		}
		c.firstAudioFrameOffsetOnPlay = 0
		c.staticPosition = 0
		c.lastReadFrame = nil
		c.leftoverVideo = c.leftoverVideo[:0]
		c.leftoverAudio = c.leftoverAudio[:0]
		c.videoPendingLoop = false
	}

	// already stopped
	if c.state == Stopped {
		return nil
	}

	// stopping logic
	c.state = Stopped
	if videoStopMode == stopModeEndOfVideo {
		err := c.noLockEnsureAudioHalt()
		if err != nil {
			return err
		}
		c.firstAudioFrameOffsetOnPlay = 0
		c.staticPosition = c.duration
		c.videoPendingLoop = false
	}

	// rewind streams
	var err error
	err = c.video.Rewind(0)
	if err != nil {
		return err
	}
	err = c.audio.Rewind(0)
	if err != nil {
		return err
	}

	// close streams
	err = c.video.Close()
	if err != nil {
		return err
	}
	err = c.audio.Close()
	if err != nil {
		return err
	}

	// close media
	return c.media.CloseDecode()
}

// preconditions: c.mutex is locked, can't be called from c.Read() if c.audioPlayer != nil
func (c *videoWithAudioController) noLockEnsureAudioHalt() error {
	if c.audioPlayer != nil {
		c.audioPlayer.Pause()
		err := c.audioPlayer.Close()
		if err != nil {
			return err
		}
		c.audioPlayer = nil
	}
	c.leftoverAudio = c.leftoverAudio[:0]
	c.needsFirstAudioFrameOffset = true
	return nil
}

// helper to store a fatal playback/decode error, stop playback, and
// return io.EOF for Ebiten. This is the only non-nil error that Read
// ever returns.
func (c *videoWithAudioController) readHandleError(err error) error {
	if err != nil && c.decodeErr == nil {
		c.decodeErr = err
	}
	// we ignore errors from noLockStop here to avoid cascading failures
	_ = c.noLockStop(stopModeEndOfVideo)
	return io.EOF
}

// Read is invoked by the Ebiten audio context to retrieve audio data.
// If an error occurs during this process, it is returned as io.EOF and also
// recorded as the last decode error, which can later be accessed via Error().
func (c *videoWithAudioController) Read(buffer []byte) (int, error) {
	// sanity assertion
	if len(buffer)&0b11 != 0 {
		if panicOnPartialSampleReads {
			panic("ebitengine should only provide buffers multiple of 4 for serving L16 audio data")
		} else { // clamp to previous multiple of 4 (for f32 audio it would have to be mult of 8)
			buffer = buffer[:len(buffer)&(math.MaxInt-0b11)]
		}
	}

	// mutex
	c.mutex.Lock()
	defer c.mutex.Unlock()

	// if we had leftover bytes from the previous read, use that
	var servedBytes int
	if len(c.leftoverAudio) > 0 {
		copiedBytes := c.noLockCopyLeftoverAudio(buffer)
		buffer = buffer[copiedBytes:]
		servedBytes += copiedBytes
	}

	if len(buffer) == 0 {
		return servedBytes, nil
	}

	// decode audio and move it into the buffer
	for len(buffer) > 0 {
		// try to decode one audio frame (data is placed on c.leftoverAudio)
		if err := c.internalReadFrame(true); err != nil {
			// real decode error: remember it, gracefully stop, and tell Ebiten
			// that the stream has finished (EOF), without crashing RunGame.
			return servedBytes, c.readHandleError(err)
		}

		// check EOF case
		if len(c.leftoverAudio) == 0 {
			// setting audioPlayer == nil and returning io.EOF will stop the player
			// from ebitengine's side and force the creation of a new player on the
			// video player when required. This is important because audioPlayer.Pause()
			// or other methods can't be called while inside Read(), so we need to
			// stop through io.EOF
			c.audioPlayer = nil

			// consider looping case
			if c.looping {
				if err := c.noLockRewindForLooping(); err != nil {
					return servedBytes, c.readHandleError(err)
				}

				if err := c.noLockHackyAudioReset(); err != nil {
					return servedBytes, c.readHandleError(err)
				}

				return servedBytes, io.EOF
			}

			// end of video
			err := c.noLockStop(stopModeEndOfVideo)
			if err != nil {
				return servedBytes, c.readHandleError(err)
			}

			return servedBytes, io.EOF
		}

		// copy data and increase served bytes
		copiedBytes := c.noLockCopyLeftoverAudio(buffer)
		buffer = buffer[copiedBytes:]
		servedBytes += copiedBytes
	}

	return servedBytes, nil
}

func (c *videoWithAudioController) noLockCopyLeftoverAudio(buffer []byte) int {
	copiedBytes := copy(buffer, c.leftoverAudio)
	if copiedBytes >= len(c.leftoverAudio) {
		c.leftoverAudio = c.leftoverAudio[:0]
	} else {
		// note: this could be extremely inneficient in theory. in practice
		// we don't hit the problematic cases, but it's still far from ideal.
		// to be improved with circular buffers.
		newLen := copy(c.leftoverAudio, c.leftoverAudio[copiedBytes:])
		c.leftoverAudio = c.leftoverAudio[:newLen]
	}
	return copiedBytes
}

// preconditions: c.mutex is locked
func (c *videoWithAudioController) noLockRewindForLooping() error {
	var err error
	err = c.audio.Rewind(0)
	if err != nil {
		return err
	}
	err = c.video.Rewind(0)
	if err != nil {
		return err
	}
	c.videoPendingLoop = true
	return nil
}

// preconditions: c.mutex is locked
func (c *videoWithAudioController) noLockCreateAudioPlayer() error {
	var err error
	c.audioPlayer, err = audio.CurrentContext().NewPlayer(&struct{ io.Reader }{c})
	if err != nil {
		return err
	}
	c.audioPlayer.SetBufferSize(playerBufferSize)
	c.audioPlayer.SetVolume(c.getEffectiveVolume())
	c.needsFirstAudioFrameOffset = true
	return nil
}

// Note: this is being used to implement audio looping, since keeping
// the same audio player and manually adding an offset can lead to drifts
// that end up making the video stop or require more hacks for the logic.
// this is not great... but it works I guess. notice that the error has
// to be handled through a named error return variable, which might not
// be obvious that's happening and accidentally changed.
//
// preconditions: c.mutex is locked
func (c *videoWithAudioController) noLockHackyAudioReset() error {
	if err := c.noLockCreateAudioPlayer(); err != nil {
		return err
	}

	c.audioPlayer.Play()
	return nil
}

func (c *videoWithAudioController) internalReadFrame(lookForAudioFrame bool) error {
	// read packets until we come across the next relevant frame packet
	for {
		packet, packetFound, err := c.media.ReadPacket()
		if err != nil {
			return err
		}

		if !packetFound {
			if packet != nil {
				panic("broken code")
			}
			return nil
		}

		switch packet.Type() {
		case reisen.StreamVideo:
			if packet.StreamIndex() != c.video.Index() {
				continue
			}
			frame, frameFound, err := c.video.ReadVideoFrame()
			if err != nil {
				return err
			}
			_ = frameFound // frameFound can be true while frame is nil: that's a frame skip
			if frame != nil {
				c.leftoverVideo = append(c.leftoverVideo, frame)
				if !lookForAudioFrame {
					return nil
				}
			}
		case reisen.StreamAudio:
			if packet.StreamIndex() != c.audio.Index() {
				continue
			}
			frame, frameFound, err := c.audio.ReadAudioFrame()
			if err != nil {
				return err
			}
			_ = frameFound // frameFound can be true while frame is nil: that's a frame skip
			if frame != nil {
				if err != nil {
					return err
				}

				c.leftoverAudio = append(c.leftoverAudio, frame.Data()...)

				// if first audio frame since play, store its offset
				if c.needsFirstAudioFrameOffset {
					var err error
					c.firstAudioFrameOffsetOnPlay, err = frame.PresentationOffset()
					if err != nil {
						return err
					}
					c.needsFirstAudioFrameOffset = false
				}

				if lookForAudioFrame {
					return nil
				}
			}
		default:
			// ignore other packets (they exist and I don't know what they are)
		}
	}
}
