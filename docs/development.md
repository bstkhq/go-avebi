# Development and testing

This document describes avebi's go-ffmpeg-ffi dependency, compatibility CI and
opt-in media tests. User-facing installation and playback examples belong in
the project [README](../README.md).

## go-ffmpeg-ffi dependency

The repository depends directly on the validated v1.1.0 release:

```text
github.com/bstkhq/go-ffmpeg-ffi v1.1.0
```

The release includes the fixes required by avebi:

- the correct FFmpeg 6 `AVFrame` audio layout;
- the correct `swr_convert` argument order;
- runtime ABI selection and validation for FFmpeg 6 through 9;
- automatic platform-aware hardware decoding with software fallback.

This is a normal module dependency. Downstream applications do not need a
`replace` directive.

## Compatibility CI

`.github/workflows/ffmpeg-integration.yml` runs required jobs for FFmpeg 6, 7,
8 and 9. Each job:

1. generates a short H.264/AAC fixture;
2. asserts the loaded libavutil, libavcodec and libavformat family;
3. runs the complete Go test suite with a paced audio sink;
4. drives the media-player example through the go-ebiten-mcp API for 100 control
   and stress cycles.

The jobs build the pinned FFmpeg 6.1.6, 7.1.5, 8.1.2 and 9.0.1 release tarballs
and cache the results. This makes ABI selection deterministic instead of relying
on the runner's system packages.

`.github/workflows/platform-integration.yml` validates the operating-system
surface separately:

- native FFmpeg playback tests on Linux `arm64`, macOS `amd64`/`arm64` and
  Windows `amd64`;
- complete Windows compilation on `amd64` and `arm64`;
- Android API 33 package/test compilation and complete `apk-ebiten-builder` APK
  assembly with FFmpeg 8 on `amd64` and `arm64`;
- iOS 13 device/simulator package and test compilation.

Keeping the workflows separate avoids repeating the complete FFmpeg release
matrix on every operating system. See [platform support](platforms.md) for the
evidence represented by each job.

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

`AVEBI_EXPECT_FFMPEG_MAJOR` accepts `6`, `7`, `8` or `9` and verifies the actual core
libraries loaded by go-ffmpeg-ffi.

## go-ebiten-mcp integration and stress test

The media-player test drives the same shared game used by the desktop and
mobile examples through the go-ebiten-mcp Go API. It exercises play, pause,
paused seek, resume, stop, natural EOF, replay and looping. It also captures an
offscreen frame and watches the Go heap, process RSS, goroutine count and open
file descriptors for suspicious growth.

```sh
AVEBI_MCP_TEST_MEDIA=/path/to/video-with-audio.mp4 \
AVEBI_MCP_TORTURE_CYCLES=500 \
  ebitenmcp run --x container --screen 1280x720 \
  go test -count=1 \
  -run '^TestFFmpegMCPIntegrationAndTorture$' -v ./examples/mediaplayer/desktop
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

Android and iOS cross-compilation use the same scripts as CI:

```sh
./scripts/verify-android-build.sh arm64
./scripts/verify-ios-build.sh iphoneos arm64
```

They require the pinned Android NDK or the corresponding Xcode SDK. Desktop and
Android builds of the player live together at `examples/mediaplayer`.
