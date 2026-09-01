//go:build amd64 || arm64

package avebi

import (
	"image"
	"math"
	"os"
	"slices"
	"testing"

	"github.com/bstkhq/go-ffmpeg-ffi"
	"github.com/hajimehoshi/ebiten/v2"
)

func TestCopyVideoPlane(t *testing.T) {
	t.Run("positive stride", func(t *testing.T) {
		src := []byte{1, 2, 3, 90, 91, 4, 5, 6, 92, 93}
		dst := make([]byte, 8)
		if err := copyVideoPlaneAt(dst, 4, 0, src, 5, 3, 2); err != nil {
			t.Fatal(err)
		}
		if want := []byte{1, 2, 3, 0, 4, 5, 6, 0}; !slices.Equal(dst, want) {
			t.Fatalf("packed plane = %v, want %v", dst, want)
		}
	})

	t.Run("negative stride", func(t *testing.T) {
		// frameData exposes negative-stride planes from the lowest address, so the
		// logical first row is the final row in this slice.
		src := []byte{4, 5, 6, 92, 93, 1, 2, 3, 90, 91}
		dst := make([]byte, 8)
		if err := copyVideoPlaneAt(dst, 4, 0, src, -5, 3, 2); err != nil {
			t.Fatal(err)
		}
		if want := []byte{1, 2, 3, 0, 4, 5, 6, 0}; !slices.Equal(dst, want) {
			t.Fatalf("packed plane = %v, want %v", dst, want)
		}
	})
}

func TestPackedYUVTextureSize(t *testing.T) {
	for _, test := range []struct {
		width, height               int
		textureWidth, textureHeight int
	}{
		{width: 1920, height: 1080, textureWidth: 480, textureHeight: 1620},
		{width: 5, height: 3, textureWidth: 2, textureHeight: 5},
	} {
		width, height := packedYUVTextureSize(test.width, test.height)
		if width != test.textureWidth || height != test.textureHeight {
			t.Errorf("packedYUVTextureSize(%d, %d) = (%d, %d), want (%d, %d)", test.width, test.height, width, height, test.textureWidth, test.textureHeight)
		}
	}
}

func TestYUVColorParameters(t *testing.T) {
	tests := []struct {
		name    string
		spec    ffmpeg.ColorSpec
		yScale  float32
		yOffset float32
		rCr     float32
		bCb     float32
	}{
		{
			name:    "BT.709 limited",
			spec:    ffmpeg.ColorSpec{Range: ffmpeg.ColorRangeMPEG, Space: ffmpeg.ColorSpaceBT709},
			yScale:  255.0 / 219.0,
			yOffset: 16.0 / 255.0,
			rCr:     1.5748,
			bCb:     1.8556,
		},
		{
			name:    "BT.601 full",
			spec:    ffmpeg.ColorSpec{Range: ffmpeg.ColorRangeJPEG, Space: ffmpeg.ColorSpaceSMPTE170M},
			yScale:  1,
			yOffset: 0,
			rCr:     1.402,
			bCb:     1.772,
		},
		{
			name:    "FCC limited",
			spec:    ffmpeg.ColorSpec{Range: ffmpeg.ColorRangeMPEG, Space: ffmpeg.ColorSpaceFCC},
			yScale:  255.0 / 219.0,
			yOffset: 16.0 / 255.0,
			rCr:     1.4,
			bCb:     1.78,
		},
		{
			name:    "SMPTE 240M limited",
			spec:    ffmpeg.ColorSpec{Range: ffmpeg.ColorRangeMPEG, Space: ffmpeg.ColorSpaceSMPTE240M},
			yScale:  255.0 / 219.0,
			yOffset: 16.0 / 255.0,
			rCr:     1.576,
			bCb:     1.826,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := yuvColorParameters(test.spec)
			closeFloat32(t, "YScale", got.YScale, test.yScale)
			closeFloat32(t, "YOffset", got.YOffset, test.yOffset)
			closeFloat32(t, "RCr", got.RCr, test.rCr)
			closeFloat32(t, "BCb", got.BCb, test.bCb)
		})
	}
}

func TestYUVShaderSupportedColorSpaces(t *testing.T) {
	for _, space := range []ffmpeg.ColorSpace{
		ffmpeg.ColorSpaceBT709,
		ffmpeg.ColorSpaceFCC,
		ffmpeg.ColorSpaceBT470BG,
		ffmpeg.ColorSpaceSMPTE170M,
		ffmpeg.ColorSpaceSMPTE240M,
		ffmpeg.ColorSpaceBT2020NCL,
	} {
		if !yuvShaderSupportsColorSpace(space) {
			t.Errorf("color space %d should use the YUV shader", space)
		}
	}
	for _, space := range []ffmpeg.ColorSpace{
		ffmpeg.ColorSpaceUnspecified,
		ffmpeg.ColorSpaceBT2020CL,
		ffmpeg.ColorSpaceSMPTE2085,
		ffmpeg.ColorSpaceChromaticityDerivedNCL,
		ffmpeg.ColorSpaceChromaticityDerivedCL,
		ffmpeg.ColorSpaceICTCP,
	} {
		if yuvShaderSupportsColorSpace(space) {
			t.Errorf("color space %d must fall back to RGBA", space)
		}
	}
}

