//go:build amd64 || arm64

package avebi

import (
	"fmt"
	"sync"

	"github.com/hajimehoshi/ebiten/v2"
)

var loadYUV420Shader = sync.OnceValues(func() (*ebiten.Shader, error) {
	return ebiten.NewShader(yuv420ShaderSource)
})

var yuv420ShaderSource = []byte(`//kage:unit pixels

package main

var YScale float
var YOffset float
var UVScale float
var UVZero float
var RCr float
var GCb float
var GCr float
var BCb float
var Interleaved float
var VideoHeight float
var ChromaWidth float

func packedByte(pos vec2) float {
	x := floor(pos.x)
	texelX := floor(x / 4)
	index := x - 4*texelX
	value := imageSrc0UnsafeAt(imageSrc0Origin() + vec2(texelX+0.5, floor(pos.y)+0.5))
	return value[int(index)]
}

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	pixel := floor(dstPos.xy - imageDstOrigin())
	y := (packedByte(pixel) - YOffset) * YScale
	// Chroma uses nearest-neighbor 2x2 upsampling. This keeps the shader and
	// texture reads small, but is intentionally lower quality than swscale's
	// bilinear RGBA path at saturated color boundaries.
	chroma := floor(pixel / 2)
	chromaY := VideoHeight + chroma.y

	var cb float
	var cr float
	if Interleaved > 0.5 {
		first := packedByte(vec2(2*chroma.x, chromaY))
		second := packedByte(vec2(2*chroma.x+1, chromaY))
		cb = first
		cr = second
	} else {
		cb = packedByte(vec2(chroma.x, chromaY))
		cr = packedByte(vec2(ChromaWidth+chroma.x, chromaY))
	}
	cb = (cb - UVZero) * UVScale
	cr = (cr - UVZero) * UVScale

	rgb := vec3(
		y + RCr*cr,
		y + GCb*cb + GCr*cr,
		y + BCb*cb,
	)
	return vec4(clamp(rgb, vec3(0), vec3(1)), 1)
}
`)

func (p *Player) drawYUVFrame(frame *backendVideoFrame) error {
	if p.yuvShader == nil {
		return fmt.Errorf("avebi: received a YUV frame without a video shader")
	}
	if frame.Width != p.currentFrame.Bounds().Dx() || frame.Height != p.currentFrame.Bounds().Dy() {
		return fmt.Errorf("avebi: decoded YUV frame is %dx%d, expected %dx%d", frame.Width, frame.Height, p.currentFrame.Bounds().Dx(), p.currentFrame.Bounds().Dy())
	}

	interleaved := float32(0)
	switch frame.Format {
	case backendVideoFormatYUV420P:
	case backendVideoFormatNV12:
		interleaved = 1
	default:
		return fmt.Errorf("avebi: unsupported shader video format %d", frame.Format)
	}

	if frame.YUVTextureW <= 0 || frame.YUVTextureH <= 0 {
		return fmt.Errorf("avebi: invalid packed YUV texture: %dx%d", frame.YUVTextureW, frame.YUVTextureH)
	}
	size := 4 * frame.YUVTextureW * frame.YUVTextureH
	if size != len(frame.YUV) {
		return fmt.Errorf("avebi: packed YUV texture has %d bytes, expected %d", len(frame.YUV), size)
	}
	if p.yuvImage == nil {
		return fmt.Errorf("avebi: received a YUV frame without a packed video texture")
	}
	if bounds := p.yuvImage.Bounds(); bounds.Dx() != frame.YUVTextureW || bounds.Dy() != frame.YUVTextureH {
		return fmt.Errorf("avebi: packed YUV texture is %dx%d, expected %dx%d", frame.YUVTextureW, frame.YUVTextureH, bounds.Dx(), bounds.Dy())
	}
	// The packed image is opaque byte storage, not color. WritePixels currently
	// preserves all four channels even when they violate premultiplied-alpha
	// invariants; the shader unpacks those channels as independent YUV bytes.
	p.yuvImage.WritePixels(frame.YUV)

	w, h := float32(frame.Width), float32(frame.Height)
	vertices := []ebiten.Vertex{
		{DstX: 0, DstY: 0, SrcX: 0, SrcY: 0, ColorR: 1, ColorG: 1, ColorB: 1, ColorA: 1},
		{DstX: w, DstY: 0, SrcX: w, SrcY: 0, ColorR: 1, ColorG: 1, ColorB: 1, ColorA: 1},
		{DstX: 0, DstY: h, SrcX: 0, SrcY: h, ColorR: 1, ColorG: 1, ColorB: 1, ColorA: 1},
		{DstX: w, DstY: h, SrcX: w, SrcY: h, ColorR: 1, ColorG: 1, ColorB: 1, ColorA: 1},
	}
	op := &ebiten.DrawTrianglesShaderOptions{
		Blend: ebiten.BlendCopy,
		Uniforms: map[string]any{
			"YScale":      frame.Color.YScale,
			"YOffset":     frame.Color.YOffset,
			"UVScale":     frame.Color.UVScale,
			"UVZero":      frame.Color.UVZero,
			"RCr":         frame.Color.RCr,
			"GCb":         frame.Color.GCb,
			"GCr":         frame.Color.GCr,
			"BCb":         frame.Color.BCb,
			"Interleaved": interleaved,
			"VideoHeight": float32(frame.Height),
			"ChromaWidth": float32((frame.Width + 1) / 2),
		},
	}
	op.Images[0] = p.yuvImage
	p.currentFrame.DrawTrianglesShader(vertices, []uint16{0, 1, 2, 1, 2, 3}, p.yuvShader, op)
	return nil
}
