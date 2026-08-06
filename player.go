//go:build !avebi_ffgo

package avebi

import (
	"errors"
	"fmt"
	"image/color"
	"path/filepath"
	"time"

	"github.com/erparts/reisen"
	"github.com/hajimehoshi/ebiten/v2"
)

// NOTES:
// - check if mono audio is relevant?

// TODO:
// - advancing one frame can be exposed through the controller, with a manual operation
//   that decreases the reference time to make the new position match the next frame. we
//   can make it only work while the video is paused, and it doesn't affect anything else,
//   it uses the same underlying "current frame" logic in a pretty clean way

// A collection of initialization errors defined by this package for [NewPlayer]().
// Other format-specific errors are also possible.
var (
	ErrNoVideo         = errors.New("file doesn't include any video stream")
	ErrNilAudioContext = errors.New("file has audio stream but audio.Context is not initialized")
	ErrBadSampleRate   = errors.New("file audio stream and audio context sample rates don't match")
	ErrTooManyChannels = errors.New("file audio streams with more than 2 channels are not supported")
)

// A [Player] represents a video player, typically also including audio.
//
// The player is a simple abstraction layer or wrapper around the lower level
// [reisen] types, which implement the underlying decoders used to make playing
// video possible on Ebitengine.
//
// Usage is quite similar to Ebitengine audio players:
//   - Create a [NewPlayer]().
//   - Call [Player.Play()] to start the video.
//   - Audio will play automatically. Frames are obtained with [Player.CurrentFrame]().
//   - Use [Player.Pause]() and [Player.Stop]() to control the video.
//
// More methods are available, but that's the main idea.
//
// [erparts/reisen]: https://github.com/erparts/reisen
type Player struct {
	controller        videoController
	currentFrame      *ebiten.Image
	currentPresOffset time.Duration // presentation offset of the current frame
	frameDuration     time.Duration // TODO: cleanup, remove most likely
	onBlackFrame      bool
	reachedEnd        bool
}

// Like [NewPlayer](), but ignoring audio streams.
func NewPlayerWithoutAudio(videoFilename string) (*Player, error) {
	ignoreAudio := true
	return newPlayer(videoFilename, ignoreAudio, nil)
}

// Creates a new video [Player]. TODO: ideally we would use io.ReadSeeker,
// but reisen only has support for explicit filenames.
func NewPlayer(videoFilename string) (*Player, error) {
	ignoreAudio := false
	return newPlayer(videoFilename, ignoreAudio, nil)
}

// Like [NewPlayer](), but for live streams.
func NewStreamPlayer(videoFilename string) (*Player, error) {
	return NewStreamPlayerWithOptions(videoFilename, nil)
}

type StreamOptions struct {
	// ConnTimeout defines the timeout for the initial connection attempt.
	ConnTimeout time.Duration

	// ReadTimeout defines the max blocking time for the internal video packet
	// reads. This affects Close() max blocking time, as Close() needs to wait
	// for packet reads to finish. If unset, a default of 200ms will be used.
	ReadTimeout time.Duration
}

func NewStreamPlayerWithOptions(videoFilename string, options *StreamOptions) (*Player, error) {
	if options == nil {
		options = &StreamOptions{}
	}
	if options.ReadTimeout == 0 {
		options.ReadTimeout = 200 * time.Millisecond
	}
	return newPlayer(videoFilename, true, options)
}

