package ui

import (
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"github.com/fatih/color"
)

var (
	Green  = color.New(color.FgGreen)
	Red    = color.New(color.FgRed)
	Yellow = color.New(color.FgYellow)
	Bold   = color.New(color.Bold)
	Dim    = color.New(color.Faint)
)

// DeviceRow is one line in the device status table.
type DeviceRow struct {
	Name      string
	Available bool
	FreeBytes int64 // -1 if not applicable or unavailable
}

func PrintDeviceTable(rows []DeviceRow) {
	fmt.Println()
	Bold.Println("  Devices")
	fmt.Println("  " + strings.Repeat("─", 52))
	for _, r := range rows {
		mark := Green.Sprint("✅")
		if !r.Available {
			mark = Red.Sprint("❌")
		}
		free := ""
		if r.FreeBytes >= 0 {
			free = "  " + Dim.Sprint(FormatBytes(r.FreeBytes)+" free")
		}
		fmt.Printf("  %s  %-30s%s\n", mark, r.Name, free)
	}
	fmt.Println()
}

// SpaceInfo holds how much needs to be copied and how much is free on a destination.
type SpaceInfo struct {
	Avail bool
	// Bypassed marks a destination that takes no part in this run — the local
	// SSD when direct_to_nas is set. Reported instead of "not available", which
	// would read as a problem.
	Bypassed  bool
	ToBytes   int64
	FreeBytes int64 // -1 if free space could not be determined
}

func PrintSummary(totalCamera int, cameraBytes int64, missingFromSSD, missingFromNAS int, ssd, nas SpaceInfo, nasAvail bool) {
	Bold.Println("  Summary")
	fmt.Println("  " + strings.Repeat("─", 52))
	fmt.Printf("  Source files found :  %d  (%s)\n", totalCamera, FormatBytes(cameraBytes))

	printDestLine("SSD", missingFromSSD, ssd)

	if nasAvail {
		printDestLine("NAS", missingFromNAS, nas)
	} else {
		Dim.Println("  NAS               :  not available")
	}
	fmt.Println()
}

func printDestLine(label string, missing int, space SpaceInfo) {
	if space.Bypassed {
		Dim.Printf("  %-18s :  bypassed — copying straight to NAS\n", label)
		return
	}
	if !space.Avail {
		Red.Printf("  %-18s :  not available\n", label)
		return
	}

	if missing == 0 {
		Green.Printf("  Missing from %-4s :  0\n", label)
		return
	}

	sizeStr := FormatBytes(space.ToBytes)

	if space.FreeBytes < 0 {
		Yellow.Printf("  Missing from %-4s :  %d  (%s to copy)\n", label, missing, sizeStr)
		return
	}

	if space.ToBytes > space.FreeBytes {
		Red.Printf("  Missing from %-4s :  %d  (%s to copy — only %s free ⚠️)\n",
			label, missing, sizeStr, FormatBytes(space.FreeBytes))
	} else {
		Yellow.Printf("  Missing from %-4s :  %d  (%s to copy, %s free)\n",
			label, missing, sizeStr, FormatBytes(space.FreeBytes))
	}
}

// PrintSeparator prints a full-width rule with a blank line on each side.
func PrintSeparator() {
	fmt.Println("\n" + strings.Repeat("═", 60) + "\n")
}

// Prompt prints msg and waits for the user to press Enter.
func Prompt(msg string) {
	fmt.Print(msg)
	buf := make([]byte, 1)
	for {
		_, err := os.Stdin.Read(buf)
		if err != nil || buf[0] == '\n' {
			break
		}
	}
}

// AskYesNo prints question and returns true if the user types 'y' or 'Y'.
func AskYesNo(question string) bool {
	fmt.Print(question)
	var answer string
	fmt.Scanln(&answer)
	return strings.ToLower(strings.TrimSpace(answer)) == "y"
}

// ProgressWriter implements io.Writer and renders an inline progress line.
type ProgressWriter struct {
	Total     int64
	written   int64
	startTime time.Time
	label     string
	out       io.Writer
}

func NewProgressWriter(label string, total int64, out io.Writer) *ProgressWriter {
	return &ProgressWriter{
		Total:     total,
		startTime: time.Now(),
		label:     label,
		out:       out,
	}
}

func (pw *ProgressWriter) Write(p []byte) (int, error) {
	n := len(p)
	pw.written += int64(n)
	pw.render()
	return n, nil
}

func (pw *ProgressWriter) render() {
	elapsed := time.Since(pw.startTime).Seconds()
	speed := float64(0)
	if elapsed > 0 {
		speed = float64(pw.written) / elapsed
	}

	pct := float64(0)
	if pw.Total > 0 {
		pct = float64(pw.written) / float64(pw.Total) * 100
	}

	const barWidth = 20
	filled := int(math.Round(float64(barWidth) * pct / 100))
	if filled > barWidth {
		filled = barWidth
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)

	label := pw.label
	if runes := []rune(label); len(runes) > 28 {
		label = "…" + string(runes[len(runes)-27:])
	}

	fmt.Fprintf(pw.out, "\r  %-28s  %8s  %9s/s  [%s]  %5.1f%%",
		label,
		FormatBytes(pw.written),
		FormatBytes(int64(speed)),
		bar,
		pct,
	)
}

// Done finalises the progress line with a newline.
func (pw *ProgressWriter) Done() {
	pw.render()
	fmt.Fprintln(pw.out)
}

// FormatBytes converts a byte count to a human-readable string (1024-based).
func FormatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
