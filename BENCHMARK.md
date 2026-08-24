# H.265 decoding benchmarks

These benchmarks measure the video path used by avebi: H.265 decoding followed
by conversion to the RGBA buffers consumed by Ebitengine. They compare software
decoding with the hardware acceleration introduced by the current v1 branch.

The results were collected on August 24, 2026 from commit `bed1678`, based on
v1.0.0 and updated to `go-ffmpeg-ffi v1.1.1`.

> [!IMPORTANT]
> These are throughput results, not display frame-rate measurements. A result
> above 60 fps means the decode and RGBA conversion path has enough throughput
> for a 60 fps source. It does not include Ebitengine texture uploads, window
> compositing, VSync, or presentation on a physical monitor.

## Results

Each value is the median of five independent samples. Every sample processes
the complete clip three times in a fresh process. `CPU %` follows the usual
process convention, where 100% represents one fully occupied logical CPU.
`CPU s/clip` is user plus system CPU time per decoded clip.

| Platform | Decoder | Actual path | Time/clip | Throughput | CPU | CPU s/clip | Peak RSS | Allocated/clip | Frames |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|
| x86 | Software | FFmpeg software | 2.537 s | 118.3 fps | 101.2% | 2.567 s | 122.1 MiB | 8.35 MiB | 300 |
| x86 | Hardware required | CUDA | 2.355 s | 127.4 fps | 86.35% | 2.033 s | 290.1 MiB | 8.46 MiB | 300 |
| Raspberry Pi 5 | Software | FFmpeg software | 5.746 s | 52.21 fps | 100.7% | 5.792 s | 104.5 MiB | 8.33 MiB | 300 |
| Raspberry Pi 5 | Hardware required | DRM/rpivid | 2.433 s | 123.3 fps | 88.43% | 2.153 s | 100.3 MiB | 8.44 MiB | 300 |
| Android SM-X210 | Software | FFmpeg software | 4.042 s | 74.23 fps | 123.0% | 4.970 s | 203.1 MiB | 8.83 MiB | 300 |
| Android SM-X210 | Hardware required | MediaCodec | 2.734 s | 109.7 fps | 114.7% | 3.135 s | 207.7 MiB | 8.73 MiB | 300 |

On x86, CUDA improves throughput by 7.7% and reduces CPU time per clip by
20.8%. Its 290.1 MiB peak RSS includes the NVIDIA context, runtime, and
hardware frame buffers. Hardware frames are still transferred to system memory
for RGBA conversion, because avebi does not currently implement zero-copy GPU
presentation.

The Raspberry Pi benefits more substantially from its stateless H.265 decoder.
DRM/rpivid improves throughput by 136.2%, reduces CPU time per clip by 62.8%,
and moves the complete decode and RGBA conversion path from 52.21 to 123.3 fps.
FFmpeg reported the V4L2 H.265 stateless decoder on `/dev/media2` and
`/dev/video19`, using DMABuf buffers without a software fallback.

On the Samsung SM-X210 Android tablet, MediaCodec improves throughput by 47.8%
and reduces CPU time per clip by 36.9%. Peak RSS increases by 2.3%. All five
hardware samples reported `active/mediacodec` with the `hevc_mediacodec`
decoder and returned all 300 frames without a fallback.

## Previous Reisen backend

The former v0.0.7 implementation used Reisen and had no API for hardware
decoding. The compact comparison below uses software decoding on both versions
so that it reflects the backend change rather than the availability of CUDA or
rpivid.

| Platform | v0.0.7 / Reisen | Current v1 / FFmpeg FFI | Throughput change | CPU change | Peak RSS change |
|---|---:|---:|---:|---:|---:|
| x86 | 93.39 fps | 118.3 fps | +26.7% | -20.0% | -31.7% |
| Raspberry Pi 5 | 36.66 fps | 52.21 fps | +42.4% | -10.5% | -28.5% |

Reisen returned 298 of the 300 H.265 frames because the delayed frames were not
drained at EOF. The v1 backend returned all 300 frames in every mode. It also
reduced allocations from approximately 4.616 GiB per clip to 8.3–8.5 MiB,
roughly a 560-fold reduction.

## Test method

- Input: H.265 Main, yuv420p, 1920x1080, 30 fps, 10 seconds, 300 frames.
- Input SHA-256:
  `6d7ec76755841122227cd8a013ad269b4f735bc149cc275d710ddab2eeb29ffb`.
- Workload: video-only decode and conversion to a reusable RGBA frame buffer.
- Sampling: five fresh processes per configuration, each using `benchtime=3x`.
- CPU measurement: `getrusage(RUSAGE_SELF)`, user plus system time.
- Memory measurement: per-process `ru_maxrss`; allocation data comes from the
  Go benchmark counters.
- x86 host: AMD Ryzen Threadripper PRO 3975WX, 64 logical CPUs, NVIDIA GeForce
  RTX 2060 with driver 550.163.01.
- ARM host: Raspberry Pi 5, four Cortex-A76 cores and 8 GiB RAM.
- Android host: Samsung SM-X210, Qualcomm SM6375, Android 16/API 36, arm64,
  with a 1200x1920 display supporting 60 and 90 Hz. Its system codec inventory
  advertises a hardware-accelerated `c2.qti.hevc.decoder` and a 1080p60
  performance point.

The tests used the container images already present on each host, with
`--pull=never`:

- v0/Reisen: `ghcr.io/erparts/ebiten-environment:1.27-bookworm-amd64` and
  `1.27-bookworm-arm64`, containing FFmpeg 5.1.9.
- v1/FFmpeg FFI: `ghcr.io/erparts/ebiten-environment:1.27-bookworm-ffmpeg9-amd64`
  and `1.27-bookworm-ffmpeg9-arm64`, containing FFmpeg 9.

The Android APK packaged FFmpeg 8.0.3 arm64 libraries and
`go-ffmpeg-ffi v1.1.1`. Each Android sample ran in a fresh application process,
decoded one unmeasured warm-up clip, and then measured three clips. Its minimal
Ebitengine host ran at one TPS and did not upload video textures. Android CPU
and RSS cover the complete application process, so their absolute values are
most useful for software-versus-MediaCodec comparison on the same device rather
than direct memory comparison with Linux.

CUDA was exposed through Podman's NVIDIA CDI device. The Raspberry Pi hardware
case exposed `/dev/video19`, `/dev/media2`, `/dev/dri/renderD128`, and
`/dev/dma_heap`, including the DMA heap device cgroup rule.

## Stability

All 50 benchmark processes passed: the original eight Linux configurations with
five samples each plus ten Android samples. They cover 150 measured complete
clip decodes, in addition to Android's unmeasured warm-up clips. There were no
crashes, hangs, or silent hardware fallbacks. Hardware runs used
`HardwareAccelerationRequired`, so an unavailable accelerator or a fallback
would have failed the test.

The Raspberry Pi finished at 63.7 °C and 2.4 GHz. Its final
`get_throttled=0xe0000` value showed no active throttling bit at measurement
time, although it retained historical flags from earlier in the boot.

The Android tablet remained stable across the ten fresh processes. Battery
temperature was 31.5–32.0 °C and the sampled SoC thermal zone was
38.0–39.6 °C during the run.

FFmpeg 5.1.9 for Reisen and FFmpeg 9 for the current backend are part of the
real product comparison. These numbers should therefore be read as an avebi v0
versus v1 comparison, not as a controlled microbenchmark of two wrappers around
the same FFmpeg build.
