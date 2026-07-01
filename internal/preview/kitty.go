package preview

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
)

const kittyChunkSize = 4096

// KittyRender writes img to the terminal using the Kitty Graphics Protocol.
// The image is scaled to fit within cols×rows cells and rendered at the current
// cursor position. Output goes directly to os.Stdout, bypassing bubbletea's renderer.
func KittyRender(img image.Image, cols, rows int) error {
	return kittyRenderTo(os.Stdout, img, cols, rows, -1, -1)
}

// KittyRenderAt renders img at a specific column/row offset within the terminal.
// col and row are 0-indexed terminal cell coordinates.
func KittyRenderAt(img image.Image, cols, rows, col, row int) error {
	return kittyRenderTo(os.Stdout, img, cols, rows, col, row)
}

func kittyRenderTo(w io.Writer, img image.Image, cols, rows, col, row int) error {
	scaled := scaleImage(img, cols*2, rows*4)

	var buf bytes.Buffer
	if err := png.Encode(&buf, scaled); err != nil {
		return fmt.Errorf("kitty: encode PNG: %w", err)
	}

	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())

	// First chunk: action=T (transmit+display), format=100 (PNG), cols/rows.
	// Additional chunks: action=m (more) or final.
	chunkCount := (len(encoded) + kittyChunkSize - 1) / kittyChunkSize
	if chunkCount == 0 {
		chunkCount = 1
	}

	for i := 0; i < chunkCount; i++ {
		start := i * kittyChunkSize
		end := start + kittyChunkSize
		if end > len(encoded) {
			end = len(encoded)
		}
		chunk := encoded[start:end]

		more := 1
		if i == chunkCount-1 {
			more = 0
		}

		var params string
		if i == 0 {
			// First chunk: full header.
			params = fmt.Sprintf("a=T,f=100,c=%d,r=%d,m=%d", cols, rows, more)
			if col >= 0 && row >= 0 {
				params += fmt.Sprintf(",X=%d,Y=%d", col, row)
			}
		} else {
			params = fmt.Sprintf("m=%d", more)
		}

		// APC sequence: ESC _ G <params> ; <data> ESC backslash
		fmt.Fprintf(w, "\033_G%s;%s\033\\", params, chunk)
	}

	return nil
}
