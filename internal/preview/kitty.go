package preview

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"strings"
)

const kittyChunkSize = 4096

// Approximate pixel size of one terminal cell, used when rasterising for Kitty.
const cellPxW, cellPxH = 9, 18

// KittySupported reports whether the terminal likely speaks the Kitty
// Graphics Protocol (Kitty itself, or Ghostty).
//
// Inside tmux it answers no even when the outer terminal does speak it: tmux
// swallows the escape sequences unless allow-passthrough is turned on, and a
// swallowed image is an empty panel with no hint as to why. Block art is drawn
// out of ordinary cells and always survives the trip. Where passthrough *is*
// configured, list_preview = "kitty" asks for the protocol anyway.
func KittySupported() bool {
	if os.Getenv("TMUX") != "" {
		return false
	}
	if os.Getenv("KITTY_WINDOW_ID") != "" || os.Getenv("GHOSTTY_RESOURCES_DIR") != "" {
		return true
	}
	term := strings.ToLower(os.Getenv("TERM") + " " + os.Getenv("TERM_PROGRAM"))
	return strings.Contains(term, "kitty") || strings.Contains(term, "ghostty")
}

// KittyClear deletes all images currently shown by the terminal.
func KittyClear() {
	fmt.Fprint(os.Stdout, "\033_Ga=d,d=A\033\\")
}

// KittyRender writes img to the terminal using the Kitty Graphics Protocol.
// The image is scaled to fit within cols×rows cells and rendered at the current
// cursor position. Output goes directly to os.Stdout, bypassing bubbletea's renderer.
func KittyRender(img image.Image, cols, rows int) error {
	return kittyRenderTo(os.Stdout, img, cols, rows, -1, -1)
}

// KittyRenderAtCell renders img sized cols×rows cells with its top-left corner
// at the 1-indexed terminal cell (row, col). The cursor position is restored.
func KittyRenderAtCell(img image.Image, cols, rows, row, col int) error {
	fmt.Fprintf(os.Stdout, "\0337\033[%d;%dH", row, col) // save cursor, move
	err := kittyRenderTo(os.Stdout, img, cols, rows, -1, -1)
	fmt.Fprint(os.Stdout, "\0338") // restore cursor
	return err
}

func kittyRenderTo(w io.Writer, img image.Image, cols, rows, col, row int) error {
	scaled := scaleImage(img, cols*cellPxW, rows*cellPxH)

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
