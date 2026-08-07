package avebi

import (
	"context"
	"time"
)

// mediaBackend is the boundary between playback and the media library. Backend
// implementations must not expose native FFmpeg values through these types.
type mediaBackend interface {
	Probe(context.Context, string, backendOpenOptions) (backendMediaInfo, error)
	Open(context.Context, string, backendOpenOptions) (mediaDecoder, error)
}

// mediaDecoder represents one opened demux/decode session. Calls other than
// Close are serialized by the playback controllers.
type mediaDecoder interface {
	Info() backendMediaInfo
	ReadFrame(context.Context) (backendFrame, error)
	// Seek positions the decoder and makes subsequent ReadFrame calls discard
	// frames until every enabled stream reaches or covers the target.
	Seek(time.Duration) error
	Close() error
}

type backendOpenOptions struct {
	DisableAudio     bool
	OutputSampleRate int
	Live             bool
	ConnTimeout      time.Duration
	ReadTimeout      time.Duration
}

type backendMediaInfo struct {
	Duration time.Duration
	Video    *backendVideoInfo
	Audio    *backendAudioInfo
}

type backendVideoInfo struct {
	Width        int
	Height       int
	FrameRateNum int
	FrameRateDen int
}

func (i backendVideoInfo) FrameDuration() time.Duration {
	if i.FrameRateNum <= 0 || i.FrameRateDen <= 0 {
		return 0
	}
	return time.Second * time.Duration(i.FrameRateDen) / time.Duration(i.FrameRateNum)
}

type backendAudioInfo struct {
	SampleRate int
	Channels   int
}

type backendFrameKind uint8

const (
	backendFrameUnknown backendFrameKind = iota
	backendFrameVideo
	backendFrameAudio
)

type backendFrame struct {
	Kind     backendFrameKind
	PTS      time.Duration
	Duration time.Duration
	Video    backendVideoFrame
	Audio    backendAudioFrame
}

type backendVideoFrame struct {
	RGBA   []byte
	Width  int
	Height int
	Stride int
}

type backendAudioFrame struct {
	PCM        []byte
	SampleRate int
	Channels   int
}
