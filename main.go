// Command irq-monitor is a live, dependency-free interrupt monitor for Linux.
// It parses /proc/interrupts and the per-IRQ affinity files and presents the
// data three ways: per category (driver/device), per CPU core, and per IRQ —
// annotated with isolation context (isolcpus / nohz_full / rcu_nocbs) so it is
// useful for tuning and debugging IRQ placement on RT / NUMA / network servers.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/IonutNechita/irq-monitor/internal/cpuset"
	"github.com/IonutNechita/irq-monitor/internal/model"
	"github.com/IonutNechita/irq-monitor/internal/sysinfo"
	"github.com/IonutNechita/irq-monitor/internal/ui"
)

func main() {
	var (
		live       = flag.Bool("live", false, "continuously refresh until Ctrl-C")
		interval   = flag.Duration("interval", time.Second, "sample interval (set 0 for an instant snapshot with no rates)")
		view       = flag.String("view", "all", "view: all | category | core | irq | ipi | isolation")
		filterStr  = flag.String("filter", "", "comma-separated categories to include (e.g. ice,i40e,nvme)")
		cpuStr     = flag.String("cpu", "", "restrict to CPUs (list form, e.g. 12-14,0)")
		top        = flag.Int("top", 40, "limit rows in the irq / isolation views (0 = all)")
		noZero     = flag.Bool("no-zero", false, "hide rows with zero interrupts")
		noTrunc    = flag.Bool("no-trunc", false, "do not truncate long columns (e.g. EFFECTIVE CPUS, ACTIONS)")
		noAffinity = flag.Bool("no-affinity", false, "skip reading per-IRQ affinity files (faster on huge IRQ counts)")
		noColor    = flag.Bool("no-color", false, "disable ANSI colors")
		jsonOut    = flag.Bool("json", false, "emit JSON instead of a table")
		width      = flag.Int("width", 0, "output width for bars (0 = auto/120)")
		columns    = flag.Int("columns", 0, "for --view core: lay out the per-CPU table in N columns (0 = auto fit, 1 = single)")
		from       = flag.String("from", "", "analyse a saved /proc/interrupts capture from a file instead of the live host (one-shot, no rates, no local sysfs)")
	)
	flag.Usage = usage
	flag.Parse()

	if !validView(*view) {
		fmt.Fprintf(os.Stderr, "irq-monitor: invalid --view %q (want all|category|core|irq|ipi|isolation)\n", *view)
		os.Exit(2)
	}

	opt := ui.Options{
		View:      *view,
		Filter:    parseFilter(*filterStr),
		CPUFilter: cpuset.ParseList(*cpuStr),
		Top:       *top,
		NoZero:    *noZero,
		NoTrunc:   *noTrunc,
		Color:     !*noColor && isTTY(os.Stdout),
		Width:     resolveWidth(*width),
		Height:    ttyHeight(),
		Columns:   *columns,
	}

	// --from analyses a static capture from another host: one snapshot, no
	// rates, and no local sysfs (topology/isolation/driver names don't apply).
	if *from != "" {
		runFromFile(*from, *live, *jsonOut, opt)
		return
	}

	info := sysinfo.Collect()
	withAff := !*noAffinity

	// A frame needs two samples to show rates. When interval > 0 we always take
	// a leading sample, wait, then take the current one. interval==0 means a
	// single instantaneous read with no rates.
	var prev model.Sample
	makeView := func() (model.View, error) {
		cur, err := model.Take(withAff)
		if err != nil {
			return model.View{}, err
		}
		v := model.Build(info, prev, cur)
		prev = cur
		return v, nil
	}

	renderer := ui.New(info, opt)

	if *live {
		if *interval <= 0 {
			*interval = time.Second
		}
		err := ui.LiveLoop(*interval, func() (string, error) {
			v, err := makeView()
			if err != nil {
				return "", err
			}
			if *width <= 0 {
				renderer.SetWidth(resolveWidth(0)) // follow terminal resizes
			}
			renderer.SetHeight(ttyHeight()) // re-evaluate auto-columns on resize
			if *jsonOut {
				b, err := ui.JSON(info, v, *view == "irq" || *view == "all")
				return string(b) + "\n", err
			}
			return renderer.Frame(v), nil
		})
		if err != nil {
			fatal(err)
		}
		return
	}

	// One-shot: take a priming sample and a second one after the interval so
	// rates are available (unless interval == 0).
	if *interval > 0 {
		if _, err := makeView(); err != nil {
			fatal(err)
		}
		time.Sleep(*interval)
	}
	v, err := makeView()
	if err != nil {
		fatal(err)
	}
	if *jsonOut {
		b, err := ui.JSON(info, v, *view == "irq" || *view == "all")
		if err != nil {
			fatal(err)
		}
		fmt.Println(string(b))
		return
	}
	fmt.Print(renderer.Frame(v))
}

