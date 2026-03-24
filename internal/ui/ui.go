package ui

import (
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"
	"unicode/utf8"

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

// FileStatusRow is one line in the file comparison table.
type FileStatusRow struct {
	RelPath    string
	Size       int64
	Category   string // "photos" or "videos"
	OnSSD      bool
	OnNAS      bool
	NASApplies bool // false for photos (not copied to NAS)
}

func PrintFileTable(rows []FileStatusRow) {
	if len(rows) == 0 {
		fmt.Println("  (no camera files found)")
		return
	}
	Bold.Println("  Files on Camera")
	fmt.Printf("  %-45s  %7s  %8s  %5s  %5s\n", "Path", "Size", "Category", "SSD", "NAS")
	fmt.Println("  " + strings.Repeat("─", 75))
	for _, r := range rows {
		ssd := Red.Sprint("  ✗  ")
		if r.OnSSD {
			ssd = Green.Sprint("  ✓  ")
		}
		var nas string
		if !r.NASApplies {
			nas = Dim.Sprint("  —  ")
		} else if r.OnNAS {
			nas = Green.Sprint("  ✓  ")
		} else {
			nas = Red.Sprint("  ✗  ")
		}
		name := r.RelPath
		if utf8.RuneCountInString(name) > 45 {
			name = "…" + name[len(name)-44:]
		}
		fmt.Printf("  %-45s  %7s  %8s  %s  %s\n", name, FormatBytes(r.Size), r.Category, ssd, nas)
	}
	fmt.Println()
}

func PrintSummary(totalCamera, missingFromSSD, missingFromNAS int, nasAvail bool) {
	Bold.Println("  Summary")
	fmt.Println("  " + strings.Repeat("─", 40))
	fmt.Printf("  Camera files found :  %d\n", totalCamera)
	if missingFromSSD > 0 {
		Yellow.Printf("  Missing from SSD  :  %d\n", missingFromSSD)
	} else {
		Green.Printf("  Missing from SSD  :  0\n")
	}
	if nasAvail {
		if missingFromNAS > 0 {
			Yellow.Printf("  Missing from NAS  :  %d\n", missingFromNAS)
		} else {
			Green.Printf("  Missing from NAS  :  0\n")
		}
	} else {
		Dim.Println("  NAS               :  not available")
	}
	fmt.Println()
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
	if utf8.RuneCountInString(label) > 28 {
		label = "…" + label[len(label)-27:]
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
