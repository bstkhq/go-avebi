# avebi

A video playing library for Ebitengine, with an API design inspired by [tinne26/mpegg](https://github.com/tinne26/mpegg).

Two compile-time media backends are available:

- `reisen` is the default and preserves the existing behavior.
- `ffgo` is an experimental purego FFmpeg binding selected with the `avebi_ffgo` build tag.

Warnings and limitations:
- The library is still quite barebones, lacking testing and only trying to cover primary needs for erparts.
- The default Reisen backend uses cgo and FFmpeg 6.1.
- The `erparts/reisen` fork is only adapted for Linux, so multi-platform support is non-existent.
- The ffgo backend is currently limited to desktop `amd64` and `arm64`; iOS and Android are excluded.
- ffgo itself uses purego, but Ebitengine can still require cgo for its graphics or audio platform drivers.

## Dependencies

Reisen depends on ffmpeg6.1, which is currently an outdated ffmpeg version.

On a linux system, the choices are:
- **Keeping only the old ffmpeg version**: which is not viable on up-to-date personal use computers where you have other programs that depend on the newest ffmpeg version.
- **Keeping ffmpeg6.1 alongside newer versions**: and using `PKG_CONFIG_PATH=/usr/lib/ffmpeg6.1/pkgconfig` or similar to point to it.

## Backends

The default build continues to use Reisen:

```sh
go build ./...
```

Build with ffgo explicitly while the backend is experimental:

```sh
go build -tags avebi_ffgo ./...
```

The ffgo build does not compile or link Reisen, and discovers the FFmpeg shared libraries at runtime. The current validation baseline is Linux `amd64` with FFmpeg 6.1. The audio path still depends on a small set of ffgo fixes being submitted upstream, so this backend is not release-ready until those fixes are pinned here. Explicit ABI detection and support for FFmpeg 7 and 8 are planned separately.

## Instrumented examples

Both examples use
[go-ebiten-mcp](https://github.com/bstkhq/go-ebiten-mcp). This intentionally
makes Go 1.25 and Ebitengine 2.9.9 project requirements. With
`EBITEN_MCP_ADDR` unset the examples behave like ordinary Ebitengine
applications.

Run the local media player with the ffgo backend:

```sh
go run -tags avebi_ffgo ./examples/mediaplayer /path/to/video.mp4
```

Expose it to go-ebiten-mcp on loopback:

```sh
EBITEN_MCP_ADDR=127.0.0.1:8384 \
  go run -tags avebi_ffgo ./examples/mediaplayer /path/to/video.mp4
```

The media player publishes `@player` with playback state, position, frame PTS,
end/loop/audio flags and errors. The stream example publishes its state,
position, frame PTS and last error through the same `@player` name.

## Usage

```Golang
func main() {
    // create video player
    videoPlayer, err := avebi.NewPlayer("../test_video.mp4")
    if err != nil {
        panic(err)
    }

    // start playing
    videoPlayer.Play()

    // ... (ebiten.RunGame)
}

// ... (game definition)

func (g *Game) Draw(canvas *ebiten.Image) {
	avebi.Draw(canvas, g.videoFrame)
}

func (g *Game) Update() error {
    var err error
    g.videoFrame, err = g.videoPlayer.CurrentFrame()
    return err
}
```

## TODO

Potential improvements on avebi:
- Consider async decoding buffering.
- Use circular buffers for audio data.
- Add support for mono audio.
- Add support for videos with audio and video channels of different lengths (annoying).

Potential improvements on reisen:
- Add support hardware acceleration, ¿..primarily h264_v4l2m2m for the raspberry pi?
- Use pools for both video and audio frames data.
