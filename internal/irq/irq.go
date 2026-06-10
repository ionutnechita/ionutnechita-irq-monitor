// Package irq parses /proc/interrupts and the per-IRQ sysfs/procfs affinity
// files into a structured, sampleable model.
package irq

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/IonutNechita/irq-monitor/internal/cpuset"
)

// IRQ is one row of /proc/interrupts together with its affinity metadata.
type IRQ struct {
	Num       int    // numeric IRQ number, or -1 for special rows (NMI, LOC, ...)
	Name      string // "61" for numeric, or the label ("NMI") for special rows
	Special   bool   // true for the non-numeric architectural counters
	Chip      string // controller, e.g. "IR-PCI-MSIX-0000:03:00.0"
	HWIRQ     string // hwirq + trigger, e.g. "1-edge"
	Actions   []string
	Category  string
	Counts    []uint64 // per-column counts, aligned with Snapshot.CPUList
	Total     uint64
	SMPAff    cpuset.Set // /proc/irq/N/smp_affinity_list (requested)
	EffAff    cpuset.Set // /proc/irq/N/effective_affinity_list (actual delivery)
	Node      int        // /proc/irq/N/node, or -1
	affLoaded bool
}

// ActionStr renders the action list as a comma-joined string.
func (q *IRQ) ActionStr() string { return strings.Join(q.Actions, ", ") }

// Snapshot is a single read of the interrupt subsystem at a point in time.
type Snapshot struct {
	CPUList []int // CPU number for each count column, in order
	IRQs    []*IRQ
	byKey   map[string]*IRQ
}

// Lookup returns the IRQ with the given key (numeric string or special label).
func (s *Snapshot) Lookup(key string) *IRQ { return s.byKey[key] }

// PerCPUTotal sums every IRQ count for each column, returning a slice aligned
// with CPUList.
func (s *Snapshot) PerCPUTotal() []uint64 {
	out := make([]uint64, len(s.CPUList))
	for _, q := range s.IRQs {
		for i, c := range q.Counts {
			if i < len(out) {
				out[i] += c
			}
		}
	}
	return out
}

// Read parses the live /proc/interrupts. When withAffinity is true it also
// reads the per-IRQ affinity files (skippable for speed on hosts with thousands
// of IRQs). PCI driver names are resolved from the local sysfs.
func Read(withAffinity bool) (*Snapshot, error) {
	f, err := os.Open("/proc/interrupts")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parse(f, readOpts{affinity: withAffinity, sysfs: true})
}

// ReadFile parses a /proc/interrupts capture saved from another host. It never
// touches local sysfs — no per-IRQ affinity and no PCI driver lookup, since
// those would describe the wrong machine — so categories come purely from the
// capture's chip/action text. A leading shell-command line (e.g. an echoed
// "cat /proc/interrupts") before the CPU header is tolerated.
func ReadFile(path string) (*Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s, err := parse(f, readOpts{affinity: false, sysfs: false})
	if err != nil {
		return nil, err
	}
	if len(s.CPUList) == 0 {
		return nil, fmt.Errorf("%s: no CPU header found (not a /proc/interrupts capture?)", path)
	}
	return s, nil
}

// readOpts controls how a source is parsed. sysfs is true only for a live read
// of the local host; an offline capture sets both fields false.
type readOpts struct {
	affinity bool // read /proc/irq/N/* affinity files
	sysfs    bool // resolve PCI driver names via local sysfs
}

// parse reads interrupt data from r. It skips any leading lines until the
// "CPU0 CPU1 ..." header, then parses one IRQ per remaining line.
func parse(r io.Reader, opt readOpts) (*Snapshot, error) {
	s := &Snapshot{byKey: map[string]*IRQ{}}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for {
		if !sc.Scan() {
			return s, sc.Err()
		}
		if cpus := parseHeader(sc.Text()); len(cpus) > 0 {
			s.CPUList = cpus
			break
		}
	}
	nCPU := len(s.CPUList)

	for sc.Scan() {
		q := parseLine(sc.Text(), nCPU, opt.sysfs)
		if q == nil {
			continue
		}
		if opt.affinity && !q.Special {
			loadAffinity(q)
		} else {
			q.SMPAff = cpuset.New()
			q.EffAff = cpuset.New()
			q.Node = -1
		}
		s.IRQs = append(s.IRQs, q)
		s.byKey[q.Name] = q
	}
	return s, sc.Err()
}

// parseHeader extracts CPU column numbers from the "CPU0 CPU1 ..." header.
func parseHeader(line string) []int {
	var cpus []int
	for _, tok := range strings.Fields(line) {
		if n, ok := strings.CutPrefix(tok, "CPU"); ok {
			if v, err := strconv.Atoi(n); err == nil {
				cpus = append(cpus, v)
			}
		}
	}
	return cpus
}

// parseLine parses one data row. The leading label ends at the first ':'.
// Following that, consecutive integer tokens (capped at nCPU) are per-CPU
// counts; the remainder is the chip / hwirq / action description.
func parseLine(line string, nCPU int, sysfs bool) *IRQ {
	colon := strings.IndexByte(line, ':')
	if colon <= 0 {
		return nil
	}
	label := strings.TrimSpace(line[:colon])
	if label == "" {
		return nil
	}
	fields := strings.Fields(line[colon+1:])

	q := &IRQ{Num: -1, Name: label, Node: -1}
	if n, err := strconv.Atoi(label); err == nil {
		q.Num = n
	} else {
		q.Special = true
	}

	i := 0
	for i < len(fields) && i < nCPU {
		v, err := strconv.ParseUint(fields[i], 10, 64)
		if err != nil {
			break
		}
		q.Counts = append(q.Counts, v)
		q.Total += v
		i++
	}
	rest := fields[i:]

	if q.Special {
		q.Chip = "kernel"
		q.Category = "kernel"
		if len(rest) > 0 {
			q.Actions = []string{strings.Join(rest, " ")}
		}
		return q
	}

	if len(rest) > 0 {
		q.Chip = rest[0]
	}
	if len(rest) > 1 {
		q.HWIRQ = rest[1]
	}
	if len(rest) > 2 {
		// Actions may be comma-separated within the remaining text.
		q.Actions = splitActions(strings.Join(rest[2:], " "))
	}
	q.Category = Categorize(q.Chip, q.Actions, sysfs)
	return q
}

func splitActions(s string) []string {
	var out []string
	for _, a := range strings.Split(s, ",") {
		a = strings.TrimSpace(a)
		if a != "" {
			out = append(out, a)
		}
	}
	return out
}

func loadAffinity(q *IRQ) {
	if q.Num < 0 {
		return
	}
	base := "/proc/irq/" + strconv.Itoa(q.Num) + "/"
	q.SMPAff = cpuset.ParseList(readTrim(base + "smp_affinity_list"))
	q.EffAff = cpuset.ParseList(readTrim(base + "effective_affinity_list"))
	if q.EffAff.Empty() {
		q.EffAff = q.SMPAff
	}
	if n, err := strconv.Atoi(readTrim(base + "node")); err == nil {
		q.Node = n
	}
	q.affLoaded = true
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
