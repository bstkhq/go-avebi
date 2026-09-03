//go:build amd64 || arm64

package avebi

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bstkhq/go-ffmpeg-ffi"
)

const expectedFFmpegMajorEnv = "AVEBI_EXPECT_FFMPEG_MAJOR"

func TestFFmpegDecoderOptionsReuseHardwareDeviceManager(t *testing.T) {
	first := ffmpegDecoderOptions(backendOpenOptions{})
	second := ffmpegDecoderOptions(backendOpenOptions{DisableAudio: true})
	if first.Hardware == nil || first.Hardware.DeviceManager == nil {
		t.Fatal("default decoder options do not configure a hardware device manager")
	}
	if second.Hardware == nil || second.Hardware.DeviceManager != first.Hardware.DeviceManager {
		t.Fatal("decoder options do not reuse the process-wide hardware device manager")
	}
}

func TestFFmpegDecoderOptionsRTSPTransport(t *testing.T) {
	opts := ffmpegDecoderOptions(backendOpenOptions{Live: true, RTSPTransport: "tcp"})
	if got := opts.AVOptions["rtsp_transport"]; got != "tcp" {
		t.Fatalf("rtsp_transport = %q, want %q", got, "tcp")
	}
	opts = ffmpegDecoderOptions(backendOpenOptions{Live: true})
	if _, ok := opts.AVOptions["rtsp_transport"]; ok {
		t.Fatal("rtsp_transport set without an explicit transport")
	}
}

func TestFFmpegDecoderOptionsProbeLimits(t *testing.T) {
	opts := ffmpegDecoderOptions(backendOpenOptions{
		ProbeSize:       262144,
		AnalyzeDuration: 500 * time.Millisecond,
	})
	if got := opts.ProbeSizeBytes; got != 262144 {
		t.Fatalf("ProbeSizeBytes = %d, want %d", got, 262144)
	}
	if got := opts.AnalyzeDuration; got != 500*time.Millisecond {
		t.Fatalf("AnalyzeDuration = %s, want %s", got, 500*time.Millisecond)
	}

	opts = ffmpegDecoderOptions(backendOpenOptions{ProbeSize: 16})
	if got := opts.ProbeSizeBytes; got != minFFmpegProbeSize {
		t.Fatalf("ProbeSizeBytes = %d, want FFmpeg minimum %d", got, minFFmpegProbeSize)
	}

	opts = ffmpegDecoderOptions(backendOpenOptions{})
	if opts.ProbeSizeBytes != 0 || opts.AnalyzeDuration != 0 {
		t.Fatal("probe limits set without explicit values")
	}
}

func TestStreamOptionsRTSPTransportValidation(t *testing.T) {
	_, err := NewStreamPlayerWithOptions("rtsp://cam.local:554/feed", &StreamOptions{RTSPTransport: "TCP"})
	if err == nil || !strings.Contains(err.Error(), "RTSPTransport") {
		t.Fatalf("invalid transport error = %v, want mention of RTSPTransport", err)
	}
}

func TestFFmpegRuntimeMajor(t *testing.T) {
	value := os.Getenv(expectedFFmpegMajorEnv)
	if value == "" {
		t.Skip("set AVEBI_EXPECT_FFMPEG_MAJOR to verify the loaded FFmpeg ABI")
	}
	release, err := strconv.Atoi(value)
	if err != nil {
		t.Fatalf("%s must be an integer: %v", expectedFFmpegMajorEnv, err)
	}
	want, ok := map[int][3]int{
		6: {58, 60, 60},
		7: {59, 61, 61},
		8: {60, 62, 62},
		9: {61, 63, 63},
	}[release]
	if !ok {
		t.Fatalf("unsupported expected FFmpeg release %d", release)
	}
	if err := ffmpeg.Init(); err != nil {
		t.Fatalf("initialize go-ffmpeg-ffi: %v", err)
	}
	avutilVersion, avcodecVersion, avformatVersion := ffmpeg.Version()
	got := [3]int{
		int(avutilVersion >> 16),
		int(avcodecVersion >> 16),
		int(avformatVersion >> 16),
	}
	t.Logf("FFmpeg %d runtime ABI: avutil=%d avcodec=%d avformat=%d", release, got[0], got[1], got[2])
	if got != want {
		t.Fatalf("FFmpeg %d runtime ABI = %v, want %v", release, got, want)
	}
}

