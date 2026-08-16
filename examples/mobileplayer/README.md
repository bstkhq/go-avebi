# Mobile player example

This example embeds avebi in an Ebitengine game that can be packaged for
Android or bound for iOS. It is a separate module because mobile toolchains
consume a library package rather than the desktop example's `main` package.

It exposes `Open`, `Seek`, `Close`, and `Error` to the native host. Player
operations are queued so that decoding, audio setup, and drawing remain on the
Ebitengine update loop.

The on-screen controls provide play/pause, stop, five-second backward and
forward seeks, looping, a tappable progress bar, and the current position and
duration. A physical keyboard can use the same controls as the desktop example:
`Space` or `P` toggles playback, `S` stops, `L` toggles looping, and the arrow
keys seek by one second. A persistent diagnostics bar reports actual Ebitengine
TPS and FPS together with the source video resolution and codec.

The layout follows the available aspect ratio instead of assuming a fixed
device size. Controls use one row on wide screens and two rows on narrow or
portrait screens, and are recalculated when the device rotates.

## Android

Android packaging uses
[`bstkhq/apk-ebiten-builder`](https://github.com/bstkhq/apk-ebiten-builder),
which generates the Android project, binds the Go package and assembles the
APK. The example follows the builder's `main` branch by default; set
`BUILDER_REF` to a commit when a reproducible external build is required. With
the Android SDK, NDK and Java 17 configured, first build the pinned FFmpeg
Android libraries:

```sh
git clone --branch v1.0.0 --depth 1 \
  https://github.com/bstkhq/go-ffmpeg-ffi.git \
  .build/go-ffmpeg-ffi
.build/go-ffmpeg-ffi/scripts/build-ffmpeg-android.sh arm64
```

Then build the complete APK through the packager:

```sh
make apk \
  ANDROID_TARGET=android/arm64 \
  FFMPEG_ANDROID_LIB_DIR=.build/go-ffmpeg-ffi/.build/ffmpeg-android/install/arm64-v8a/lib
```

Use `amd64` and `android/amd64` with the `x86_64/lib` output when building for
an x86-64 emulator. `make compile` can be used when only the generated AAR is
needed. Both commands use `apk-ebiten-builder`; the example does not invoke
`ebitenmobile` directly.

The Android build also discovers the example's optional `FilePickerBridge`.
Tapping **Open** launches Android's document picker without requesting a
storage permission. The builder copies the selected document into the
application cache and returns a local path to Go; the example removes that copy
when playback closes or another selection replaces it. Applications that
already own a local path can continue to call `Open` directly.

CI builds:

- Android APKs containing FFmpeg for `arm64` and `amd64` (x86-64), API 33;
- an iOS 13 XCFramework with device and simulator slices.

The example renders decoded frames through `avebi.Draw`. CI proves APK
assembly, but it does not claim physical-device audio or lifecycle
qualification.
