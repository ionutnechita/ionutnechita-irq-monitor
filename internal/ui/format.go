package ui

import (
	"strconv"
	"strings"
)

// ANSI color helpers. All are no-ops when the renderer has color disabled;
// callers go through (*Renderer).c.
const (
	reset   = "\x1b[0m"
	bold    = "\x1b[1m"
	dim     = "\x1b[2m"
	red     = "\x1b[31m"
	green   = "\x1b[32m"
	yellow  = "\x1b[33m"
	blue    = "\x1b[34m"
	magenta = "\x1b[35m"
	cyan    = "\x1b[36m"
	gray    = "\x1b[90m"

	clearScreen = "\x1b[H\x1b[2J\x1b[3J"
	hideCursor  = "\x1b[?25l"
	showCursor  = "\x1b[?25h"
	cursorHome  = "\x1b[H"
	clearToEnd  = "\x1b[0J"
)

// commaUint formats an unsigned integer with thousands separators.
func commaUint(n uint64) string {
	s := strconv.FormatUint(n, 10)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// humanShort formats a count compactly for dense tables: 6.5M, 980k, 138.
func humanShort(n uint64) string {
	f := float64(n)
	switch {
	case n >= 1e6:
		return strconv.FormatFloat(f/1e6, 'f', 1, 64) + "M"
	case n >= 1e3:
		return strconv.FormatFloat(f/1e3, 'f', 1, 64) + "k"
	default:
		return strconv.FormatUint(n, 10)
	}
}

// rate formats interrupts/sec compactly: 1.2M, 34.5k, 980, 0.
func rate(r float64) string {
	switch {
	case r <= 0:
		return "0"
	case r >= 1e6:
		return strconv.FormatFloat(r/1e6, 'f', 2, 64) + "M"
	case r >= 1e3:
		return strconv.FormatFloat(r/1e3, 'f', 1, 64) + "k"
	case r >= 100:
		return strconv.FormatFloat(r, 'f', 0, 64)
	default:
		return strconv.FormatFloat(r, 'f', 1, 64)
	}
}

// padRight left-justifies s into width w (no truncation shorter than s).
func padRight(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len(s))
}

// padLeft right-justifies s into width w.
func padLeft(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return strings.Repeat(" ", w-len(s)) + s
}

// trunc clamps s to at most w runes, adding an ellipsis when cut.
func trunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if len(s) <= w {
		return s
	}
	if w <= 1 {
		return s[:w]
	}
	return s[:w-1] + "…"
}

// bar renders a proportional bar of the given cell width using block glyphs.
func bar(value, max float64, width int) string {
	if width <= 0 {
		return ""
	}
	if max <= 0 || value <= 0 {
		return strings.Repeat(" ", width)
	}
	frac := value / max
	if frac > 1 {
		frac = 1
	}
	full := int(frac * float64(width))
	rem := frac*float64(width) - float64(full)
	parts := []rune("▏▎▍▌▋▊▉█")
	var b strings.Builder
	for i := 0; i < full && i < width; i++ {
		b.WriteRune('█')
	}
	if full < width && rem > 0 {
		idx := int(rem * 8)
		if idx > 7 {
			idx = 7
		}
		b.WriteRune(parts[idx])
		full++
	}
	for i := full; i < width; i++ {
		b.WriteByte(' ')
	}
	return b.String()
}
