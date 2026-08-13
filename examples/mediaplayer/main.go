package main

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	ebitenmcp "github.com/bstkhq/go-ebiten-mcp"
	"github.com/erparts/go-avebi"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
)

func main() {
	// get video path
	if len(os.Args) != 2 {
		fmt.Printf("Usage: go run main.go path/to/video.mp4\n")
		os.Exit(1)
	}

	// check that file exists
	path, err := filepath.Abs(os.Args[1])
	if err != nil {
		panic(err)
	}
	_, err = os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Printf("'%s' not found.", path)
			os.Exit(1)
		}
		panic(err)
	}

	// create video player
	err = avebi.CreateAudioContextForMedia(path)
	if errors.Is(err, avebi.ErrNoAudio) {
		fmt.Printf("WARNING: no audio found in video, omitting audio context creation\n")
		err = nil
	}
	if err != nil {
		panic(err)
	}

	videoPlayer, err := avebi.NewPlayer(path)
	if err != nil {
		panic(err)
	}
	videoPlayer.Play()

	// window config
	ebiten.SetWindowTitle("avebi/mediaplayer")
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetWindowSize(1280, 720)

	// Create the test app. go-ebiten-mcp is inert unless EBITEN_MCP_ADDR is
	// configured, so normal example runs keep their original behavior.
	game := &MediaPlayer{
		videoPath:   path,
		videoPlayer: videoPlayer,
		duration:    videoPlayer.Duration(),
	}
	err = ebitenmcp.RunGame(
		game,
		ebitenmcp.WithName("avebi-mediaplayer"),
		ebitenmcp.WithCaptureStage(ebitenmcp.StageOffscreen),
		ebitenmcp.WithState("player", func(current ebiten.Game) any {
			return current.(*MediaPlayer).snapshot()
		}),
	)
	if err != nil {
		panic(err)
	}
}

type MediaPlayer struct {
	videoPath   string
	videoPlayer *avebi.Player
	videoFrame  *ebiten.Image

	lastPosition time.Duration
	duration     time.Duration

	rectVertices  [4]ebiten.Vertex // clockwise starting from top-left
	rectWhiteMask *ebiten.Image
}

type playerSnapshot struct {
	State      string  `json:"state"`
	PositionMS int64   `json:"position_ms"`
	DurationMS int64   `json:"duration_ms"`
	FramePTS   int64   `json:"frame_pts_ms"`
	HasEnded   bool    `json:"has_ended"`
	HasAudio   bool    `json:"has_audio"`
	Looping    bool    `json:"looping"`
	Muted      bool    `json:"muted"`
	Volume     float64 `json:"volume"`
	Error      string  `json:"error,omitempty"`
}

func (m *MediaPlayer) snapshot() playerSnapshot {
	state, err := m.videoPlayer.State()
	if err == nil {
		err = m.videoPlayer.Error()
	}
	result := playerSnapshot{
		State:      state.String(),
		PositionMS: m.lastPosition.Milliseconds(),
		DurationMS: m.duration.Milliseconds(),
		FramePTS:   m.videoPlayer.LastPresentationOffset().Milliseconds(),
		HasEnded:   m.videoPlayer.HasEnded(),
		HasAudio:   m.videoPlayer.HasAudio(),
		Looping:    m.videoPlayer.GetLooping(),
		Muted:      m.videoPlayer.GetMuted(),
		Volume:     m.videoPlayer.GetVolume(),
	}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

func (m *MediaPlayer) Layout(_, _ int) (int, int) {
	panic("Layout() should not be called when LayoutF() exists")
}

func (m *MediaPlayer) LayoutF(w, h float64) (float64, float64) {
	scaleFactor := ebiten.Monitor().DeviceScaleFactor()
	return w * scaleFactor, h * scaleFactor
}

func (m *MediaPlayer) Draw(canvas *ebiten.Image) {
	avebi.Draw(canvas, m.videoFrame)
	m.drawGUI(canvas)
}

func (m *MediaPlayer) Update() error {
	var err error
	m.videoFrame, err = m.videoPlayer.CurrentFrame()
	if err != nil {
		return err
	}

	m.lastPosition, err = m.videoPlayer.Position()
	if err != nil {
		return err
	}

	if inpututil.IsKeyJustPressed(ebiten.KeyEscape) {
		err := m.videoPlayer.Close()
		if err != nil {
			return err
		}
		return ebiten.Termination
	}

	if inpututil.IsKeyJustPressed(ebiten.KeyP) || inpututil.IsKeyJustPressed(ebiten.KeySpace) {
		state, err := m.videoPlayer.State()
		if err != nil {
			return err
		}
		if state == avebi.Playing {
			err := m.videoPlayer.Pause()
			if err != nil {
				return err
			}
		} else {
			err := m.videoPlayer.Play()
			if err != nil {
				return err
			}
		}
	} else if inpututil.IsKeyJustPressed(ebiten.KeyS) {
		err := m.videoPlayer.Stop()
		if err != nil {
			return err
		}
	} else if inpututil.IsKeyJustPressed(ebiten.KeyL) {
		m.videoPlayer.SetLooping(!m.videoPlayer.GetLooping())
	} else if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) {
		err := m.videoPlayer.Seek(max(0, m.lastPosition-time.Second))
		if err != nil {
			return err
		}
	} else if inpututil.IsKeyJustPressed(ebiten.KeyArrowRight) {
		err := m.videoPlayer.Seek(min(m.duration, m.lastPosition+time.Second))
		if err != nil {
			return err
		}
	}

	if inpututil.IsKeyJustPressed(ebiten.KeyI) {
		state, err := m.videoPlayer.State()
		if err != nil {
			return err
		}
		fmt.Printf("Video state: %s\n", state.String())
	}

	if err := m.videoPlayer.Error(); err != nil {
		return fmt.Errorf("video player error: %w", err)
	}

	return nil
}