func newPlayer(videoFilename string, ignoreAudio bool, streamOptions *StreamOptions) (*Player, error) {
	// initialize stream
	var container *reisen.Media
	var err error

	if streamOptions != nil && streamOptions.ConnTimeout != 0 {
		mediaOpts := reisen.Options{Timeout: streamOptions.ConnTimeout}
		container, err = reisen.NewMediaWithOptions(videoFilename, &mediaOpts)
	} else {
		container, err = reisen.NewMedia(videoFilename)
	}
	if err != nil {
		return nil, err
	}

	// make sure there's video stream and headers
	videoStreams := container.VideoStreams()
	audioStreams := container.AudioStreams()
	if len(videoStreams) == 0 {
		return nil, ErrNoVideo
	}
	if len(videoStreams) > 1 {
		pkgLogger.Printf("WARNING: '%s' has multiple video streams; defaulting to the first", filepath.Base(videoFilename))
	}
	videoStream := videoStreams[0]

	// compute frame duration for later use
	frNum, frDenom := videoStream.FrameRate()
	frameDuration := (time.Second * time.Duration(frDenom)) / time.Duration(frNum)

	// check if there's audio streams
	var controller videoController

	switch {
	case streamOptions != nil:
		controller, err = newStreamVideoController(container, videoStream, streamOptions)
	case len(audioStreams) > 0 && !ignoreAudio:
		controller, err = newVideoWithAudioController(container, videoStream, audioStreams[0])
	default:
		controller, err = newVideoOnlyController(container, videoStream)
	}

	if err != nil {
		return nil, err
	}

	// create video player
	w, h := videoStream.Width(), videoStream.Height()
	if w == 0 || h == 0 {
		return nil, fmt.Errorf("invalid stream resolution %dx%d", w, h)
	}
	img := ebiten.NewImage(w, h)
	img.Fill(color.Black)
	return &Player{
		currentFrame:  img,
		controller:    controller,
		frameDuration: frameDuration,
		onBlackFrame:  true,
	}, nil
}

// --- frames and resolution ---

// Returns the image corresponding to the underlying video stream frame at
// the current [Player.Position](). This means that as long as the video is
// playing, calling this method at different times will return different
// frames.
//
// The returned image is reused, so calling this method again will overwrite
// its contents. This means you can use the image between calls, but you should
// not store it for later use expecting the image to remain the same.
func (p *Player) CurrentFrame() (*ebiten.Image, error) {
	frame, reachedEnd, err := p.controller.CurrentVideoFrame()
	if err != nil {
		return nil, err
	}
	if reachedEnd {
		p.reachedEnd = true
	}
	if frame == nil {
		// we either reached end or had been stopped already
		if !p.reachedEnd {
			p.copyFrame(frame)
		}
		return p.currentFrame, nil
	}

	presOffset, err := frame.PresentationOffset()
	if err != nil {
		return nil, err
	}

	if presOffset != p.currentPresOffset || p.currentFrame == nil || p.onBlackFrame { // *
		// * the p.onBlackFrame condition is for safety to disambiguate the zero
		//   value of currentPresOffset with frames starting at exactly 0
		p.currentPresOffset = presOffset
		p.copyFrame(frame)
		return p.currentFrame, nil
	}
	return p.currentFrame, nil
}

// LastPresentationOffset returns the presentation offset for the last frame obtained
// with [Player.CurrentFrame](). This is a low-level function intended mainly for stream
// health checks and debug.
func (p *Player) LastPresentationOffset() time.Duration {
	return p.currentPresOffset
}

// Advances the video stream by one frame. This can be used while a video is paused to
// examine it frame by frame. Going back is not natively supported by the streams and
// would require a much more complex implementation.
func (p *Player) NextVideoFrame() (*ebiten.Image, error) {
	panic("unimplemented")
}

// Returns the width and height of the video.
func (p *Player) Resolution() (int, int) {
	// resolution could also be obtained from the video stream itself
	bounds := p.currentFrame.Bounds()
	return bounds.Dx(), bounds.Dy()
}

// ---- video playback states ----

// Returns the current player's state, which can be [Stopped], [Playing] or
// [Paused]. Notice that even when playing, video frames need to be retrieved
// manually through [Player.CurrentFrame]().
func (p *Player) State() (PlaybackState, error) { return p.controller.State() }

// HasEnded returns whether the video has ended.
func (p *Player) HasEnded() bool { return p.reachedEnd }