// TestFFmpegBackendMedia exercises go-ffmpeg-ffi against a real media file. It is opt-in
// so the normal test suite stays hermetic:
//
//	AVEBI_TEST_MEDIA=/path/to/video.mp4 go test -run TestFFmpegBackendMedia
func TestFFmpegBackendMedia(t *testing.T) {
	mediaPath := os.Getenv("AVEBI_TEST_MEDIA")
	if mediaPath == "" {
		t.Skip("set AVEBI_TEST_MEDIA to run the FFmpeg integration test")
	}

	pinnedBefore := ffmpeg.WrappedBufferMemoryUsage()
	decoder, err := newMediaBackend().Open(context.Background(), mediaPath, backendOpenOptions{
		OutputSampleRate: 48_000,
	})
	if err != nil {
		t.Fatalf("open media: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if closed {
			return
		}
		if err := decoder.Close(); err != nil {
			t.Errorf("close media: %v", err)
		}
	})

	info := decoder.Info()
	if info.Video == nil || info.Video.Width <= 0 || info.Video.Height <= 0 {
		t.Fatalf("invalid video metadata: %#v", info.Video)
	}
	if info.Duration <= 0 {
		t.Fatalf("invalid duration: %s", info.Duration)
	}

	wantAudio := info.Audio != nil
	gotVideo, gotAudio := readAndValidateFFmpegFrames(t, decoder, wantAudio)
	if !gotVideo {
		t.Fatal("no video frame decoded")
	}
	if wantAudio && !gotAudio {
		t.Fatal("media reports audio but no audio frame was decoded")
	}

	seekTarget := min(time.Second, info.Duration/2)
	if err := decoder.Seek(seekTarget); err != nil {
		t.Fatalf("seek to %s: %v", seekTarget, err)
	}
	validateFFmpegSeek(t, decoder, seekTarget, wantAudio)

	pinnedDuringDecode := ffmpeg.WrappedBufferMemoryUsage()
	if pinnedDuringDecode.PinnedBuffers != pinnedBefore.PinnedBuffers+1 {
		t.Fatalf("wrapped video buffers while decoding = %d, want %d", pinnedDuringDecode.PinnedBuffers, pinnedBefore.PinnedBuffers+1)
	}
	if err := decoder.Close(); err != nil {
		t.Fatalf("close media: %v", err)
	}
	closed = true
	if pinnedAfterClose := ffmpeg.WrappedBufferMemoryUsage(); pinnedAfterClose != pinnedBefore {
		t.Fatalf("wrapped video buffer usage after Close = %+v, want %+v", pinnedAfterClose, pinnedBefore)
	}
}

func TestFFmpegPlayerWithDisabledAudioMedia(t *testing.T) {
	mediaPath := os.Getenv("AVEBI_TEST_MEDIA")
	if mediaPath == "" {
		t.Skip("set AVEBI_TEST_MEDIA to run the FFmpeg integration test")
	}
	for _, test := range []struct {
		name      string
		yuvShader bool
	}{
		{name: "RGBA"},
		{name: "YUV shader", yuvShader: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			testFFmpegPlayerWithDisabledAudioMedia(t, mediaPath, test.yuvShader)
		})
	}
}

