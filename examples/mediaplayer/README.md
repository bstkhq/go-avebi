# Media player example

This directory contains one Ebitengine media player for desktop and Android.
Both builds use the same playback lifecycle, controls, diagnostics and
responsive layout. Each platform has only a small adapter around that shared
game.

## Shared player

The on-screen controls provide play/pause, stop, five-second backward and
forward seeks, looping, a tappable progress bar, and the current position and
duration. **Open** appears only when a platform registers a file picker.

A persistent diagnostics bar reports actual Ebitengine TPS and FPS together
with the source video resolution and codec. The layout follows the available
aspect ratio instead of assuming a fixed device size: controls use one row on
wide screens and two rows on narrow or portrait screens.

## Desktop

Run the desktop build with:

```sh
go run ./examples/mediaplayer/desktop /path/to/video.mp4
```

The desktop UI does not show **Open** because its media path comes from the
command line. `Space` or `P` toggles playback, `S` stops, `L` toggles looping,
and the arrow keys seek by one second.

## Android

The Android adapter registers the shared game with Ebitengine and exposes
`Open`, `Seek`, `Close`, and `Error` to the native host. These operations are
queued so that decoding, audio setup, and drawing remain on the Ebitengine
update loop.

Android packaging uses
[`bstkhq/apk-ebiten-builder`](https://github.com/bstkhq/apk-ebiten-builder),
which generates the Android project, binds the Go package and assembles the
APK. The example follows the builder's `main` branch by default; set
`BUILDER_REF` to a commit when a reproducible external build is required. With
the Android SDK, NDK and Java 17 configured, build the complete APK with:

```sh
cd examples/mediaplayer/android
make apk ANDROID_TARGET=android/arm64
```

On the first build, the Makefile clones `go-ffmpeg-ffi` v1.1.0 and builds its
pinned FFmpeg release. Later builds reuse that output. Set
`FFMPEG_ANDROID_LIB_DIR` to use an existing compatible library directory
instead. Use `ANDROID_TARGET=android/amd64` for an x86-64 emulator. `make
compile` can be used when only the generated AAR is needed. Both commands use
`apk-ebiten-builder`; the example does not invoke `ebitenmobile` directly.

The Android build also discovers the example's optional `FilePickerBridge`.
Tapping **Open** launches Android's document picker without requesting a
storage permission. The builder copies the selected document into the
application cache and returns a local path to Go; the example removes that copy
when playback closes or another selection replaces it. Applications that
already own a local path can continue to call `Open` directly.

CI builds Android APKs containing FFmpeg for `arm64` and `amd64` (x86-64), API
33.

The example renders decoded frames through `avebi.Draw`. On an Android 14
`arm64` tablet it has also been exercised with a 1080p60 H.264/AAC file: the
document picker, active MediaCodec decoder, audible audio, pause, seek, loop,
rotation and A/V synchronization all worked. This does not qualify sustained
60 FPS or thermal behavior.
