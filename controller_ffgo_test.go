//go:build !ios && !android && (amd64 || arm64)

package avebi

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

type fakeMediaDecoder struct {
	info      backendMediaInfo
	frames    []backendFrame
	readIndex int
	seekCalls []time.Duration
	closed    bool
}

type fakeFFGOAudioPlayer struct {
	playing  bool
	closed   bool
	position time.Duration
	volume   float64
	buffer   time.Duration
}

func (p *fakeFFGOAudioPlayer) Play()                         { p.playing = true }
func (p *fakeFFGOAudioPlayer) Pause()                        { p.playing = false }
func (p *fakeFFGOAudioPlayer) IsPlaying() bool               { return p.playing }
func (p *fakeFFGOAudioPlayer) Position() time.Duration       { return p.position }
func (p *fakeFFGOAudioPlayer) SetBufferSize(v time.Duration) { p.buffer = v }
func (p *fakeFFGOAudioPlayer) SetVolume(v float64)           { p.volume = v }
func (p *fakeFFGOAudioPlayer) Close() error {
	p.closed = true
	p.playing = false
	return nil
}

func (d *fakeMediaDecoder) Info() backendMediaInfo { return d.info }

func (d *fakeMediaDecoder) ReadFrame(context.Context) (backendFrame, error) {
	if d.readIndex >= len(d.frames) {
		return backendFrame{}, io.EOF
	}
	frame := d.frames[d.readIndex]
	d.readIndex++
	return frame, nil
}

func (d *fakeMediaDecoder) Seek(position time.Duration) error {
	d.seekCalls = append(d.seekCalls, position)
	d.readIndex = 0
	return nil
}

func (d *fakeMediaDecoder) Close() error {
	d.closed = true
	return nil
}

func TestFFGOHasEndedDoesNotReadAFrame(t *testing.T) {
	decoder := &fakeMediaDecoder{info: backendMediaInfo{
		Duration: time.Second,
		Video:    &backendVideoInfo{Width: 2, Height: 2, FrameRateNum: 25, FrameRateDen: 1},
	}}
	controller := newFFGOLocalController(decoder)
	if err := controller.Play(); err != nil {
		t.Fatalf("Play: %v", err)
	}

	controller.mutex.Lock()
	controller.referenceTime = time.Now().Add(-2 * time.Second)
	controller.mutex.Unlock()

	if !controller.HasEnded() {
		t.Fatal("HasEnded returned false after the playback clock passed Duration")
	}
	if decoder.readIndex != 0 {
		t.Fatalf("HasEnded decoded %d frames; want 0", decoder.readIndex)
	}
}

func TestFFGOStoppedControllerDoesNotDecode(t *testing.T) {
	decoder := &fakeMediaDecoder{
		info: backendMediaInfo{
			Duration: time.Second,
			Video:    &backendVideoInfo{Width: 2, Height: 2, FrameRateNum: 25, FrameRateDen: 1},
		},
		frames: []backendFrame{{Kind: backendFrameVideo}},
	}
	controller := newFFGOLocalController(decoder)

	frame, ended, err := controller.CurrentVideoFrame()
	if err != nil {
		t.Fatalf("CurrentVideoFrame: %v", err)
	}
	if frame != nil || ended {
		t.Fatalf("stopped frame = %#v, ended = %v; want nil, false", frame, ended)
	}
	if decoder.readIndex != 0 {
		t.Fatalf("stopped controller decoded %d frames; want 0", decoder.readIndex)
	}
}

func TestFFGOSeekUsesOneContainerSeekAndPreservesPause(t *testing.T) {
	const target = 5 * time.Second
	decoder := &fakeMediaDecoder{
		info: backendMediaInfo{
			Duration: 10 * time.Second,
			Video:    &backendVideoInfo{Width: 2, Height: 2, FrameRateNum: 25, FrameRateDen: 1},
		},
		frames: []backendFrame{{
			Kind:     backendFrameVideo,
			PTS:      target - 20*time.Millisecond,
			Duration: 40 * time.Millisecond,
			Video:    backendVideoFrame{RGBA: make([]byte, 16), Width: 2, Height: 2, Stride: 8},
		}},
	}
	controller := newFFGOLocalController(decoder)
	controller.state = Paused

	frame, err := controller.Seek(target)
	if err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if len(decoder.seekCalls) != 1 || decoder.seekCalls[0] != target {
		t.Fatalf("container seeks = %v, want [%s]", decoder.seekCalls, target)
	}
	if frame == nil || frame.PTS != target-20*time.Millisecond {
		t.Fatalf("seek frame = %#v", frame)
	}
	state, err := controller.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state != Paused {
		t.Fatalf("state after paused seek = %v, want Paused", state)
	}
}