func testFFmpegPlayerWithDisabledAudioMedia(t *testing.T, mediaPath string, yuvShader bool) {
	t.Helper()
	player, err := NewPlayerWithOptions(mediaPath, &PlayerOptions{
		DisableAudio: true,
		UseYUVShader: yuvShader,
	})
	if err != nil {
		t.Fatalf("NewPlayerWithOptions: %v", err)
	}
	t.Cleanup(func() {
		if err := player.Close(); err != nil {
			t.Errorf("close player: %v", err)
		}
	})

	width, height := player.Resolution()
	if width <= 0 || height <= 0 {
		t.Fatalf("invalid player resolution: %dx%d", width, height)
	}
	if player.VideoCodec() == "" {
		t.Fatal("player reported an empty video codec")
	}
	if player.Duration() <= 0 {
		t.Fatalf("invalid player duration: %s", player.Duration())
	}
	if player.HasAudio() {
		t.Fatal("player with disabled audio reported an audio stream")
	}
	frame, err := player.CurrentFrame()
	if err != nil {
		t.Fatalf("CurrentFrame while stopped: %v", err)
	}
	if frame != nil {
		t.Fatal("stopped player returned the black placeholder as a current frame")
	}
	if player.LastPresentationOffset() != 0 {
		t.Fatalf("stopped player decoded a frame at %s", player.LastPresentationOffset())
	}

	if err := player.Play(); err != nil {
		t.Fatalf("Play: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	frame, err = player.CurrentFrame()
	if err != nil {
		t.Fatalf("CurrentFrame while playing: %v", err)
	}
	if frame == nil {
		t.Fatal("playing player did not return its decoded video frame")
	}
	if numerator, denominator := player.VideoFrameRate(); numerator <= 0 || denominator <= 0 {
		t.Fatalf("invalid video frame rate: %d/%d", numerator, denominator)
	}

	target := min(time.Second, player.Duration()/2)
	if err := player.Seek(target); err != nil {
		t.Fatalf("Seek(%s): %v", target, err)
	}
	if got := player.LastPresentationOffset(); got+50*time.Millisecond < target || got > target {
		t.Fatalf("presentation offset after seek = %s, want a frame covering %s", got, target)
	}

	if err := player.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if ended := player.HasEnded(); ended {
		t.Fatal("manual Stop was reported as a natural end")
	}
	frame, err = player.CurrentFrame()
	if err != nil {
		t.Fatalf("CurrentFrame after Stop: %v", err)
	}
	if frame != nil {
		t.Fatal("stopped player retained a decoded video frame")
	}
}

// TestFFmpegPlayerWithAudioMedia exercises the public audio controller and needs
// a working, real-time audio sink. It is separately gated so decoder-only CI
// can still use AVEBI_TEST_MEDIA without configuring ALSA/PipeWire:
//
//	AVEBI_TEST_MEDIA=/path/to/video-with-audio.mp4 AVEBI_TEST_AUDIO=1 \
//	  go test -run TestFFmpegPlayerWithAudioMedia
func TestFFmpegPlayerWithAudioMedia(t *testing.T) {
	mediaPath := os.Getenv("AVEBI_TEST_MEDIA")
	if mediaPath == "" || os.Getenv("AVEBI_TEST_AUDIO") != "1" {
		t.Skip("set AVEBI_TEST_MEDIA and AVEBI_TEST_AUDIO=1 to run the FFmpeg audio-player integration test")
	}

	if err := CreateAudioContextForMedia(mediaPath); err != nil && !errors.Is(err, ErrNonNilAudioContext) {
		t.Fatalf("CreateAudioContextForMedia: %v", err)
	}
	player, err := NewPlayer(mediaPath)
	if err != nil {
		t.Fatalf("NewPlayer: %v", err)
	}
	t.Cleanup(func() {
		if err := player.Close(); err != nil {
			t.Errorf("close player: %v", err)
		}
	})
	if !player.HasAudio() {
		t.Fatal("audio media was opened without an audio controller")
	}

	if err := player.Play(); err != nil {
		t.Fatalf("Play: %v", err)
	}
	waitForFFmpegPlayerPosition(t, player, 100*time.Millisecond, player.Duration())
	if err := player.Pause(); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if state, err := player.State(); err != nil || state != Paused {
		t.Fatalf("state after Pause = %v, %v; want Paused", state, err)
	}

	target := min(time.Second, player.Duration()/2)
	if err := player.Seek(target); err != nil {
		t.Fatalf("Seek(%s): %v", target, err)
	}
	if state, err := player.State(); err != nil || state != Paused {
		t.Fatalf("state after paused seek = %v, %v; want Paused", state, err)
	}
	if got := player.LastPresentationOffset(); got+50*time.Millisecond < target || got > target {
		t.Fatalf("presentation offset after A/V seek = %s, want a frame covering %s", got, target)
	}

	if err := player.Play(); err != nil {
		t.Fatalf("resume: %v", err)
	}
	deadline := time.Now().Add(player.Duration() + 5*time.Second)
	for !player.HasEnded() {
		if err := player.Error(); err != nil {
			t.Fatalf("playback error: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("audio playback did not reach its natural end")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if state, err := player.State(); err != nil || state != Stopped {
		t.Fatalf("state at natural end = %v, %v; want Stopped", state, err)
	}

	if err := player.Play(); err != nil {
		t.Fatalf("replay: %v", err)
	}
	waitForFFmpegPlayerPosition(t, player, 100*time.Millisecond, player.Duration())
	if player.HasEnded() {
		t.Fatal("replay remained in the ended state")
	}
	if err := player.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if player.HasEnded() {
		t.Fatal("manual Stop after replay was reported as a natural end")
	}
}

func waitForFFmpegPlayerPosition(t *testing.T, player *Player, minimum, maximum time.Duration) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		position, err := player.Position()
		if err != nil {
			t.Fatalf("Position: %v", err)
		}
		if position >= minimum && position < maximum {
			return
		}
		if err := player.Error(); err != nil {
			t.Fatalf("playback error: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("position did not enter [%s, %s); last value %s", minimum, maximum, position)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readAndValidateFFmpegFrames(t *testing.T, decoder mediaDecoder, wantAudio bool) (bool, bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var gotVideo, gotAudio bool
	var videoBuffers backendVideoBufferPool
	for time.Now().Before(deadline) && (!gotVideo || wantAudio && !gotAudio) {
		frame, err := decoder.ReadFrame(context.Background(), &videoBuffers)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("read frame: %v", err)
		}
		switch frame.Kind {
		case backendFrameVideo:
			wantBytes := frame.Video.Width * frame.Video.Height * 4
			if len(frame.Video.RGBA) != wantBytes {
				t.Fatalf("invalid RGBA frame: %dx%d bytes=%d", frame.Video.Width, frame.Video.Height, len(frame.Video.RGBA))
			}
			gotVideo = true
		case backendFrameAudio:
			if frame.Audio.SampleRate != 48_000 || frame.Audio.Channels != 2 || len(frame.Audio.PCM) == 0 || len(frame.Audio.PCM)%4 != 0 {
				t.Fatalf("invalid PCM frame: rate=%d channels=%d bytes=%d", frame.Audio.SampleRate, frame.Audio.Channels, len(frame.Audio.PCM))
			}
			gotAudio = true
		}
		recycleBackendFrame(&videoBuffers, &frame)
	}
	return gotVideo, gotAudio
}

func validateFFmpegSeek(t *testing.T, decoder mediaDecoder, target time.Duration, wantAudio bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var gotVideo, gotAudio bool
	var videoBuffers backendVideoBufferPool
	for time.Now().Before(deadline) && (!gotVideo || wantAudio && !gotAudio) {
		frame, err := decoder.ReadFrame(context.Background(), &videoBuffers)
		if err != nil {
			t.Fatalf("read after seek: %v", err)
		}
		if frame.PTS+frame.Duration < target {
			t.Fatalf("%v frame ends at %s before seek target %s", frame.Kind, frame.PTS+frame.Duration, target)
		}
		switch frame.Kind {
		case backendFrameVideo:
			gotVideo = true
		case backendFrameAudio:
			gotAudio = true
		}
		recycleBackendFrame(&videoBuffers, &frame)
	}
	if !gotVideo {
		t.Fatal("no video frame decoded after seek")
	}
	if wantAudio && !gotAudio {
		t.Fatal("no audio frame decoded after seek")
	}
}
