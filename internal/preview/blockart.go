package preview

import (
	"fmt"
	"image"
	"image/color"
	"strings"

	"golang.org/x/image/draw"
)

// BlockArt renders img as ANSI half-block art fitting within cols×rows terminal cells.
// Each character cell represents 2 vertical pixels using ▀ (U+2580) with 24-bit ANSI colors:
// top pixel → foreground color, bottom pixel → background color.
// Returns a multi-line string with ANSI escape codes; each line is cols characters wide.
func BlockArt(img image.Image, cols, rows int) string {
	if img == nil || cols <= 0 || rows <= 0 {
		return ""
	}

	// Each cell covers 2 pixel rows, so pixel height = rows*2.
	pixW := cols
	pixH := rows * 2
	scaled := scaleImage(img, pixW, pixH)

	var sb strings.Builder
	bounds := scaled.Bounds()

	for y := bounds.Min.Y; y < bounds.Max.Y-1; y += 2 {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			top := scaled.At(x, y)
			bot := scaled.At(x, y+1)
			tr, tg, tb, _ := top.RGBA()
			br, bg, bb, _ := bot.RGBA()
			// RGBA returns 16-bit values; shift to 8-bit.
			sb.WriteString(fmt.Sprintf("\033[38;2;%d;%d;%dm\033[48;2;%d;%d;%dm▀",
				tr>>8, tg>>8, tb>>8,
				br>>8, bg>>8, bb>>8,
			))
		}
		sb.WriteString("\033[0m\n")
	}

	// Handle odd pixel height: bottom row uses ▀ with background = black.
	if (bounds.Max.Y-bounds.Min.Y)%2 != 0 {
		y := bounds.Max.Y - 1
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			top := scaled.At(x, y)
			tr, tg, tb, _ := top.RGBA()
			sb.WriteString(fmt.Sprintf("\033[38;2;%d;%d;%dm\033[48;2;0;0;0m▀",
				tr>>8, tg>>8, tb>>8,
			))
		}
		sb.WriteString("\033[0m\n")
	}

	return sb.String()
}

// scaleImage resizes img to fit within w×h pixels while preserving aspect ratio.
// The result is always exactly w×h (letterboxed with black).
func scaleImage(img image.Image, w, h int) image.Image {
	src := img.Bounds()
	srcW := src.Dx()
	srcH := src.Dy()

	// Compute scale to fit while preserving aspect ratio.
	scaleX := float64(w) / float64(srcW)
	scaleY := float64(h) / float64(srcH)
	scale := scaleX
	if scaleY < scale {
		scale = scaleY
	}

	fitW := int(float64(srcW) * scale)
	fitH := int(float64(srcH) * scale)
	if fitW < 1 {
		fitW = 1
	}
	if fitH < 1 {
		fitH = 1
	}

	// Draw scaled image onto a black canvas.
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	fillBlack(dst)

	offsetX := (w - fitW) / 2
	offsetY := (h - fitH) / 2
	dstRect := image.Rect(offsetX, offsetY, offsetX+fitW, offsetY+fitH)

	draw.BiLinear.Scale(dst, dstRect, img, src, draw.Over, nil)
	return dst
}

func fillBlack(img *image.RGBA) {
	b := img.Bounds()
	black := color.RGBA{0, 0, 0, 255}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			img.SetRGBA(x, y, black)
		}
	}
}