func TestYUVShaderCompiles(t *testing.T) {
	shader, err := loadYUV420Shader()
	if err != nil {
		t.Fatalf("compile YUV shader: %v", err)
	}
	again, err := loadYUV420Shader()
	if err != nil {
		t.Fatalf("reuse YUV shader: %v", err)
	}
	if shader != again {
		t.Fatal("YUV shader was compiled more than once")
	}
}

func TestYUVShaderRendersBT709Limited(t *testing.T) {
	if os.Getenv("AVEBI_TEST_YUV_SHADER_RENDER") != "1" {
		t.Skip("set AVEBI_TEST_YUV_SHADER_RENDER=1 on a machine with a working graphics context")
	}
	// Keep future pixel cases in this RunGame: starting Ebitengine more than
	// once in the same process is not supported reliably on every platform.
	const width, height = 4, 2
	colorParameters := yuvColorParameters(ffmpeg.ColorSpec{
		Range: ffmpeg.ColorRangeMPEG,
		Space: ffmpeg.ColorSpaceBT709,
	})
	frame := backendVideoFrame{
		YUV: []byte{
			16, 235, 81, 145,
			32, 219, 96, 160,
			128, 128, 90, 240,
		},
		YUVTextureW: 1,
		YUVTextureH: 3,
		Format:      backendVideoFormatYUV420P,
		Color:       colorParameters,
		Width:       width,
		Height:      height,
	}
	shader, err := loadYUV420Shader()
	if err != nil {
		t.Fatal(err)
	}
	player := &Player{
		currentFrame: ebiten.NewImageWithOptions(image.Rect(0, 0, width, height), &ebiten.NewImageOptions{Unmanaged: true}),
		yuvShader:    shader,
		yuvImage:     ebiten.NewImageWithOptions(image.Rect(0, 0, frame.YUVTextureW, frame.YUVTextureH), &ebiten.NewImageOptions{Unmanaged: true}),
	}
	game := &yuvShaderRenderGame{player: player, frame: frame}
	if err := ebiten.RunGame(game); err != nil && err != ebiten.Termination {
		t.Fatalf("run shader render: %v", err)
	}
	if game.err != nil {
		t.Fatal(game.err)
	}
	if len(game.pixels) != width*height*4 {
		t.Fatalf("rendered bytes = %d, want %d", len(game.pixels), width*height*4)
	}

	for pixel := 0; pixel < width*height; pixel++ {
		x := pixel % width
		y := pixel / width
		yValue := float32(frame.YUV[y*4+x]) / 255
		chromaX := x / 2
		cb := float32(frame.YUV[8+chromaX]) / 255
		cr := float32(frame.YUV[8+width/2+chromaX]) / 255
		want := yuvToRGBReference(yValue, cb, cr, colorParameters)
		for channel := 0; channel < 3; channel++ {
			got := game.pixels[pixel*4+channel]
			if difference := int(got) - int(want[channel]); difference < -2 || difference > 2 {
				t.Errorf("pixel (%d,%d) channel %d = %d, want %d", x, y, channel, got, want[channel])
			}
		}
		if alpha := game.pixels[pixel*4+3]; alpha != 255 {
			t.Errorf("pixel (%d,%d) alpha = %d, want 255", x, y, alpha)
		}
	}
}

type yuvShaderRenderGame struct {
	player *Player
	frame  backendVideoFrame
	tick   int
	pixels []byte
	err    error
}

func (g *yuvShaderRenderGame) Update() error {
	switch g.tick {
	case 0:
		g.err = g.player.drawYUVFrame(&g.frame)
		g.tick++
		return g.err
	default:
		g.pixels = make([]byte, 4*g.frame.Width*g.frame.Height)
		g.player.currentFrame.ReadPixels(g.pixels)
		return ebiten.Termination
	}
}

func (g *yuvShaderRenderGame) Draw(screen *ebiten.Image) {
	screen.DrawImage(g.player.currentFrame, nil)
}

func (g *yuvShaderRenderGame) Layout(_, _ int) (int, int) {
	return g.frame.Width, g.frame.Height
}

func yuvToRGBReference(y, cb, cr float32, color backendVideoColor) [3]byte {
	y = (y - color.YOffset) * color.YScale
	cb = (cb - color.UVZero) * color.UVScale
	cr = (cr - color.UVZero) * color.UVScale
	values := [3]float32{
		y + color.RCr*cr,
		y + color.GCb*cb + color.GCr*cr,
		y + color.BCb*cb,
	}
	var result [3]byte
	for index, value := range values {
		value = max(0, min(1, value))
		result[index] = byte(math.Round(float64(255 * value)))
	}
	return result
}

func closeFloat32(t *testing.T, name string, got, want float32) {
	t.Helper()
	if math.Abs(float64(got-want)) > 0.0001 {
		t.Fatalf("%s = %.6f, want %.6f", name, got, want)
	}
}