func TestFFGOSeekToEndAndManualStopHaveDifferentEndState(t *testing.T) {
	const duration = 10 * time.Second
	decoder := &fakeMediaDecoder{info: backendMediaInfo{
		Duration: duration,
		Video:    &backendVideoInfo{Width: 2, Height: 2, FrameRateNum: 25, FrameRateDen: 1},
	}}
	controller := newFFGOLocalController(decoder)

	if _, err := controller.Seek(duration); err != nil {
		t.Fatalf("Seek(end): %v", err)
	}
	if !controller.HasEnded() {
		t.Fatal("seek to Duration did not mark playback as ended")
	}
	if err := controller.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if controller.HasEnded() {
		t.Fatal("manual Stop was reported as a natural end")
	}
}

func TestFFGOAudioEOFWaitsForPlaybackAndCanReplay(t *testing.T) {
	const duration = 3 * time.Second
	decoder := &fakeMediaDecoder{info: backendMediaInfo{
		Duration: duration,
		Video:    &backendVideoInfo{Width: 2, Height: 2, FrameRateNum: 25, FrameRateDen: 1},
		Audio:    &backendAudioInfo{SampleRate: 48_000, Channels: 2},
	}}
	oldPlayer := &fakeFFGOAudioPlayer{playing: true, position: 500 * time.Millisecond}
	newPlayer := &fakeFFGOAudioPlayer{}
	controller := newFFGOLocalController(decoder)
	controller.state = Playing
	controller.audioPlayer = oldPlayer
	controller.waitingForAudioPTS = false
	controller.newAudioPlayer = func(io.Reader) (ffgoAudioPlayer, error) {
		return newPlayer, nil
	}

	if n, err := controller.Read(make([]byte, 4)); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("audio EOF read = %d, %v; want 0, io.EOF", n, err)
	}
	position, err := controller.noLockPosition(time.Now())
	if err != nil {
		t.Fatalf("position while draining: %v", err)
	}
	if position != oldPlayer.position || controller.ended || controller.state != Playing {
		t.Fatalf("state while draining = position %s, ended %v, state %v", position, controller.ended, controller.state)
	}

	oldPlayer.position = duration
	oldPlayer.playing = false
	position, err = controller.noLockPosition(time.Now())
	if err != nil {
		t.Fatalf("position after drain: %v", err)
	}
	if position != duration || !controller.ended || controller.state != Stopped {
		t.Fatalf("state after drain = position %s, ended %v, state %v", position, controller.ended, controller.state)
	}

	if err := controller.Play(); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !oldPlayer.closed {
		t.Fatal("replay did not close the exhausted audio player")
	}
	if controller.audioPlayer != newPlayer || !newPlayer.playing {
		t.Fatal("replay did not start a fresh audio player")
	}
	if controller.ended || controller.audioEOF || controller.state != Playing {
		t.Fatalf("replay state = ended %v, audioEOF %v, state %v", controller.ended, controller.audioEOF, controller.state)
	}
	if len(decoder.seekCalls) != 1 || decoder.seekCalls[0] != 0 {
		t.Fatalf("replay seeks = %v, want [0s]", decoder.seekCalls)
	}
}

func TestFFGOAudioLoopRestartsAfterPlaybackDrain(t *testing.T) {
	const duration = 3 * time.Second
	decoder := &fakeMediaDecoder{info: backendMediaInfo{
		Duration: duration,
		Video:    &backendVideoInfo{Width: 2, Height: 2, FrameRateNum: 25, FrameRateDen: 1},
		Audio:    &backendAudioInfo{SampleRate: 48_000, Channels: 2},
	}}
	oldPlayer := &fakeFFGOAudioPlayer{position: duration}
	newPlayer := &fakeFFGOAudioPlayer{}
	controller := newFFGOLocalController(decoder)
	controller.state = Playing
	controller.looping = true
	controller.audioEOF = true
	controller.audioPlayer = oldPlayer
	controller.waitingForAudioPTS = false
	controller.newAudioPlayer = func(io.Reader) (ffgoAudioPlayer, error) {
		return newPlayer, nil
	}

	position, err := controller.noLockPosition(time.Now())
	if err != nil {
		t.Fatalf("loop position: %v", err)
	}
	if position != 0 || controller.state != Playing || controller.ended {
		t.Fatalf("loop state = position %s, state %v, ended %v", position, controller.state, controller.ended)
	}
	if !oldPlayer.closed || controller.audioPlayer != newPlayer || !newPlayer.playing {
		t.Fatal("audio loop did not replace and start the exhausted player")
	}
	if len(decoder.seekCalls) != 1 || decoder.seekCalls[0] != 0 {
		t.Fatalf("audio loop seeks = %v, want [0s]", decoder.seekCalls)
	}
}

