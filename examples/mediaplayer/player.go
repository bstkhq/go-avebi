// Package mediaplayer contains the player shared by this example's platform
// entrypoints.
package mediaplayer

import (
	"errors"
	"os"
	"sync"
	"time"

	"github.com/erparts/go-avebi"
	"github.com/hajimehoshi/ebiten/v2"
)

// Options configures platform-specific behavior without putting platform code
// in the shared player.
type Options struct {
	TerminateOnEscape bool
	UseYUVShader      bool
}

// Snapshot is a stable view of the player for diagnostics and integration
// tests. Durations are available to Go callers while the millisecond fields
// make the same state useful through go-ebiten-mcp.
type Snapshot struct {
	State       avebi.PlaybackState `json:"-"`
	StateName   string              `json:"state"`
	Position    time.Duration       `json:"-"`
	PositionMS  int64               `json:"position_ms"`
	Duration    time.Duration       `json:"-"`
	DurationMS  int64               `json:"duration_ms"`
	FramePTS    time.Duration       `json:"-"`
	FramePTSMS  int64               `json:"frame_pts_ms"`
	HasEnded    bool                `json:"has_ended"`
	HasAudio    bool                `json:"has_audio"`
	Looping     bool                `json:"looping"`
	Muted       bool                `json:"muted"`
	Volume      float64             `json:"volume"`
	VideoWidth  int                 `json:"video_width"`
	VideoHeight int                 `json:"video_height"`
	VideoCodec  string              `json:"video_codec"`
	YUVShader   bool                `json:"yuv_shader"`
	Updates     uint64              `json:"updates"`
	Error       string              `json:"error,omitempty"`
}

type pendingOpen struct {
	path  string
	owned bool
	set   bool
}

// Game implements ebiten.Game and owns every avebi operation on the update
// loop. Native hosts can safely queue operations from another thread.
type Game struct {
	mutex sync.Mutex
	opts  Options

	player *avebi.Player
	frame  *ebiten.Image

	filePicker    func()
	pickerPending bool
	pendingOpen   pendingOpen
	pendingSeek   time.Duration
	seekPending   bool
	closePending  bool
	ownedPath     string
	sourcePath    string
	lastError     error
	position      time.Duration
	duration      time.Duration
	framePTS      time.Duration
	state         avebi.PlaybackState
	looping       bool
	hasEnded      bool
	hasAudio      bool
	muted         bool
	volume        float64
	videoWidth    int
	videoHeight   int
	videoCodec    string
	layoutWidth   int
	layoutHeight  int
	updates       uint64
}

func New(options Options) *Game {
	return &Game{
		opts:         options,
		state:        avebi.Stopped,
		layoutWidth:  defaultLogicalWidth,
		layoutHeight: defaultLogicalHeight,
	}
}

// Open replaces the current media and starts playback immediately. It is
// intended for setup and code already running on the Ebitengine thread.
func (g *Game) Open(path string) error {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	err := g.openLocked(path, false)
	g.recordErrorLocked(err)
	return err
}

// QueueOpen schedules a media replacement on the Ebitengine update loop. When
// owned is true, the file is removed after it is replaced or closed.
func (g *Game) QueueOpen(path string, owned bool) {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	g.queueOpenLocked(path, owned)
}

func (g *Game) queueOpenLocked(path string, owned bool) {
	if g.pendingOpen.set && g.pendingOpen.owned {
		if g.pendingOpen.path == path {
			owned = true
		} else {
			_ = os.Remove(g.pendingOpen.path)
		}
	}
	g.pendingOpen = pendingOpen{path: path, owned: owned, set: true}
}

// QueueSeek schedules a seek on the Ebitengine update loop.
func (g *Game) QueueSeek(position time.Duration) {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	g.pendingSeek = position
	g.seekPending = true
}

// Seek moves playback immediately. It is primarily useful to code already
// synchronized with the Ebitengine update loop.
func (g *Game) Seek(position time.Duration) error {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	err := g.seekLocked(position)
	g.recordErrorLocked(err)
	return err
}

