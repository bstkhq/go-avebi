# avebi

avebi plays local video and live video streams in
[Ebitengine](https://ebitengine.org/) applications. It decodes media through
[go-ffmpeg-ffi](https://github.com/bstkhq/go-ffmpeg-ffi/releases/tag/v1.1.0),
converts video frames to RGBA
for `ebiten.Image`, and feeds decoded audio to Ebitengine's audio context.

## Features

- Local playback with play, pause, stop, seek, replay and looping.
- Optional synchronized audio with volume and mute controls.
- Video-only live stream playback with connection and read timeouts.
- Aspect-ratio-preserving rendering through `avebi.Draw`.
- Runtime FFmpeg loading through purego.
- Automatic platform-aware hardware decoding with software fallback.

## Requirements and compatibility

- Go 1.25 or newer.
- Ebitengine 2.9.9.
- FFmpeg 6.x, 7.x, 8.x or 9.x shared libraries available at runtime.
- A supported 64-bit target: Linux, macOS, Windows, Android or iOS on the
  architectures listed in the [platform matrix](docs/platforms.md).

The integration suite validates Linux `amd64` with FFmpeg 6.1.6, 7.1.5, 8.1.2
and 9.0.1. Platform CI also validates native playback on the desktop targets
where GitHub provides runners and compiles Ebitengine bindings for Android and
iOS. Codec and container availability still depends on how the installed FFmpeg
libraries were built.

## Installation

```sh
go get github.com/erparts/go-avebi
```

Avebi depends directly on go-ffmpeg-ffi v1.1.0. Applications do not need a
dependency override. See [development and testing](docs/development.md) for
the validated changes.

### Selecting an FFmpeg installation

go-ffmpeg-ffi checks the platform's dynamic-library search path before standard
system locations. Set it before starting the program and point it at a directory
that contains only the FFmpeg family you want to load.

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

The path must be configured before process startup because go-ffmpeg-ffi
initializes its bindings while Go packages are initialized. If one directory
contains multiple families, go-ffmpeg-ffi prefers the newest supported family
and verifies that all loaded libraries belong to the same ABI.

Android applications package unversioned `libav*.so` files in the APK or AAR.
iOS applications embed signed FFmpeg frameworks or link FFmpeg into the process
image. See [platform support and packaging](docs/platforms.md) for the precise
validation level and mobile requirements.

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

The player also exposes `Pause`, `Stop`, `Seek`, `Position`, `Duration`,
`State`, `HasEnded`, loop controls, and volume/mute controls. Use
`NewPlayerWithOptions` for further audio options.

If the media and Ebitengine audio context use different sample rates,
`NewPlayer` converts the audio and reports the mismatch through the package
logger. Applications that prefer to reject the media can opt out:

```go
player, err := avebi.NewPlayerWithOptions(path, &avebi.PlayerOptions{
	RejectSampleRateMismatch: true,
})
```

In that case, a mismatch returns `ErrBadSampleRate` with both sample rates in
the error message. `GetMediaAudioSampleRate` can be used to inspect a source
before opening a player.

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
See [`examples/mobileplayer`](examples/mobileplayer) for Android and iOS
binding.

## Current limitations

- Live streams are video-only.
- `NextVideoFrame` is not implemented.
- Hardware-decoded video is transferred to CPU-accessible frames; zero-copy
  GPU presentation is not implemented.
- Mobile applications must package their own compatible FFmpeg libraries.

See [development and testing](docs/development.md) for the compatibility matrix,
platform checks and stress-test commands.