func TestFFGOSeekFilterWaitsForAudioAndVideo(t *testing.T) {
	decoder := &ffgoDecoder{
		info: backendMediaInfo{
			Video: &backendVideoInfo{},
			Audio: &backendAudioInfo{},
		},
		seeking:    true,
		seekTarget: 5 * time.Second,
	}
	decoder.mutex.Lock()
	defer decoder.mutex.Unlock()

	if decoder.acceptSeekFrameLocked(backendFrame{Kind: backendFrameAudio, PTS: 4 * time.Second, Duration: 20 * time.Millisecond}) {
		t.Fatal("accepted an audio frame entirely before the seek target")
	}
	if !decoder.acceptSeekFrameLocked(backendFrame{Kind: backendFrameVideo, PTS: 4980 * time.Millisecond, Duration: 40 * time.Millisecond}) {
		t.Fatal("rejected the video frame covering the seek target")
	}
	if !decoder.seeking {
		t.Fatal("seek filtering stopped before audio reached the target")
	}
	if !decoder.acceptSeekFrameLocked(backendFrame{Kind: backendFrameAudio, PTS: 4990 * time.Millisecond, Duration: 20 * time.Millisecond}) {
		t.Fatal("rejected the audio frame covering the seek target")
	}
	if decoder.seeking {
		t.Fatal("seek filtering remained active after both streams reached the target")
	}
}

type eofMediaBackend struct {
	mutex    sync.Mutex
	decoders []*eofMediaDecoder
	opens    int
}

func (*eofMediaBackend) Probe(context.Context, string, backendOpenOptions) (backendMediaInfo, error) {
	return backendMediaInfo{}, errors.New("unexpected Probe call")
}

func (b *eofMediaBackend) Open(context.Context, string, backendOpenOptions) (mediaDecoder, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	decoder := &eofMediaDecoder{closed: make(chan struct{})}
	b.decoders = append(b.decoders, decoder)
	b.opens++
	return decoder, nil
}

type eofMediaDecoder struct {
	closeOnce sync.Once
	closed    chan struct{}
}

func (*eofMediaDecoder) Info() backendMediaInfo { return backendMediaInfo{} }
func (*eofMediaDecoder) ReadFrame(context.Context) (backendFrame, error) {
	return backendFrame{}, io.EOF
}
func (*eofMediaDecoder) Seek(time.Duration) error { return nil }
func (d *eofMediaDecoder) Close() error {
	d.closeOnce.Do(func() { close(d.closed) })
	return nil
}

func TestFFGOStreamDecodeErrorCleansSession(t *testing.T) {
	backend := &eofMediaBackend{}
	decoder := &eofMediaDecoder{closed: make(chan struct{})}
	controller := newFFGOStreamController(backend, "stream", backendOpenOptions{}, backendMediaInfo{}, decoder)
	if err := controller.Play(); err != nil {
		t.Fatalf("Play: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for {
		state, err := controller.State()
		if state == Stopped {
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("decode error = %v, want io.ErrUnexpectedEOF", err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stream controller did not stop after EOF")
		}
		time.Sleep(time.Millisecond)
	}

	backend.mutex.Lock()
	if backend.opens != 0 {
		t.Fatalf("stream was reopened %d times before first playback; want 0", backend.opens)
	}
	backend.mutex.Unlock()
	select {
	case <-decoder.closed:
	case <-time.After(time.Second):
		t.Fatal("decoder was not closed after the decode loop failed")
	}

	controller.mutex.Lock()
	if controller.decoder != nil || controller.stopCh != nil || controller.decodedCh != nil {
		t.Fatalf("stream session was not detached: decoder=%v stop=%v decoded=%v", controller.decoder, controller.stopCh, controller.decodedCh)
	}
	controller.mutex.Unlock()

	done := make(chan struct{})
	go func() {
		controller.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream goroutines did not exit after the decode loop failed")
	}
}
