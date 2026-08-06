//go:build !avebi_ffgo

package avebi

import (
	"time"

	"github.com/erparts/reisen"
)

// A common interface that helps us control the timing and position
// of the video.
type videoController interface {
	// --- playback state ---

	// Returns the playback state: [Stopped], [Playing] or [Paused].
	// TODO: state possibly updating the state is somewhat dangerous and
	// unexpected in certain situations.
	State() (PlaybackState, error)

	// Starts or resumes the video playback.
	Play() error

	// Pauses the video. If the video is already not playing, it does nothing.
	Pause() error

	// Stops playing and rewinds to position 0.
	Stop() error

	// Permanently closes the video. The controller becomes unusable after this.
	Close() error

	// Moves to the specified position. The playing/paused state should be unaffected.
	Seek(time.Duration) (*reisen.VideoFrame, error)

	// --- timing ---

	// Returns the current playback position. If the video is [Stopped],
	// the position can only be 0 (start) or Duration() (end).
	Position() (time.Duration, error)

	// Returns the total video length.
	Duration() time.Duration

	// --- looping ---

	// Sets whether the video should loop back to the start when reaching the end or not.
	SetLooping(bool)

	// Gets whether the video is configured to loop or not. See SetLooping().
	GetLooping() bool

	// --- raw methods for reisen values ---

	// Returns the current video frame, and whether we reached the end of the video.
	CurrentVideoFrame() (*reisen.VideoFrame, bool, error)

	// Returns the last decode error. This is useful to handle errors during the
	// audio Read operation.
	Error() error
}

// aux type for noLockStop operations on both video only and standard video controllers
type stopMode bool

const (
	stopModeManual     stopMode = true
	stopModeEndOfVideo stopMode = false
)
