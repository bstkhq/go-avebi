# Platform support

Avebi follows the operating-system and architecture surface of
[`go-ffmpeg-ffi v1.1.1`](https://github.com/bstkhq/go-ffmpeg-ffi/releases/tag/v1.1.1).
Support is evidence-based: compilation, native runtime and physical-device
qualification are different claims.

| Target | Avebi evidence | FFmpeg delivery |
| --- | --- | --- |
| Linux `amd64` | Native runtime; complete FFmpeg 6–9 matrix, real H.264/AAC playback and MCP stress tests. | Shared libraries on the dynamic-loader path. |
| Linux `arm64` | Native runtime with the distribution FFmpeg and real H.264/AAC media. | Shared libraries on the dynamic-loader path. |
| macOS `amd64`, `arm64` | Native runtime with Homebrew FFmpeg and real H.264/AAC media. | `.dylib` files on the loader path. |
| Windows `amd64` | Native runtime with the pinned FFmpeg 9.0.1 shared build and real H.264/AAC media. | `.dll` files on `PATH`. |
| Windows `arm64` | Complete package and test-binary compilation. Native runtime remains unqualified because the project has no public native runner. | ARM64 `.dll` files on `PATH`. |
| Android `amd64`, `arm64` | API 33 package/test compilation and complete APK assembly through `apk-ebiten-builder`, including FFmpeg 8 shared libraries. Physical `arm64` validation covers the document picker, MediaCodec H.264 decoding, audible AAC audio, pause, seek, loop, rotation and A/V synchronization. | Unversioned `libav*.so` files packaged for each APK ABI. |
| iOS device `arm64`; simulator `amd64`, `arm64` | iOS 13 package/test compilation. No physical-device playback qualification. | Signed FFmpeg frameworks embedded in the app, or FFmpeg linked into its process image. |

The Android and iOS jobs prove that the public avebi player, its audio path and
the `go-ffmpeg-ffi` backend compile for their mobile targets. Android
additionally assembles an installable APK with FFmpeg through
[`apk-ebiten-builder`](https://github.com/bstkhq/apk-ebiten-builder). The
physical Android validation supplements CI, but thermal stability and sustained
performance remain unqualified.

FFmpeg builds decide which codecs, containers, protocols and hardware backends
are available. A supported operating system does not imply MediaCodec,
VideoToolbox, D3D11VA or a particular frame rate.

## Media player example

[`examples/mediaplayer`](../examples/mediaplayer) contains the shared game and
thin desktop and Android adapters. A host can queue `Open`, `Seek` and
`Close` operations, while the Ebitengine update loop owns the player and
renders decoded frames through `avebi.Draw`. Its Makefile delegates
Android project generation, AAR binding and APK assembly to
`apk-ebiten-builder`; CI packages the example for Android.

Applications remain responsible for packaging a coherent FFmpeg 6, 7, 8 or 9
library family. Mobile platforms require `CGO_ENABLED=1` and the native Android
NDK or Xcode toolchain, as required by PureGo and Ebitengine.
