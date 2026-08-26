package preview

import (
	"image"
	"image/color"
	"testing"
)

// stripes is the worst case for a downscale that samples instead of averaging:
// one-pixel columns of black and white. Averaged, it is uniform grey; sampled,
// it comes out as bands whose colour depends on where each sample landed.
func stripes(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{0, 0, 0, 255}
			if x%2 == 0 {
				c = color.RGBA{255, 255, 255, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func TestScaleImage_AveragesFineDetail(t *testing.T) {
	const w, h = 30, 20
	scaled := scaleImage(stripes(1200, 800), w, h)

	// The fit is by width, so the middle row is inside the image rather than
	// in the letterboxing above and below it.
	y := h / 2
	for x := 0; x < w; x++ {
		r, g, b, _ := scaled.At(x, y).RGBA()
		grey := int(r >> 8)
		if grey < 96 || grey > 160 {
			t.Fatalf("pixel %d,%d = %d,%d,%d — want mid-grey; the stripes were sampled, not averaged",
				x, y, r>>8, g>>8, b>>8)
		}
	}
}

// A frame far larger than the preview is cut down before the quality kernel
// runs, which is what keeps a 24-megapixel file from costing half a second.
func TestPrescale_ReducesLargeSources(t *testing.T) {
	src := image.Rect(0, 0, 6000, 4000)
	img := image.NewRGBA(src)

	out, rect := prescale(img, src, 30, 20)
	if rect.Dx() >= src.Dx() {
		t.Fatalf("prescale left the image at %dx%d", rect.Dx(), rect.Dy())
	}
	if rect.Dx() < 30*prescaleFactor {
		t.Errorf("prescale went down to %dx%d, past the %d× margin the kernel needs",
			rect.Dx(), rect.Dy(), prescaleFactor)
	}
	if out.Bounds() != rect {
		t.Errorf("bounds %v do not match the returned rectangle %v", out.Bounds(), rect)
	}
}

// An image already near the target size is passed through untouched: halving it
// first would throw away detail the kernel wants.
func TestPrescale_LeavesSmallSourcesAlone(t *testing.T) {
	src := image.Rect(0, 0, 160, 120)
	img := image.NewRGBA(src)

	out, rect := prescale(img, src, 30, 20)
	if rect != src || out != image.Image(img) {
		t.Errorf("prescale touched a %v source, returning %v", src, rect)
	}
}