// Play() activates the player's playback clock. If the player is already
// playing, it just keeps playing and nothing new happens.
//
// If the underlying stream contains any audio, the audio will also
// start or resume. Video frames need to be retrieved manually through
// [Player.CurrentFrame]() instead.
func (p *Player) Play() error {
	if p.reachedEnd {
		p.copyFrame(nil)
		p.currentPresOffset = 0
		p.reachedEnd = false
	}

	return p.controller.Play()
}

// Pauses the player's playback clock. If the player is already paused, it
// just stays paused and nothing new happens.
//
// If the underlying mpeg contains any audio, the audio will also be paused.
func (p *Player) Pause() error { return p.controller.Pause() }

// Stops the player. Using [Player.Play]() again will cause the video to
// restart from the beginning.
func (p *Player) Stop() error {
	p.currentPresOffset = 0
	p.copyFrame(nil)
	return p.controller.Stop()
}

// --- timing ---

// Returns the player's current playback position. If the video is
// [Stopped], the position can only be 0 (start) or [Player.Duration]().
// (if the video naturally reached the end).
func (p *Player) Position() (time.Duration, error) {
	return p.controller.Position()
}

// Returns the video duration.
func (p *Player) Duration() time.Duration {
	return p.controller.Duration()
}

// --- audio ---

// Returns whether the video has audio.
func (p *Player) HasAudio() bool {
	_, isVideoWithAudio := p.controller.(*videoWithAudioController)
	return isVideoWithAudio
}

// Gets the video's volume. If the video has no audio, 0 will be returned.
func (p *Player) GetVolume() float64 {
	controller, isVideoWithAudio := p.controller.(*videoWithAudioController)
	if !isVideoWithAudio {
		return 0
	}
	return controller.GetVolume()
}

// Sets the volume of the video. If the video has no audio, this method will have no effect.
func (p *Player) SetVolume(volume float64) {
	controller, isVideoWithAudio := p.controller.(*videoWithAudioController)
	if isVideoWithAudio {
		controller.SetVolume(volume)
	}
}

// Returns whether the video is muted or not. If the video has no audio,
// true will be returned.
func (p *Player) GetMuted() bool {
	controller, isVideoWithAudio := p.controller.(*videoWithAudioController)
	if isVideoWithAudio {
		return controller.GetMuted()
	} else {
		return true
	}
}

// Mutes or unmutes the video. If the video has no audio, this method will have no effect.
func (p *Player) SetMuted(muted bool) {
	controller, isVideoWithAudio := p.controller.(*videoWithAudioController)
	if isVideoWithAudio {
		controller.SetMuted(muted)
	}
}

// --- looping ---

func (p *Player) SetLooping(looping bool) {
	p.controller.SetLooping(looping)
}

func (p *Player) GetLooping() bool {
	return p.controller.GetLooping()
}

func (p *Player) Error() error {
	return p.controller.Error()
}

// --- advanced operations ---

// Completely closes the video player, freeing associated resources. This makes
// the player unusable afterwards. I honestly don't know how necessary this is,
// but the resources are allocated through cgo, so if possible, use this method.
// This should be treated like a C free() operation.
//
// Do not confuse with [Player.Stop]().
func (p *Player) Close() error {
	return p.controller.Close()
}

// Moves the player's playback position to the given one, relative to the start
// of the video.
//
// The precision of the method is not well explored, and it might depend on the
// amount of inter-frames encoded in the video.
func (p *Player) Seek(position time.Duration) error {
	frame, err := p.controller.Seek(position)
	if err != nil {
		return err
	}

	p.copyFrame(frame)
	start, err := frame.PresentationOffset()
	if err != nil {
		panic(err)
	}
	p.currentPresOffset = start
	return nil
}

// --- internal ---

func (p *Player) copyFrame(frame *reisen.VideoFrame) {
	if frame == nil {
		if !p.onBlackFrame {
			p.currentFrame.Fill(color.Black)
			p.onBlackFrame = true
		}
	} else {
		p.currentFrame.WritePixels(frame.Data())
		p.onBlackFrame = false
	}
}
