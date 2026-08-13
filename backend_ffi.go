//go:build !ios && !android && (amd64 || arm64)

package avebi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/bstkhq/go-ffmpeg-ffi"
	"github.com/bstkhq/go-ffmpeg-ffi/avutil"
)

type ffiBackend struct{}

var _ mediaBackend = ffiBackend{}
var _ mediaDecoder = (*ffiDecoder)(nil)

func newMediaBackend() mediaBackend { return ffiBackend{} }

func (ffiBackend) Probe(ctx context.Context, source string, opts backendOpenOptions) (backendMediaInfo, error) {
	decoder, err := openFFmpegDecoder(ctx, source, opts)
	if err != nil {
		return backendMediaInfo{}, err
	}
	info := decoder.Info()
	return info, decoder.Close()
}

func (ffiBackend) Open(ctx context.Context, source string, opts backendOpenOptions) (mediaDecoder, error) {
	return openFFmpegDecoder(ctx, source, opts)
}

type ffiDecoder struct {
	mutex sync.Mutex

	decoder *ffmpeg.Decoder
	info    backendMediaInfo

	outputSampleRate  int
	scaler            *ffmpeg.Scaler
	scalerWidth       int
	scalerHeight      int
	scalerFormat      ffmpeg.PixelFormat
	resampler         *ffmpeg.Resampler
	resamplerRate     int
	resamplerChannels int
	resamplerFormat   ffmpeg.SampleFormat

	seeking        bool
	seekTarget     time.Duration
	seekVideoReady bool
	seekAudioReady bool
	closed         bool
}

func openFFmpegDecoder(ctx context.Context, source string, opts backendOpenOptions) (*ffiDecoder, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ffmpeg.Init(); err != nil {
		return nil, fmt.Errorf("initialize go-ffmpeg-ffi: %w", err)
	}

	decoderOpts := &ffmpeg.DecoderOptions{}
	if opts.DisableAudio {
		decoderOpts.Streams = []ffmpeg.MediaType{ffmpeg.MediaTypeVideo}
	}
	if opts.Live {
		decoderOpts.AVOptions = make(map[string]string)
		if opts.ConnTimeout > 0 {
			value := strconv.FormatInt(opts.ConnTimeout.Microseconds(), 10)
			decoderOpts.AVOptions["timeout"] = value
			decoderOpts.AVOptions["stimeout"] = value
		}
		if opts.ReadTimeout > 0 {
			decoderOpts.AVOptions["rw_timeout"] = strconv.FormatInt(opts.ReadTimeout.Microseconds(), 10)
		}
	}

	decoder, err := ffmpeg.NewDecoder(source, decoderOpts)
	if err != nil {
		return nil, err
	}

	info := mediaInfoFromFFmpeg(decoder)
	if info.Video == nil || info.Video.Width <= 0 || info.Video.Height <= 0 {
		_ = decoder.Close()
		return nil, ErrNoVideo
	}

	outputSampleRate := opts.OutputSampleRate
	if outputSampleRate <= 0 && info.Audio != nil {
		outputSampleRate = info.Audio.SampleRate
	}

	return &ffiDecoder{
		decoder:          decoder,
		info:             info,
		outputSampleRate: outputSampleRate,
	}, nil
}

func mediaInfoFromFFmpeg(decoder *ffmpeg.Decoder) backendMediaInfo {
	info := backendMediaInfo{Duration: decoder.Duration()}
	if stream := decoder.VideoStream(); stream != nil {
		info.Video = &backendVideoInfo{
			Width:        stream.Width,
			Height:       stream.Height,
			FrameRateNum: int(stream.FrameRate.Num),
			FrameRateDen: int(stream.FrameRate.Den),
		}
	}
	if stream := decoder.AudioStream(); stream != nil {
		info.Audio = &backendAudioInfo{
			SampleRate: stream.SampleRate,
			Channels:   stream.Channels,
		}
	}
	return info
}

func (d *ffiDecoder) Info() backendMediaInfo { return d.info }

func (d *ffiDecoder) ReadFrame(ctx context.Context, videoBuffers *backendVideoBufferPool) (backendFrame, error) {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	if d.closed {
		return backendFrame{}, errors.New("avebi: FFmpeg decoder is closed")
	}

	for {
		if err := ctx.Err(); err != nil {
			return backendFrame{}, err
		}

		frame, err := d.decoder.ReadFrame()
		if err != nil {
			return backendFrame{}, err
		}
		if frame == nil {
			return backendFrame{}, io.EOF
		}

		converted, ok, err := d.convertFrameLocked(frame, videoBuffers)
		if err != nil {
			return backendFrame{}, err
		}
		if !ok || !d.acceptSeekFrameLocked(converted) {
			recycleBackendFrame(videoBuffers, &converted)
			continue
		}
		return converted, nil
	}
}

