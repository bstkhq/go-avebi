package avebi

import (
	"context"
	"sync"
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
	// ReadFrame transfers ownership of a returned video frame's RGBA buffer to
	// the caller. The caller must recycle it through recycleBackendFrame after it
	// is no longer displayed or queued.
	ReadFrame(context.Context, *backendVideoBufferPool) (backendFrame, error)
	// Seek positions the decoder and makes subsequent ReadFrame calls discard
	// frames until every enabled stream reaches or covers the target.
	Seek(time.Duration) error
	Close() error
}

type backendOpenOptions struct {
	DisableAudio     bool
	OutputSampleRate int
	UseYUVShader     bool
	Live             bool
	ConnTimeout      time.Duration
	ReadTimeout      time.Duration
	RTSPTransport    string
}

type backendMediaInfo struct {
	Duration time.Duration
	Video    *backendVideoInfo
	Audio    *backendAudioInfo
}

type backendVideoInfo struct {
	Width        int
	Height       int
	Codec        string
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
	RGBA []byte
	YUV  []byte
	// YUVTextureW and YUVTextureH describe the packed byte-storage texture,
	// not the decoded video's display dimensions. Four YUV bytes are stored in
	// each RGBA texel and the chroma rows follow the luma rows.
	YUVTextureW int
	YUVTextureH int
	Format      backendVideoFormat
	Color       backendVideoColor
	Width       int
	Height      int
}

func packedYUVTextureSize(width, height int) (int, int) {
	packedStride := (width + 3) &^ 3
	return packedStride / 4, height + (height+1)/2
}

type backendVideoFormat uint8

const (
	backendVideoFormatRGBA backendVideoFormat = iota
	backendVideoFormatYUV420P
	backendVideoFormatNV12
)

type backendVideoColor struct {
	YScale  float32
	YOffset float32
	UVScale float32
	UVZero  float32
	RCr     float32
	GCb     float32
	GCr     float32
	BCb     float32
}

const maxPooledVideoBuffers = 8

// backendVideoBufferPool reuses tightly packed RGBA or YUV storage filled by
// the decoder and later uploaded to Ebitengine.
type backendVideoBufferPool struct {
	mutex   sync.Mutex
	buffers [][]byte
}

func (p *backendVideoBufferPool) get(size int) []byte {
	if size <= 0 {
		return nil
	}
	if p == nil {
		return make([]byte, size)
	}

	p.mutex.Lock()
	var buffer []byte
	if last := len(p.buffers) - 1; last >= 0 {
		buffer = p.buffers[last]
		p.buffers[last] = nil
		p.buffers = p.buffers[:last]
	}
	p.mutex.Unlock()

	if cap(buffer) < size {
		return make([]byte, size)
	}
	return buffer[:size]
}

func (p *backendVideoBufferPool) put(buffer []byte) {
	if p == nil || cap(buffer) == 0 {
		return
	}
	buffer = buffer[:0]
	p.mutex.Lock()
	defer p.mutex.Unlock()
	if len(p.buffers) < maxPooledVideoBuffers {
		p.buffers = append(p.buffers, buffer)
		return
	}

	// Keep the pool useful after a resolution increase even if older, smaller
	// buffers return concurrently with the new frames.
	smallest := 0
	for i := 1; i < len(p.buffers); i++ {
		if cap(p.buffers[i]) < cap(p.buffers[smallest]) {
			smallest = i
		}
	}
	if cap(buffer) > cap(p.buffers[smallest]) {
		last := len(p.buffers) - 1
		p.buffers[smallest] = p.buffers[last]
		p.buffers[last] = buffer
	}
}

func (p *backendVideoBufferPool) clear() {
	if p == nil {
		return
	}
	p.mutex.Lock()
	clear(p.buffers)
	p.buffers = nil
	p.mutex.Unlock()
}

func recycleBackendFrame(pool *backendVideoBufferPool, frame *backendFrame) {
	if frame == nil {
		return
	}
	if frame.Kind == backendFrameVideo {
		if frame.Video.YUV != nil {
			pool.put(frame.Video.YUV)
		} else {
			pool.put(frame.Video.RGBA)
		}
	}
	*frame = backendFrame{}
}

type backendAudioFrame struct {
	PCM        []byte
	SampleRate int
	Channels   int
}
