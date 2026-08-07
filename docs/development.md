# Development and testing

This document describes avebi's temporary ffgo dependency, compatibility CI and
opt-in media tests. User-facing installation and playback examples belong in
the project [README](../README.md).

## Temporary ffgo integration pin

The repository imports ffgo through its upstream module path and temporarily
replaces it with this integration commit:

```text
github.com/erparts/ffgo v0.0.0-20260806221945-0fd8f50d50c3
```

The commit combines three independently prepared changes:

- the correct FFmpeg 6 `AVFrame` audio layout;
- the correct `swr_convert` argument order;
- runtime ABI selection and validation for FFmpeg 6 and 7.

The fork retains ffgo's upstream module identity so its fixes can be proposed
upstream independently. Because dependency replacements are not transitive,
applications testing avebi before those changes are released must repeat the
`replace` directive shown in the README.

## Compatibility CI

`.github/workflows/ffmpeg-integration.yml` runs required jobs for FFmpeg 6 and
FFmpeg 7. Each job:

1. generates a short H.264/AAC fixture;
2. asserts the loaded libavutil, libavcodec and libavformat family;
3. runs the complete Go test suite with a paced audio sink;
4. drives the media-player example through the go-ebiten-mcp API for 100 control
   and stress cycles.

The FFmpeg 7 job builds FFmpeg 7.1.1 from its pinned source commit and caches the
result. This makes ABI selection deterministic instead of relying on the runner's
system packages.

## Real-media integration tests

The root integration tests are opt-in. Provide media containing both video and
audio, and configure a working real-time audio sink when enabling the audio
controller test:

```sh
AVEBI_EXPECT_FFMPEG_MAJOR=7 \
AVEBI_TEST_MEDIA=/path/to/video-with-audio.mp4 \
AVEBI_TEST_AUDIO=1 \
  go test -count=1 ./...
```

`AVEBI_EXPECT_FFMPEG_MAJOR` accepts `6` or `7` and verifies the actual core
libraries loaded by ffgo.

## go-ebiten-mcp integration and stress test

The media-player test uses the go-ebiten-mcp Go API to exercise play, pause,
paused seek, resume, stop, natural EOF, replay and looping. It also captures an
offscreen frame and watches the Go heap, process RSS, goroutine count and open
file descriptors for suspicious growth.

```sh
AVEBI_MCP_TEST_MEDIA=/path/to/video-with-audio.mp4 \
AVEBI_MCP_TORTURE_CYCLES=500 \
  ebitenmcp run --x container --screen 1280x720 \
  go test -count=1 \
  -run '^TestFFGOMCPIntegrationAndTorture$' -v ./examples/mediaplayer
```

The default is 100 cycles. The thresholds can be adjusted with:

- `AVEBI_MCP_MAX_HEAP_GROWTH_MB`
- `AVEBI_MCP_MAX_RSS_GROWTH_MB`
- `AVEBI_MCP_MAX_GOROUTINE_GROWTH`
- `AVEBI_MCP_MAX_FD_GROWTH`

These thresholds detect regressions; a single RSS increase is not by itself
proof of a native allocation leak. Repeat suspicious runs with more cycles and
compare the midpoint and final samples.

## Routine checks

```sh
go test -count=1 ./...
go vet ./...
```

Run `actionlint` after changing GitHub Actions workflows.
