package iconimage

import (
	"image"
	"math"

	xdraw "golang.org/x/image/draw"
)

// Scale resizes an image with a high-quality reconstruction filter.
func Scale(source image.Image, width, height int) *image.RGBA {
	destination := image.NewRGBA(image.Rect(0, 0, width, height))
	xdraw.CatmullRom.Scale(
		destination,
		destination.Bounds(),
		source,
		source.Bounds(),
		xdraw.Src,
		nil,
	)
	return destination
}

// FitSquare preserves the source aspect ratio and centers it on a transparent
// square canvas.
func FitSquare(source image.Image, size int) *image.RGBA {
	destination := image.NewRGBA(image.Rect(0, 0, size, size))
	sourceBounds := source.Bounds()
	if size <= 0 || sourceBounds.Empty() {
		return destination
	}

	scale := math.Min(
		float64(size)/float64(sourceBounds.Dx()),
		float64(size)/float64(sourceBounds.Dy()),
	)
	width := max(1, int(math.Round(float64(sourceBounds.Dx())*scale)))
	height := max(1, int(math.Round(float64(sourceBounds.Dy())*scale)))
	target := image.Rect(
		(size-width)/2,
		(size-height)/2,
		(size-width)/2+width,
		(size-height)/2+height,
	)
	xdraw.CatmullRom.Scale(
		destination,
		target,
		source,
		sourceBounds,
		xdraw.Src,
		nil,
	)
	return destination
}