// QueueClose schedules release of the current player on the update loop.
func (g *Game) QueueClose() {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	if g.pendingOpen.set && g.pendingOpen.owned {
		_ = os.Remove(g.pendingOpen.path)
	}
	g.pendingOpen = pendingOpen{}
	g.seekPending = false
	g.closePending = true
}

// Close releases the player and any picker-owned file immediately.
func (g *Game) Close() error {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	if g.pendingOpen.set && g.pendingOpen.owned {
		_ = os.Remove(g.pendingOpen.path)
	}
	g.pendingOpen = pendingOpen{}
	g.seekPending = false
	g.closePending = false
	g.pickerPending = false
	return g.closeLocked()
}

// SetFilePicker installs the platform file-picker action. Passing nil removes
// the Open control from both the UI and its hit testing.
func (g *Game) SetFilePicker(open func()) {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	g.filePicker = open
	if open == nil {
		g.pickerPending = false
	}
}

// CompleteFilePicker delivers an asynchronous picker result. Picker files are
// treated as cache-owned and removed after use.
func (g *Game) CompleteFilePicker(path string, err error) {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	g.pickerPending = false
	if err != nil {
		if path != "" {
			_ = os.Remove(path)
		}
		g.recordErrorLocked(err)
		return
	}
	if path != "" {
		g.queueOpenLocked(path, true)
	}
}

// Error returns the latest asynchronous playback or picker error.
func (g *Game) Error() string {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	if g.lastError == nil {
		return ""
	}
	return g.lastError.Error()
}

// Snapshot returns the current diagnostic state.
func (g *Game) Snapshot() Snapshot {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	snapshot := Snapshot{
		State:       g.state,
		StateName:   g.state.String(),
		Position:    g.position,
		PositionMS:  g.position.Milliseconds(),
		Duration:    g.duration,
		DurationMS:  g.duration.Milliseconds(),
		FramePTS:    g.framePTS,
		FramePTSMS:  g.framePTS.Milliseconds(),
		HasEnded:    g.hasEnded,
		HasAudio:    g.hasAudio,
		Looping:     g.looping,
		Muted:       g.muted,
		Volume:      g.volume,
		VideoWidth:  g.videoWidth,
		VideoHeight: g.videoHeight,
		VideoCodec:  g.videoCodec,
		YUVShader:   g.opts.UseYUVShader,
		Updates:     g.updates,
	}
	if g.lastError != nil {
		snapshot.Error = g.lastError.Error()
	}
	return snapshot
}

func (g *Game) Update() error {
	g.mutex.Lock()
	layout := newControlLayout(g.layoutWidth, g.layoutHeight, g.filePicker != nil)
	g.mutex.Unlock()
	input := readControlInput(layout)

	var picker func()
	g.mutex.Lock()
	g.updates++
	if g.closePending {
		g.closePending = false
		g.recordErrorLocked(g.closeLocked())
	}
	if g.pendingOpen.set {
		pending := g.pendingOpen
		g.pendingOpen = pendingOpen{}
		g.recordErrorLocked(g.openLocked(pending.path, pending.owned))
	}
	if g.seekPending {
		g.seekPending = false
		g.recordErrorLocked(g.seekLocked(g.pendingSeek))
	}

	if g.opts.TerminateOnEscape && escapePressed() {
		err := g.closeLocked()
		g.mutex.Unlock()
		if err != nil {
			return err
		}
		return ebiten.Termination
	}

	if input.action == actionOpen && g.filePicker != nil && !g.pickerPending {
		g.pickerPending = true
		picker = g.filePicker
	} else {
		g.applyPlaybackControlLocked(input)
	}
	if g.player != nil {
		g.refreshPlayerLocked()
	}
	g.mutex.Unlock()

	if picker != nil {
		picker()
	}
	return nil
}

