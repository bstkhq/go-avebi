//go:build !android && !ios

package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	ebitenmcp "github.com/bstkhq/go-ebiten-mcp"
	mediaplayer "github.com/erparts/go-avebi/examples/mediaplayer"
	"github.com/hajimehoshi/ebiten/v2"
)

func main() {
	useYUVShader := flag.Bool("yuv-shader", false, "convert supported YUV video with an Ebitengine shader")
	flag.Usage = func() {
		fmt.Printf("Usage: go run ./examples/mediaplayer/desktop [--yuv-shader] path/to/video.mp4\n")
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(1)
	}

	path, err := filepath.Abs(flag.Arg(0))
	if err != nil {
		panic(err)
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Printf("'%s' not found.\n", path)
			os.Exit(1)
		}
		panic(err)
	}

	game := mediaplayer.New(mediaplayer.Options{
		TerminateOnEscape: true,
		UseYUVShader:      *useYUVShader,
	})
	if err := game.Open(path); err != nil {
		panic(err)
	}
	defer func() { _ = game.Close() }()

	ebiten.SetWindowTitle("avebi/mediaplayer")
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetWindowSize(1280, 720)

	if err := ebitenmcp.RunGame(
		game,
		ebitenmcp.WithName("avebi-mediaplayer"),
		ebitenmcp.WithCaptureStage(ebitenmcp.StageOffscreen),
		ebitenmcp.WithState("player", func(current ebiten.Game) any {
			return current.(*mediaplayer.Game).Snapshot()
		}),
	); err != nil {
		panic(err)
	}
}