// --- additional info and instructions ---

func (m *MediaPlayer) drawGUI(canvas *ebiten.Image) {
	bounds := canvas.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	playWidth := (w * 2) / 3
	playHeight := h / 48
	ox := (w - playWidth) / 2
	oy := h - playHeight*2

	m.setRectTopColor(color.RGBA{0, 0, 0, 0})
	m.setRectBottomColor(color.RGBA{0, 0, 0, 128})
	fadeBounds := bounds
	fadeBounds.Min.Y = fadeBounds.Max.Y - fadeBounds.Dy()/8
	m.drawRect(canvas, fadeBounds)

	playRect := image.Rect(ox, oy, ox+playWidth, oy+playHeight)
	m.setRectColor(color.RGBA{255, 255, 255, 255})
	m.drawRect(canvas, playRect)

	const BorderThickness = 3
	playRect = insetRect(playRect, BorderThickness)
	m.setRectColor(color.RGBA{0, 0, 0, 255})
	m.drawRect(canvas, playRect)

	const InnerMargin = 2
	playRect = insetRect(playRect, InnerMargin)
	t := float64(m.lastPosition) / float64(m.duration)
	playRect.Max.X = playRect.Min.X + int(float64(playRect.Dx())*t)
	m.setRectColor(color.RGBA{255, 255, 255, 255})
	m.drawRect(canvas, playRect)

	positionStr := durationToMMSS(m.lastPosition)
	durationStr := durationToMMSS(m.duration)
	spaceAction := "play"
	if state, _ := m.videoPlayer.State(); state == avebi.Playing {
		spaceAction = "pause"
	}
	loopAction := "enable"
	if m.videoPlayer.GetLooping() {
		loopAction = "disable"
	}
	info := positionStr + " / " + durationStr + " (SPACE to " + spaceAction + ", LEFT/RIGHT to seek, S to stop, L to " + loopAction + " looping)"
	if m.videoPlayer.HasEnded() {
		info += " (ended)"
	}
	ebitenutil.DebugPrintAt(canvas, info, ox, oy-16)
}

func insetRect(rect image.Rectangle, in int) image.Rectangle {
	return image.Rect(rect.Min.X+in, rect.Min.Y+in, rect.Max.X-in, rect.Max.Y-in)
}

func durationToMMSS(duration time.Duration) string {
	millis := duration.Milliseconds()
	rem := millis
	seconds := rem / 1000
	minutes := seconds / 60
	seconds = seconds % 60
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}

func (m *MediaPlayer) drawRect(canvas *ebiten.Image, rect image.Rectangle) {
	if m.rectWhiteMask == nil {
		m.rectWhiteMask = ebiten.NewImage(1, 1)
		m.rectWhiteMask.Fill(color.White)
		for i := range m.rectVertices {
			m.rectVertices[i].SrcX = 0.5
			m.rectVertices[i].SrcY = 0.5
		}
	}

	m.rectVertices[0].DstX = float32(rect.Min.X) // top-left
	m.rectVertices[0].DstY = float32(rect.Min.Y) // top-left
	m.rectVertices[1].DstX = float32(rect.Max.X) // top-right
	m.rectVertices[1].DstY = float32(rect.Min.Y) // top-right
	m.rectVertices[2].DstX = float32(rect.Max.X) // bottom-right
	m.rectVertices[2].DstY = float32(rect.Max.Y) // bottom-right
	m.rectVertices[3].DstX = float32(rect.Min.X) // bottom-left
	m.rectVertices[3].DstY = float32(rect.Max.Y) // bottom-left
	canvas.DrawTriangles(m.rectVertices[:], []uint16{0, 1, 2, 2, 3, 0}, m.rectWhiteMask, nil)
}

func (m *MediaPlayer) setRectColor(clr color.RGBA) {
	r, g, b, a := rgbaToF32(clr)
	for i := range 4 {
		setVertexColor(&m.rectVertices[i], r, g, b, a)
	}
}

func (m *MediaPlayer) setRectTopColor(clr color.RGBA) {
	r, g, b, a := rgbaToF32(clr)
	setVertexColor(&m.rectVertices[0], r, g, b, a)
	setVertexColor(&m.rectVertices[1], r, g, b, a)
}

func (m *MediaPlayer) setRectBottomColor(clr color.RGBA) {
	r, g, b, a := rgbaToF32(clr)
	setVertexColor(&m.rectVertices[2], r, g, b, a)
	setVertexColor(&m.rectVertices[3], r, g, b, a)
}

func setVertexColor(vertex *ebiten.Vertex, r, g, b, a float32) {
	vertex.ColorR = r
	vertex.ColorG = g
	vertex.ColorB = b
	vertex.ColorA = a
}

func rgbaToF32(rgba color.RGBA) (float32, float32, float32, float32) {
	return float32(rgba.R) / 255.0, float32(rgba.G) / 255.0, float32(rgba.B) / 255.0, float32(rgba.A) / 255.0
}
