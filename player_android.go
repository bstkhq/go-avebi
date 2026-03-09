//go:build android

package avebi

import (
	"errors"
	"image/color"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
)

// A collection of initialization errors defined by this package for [NewPlayer]().
// Other format-specific errors are also possible.
var (
	ErrNoVideo         = errors.New("file doesn't include any video stream")
	ErrNilAudioContext = errors.New("file has audio stream but audio.Context is not initialized")
	ErrBadSampleRate   = errors.New("file audio stream and audio context sample rates don't match")
	ErrTooManyChannels = errors.New("file audio streams with more than 2 channels are not supported")
)

// A [Player] represents a mock video player on Android.
//
// This implementation exists so packages depending on avebi can compile for
// Android even though the desktop backend is not available there.
type Player struct {
	currentFrame      *ebiten.Image
	currentPresOffset time.Duration
	frameDuration     time.Duration
	onBlackFrame      bool
	reachedEnd        bool
	state             PlaybackState
	looping           bool
	volume            float64
	muted             bool
	hasAudio          bool
	duration          time.Duration
	position          time.Duration
	err               error
}

// Like [NewPlayer](), but ignoring audio streams.
func NewPlayerWithoutAudio(videoFilename string) (*Player, error) {
	player := newMockPlayer()
	player.hasAudio = false
	return player, nil
}

// Creates a new mock video [Player].
func NewPlayer(videoFilename string) (*Player, error) {
	player := newMockPlayer()
	player.hasAudio = true
	return player, nil
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
	player := newMockPlayer()
	player.hasAudio = false
	return player, nil
}

func newMockPlayer() *Player {
	img := ebiten.NewImage(16, 16)
	img.Fill(color.Black)
	return &Player{
		currentFrame:  img,
		frameDuration: time.Second / 60,
		onBlackFrame:  true,
		state:         Stopped,
		volume:        1,
		muted:         true,
	}
}

// Returns the image corresponding to the underlying video stream frame at
// the current [Player.Position]().
func (p *Player) CurrentFrame() (*ebiten.Image, error) {
	return p.currentFrame, p.err
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
	return p.currentFrame, p.err
}

// Returns the width and height of the video.
func (p *Player) Resolution() (int, int) {
	bounds := p.currentFrame.Bounds()
	return bounds.Dx(), bounds.Dy()
}

// Returns the current player's state, which can be [Stopped], [Playing] or
// [Paused]. Notice that even when playing, video frames need to be retrieved
// manually through [Player.CurrentFrame]().
func (p *Player) State() (PlaybackState, error) { return p.state, p.err }

// HasEnded returns whether the video has ended.
func (p *Player) HasEnded() bool { return p.reachedEnd }

// Play() activates the player's playback clock.
func (p *Player) Play() error {
	p.state = Playing
	p.reachedEnd = false
	return p.err
}

// Pauses the player's playback clock.
func (p *Player) Pause() error {
	p.state = Paused
	return p.err
}

// Stops the player. Using [Player.Play]() again will cause the video to
// restart from the beginning.
func (p *Player) Stop() error {
	p.state = Stopped
	p.position = 0
	p.currentPresOffset = 0
	p.reachedEnd = false
	if !p.onBlackFrame {
		p.currentFrame.Fill(color.Black)
		p.onBlackFrame = true
	}
	return p.err
}

// Returns the player's current playback position. If the video is
// [Stopped], the position can only be 0 (start) or [Player.Duration]().
// (if the video naturally reached the end).
func (p *Player) Position() (time.Duration, error) {
	return p.position, p.err
}

// Returns the video duration.
func (p *Player) Duration() time.Duration {
	return p.duration
}

// Returns whether the video has audio.
func (p *Player) HasAudio() bool {
	return p.hasAudio
}

// Gets the video's volume. If the video has no audio, 0 will be returned.
func (p *Player) GetVolume() float64 {
	if !p.hasAudio {
		return 0
	}
	return p.volume
}

// Sets the volume of the video. If the video has no audio, this method will have no effect.
func (p *Player) SetVolume(volume float64) {
	if p.hasAudio {
		p.volume = volume
	}
}

// Returns whether the video is muted or not. If the video has no audio,
// true will be returned.
func (p *Player) GetMuted() bool {
	if p.hasAudio {
		return p.muted
	}
	return true
}

// Mutes or unmutes the video. If the video has no audio, this method will have no effect.
func (p *Player) SetMuted(muted bool) {
	if p.hasAudio {
		p.muted = muted
	}
}

// --- looping ---

func (p *Player) SetLooping(looping bool) {
	p.looping = looping
}

func (p *Player) GetLooping() bool {
	return p.looping
}

func (p *Player) Error() error {
	return p.err
}

// Completely closes the video player, freeing associated resources. This makes
// the player unusable afterwards.
func (p *Player) Close() error {
	p.state = Stopped
	return p.err
}

// Moves the player's playback position to the given one, relative to the start
// of the video.
func (p *Player) Seek(position time.Duration) error {
	if position < 0 {
		position = 0
	}
	p.position = position
	p.currentPresOffset = position
	return p.err
}