func (g *Game) openLocked(path string, owned bool) error {
	if path == "" {
		return errors.New("media path is empty")
	}
	if err := g.closeLocked(); err != nil {
		if owned {
			_ = os.Remove(path)
		}
		return err
	}

	err := avebi.CreateAudioContextForMedia(path)
	if err != nil && !errors.Is(err, avebi.ErrNoAudio) && !errors.Is(err, avebi.ErrNonNilAudioContext) {
		if owned {
			_ = os.Remove(path)
		}
		return err
	}
	mediaPlayer, err := avebi.NewPlayerWithOptions(path, &avebi.PlayerOptions{
		UseYUVShader: g.opts.UseYUVShader,
	})
	if err != nil {
		if owned {
			_ = os.Remove(path)
		}
		return err
	}
	if err := mediaPlayer.Play(); err != nil {
		_ = mediaPlayer.Close()
		if owned {
			_ = os.Remove(path)
		}
		return err
	}

	g.player = mediaPlayer
	g.sourcePath = path
	g.duration = mediaPlayer.Duration()
	g.state = avebi.Playing
	g.hasAudio = mediaPlayer.HasAudio()
	g.looping = mediaPlayer.GetLooping()
	g.muted = mediaPlayer.GetMuted()
	g.volume = mediaPlayer.GetVolume()
	g.videoWidth, g.videoHeight = mediaPlayer.Resolution()
	g.videoCodec = mediaPlayer.VideoCodec()
	if owned {
		g.ownedPath = path
	}
	g.lastError = nil
	return nil
}

func (g *Game) closeLocked() error {
	var err error
	if g.player != nil {
		err = g.player.Close()
	}
	g.player = nil
	g.frame = nil
	g.sourcePath = ""
	g.resetPlaybackStateLocked()
	g.removeOwnedPathLocked()
	return err
}

func (g *Game) resetPlaybackStateLocked() {
	g.position = 0
	g.duration = 0
	g.framePTS = 0
	g.state = avebi.Stopped
	g.looping = false
	g.hasEnded = false
	g.hasAudio = false
	g.muted = false
	g.volume = 0
	g.videoWidth = 0
	g.videoHeight = 0
	g.videoCodec = ""
}

func (g *Game) removeOwnedPathLocked() {
	if g.ownedPath == "" {
		return
	}
	_ = os.Remove(g.ownedPath)
	g.ownedPath = ""
}

func (g *Game) applyPlaybackControlLocked(input controlInput) {
	if g.player == nil {
		return
	}

	var err error
	switch input.action {
	case actionPlayPause:
		state, stateErr := g.player.State()
		if stateErr != nil {
			err = stateErr
		} else if state == avebi.Playing {
			err = g.player.Pause()
		} else {
			err = g.player.Play()
		}
	case actionStop:
		err = g.player.Stop()
	case actionSeekRelative:
		err = g.seekLocked(g.position + input.seekDelta)
	case actionSeekAbsolute:
		err = g.seekLocked(time.Duration(float64(g.duration) * input.seekRatio))
	case actionToggleLoop:
		g.player.SetLooping(!g.player.GetLooping())
	}
	g.recordErrorLocked(err)
}

func (g *Game) seekLocked(position time.Duration) error {
	if g.player == nil {
		return nil
	}
	if position < 0 {
		position = 0
	}
	if g.duration > 0 && position > g.duration {
		position = g.duration
	}
	return g.player.Seek(position)
}

func (g *Game) refreshPlayerLocked() {
	frame, err := g.player.CurrentFrame()
	if err != nil {
		g.recordErrorLocked(err)
	} else {
		g.frame = frame
	}
	if position, err := g.player.Position(); err != nil {
		g.recordErrorLocked(err)
	} else {
		g.position = position
	}
	if state, err := g.player.State(); err != nil {
		g.recordErrorLocked(err)
	} else {
		g.state = state
	}
	g.duration = g.player.Duration()
	g.framePTS = g.player.LastPresentationOffset()
	g.looping = g.player.GetLooping()
	g.hasEnded = g.player.HasEnded()
	g.hasAudio = g.player.HasAudio()
	g.muted = g.player.GetMuted()
	g.volume = g.player.GetVolume()
	g.videoWidth, g.videoHeight = g.player.Resolution()
	g.videoCodec = g.player.VideoCodec()
	g.recordErrorLocked(g.player.Error())
}

func (g *Game) recordErrorLocked(err error) {
	if err != nil {
		g.lastError = err
	}
}
