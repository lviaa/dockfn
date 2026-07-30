package iconimage

import (
	"image"
	"image/color"
	"testing"
)

func TestScaleUsesSmoothInterpolation(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 2, 1))
	source.SetRGBA(0, 0, color.RGBA{A: 255})
	source.SetRGBA(1, 0, color.RGBA{R: 255, G: 255, B: 255, A: 255})

	scaled := Scale(source, 8, 1)
	pixel := scaled.RGBAAt(3, 0)
	if pixel.R == 0 || pixel.R == 255 {
		t.Fatalf("expected an interpolated pixel, got %#v", pixel)
	}
}

func TestFitSquarePreservesAspectRatioAndTransparency(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			source.SetRGBA(x, y, color.RGBA{R: 30, G: 80, B: 180, A: 255})
		}
	}

	fitted := FitSquare(source, 16)
	if fitted.Bounds() != image.Rect(0, 0, 16, 16) {
		t.Fatalf("unexpected output bounds: %v", fitted.Bounds())
	}
	if got := fitted.RGBAAt(0, 0).A; got != 0 {
		t.Fatalf("expected transparent letterbox, got alpha %d", got)
	}
	if got := fitted.RGBAAt(8, 8).A; got != 255 {
		t.Fatalf("expected opaque image content, got alpha %d", got)
	}
}