// The *Locked helpers below require d.mutex to be held by the caller.
func (d *ffiDecoder) convertFrameLocked(frame *ffmpeg.FrameWrapper, videoBuffers *backendVideoBufferPool) (backendFrame, bool, error) {
	switch frame.MediaType() {
	case ffmpeg.MediaTypeVideo:
		return d.convertVideoFrameLocked(frame, videoBuffers)
	case ffmpeg.MediaTypeAudio:
		return d.convertAudioFrameLocked(frame)
	default:
		return backendFrame{}, false, nil
	}
}

func (d *ffiDecoder) convertVideoFrameLocked(frame *ffmpeg.FrameWrapper, videoBuffers *backendVideoBufferPool) (backendFrame, bool, error) {
	width, height := frame.Width(), frame.Height()
	if width <= 0 || height <= 0 {
		width, height = d.info.Video.Width, d.info.Video.Height
	}
	sourceFormat := frame.PixelFormat()
	if d.scaler == nil || d.scalerWidth != width || d.scalerHeight != height || d.scalerFormat != sourceFormat {
		if d.scaler != nil {
			_ = d.scaler.Close()
		}
		// Source and destination dimensions intentionally match: swscale is used
		// for pixel-format and YUV-to-RGB conversion, not geometric resizing. We
		// keep go-ffmpeg-ffi's bilinear default, but FFmpeg does not clearly specify how the
		// choice affects chroma handling on this unscaled conversion, and avebi does
		// not rely on a particular effect.
		scaler, err := ffmpeg.NewScaler(width, height, sourceFormat, width, height, ffmpeg.PixelFormatRGBA, ffmpeg.ScaleBilinear)
		if err != nil {
			return backendFrame{}, false, err
		}
		d.scaler = scaler
		d.scalerWidth = width
		d.scalerHeight = height
		d.scalerFormat = sourceFormat
	}

	scaled, err := d.scaler.Scale(frame.Raw())
	if err != nil {
		return backendFrame{}, false, err
	}
	// Scale returns a scaler-owned raw frame that is overwritten by the next
	// call. WrapFrame exposes its data pointer and stride; copyFFmpegRGBA detaches
	// it into reusable, tightly packed storage for ebiten.Image.WritePixels.
	wrapped := ffmpeg.WrapFrame(scaled, ffmpeg.MediaTypeVideo)
	stride := wrapped.Linesize(0)
	rowSize := width * 4
	if stride < rowSize {
		return backendFrame{}, false, fmt.Errorf("avebi: go-ffmpeg-ffi returned RGBA stride %d for row size %d", stride, rowSize)
	}
	data := wrapped.Data(0)
	if len(data) < stride*height {
		return backendFrame{}, false, fmt.Errorf("avebi: go-ffmpeg-ffi returned a truncated RGBA frame")
	}

	rgba := videoBuffers.get(rowSize * height)
	copyFFmpegRGBA(rgba, data, stride, rowSize, height)

	pts := ffmpegPTS(frame.PTS(), d.decoder.VideoStream().TimeBase)
	duration := d.info.Video.FrameDuration()
	return backendFrame{
		Kind:     backendFrameVideo,
		PTS:      pts,
		Duration: duration,
		Video: backendVideoFrame{
			RGBA:   rgba,
			Width:  width,
			Height: height,
			Stride: rowSize,
		},
	}, true, nil
}

func copyFFmpegRGBA(dst, src []byte, stride, rowSize, height int) {
	if stride == rowSize {
		copy(dst, src[:len(dst)])
		return
	}
	for row := 0; row < height; row++ {
		copy(dst[row*rowSize:(row+1)*rowSize], src[row*stride:row*stride+rowSize])
	}
}