// runFromFile renders a single view from a saved /proc/interrupts capture. The
// capture is one point in time, so there are no rates; topology and isolation
// are unknown for a remote host, so a neutral Info sized to the capture's CPU
// columns is used. opt.View == "ipi"/"core" still work; rate columns stay blank.
func runFromFile(path string, live, jsonOut bool, opt ui.Options) {
	if live {
		fmt.Fprintln(os.Stderr, "irq-monitor: --from is a static capture; --live ignored")
	}
	cur, err := model.TakeFile(path)
	if err != nil {
		fatal(err)
	}
	info := sysinfo.Offline(cur.Snap.CPUList, "(capture "+filepath.Base(path)+")")
	v := model.Build(info, model.Sample{}, cur)
	if jsonOut {
		b, err := ui.JSON(info, v, opt.View == "irq" || opt.View == "all")
		if err != nil {
			fatal(err)
		}
		fmt.Println(string(b))
		return
	}
	fmt.Print(ui.New(info, opt).Frame(v))
}

func validView(v string) bool {
	switch v {
	case "all", "category", "core", "irq", "ipi", "isolation", "isol":
		return true
	}
	return false
}

func parseFilter(s string) map[string]bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	m := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			m[strings.ToLower(p)] = true
		}
	}
	return m
}

func resolveWidth(w int) int {
	if w > 0 {
		return w
	}
	if c := ttyWidth(); c > 0 {
		return c
	}
	if c := os.Getenv("COLUMNS"); c != "" {
		var n int
		if _, err := fmt.Sscanf(c, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return 120
}

// ttyWidth returns the terminal column count via the TIOCGWINSZ ioctl, or 0
// when stdout is not a terminal. Linux-only; the whole tool is Linux-only.
func ttyWidth() int {
	var ws struct{ Row, Col, X, Y uint16 }
	const tiocgwinsz = 0x5413 // Linux, all common arches
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdout.Fd(),
		uintptr(tiocgwinsz), uintptr(unsafe.Pointer(&ws)))
	if errno == 0 && ws.Col > 0 {
		return int(ws.Col)
	}
	return 0
}

// ttyHeight returns the terminal row count via TIOCGWINSZ, or 0 if not a TTY.
func ttyHeight() int {
	var ws struct{ Row, Col, X, Y uint16 }
	const tiocgwinsz = 0x5413
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdout.Fd(),
		uintptr(tiocgwinsz), uintptr(unsafe.Pointer(&ws)))
	if errno == 0 && ws.Row > 0 {
		return int(ws.Row)
	}
	return 0
}

func isTTY(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "irq-monitor:", err)
	os.Exit(1)
}

func usage() {
	fmt.Fprint(os.Stderr, `irq-monitor — live Linux interrupt monitor (per CPU, per driver, with isolation)

Usage:
  irq-monitor [flags]

Examples:
  irq-monitor                      one-shot, all views, 1s rate sample
  irq-monitor --live               continuous dashboard
  irq-monitor --view category      aggregate by driver (ice, i40e, nvme, mlx5_core)
  irq-monitor --view core --no-zero    per-CPU load, skip idle CPUs
  irq-monitor --view core --columns 3  fold a long CPU list into 3 columns
  irq-monitor --view irq --filter ice --top 50   busiest ice vectors
  irq-monitor --cpu 12-14 --view irq   what still fires on isolated cores
  irq-monitor --view ipi               per-CPU timer/IPI matrix (LOC/RES/CAL/TLB)
  irq-monitor --view ipi --cpu 12-14   check isolation health on isolated cores
  irq-monitor --view isolation         which IRQs leak onto isolcpus (+ fix commands)
  irq-monitor --json --interval 2s     machine-readable, 2s rate window
  irq-monitor --from irq.txt --view ipi   analyse a saved capture from another host

Flags:
`)
	flag.PrintDefaults()
}
