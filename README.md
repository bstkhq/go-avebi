# avebi

A video playing library for Ebitengine, with an API design inspired by [tinne26/mpegg](https://github.com/tinne26/mpegg). It uses the purego-based [ffgo](https://github.com/obinnaokechukwu/ffgo) FFmpeg bindings and does not compile or link Reisen.

Warnings and limitations:
- The library is still quite barebones and focused on the primary needs of erparts.
- The current validation baseline is Linux `amd64` with FFmpeg 6.1.
- The library is currently limited to desktop `amd64` and `arm64`; iOS and Android are excluded.
- ffgo itself uses purego, but Ebitengine can still require cgo for its graphics or audio platform drivers.

## Dependencies

ffgo discovers FFmpeg shared libraries at runtime, so building avebi does not require FFmpeg headers or `pkg-config`:

```sh
go build ./...
```

This repository currently pins ffgo through `go.mod` to the [`erparts/ffgo` integration branch](https://github.com/erparts/ffgo/tree/integration/go-avebi), which combines the two independently prepared fixes for the FFmpeg 6 `AVFrame` audio layout and the `swr_convert` ABI. Go does not propagate a dependency's `replace` directives, so downstream modules must repeat this replacement until the fixes are released upstream:

```sh
go mod edit -replace=github.com/obinnaokechukwu/ffgo=github.com/erparts/ffgo@v0.0.0-20260806205326-4bb322ae3396
```

Explicit ABI detection and support for FFmpeg 7 and 8 are planned separately.

## Instrumented examples

Both examples use
[go-ebiten-mcp](https://github.com/bstkhq/go-ebiten-mcp). This intentionally
makes Go 1.25 and Ebitengine 2.9.9 project requirements. With
`EBITEN_MCP_ADDR` unset the examples behave like ordinary Ebitengine
applications.

Run the local media player:

```sh
go run ./examples/mediaplayer /path/to/video.mp4
```

Expose it to go-ebiten-mcp on loopback:

```sh
EBITEN_MCP_ADDR=127.0.0.1:8384 \
  go run ./examples/mediaplayer /path/to/video.mp4
```

The media player publishes `@player` with playback state, position, frame PTS,
end/loop/audio flags and errors. The stream example publishes its state,
position, frame PTS and last error through the same `@player` name.

### ffgo integration and torture test

The opt-in media player test uses the go-ebiten-mcp Go API directly. It drives
play, pause, seek, stop, replay and looping, validates an offscreen capture, and
then repeats the controls while watching for panics and growth in the Go heap,
process RSS, goroutines and open file descriptors. Use an H.264 file containing
an AAC audio track so both decoder paths are exercised:

```sh
AVEBI_MCP_TEST_MEDIA=/path/to/video-with-audio.mp4 \
AVEBI_MCP_TORTURE_CYCLES=500 \
  ebitenmcp run --x container --screen 1280x720 \
  go test \
  -run '^TestFFGOMCPIntegrationAndTorture$' -v ./examples/mediaplayer
```

The default is 100 torture cycles. The tolerated growth can be adjusted with
`AVEBI_MCP_MAX_HEAP_GROWTH_MB`, `AVEBI_MCP_MAX_RSS_GROWTH_MB`,
`AVEBI_MCP_MAX_GOROUTINE_GROWTH` and `AVEBI_MCP_MAX_FD_GROWTH`. These are upper
bounds for detecting regressions, not proof that a native allocation is leaked;
for a suspicious result, repeat with more cycles and inspect the reported
midpoint and final samples.

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

Potential improvements on ffgo:
- Add support hardware acceleration, ¿..primarily h264_v4l2m2m for the raspberry pi?
- Use pools for both video and audio frames data.
