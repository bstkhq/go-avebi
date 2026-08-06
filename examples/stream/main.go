package main

import (
	"fmt"
	"image/color"
	"os"

	ebitenmcp "github.com/bstkhq/go-ebiten-mcp"
	"github.com/erparts/go-avebi"
	"github.com/hajimehoshi/ebiten/v2"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Println("Usage: go run main.go rtsp://<username>:<password>@<ip>:<port>")
		os.Exit(1)
	}

	path := os.Args[1]

	player, err := avebi.NewStreamPlayer(path)
	if err != nil {
		panic(err)
	}

	defer player.Close()

	if err := player.Play(); err != nil {
		panic(err)
	}

	ebiten.SetWindowTitle("Basic Stream Player")
	ebiten.SetWindowSize(1280, 720)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)

	g := &game{p: player}
	if err := ebitenmcp.RunGame(
		g,
		ebitenmcp.WithName("avebi-stream"),
		ebitenmcp.WithCaptureStage(ebitenmcp.StageOffscreen),
		ebitenmcp.WithState("player", func(current ebiten.Game) any {
			return current.(*game).snapshot()
		}),
	); err != nil {
		panic(err)
	}
}

type game struct {
	p       *avebi.Player
	frame   *ebiten.Image
	lastErr error
}

type streamSnapshot struct {
	State      string `json:"state"`
	PositionMS int64  `json:"position_ms"`
	FramePTS   int64  `json:"frame_pts_ms"`
	Error      string `json:"error,omitempty"`
}

func (g *game) snapshot() streamSnapshot {
	state, stateErr := g.p.State()
	position, positionErr := g.p.Position()
	result := streamSnapshot{
		State:      state.String(),
		PositionMS: position.Milliseconds(),
		FramePTS:   g.p.LastPresentationOffset().Milliseconds(),
	}
	for _, err := range []error{g.lastErr, stateErr, positionErr, g.p.Error()} {
		if err != nil {
			result.Error = err.Error()
			break
		}
	}
	return result
}

func (g *game) Update() error {
	if ebiten.IsKeyPressed(ebiten.KeyEscape) {
		return ebiten.Termination
	}

	f, err := g.p.CurrentFrame()
	if err != nil {
		g.lastErr = err
		fmt.Printf("error getting current frame: %v\n", err)
		return nil
	}
	g.frame = f
	return nil
}

func (g *game) Draw(screen *ebiten.Image) {
	screen.Fill(color.Black)
	avebi.Draw(screen, g.frame)
}

func (g *game) Layout(outsideWidth, outsideHeight int) (int, int) {
	return outsideWidth, outsideHeight
}