func (d *ffiDecoder) convertAudioFrameLocked(frame *ffmpeg.FrameWrapper) (backendFrame, bool, error) {
	if d.info.Audio == nil || d.outputSampleRate <= 0 {
		return backendFrame{}, false, nil
	}

	sampleRate := frame.SampleRate()
	if sampleRate <= 0 {
		sampleRate = d.info.Audio.SampleRate
	}
	channels := d.info.Audio.Channels
	if sampleRate <= 0 || channels <= 0 {
		return backendFrame{}, false, fmt.Errorf("avebi: invalid FFmpeg audio format %d Hz/%d channels", sampleRate, channels)
	}
	sampleFormat := frame.SampleFormat()
	if d.resampler == nil || d.resamplerRate != sampleRate || d.resamplerChannels != channels || d.resamplerFormat != sampleFormat {
		if d.resampler != nil {
			_ = d.resampler.Close()
		}
		// Ebitengine has one process-wide output sample rate. It can differ from
		// this media (notably after another player created the audio context), and
		// decoded FFmpeg audio is commonly planar or non-S16. Normalize every
		// source to packed stereo S16 at the active context's sample rate.
		resampler, err := ffmpeg.NewResampler(
			ffmpeg.AudioFormat{SampleRate: sampleRate, Channels: channels, SampleFormat: sampleFormat},
			ffmpeg.AudioFormat{SampleRate: d.outputSampleRate, Channels: 2, ChannelLayout: ffmpeg.ChannelLayoutStereo, SampleFormat: ffmpeg.SampleFormatS16},
		)
		if err != nil {
			return backendFrame{}, false, err
		}
		d.resampler = resampler
		d.resamplerRate = sampleRate
		d.resamplerChannels = channels
		d.resamplerFormat = sampleFormat
	}

	resampled, err := d.resampler.Resample(frame.Raw())
	if err != nil {
		return backendFrame{}, false, err
	}
	if resampled.IsNil() {
		return backendFrame{}, false, nil
	}
	defer resampled.Free()

	wrapped := ffmpeg.WrapFrame(resampled, ffmpeg.MediaTypeAudio)
	samples := wrapped.NumSamples()
	if samples <= 0 {
		return backendFrame{}, false, nil
	}
	expectedBytes := samples * 2 * 2
	data := wrapped.Data(0)
	if len(data) < expectedBytes {
		return backendFrame{}, false, fmt.Errorf("avebi: go-ffmpeg-ffi returned %d audio bytes, expected at least %d", len(data), expectedBytes)
	}
	pcm := append([]byte(nil), data[:expectedBytes]...)

	pts := ffmpegPTS(frame.PTS(), d.decoder.AudioStream().TimeBase)
	duration := time.Second * time.Duration(samples) / time.Duration(d.outputSampleRate)
	return backendFrame{
		Kind:     backendFrameAudio,
		PTS:      pts,
		Duration: duration,
		Audio: backendAudioFrame{
			PCM:        pcm,
			SampleRate: d.outputSampleRate,
			Channels:   2,
		},
	}, true, nil
}

func ffmpegPTS(pts int64, timeBase ffmpeg.Rational) time.Duration {
	if pts == avutil.AV_NOPTS_VALUE || timeBase.Num <= 0 || timeBase.Den <= 0 {
		return 0
	}
	seconds := float64(pts) * float64(timeBase.Num) / float64(timeBase.Den)
	return time.Duration(seconds * float64(time.Second))
}

func (d *ffiDecoder) acceptSeekFrameLocked(frame backendFrame) bool {
	if !d.seeking {
		return true
	}

	ready := false
	switch frame.Kind {
	case backendFrameVideo:
		if d.seekVideoReady {
			return true
		}
		ready = frame.PTS+frame.Duration >= d.seekTarget
		if ready {
			d.seekVideoReady = true
		}
	case backendFrameAudio:
		if d.seekAudioReady {
			return true
		}
		ready = frame.PTS+frame.Duration >= d.seekTarget
		if ready {
			d.seekAudioReady = true
		}
	}

	if d.seekVideoReady && d.seekAudioReady {
		d.seeking = false
	}
	return ready
}

func (d *ffiDecoder) Seek(position time.Duration) error {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	if d.closed {
		return errors.New("avebi: FFmpeg decoder is closed")
	}
	position = max(position, 0)
	if d.info.Duration > 0 {
		position = min(position, d.info.Duration)
	}
	if err := d.decoder.Seek(position); err != nil {
		return err
	}
	if d.resampler != nil {
		// Flush would emit delayed samples from before the seek. Close instead so
		// the next audio frame creates a context with no pre-seek history.
		_ = d.resampler.Close()
		d.resampler = nil
	}
	d.seeking = true
	d.seekTarget = position
	d.seekVideoReady = d.info.Video == nil
	d.seekAudioReady = d.info.Audio == nil
	return nil
}

func (d *ffiDecoder) Close() error {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	if d.closed {
		return nil
	}
	d.closed = true

	var errs []error
	if d.resampler != nil {
		errs = append(errs, d.resampler.Close())
		d.resampler = nil
	}
	if d.scaler != nil {
		errs = append(errs, d.scaler.Close())
		d.scaler = nil
	}
	if d.decoder != nil {
		errs = append(errs, d.decoder.Close())
		d.decoder = nil
	}
	return errors.Join(errs...)
}
