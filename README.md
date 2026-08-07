# avebi

avebi plays local video and live video streams in
[Ebitengine](https://ebitengine.org/) applications. It decodes media through
[ffgo](https://github.com/obinnaokechukwu/ffgo), converts video frames to RGBA
for `ebiten.Image`, and feeds decoded audio to Ebitengine's audio context.

## Features

- Local playback with play, pause, stop, seek, replay and looping.
- Optional synchronized audio with volume and mute controls.
- Video-only live stream playback with connection and read timeouts.
- Aspect-ratio-preserving rendering through `avebi.Draw`.
- Runtime FFmpeg loading through purego.

## Requirements and compatibility

- Go 1.25 or newer.
- Ebitengine 2.9.9.
- FFmpeg 6.x or 7.x shared libraries available at runtime.
- A desktop `amd64` or `arm64` target. iOS and Android are not supported.

The integration suite currently validates Linux `amd64` with FFmpeg 6.1 and
7.1.1. Codec and container availability still depends on how the installed
FFmpeg libraries were built. FFmpeg 8 is not currently supported.

## Installation

```sh
go get github.com/erparts/go-avebi
```

Until the required ffgo fixes are released upstream, applications importing
avebi must also apply its temporary dependency override:

```sh
go mod edit -replace=github.com/obinnaokechukwu/ffgo=github.com/erparts/ffgo@v0.0.0-20260806221945-0fd8f50d50c3
go mod tidy
```

Go does not propagate `replace` directives from dependencies. The override can
be removed once avebi updates to an upstream ffgo release containing the fixes.
See [development and testing](docs/development.md) for the pinned changes.

### Selecting an FFmpeg installation

ffgo checks the platform's dynamic-library search path before standard system
locations. Set it before starting the program and point it at a directory that
contains only the FFmpeg family you want to load.

Linux:

```sh
LD_LIBRARY_PATH=/opt/ffmpeg-7/lib go run .
```

macOS:

```sh
DYLD_LIBRARY_PATH=/opt/ffmpeg-7/lib go run .
```

Windows PowerShell:

```powershell
$env:PATH = "C:\ffmpeg-7\bin;$env:PATH"
go run .
```

The path must be configured before process startup because ffgo initializes its
bindings while Go packages are initialized. If one directory contains both
families, ffgo prefers FFmpeg 7 and verifies that all loaded libraries belong to
the same supported ABI.

## Basic usage

Create the audio context before the player when audio playback is wanted. Files
without audio can use the same setup; `ErrNoAudio` simply leaves audio disabled.

```go
package main

import (
	"errors"
	"log"
	"os"

	"github.com/erparts/go-avebi"
	"github.com/hajimehoshi/ebiten/v2"
)

type game struct {
	player *avebi.Player
	frame  *ebiten.Image
}

func (g *game) Update() error {
	frame, err := g.player.CurrentFrame()
	if err != nil {
		return err
	}
	g.frame = frame
	return g.player.Error()
}

func (g *game) Draw(screen *ebiten.Image) {
	if g.frame != nil {
		avebi.Draw(screen, g.frame)
	}
}

func (*game) Layout(_, _ int) (int, int) { return 1280, 720 }

func main() {
	path := os.Args[1]
	if err := avebi.CreateAudioContextForMedia(path); err != nil && !errors.Is(err, avebi.ErrNoAudio) {
		log.Fatal(err)
	}

	player, err := avebi.NewPlayer(path)
	if err != nil {
		log.Fatal(err)
	}
	defer player.Close()
	if err := player.Play(); err != nil {
		log.Fatal(err)
	}

	if err := ebiten.RunGame(&game{player: player}); err != nil {
		log.Fatal(err)
	}
}
```

Use `NewPlayerWithoutAudio` when no audio context should be created. The player
also exposes `Pause`, `Stop`, `Seek`, `Position`, `Duration`, `State`,
`HasEnded`, loop controls, and volume/mute controls.

For a video-only live stream:

```go
player, err := avebi.NewStreamPlayer("rtsp://example.test/live")
```

Connection and read deadlines can be configured with
`NewStreamPlayerWithOptions`.

## Examples

```sh
go run ./examples/mediaplayer /path/to/video.mp4
go run ./examples/stream rtsp://example.test/live
```

The examples are instrumented for automated interaction but behave as ordinary
Ebitengine applications when that instrumentation is not enabled.

## Current limitations

- Live streams are video-only.
- `NextVideoFrame` is not implemented.
- Hardware-accelerated decoding is not enabled by avebi.
- FFmpeg 8 and mobile platforms are not supported.

See [development and testing](docs/development.md) for the compatibility matrix,
integration fixtures and stress-test commands.
