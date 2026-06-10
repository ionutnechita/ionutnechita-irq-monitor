package ui

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Terminal control strings exported for the live driver in main.
func HideCursor() { fmt.Print(hideCursor) }
func ShowCursor() { fmt.Print(showCursor) }

// ClearAndHome moves the cursor home and clears below it. Using home+clear
// instead of a full wipe avoids the flicker of erasing the whole screen.
func ClearAndHome() { fmt.Print(cursorHome + clearToEnd) }

// FullClear erases the screen and scrollback (used once at live start).
func FullClear() { fmt.Print(clearScreen) }

// LiveLoop renders frames produced by next() every interval until SIGINT/SIGTERM.
// next returns the full frame text for the current sample. The terminal cursor
// is hidden during the loop and restored on exit.
func LiveLoop(interval time.Duration, next func() (string, error)) error {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)

	HideCursor()
	FullClear()
	restore := func() { ShowCursor() }
	defer restore()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// First frame immediately.
	if err := renderOnce(next); err != nil {
		return err
	}
	for {
		select {
		case <-sig:
			return nil
		case <-ticker.C:
			if err := renderOnce(next); err != nil {
				return err
			}
		}
	}
}

func renderOnce(next func() (string, error)) error {
	frame, err := next()
	if err != nil {
		return err
	}
	ClearAndHome()
	fmt.Print(frame)
	fmt.Print(dim + "\n(press Ctrl-C to quit)" + reset + "\n")
	return nil
}
