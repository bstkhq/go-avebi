//go:build !ios && !android && (amd64 || arm64)

package avebi

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"testing"
	"time"

	ffgo "github.com/bstkhq/go-ffmpeg-ffi"
)

const expectedFFmpegMajorEnv = "AVEBI_EXPECT_FFMPEG_MAJOR"

func TestFFGORuntimeMajor(t *testing.T) {
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
	}[release]
	if !ok {
		t.Fatalf("unsupported expected FFmpeg release %d", release)
	}
	if err := ffgo.Init(); err != nil {
		t.Fatalf("initialize ffgo: %v", err)
	}
	avutilVersion, avcodecVersion, avformatVersion := ffgo.Version()
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

// TestFFGOBackendMedia exercises ffgo against a real media file. It is opt-in
// so the normal test suite stays hermetic:
//
//	AVEBI_TEST_MEDIA=/path/to/video.mp4 go test -run TestFFGOBackendMedia
func TestFFGOBackendMedia(t *testing.T) {
	mediaPath := os.Getenv("AVEBI_TEST_MEDIA")
	if mediaPath == "" {
		t.Skip("set AVEBI_TEST_MEDIA to run the ffgo integration test")
	}

	decoder, err := newMediaBackend().Open(context.Background(), mediaPath, backendOpenOptions{
		OutputSampleRate: 48_000,
	})
	if err != nil {
		t.Fatalf("open media: %v", err)
	}
	t.Cleanup(func() {
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
	gotVideo, gotAudio := readAndValidateFFGOFrames(t, decoder, wantAudio)
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
	validateFFGOSeek(t, decoder, seekTarget, wantAudio)
}

func TestFFGOPlayerWithoutAudioMedia(t *testing.T) {
	mediaPath := os.Getenv("AVEBI_TEST_MEDIA")
	if mediaPath == "" {
		t.Skip("set AVEBI_TEST_MEDIA to run the ffgo integration test")
	}

	player, err := NewPlayerWithoutAudio(mediaPath)
	if err != nil {
		t.Fatalf("NewPlayerWithoutAudio: %v", err)
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
	if player.Duration() <= 0 {
		t.Fatalf("invalid player duration: %s", player.Duration())
	}
	if player.HasAudio() {
		t.Fatal("NewPlayerWithoutAudio reported an audio stream")
	}
	if _, err := player.CurrentFrame(); err != nil {
		t.Fatalf("CurrentFrame while stopped: %v", err)
	}
	if player.LastPresentationOffset() != 0 {
		t.Fatalf("stopped player decoded a frame at %s", player.LastPresentationOffset())
	}

	if err := player.Play(); err != nil {
		t.Fatalf("Play: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := player.CurrentFrame(); err != nil {
		t.Fatalf("CurrentFrame while playing: %v", err)
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
}

// TestFFGOPlayerWithAudioMedia exercises the public audio controller and needs
// a working, real-time audio sink. It is separately gated so decoder-only CI
// can still use AVEBI_TEST_MEDIA without configuring ALSA/PipeWire:
//
//	AVEBI_TEST_MEDIA=/path/to/video-with-audio.mp4 AVEBI_TEST_AUDIO=1 \
//	  go test -run TestFFGOPlayerWithAudioMedia
func TestFFGOPlayerWithAudioMedia(t *testing.T) {
	mediaPath := os.Getenv("AVEBI_TEST_MEDIA")
	if mediaPath == "" || os.Getenv("AVEBI_TEST_AUDIO") != "1" {
		t.Skip("set AVEBI_TEST_MEDIA and AVEBI_TEST_AUDIO=1 to run the ffgo audio-player integration test")
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
	waitForFFGOPlayerPosition(t, player, 100*time.Millisecond, player.Duration())
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
	waitForFFGOPlayerPosition(t, player, 100*time.Millisecond, player.Duration())
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

func waitForFFGOPlayerPosition(t *testing.T, player *Player, minimum, maximum time.Duration) {
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

func readAndValidateFFGOFrames(t *testing.T, decoder mediaDecoder, wantAudio bool) (bool, bool) {
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
			if frame.Video.Stride != frame.Video.Width*4 || len(frame.Video.RGBA) != wantBytes {
				t.Fatalf("invalid RGBA frame: %dx%d stride=%d bytes=%d", frame.Video.Width, frame.Video.Height, frame.Video.Stride, len(frame.Video.RGBA))
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

func validateFFGOSeek(t *testing.T, decoder mediaDecoder, target time.Duration, wantAudio bool) {
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
