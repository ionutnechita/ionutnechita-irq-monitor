// Package ui renders the aggregated model to a terminal frame (one-shot or live)
// using plain ANSI escapes — no external dependencies.
package ui

import (
	"sort"
	"strconv"
	"strings"

	"github.com/IonutNechita/irq-monitor/internal/cpuset"
	"github.com/IonutNechita/irq-monitor/internal/model"
	"github.com/IonutNechita/irq-monitor/internal/sysinfo"
)

// Options control what and how the renderer draws.
type Options struct {
	View      string          // "core", "category", "irq", or "all"
	Filter    map[string]bool // include only these categories (nil = all)
	CPUFilter cpuset.Set      // restrict core/irq rows to these CPUs (empty = all)
	Top       int             // limit IRQ rows (0 = all)
	NoZero    bool            // hide rows with zero activity
	NoTrunc   bool            // do not truncate long columns (e.g. EFFECTIVE CPUS, ACTIONS)
	Color     bool
	Width     int // terminal width for bar sizing
	Height    int // terminal height (rows) used by auto column layout (0 = unknown)
	Columns   int // for "core" view: lay out rows in N columns (0 = auto, 1 = single)
}

// Renderer draws frames for a given host and option set.
type Renderer struct {
	info *sysinfo.Info
	opt  Options
}

// New builds a Renderer.
func New(info *sysinfo.Info, opt Options) *Renderer {
	if opt.Width <= 0 {
		opt.Width = 120
	}
	return &Renderer{info: info, opt: opt}
}

// SetWidth updates the output width used for bars and the IPI matrix wrap.
func (r *Renderer) SetWidth(w int) {
	if w > 0 {
		r.opt.Width = w
	}
}

// SetHeight updates the terminal row count used to auto-fit the per-CPU
// table when --columns is 0. Pass 0 to clear (no auto).
func (r *Renderer) SetHeight(h int) {
	if h >= 0 {
		r.opt.Height = h
	}
}

// c conditionally wraps s in an ANSI color code.
func (r *Renderer) c(code, s string) string {
	if !r.opt.Color {
		return s
	}
	return code + s + reset
}

// catIncluded reports whether a category passes --filter. A filter term matches
// either the full category ("iavf-mbx") or its base driver, i.e. the part before
// the first "-" function suffix ("iavf", "ice", "nvme"), so the documented
// "--filter ice" selects ice-queue / ice-ctrl / ice-ctrl-txrx together.
func (r *Renderer) catIncluded(name string) bool {
	if r.opt.Filter == nil {
		return true
	}
	if r.opt.Filter[name] {
		return true
	}
	if base, _, ok := strings.Cut(name, "-"); ok {
		return r.opt.Filter[base]
	}
	return false
}

func (r *Renderer) cpuIncluded(cpu int) bool {
	return r.opt.CPUFilter.Empty() || r.opt.CPUFilter.Contains(cpu)
}

// Frame renders a complete view to a string.
func (r *Renderer) Frame(v model.View) string {
	var b strings.Builder
	r.header(&b, v)
	switch r.opt.View {
	case "core":
		r.cores(&b, v)
	case "category":
		r.categories(&b, v)
	case "irq":
		r.irqs(&b, v)
	case "ipi":
		r.ipis(&b, v)
	case "isolation", "isol":
		r.isolation(&b, v)
	default: // "all"
		r.categories(&b, v)
		b.WriteByte('\n')
		r.cores(&b, v)
	}
	return b.String()
}

func (r *Renderer) header(b *strings.Builder, v model.View) {
	in := r.info
	rt := ""
	if in.Realtime {
		rt = r.c(green+bold, " PREEMPT_RT")
	}
	b.WriteString(r.c(bold+cyan, "irq-monitor"))
	b.WriteString("  kernel ")
	b.WriteString(r.c(bold, in.Kernel))
	b.WriteString(rt)
	b.WriteString("   CPUs: ")
	b.WriteString(strconv.Itoa(in.OnlineCPUs.Len()))
	b.WriteString(" online   NUMA nodes: ")
	b.WriteString(strconv.Itoa(len(in.NodeCPUs)))
	b.WriteByte('\n')

	b.WriteString(r.c(gray, "Isolation "))
	b.WriteString("isolcpus=" + r.c(yellow, in.Isolcpus.String()))
	b.WriteString("  nohz_full=" + r.c(yellow, in.NohzFull.String()))
	b.WriteString("  rcu_nocbs=" + r.c(yellow, in.RcuNocbs.String()))
	b.WriteString("   Housekeeping=" + r.c(green, in.Housekeeping().String()))
	b.WriteByte('\n')

	b.WriteString(r.c(gray, "Default IRQ aff "))
	b.WriteString(in.DefaultAff.String())
	if !in.IrqAffinity.Empty() {
		b.WriteString("   cmdline irqaffinity=" + in.IrqAffinity.String())
	}
	b.WriteByte('\n')

	if in.Hybrid {
		b.WriteString(r.c(gray, "Hybrid "))
		b.WriteString("P-cores=" + r.c(cyan, in.PCores.String()))
		b.WriteString("  E-cores=" + r.c(yellow, in.ECores.String()))
		b.WriteByte('\n')
	}

	b.WriteString(r.c(gray, "Totals "))
	b.WriteString(strconv.Itoa(v.TotalIRQs) + " IRQs, ")
	b.WriteString(strconv.Itoa(len(v.Categories)) + " categories, ")
	b.WriteString(commaUint(v.GrandTotal) + " interrupts")
	if v.HasRate {
		b.WriteString("   sample " + strconv.FormatFloat(v.Interval.Seconds(), 'f', 2, 64) + "s")
		b.WriteString("   " + r.c(bold, rate(v.GrandRate)+"/s total"))
	}
	b.WriteString("\n")
}

func (r *Renderer) categories(b *strings.Builder, v model.View) {
	b.WriteString(r.c(bold+blue, "── Categories (by driver/device) ──\n"))
	hdr := padRight("CATEGORY", 18) + padLeft("IRQs", 6) + padLeft("TOTAL", 16) +
		padLeft("RATE/s", 11) + padLeft("HOTCPU", 8) + "  " + "EFFECTIVE CPUS"
	b.WriteString(r.c(gray, hdr) + "\n")

	var maxRate float64
	for _, c := range v.Categories {
		if c.Rate > maxRate {
			maxRate = c.Rate
		}
	}
	for _, c := range v.Categories {
		if !r.catIncluded(c.Name) {
			continue
		}
		if r.opt.NoZero && c.Total == 0 {
			continue
		}
		hot := "-"
		if c.HotCPU >= 0 {
			hot = strconv.Itoa(c.HotCPU)
		}
		// EFFECTIVE CPUS is the last column on the row; size it to fit the
		// remaining terminal width and only truncate if --no-trunc isn't set.
		effW := r.opt.Width - 61
		if effW < 40 {
			effW = 40
		}
		effStr := c.EffCPUs.String()
		if !r.opt.NoTrunc {
			effStr = trunc(effStr, effW)
		}
		line := padRight(trunc(c.Name, 18), 18) +
			padLeft(strconv.Itoa(c.NumIRQs), 6) +
			padLeft(commaUint(c.Total), 16) +
			padLeft(rate(c.Rate), 11) +
			padLeft(hot, 8) + "  " +
			effStr
		if v.HasRate && c.Rate >= maxRate && maxRate > 0 {
			line = r.c(bold+green, line)
		}
		b.WriteString(line + "\n")
	}
}

func (r *Renderer) cores(b *strings.Builder, v model.View) {
	b.WriteString(r.c(bold+blue, "── Per-CPU interrupt load ──"))
	b.WriteString(r.c(gray, "   flags: i=isolcpus n=nohz_full r=rcu_nocbs   TYPE: P/E core or ht=SMT   same CORE = SMT siblings\n"))

	cores := make([]model.CoreStat, 0, len(v.Cores))
	for _, cs := range v.Cores {
		if r.cpuIncluded(cs.CPU) {
			cores = append(cores, cs)
		}
	}

	cols := r.opt.Columns
	if cols < 0 {
		cols = 0
	}
	if cols > 8 {
		cols = 8
	}
	if cols == 0 {
		cols = r.autoCols(len(cores))
	}
	if cols > 1 {
		// Drop zero rows up front so the column layout has no gaps.
		if r.opt.NoZero {
			kept := cores[:0]
			for _, cs := range cores {
				if cs.Total != 0 {
					kept = append(kept, cs)
				}
			}
			cores = kept
		}
		r.coresMulti(b, cores, v, cols)
		return
	}
	r.coresSingle(b, cores, v)
}

func (r *Renderer) coresSingle(b *strings.Builder, cores []model.CoreStat, v model.View) {
	hdr := padLeft("CPU", 4) + "  " + padRight("ROLE", 5) + padRight("FLAGS", 6) +
		padRight("TYPE", 5) + padLeft("CORE", 5) + padLeft("NODE", 5) +
		padLeft("ACT", 5) + padLeft("AFF", 5) +
		padLeft("TOTAL", 15) + padLeft("RATE/s", 10) + "  " + "LOAD"
	b.WriteString(r.c(gray, hdr) + "\n")

	var maxRate, maxTotal float64
	for _, cs := range cores {
		if cs.Rate > maxRate {
			maxRate = cs.Rate
		}
		if float64(cs.Total) > maxTotal {
			maxTotal = float64(cs.Total)
		}
	}
	barW := r.opt.Width - 80
	if barW < 8 {
		barW = 8
	}
	if barW > 40 {
		barW = 40
	}

	for _, cs := range cores {
		if r.opt.NoZero && cs.Total == 0 {
			continue
		}
		role := cs.Role.String()
		roleCol := role
		if cs.Role == sysinfo.RoleIsolated {
			roleCol = r.c(magenta, role)
		}
		node := "-"
		if cs.Node >= 0 {
			node = strconv.Itoa(cs.Node)
		}
		typ := cs.Class // "P"/"E" on hybrid
		if typ == "" {
			if cs.SMT {
				typ = "ht"
			} else {
				typ = "-"
			}
		}
		core := "-"
		if cs.CoreID >= 0 {
			core = strconv.Itoa(cs.CoreID)
		}
		var barVal, barMax float64
		if v.HasRate {
			barVal, barMax = cs.Rate, maxRate
		} else {
			barVal, barMax = float64(cs.Total), maxTotal
		}
		barStr := bar(barVal, barMax, barW)
		barColored := barStr
		if r.opt.Color {
			col := green
			if barMax > 0 && barVal/barMax > 0.66 {
				col = red
			} else if barMax > 0 && barVal/barMax > 0.33 {
				col = yellow
			}
			barColored = col + barStr + reset
		}

		// Build with raw role first (for width), then swap in colored role.
		row := padLeft(strconv.Itoa(cs.CPU), 4) + "  " +
			padRight(role, 5) + padRight(cs.Flags, 6) +
			padRight(typ, 5) + padLeft(core, 5) +
			padLeft(node, 5) +
			padLeft(strconv.Itoa(cs.ActiveIRQs), 5) +
			padLeft(strconv.Itoa(cs.AffIRQs), 5) +
			padLeft(commaUint(cs.Total), 15) +
			padLeft(rate(cs.Rate), 10) + "  " +
			"│" + barColored + "│"
		if roleCol != role {
			row = strings.Replace(row, padRight(role, 5), padRight(roleCol, 5+len(roleCol)-len(role)), 1)
		}
		b.WriteString(row + "\n")
	}
}

// autoCols picks a column count for the per-CPU table that fits the terminal
// height (when known). Falls back to 1 if Height is 0. The chosen count is
// always wide enough to render the compact row format without wrapping.
func (r *Renderer) autoCols(nCPUs int) int {
	if nCPUs <= 0 {
		return 1
	}
	rows := r.opt.Height
	if rows <= 0 {
		return 1
	}
	// Reserve space for header / legend / column headers / status line.
	margin := 8
	if r.opt.View == "all" {
		// "all" view also renders the categories block above cores.
		// Budget ~3 + min(N, ~24) rows for it.
		margin += 28
	}
	avail := rows - margin
	if avail < 4 {
		avail = 4
	}
	cols := (nCPUs + avail - 1) / avail
	if cols < 1 {
		cols = 1
	}
	if cols > 6 {
		cols = 6
	}
	// Don't fold into more columns than the terminal can hold side by side.
	const fixedW, gap = 44, 3
	for cols > 1 && (r.opt.Width-gap*(cols-1))/cols < fixedW {
		cols--
	}
	return cols
}

// coresMulti renders the per-CPU table arranged in N columns, using a compact
// row format. Rows fill column-major (col 0 = first 1/N, col 1 = next 1/N…)
// so reading top-to-bottom in each column preserves CPU order.
func (r *Renderer) coresMulti(b *strings.Builder, cores []model.CoreStat, v model.View, cols int) {
	var maxRate, maxTotal float64
	for _, cs := range cores {
		if cs.Rate > maxRate {
			maxRate = cs.Rate
		}
		if float64(cs.Total) > maxTotal {
			maxTotal = float64(cs.Total)
		}
	}

	// Compact row visible width: 44 fixed cols + (4 + barW) when bar is shown.
	const fixedW = 44
	const gap = 3
	perCol := (r.opt.Width - gap*(cols-1)) / cols
	if perCol < fixedW {
		perCol = fixedW
	}
	barW := perCol - fixedW - 4 // 4 = "  │" + "│"
	switch {
	case barW < 4:
		barW = 0
	case barW > 24:
		barW = 24
	}
	rowW := fixedW
	if barW > 0 {
		rowW = fixedW + 4 + barW
	}

	// Header (one column) then repeat with `gap`-wide spacers.
	headerCol := padLeft("CPU", 4) + "  " +
		padRight("ROLE", 5) + padRight("FLG", 4) + padRight("TYP", 4) +
		padLeft("CORE", 5) + padLeft("ND", 4) +
		padLeft("TOTAL", 8) + padLeft("RATE/s", 8)
	if barW > 0 {
		headerCol += "  │" + padRight("LOAD", barW) + "│"
	}
	for i := 0; i < cols; i++ {
		if i > 0 {
			b.WriteString(strings.Repeat(" ", gap))
		}
		b.WriteString(r.c(gray, headerCol))
	}
	b.WriteByte('\n')

	n := len(cores)
	if n == 0 {
		return
	}
	rowsPerCol := (n + cols - 1) / cols
	for i := 0; i < rowsPerCol; i++ {
		for c := 0; c < cols; c++ {
			if c > 0 {
				b.WriteString(strings.Repeat(" ", gap))
			}
			idx := c*rowsPerCol + i
			if idx >= n {
				b.WriteString(strings.Repeat(" ", rowW))
				continue
			}
			r.writeCompactCoreCell(b, cores[idx], v, maxRate, maxTotal, barW)
		}
		b.WriteByte('\n')
	}
}

// writeCompactCoreCell writes one CPU row (no trailing newline) in the layout
// used by coresMulti. Total visible width matches the header cell.
func (r *Renderer) writeCompactCoreCell(b *strings.Builder, cs model.CoreStat, v model.View, maxRate, maxTotal float64, barW int) {
	role := cs.Role.String()
	typ := cs.Class
	if typ == "" {
		if cs.SMT {
			typ = "ht"
		} else {
			typ = "-"
		}
	}
	core := "-"
	if cs.CoreID >= 0 {
		core = strconv.Itoa(cs.CoreID)
	}
	node := "-"
	if cs.Node >= 0 {
		node = strconv.Itoa(cs.Node)
	}

	b.WriteString(padLeft(strconv.Itoa(cs.CPU), 4))
	b.WriteString("  ")

	// ROLE: visible width 5, optionally colored without inflating padding.
	if cs.Role == sysinfo.RoleIsolated && r.opt.Color {
		b.WriteString(magenta + role + reset)
	} else {
		b.WriteString(role)
	}
	b.WriteString(strings.Repeat(" ", 5-len(role)))

	b.WriteString(padRight(cs.Flags, 4))
	b.WriteString(padRight(typ, 4))
	b.WriteString(padLeft(core, 5))
	b.WriteString(padLeft(node, 4))
	b.WriteString(padLeft(humanShort(cs.Total), 8))
	b.WriteString(padLeft(rate(cs.Rate), 8))

	if barW > 0 {
		var barVal, barMax float64
		if v.HasRate {
			barVal, barMax = cs.Rate, maxRate
		} else {
			barVal, barMax = float64(cs.Total), maxTotal
		}
		barStr := bar(barVal, barMax, barW)
		if r.opt.Color {
			col := green
			if barMax > 0 && barVal/barMax > 0.66 {
				col = red
			} else if barMax > 0 && barVal/barMax > 0.33 {
				col = yellow
			}
			barStr = col + barStr + reset
		}
		b.WriteString("  │" + barStr + "│")
	}
}

func (r *Renderer) irqs(b *strings.Builder, v model.View) {
	b.WriteString(r.c(bold+blue, "── Per-IRQ detail ──\n"))

	type row struct {
		name, cat, eff, smp, actions string
		rate                         float64
		total                        uint64
	}
	rows := make([]row, 0, len(v.Cur.IRQs))
	for _, q := range v.Cur.IRQs {
		// Architectural counters (LOC/RES/CAL/TLB/NMI/...) have no affinity and
		// dominate by total; they have a dedicated --view ipi, so keep the
		// per-IRQ detail to real device IRQs.
		if q.Special {
			continue
		}
		if !r.catIncluded(q.Category) {
			continue
		}
		if r.opt.NoZero && q.Total == 0 {
			continue
		}
		if !r.opt.CPUFilter.Empty() {
			// Keep only IRQs whose effective affinity intersects the filter.
			hit := false
			for _, cpu := range q.EffAff.Slice() {
				if r.opt.CPUFilter.Contains(cpu) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		rows = append(rows, row{
			name: q.Name, cat: q.Category, eff: q.EffAff.String(),
			smp: q.SMPAff.String(), actions: q.ActionStr(),
			rate: v.IRQRate[q.Name], total: q.Total,
		})
	}
	sort.Slice(rows, func(a, c int) bool {
		if v.HasRate && rows[a].rate != rows[c].rate {
			return rows[a].rate > rows[c].rate
		}
		return rows[a].total > rows[c].total
	})
	limit := len(rows)
	if r.opt.Top > 0 && r.opt.Top < limit {
		limit = r.opt.Top
	}

	// EFF/SMP affinity masks can exceed the default column widths on many-CPU
	// hosts (e.g. a NUMA-split mask like "0-35,72-107"). Truncating them would
	// hide exactly the isolation detail this view exists for, so --no-trunc
	// widens these columns to fit the rows actually shown.
	effW, smpW := 8, 10
	if r.opt.NoTrunc {
		for i := 0; i < limit; i++ {
			if l := len(rows[i].eff); l > effW {
				effW = l
			}
			if l := len(rows[i].smp); l > smpW {
				smpW = l
			}
		}
	}
	hdr := padLeft("IRQ", 5) + "  " + padRight("CATEGORY", 12) +
		padRight("EFF", effW) + padRight("SMP_AFF", smpW) +
		padLeft("RATE/s", 10) + padLeft("TOTAL", 15) + "  ACTIONS"
	b.WriteString(r.c(gray, hdr) + "\n")

	for i := 0; i < limit; i++ {
		q := rows[i]
		actW := r.opt.Width - (46 + effW + smpW)
		if actW < 8 {
			actW = 8
		}
		rateStr := "-"
		if v.HasRate {
			rateStr = rate(q.rate)
		}
		actions := q.actions
		eff, smp := q.eff, q.smp
		if !r.opt.NoTrunc {
			actions = trunc(actions, actW)
			eff = trunc(q.eff, effW)
			smp = trunc(q.smp, smpW)
		}
		b.WriteString(padLeft(q.name, 5) + "  " +
			padRight(trunc(q.cat, 12), 12) +
			padRight(eff, effW) +
			padRight(smp, smpW) +
			padLeft(rateStr, 10) +
			padLeft(commaUint(q.total), 15) + "  " +
			actions + "\n")
	}
	if limit < len(rows) {
		b.WriteString(r.c(gray, "  … "+strconv.Itoa(len(rows)-limit)+" more (use --top to show)\n"))
	}
}

// ipis renders the architectural per-CPU counters (LOC/RES/CAL/TLB/NMI/...) as
// a matrix. Isolated CPUs are marked with '*' and colored, so it's immediately
// visible whether nohz_full/isolcpus are quiescing the timer tick and IPIs.
func (r *Renderer) ipis(b *strings.Builder, v model.View) {
	b.WriteString(r.c(bold+blue, "── Per-CPU kernel / IPI counters ──"))
	if v.HasRate {
		b.WriteString(r.c(gray, "   cells = interrupts/s\n"))
	} else {
		b.WriteString(r.c(gray, "   cells = lifetime totals\n"))
	}

	// Columns to display, honoring the --cpu filter.
	var cols []int
	for i, cpu := range v.CPUList {
		if r.cpuIncluded(cpu) {
			cols = append(cols, i)
		}
	}

	// Filter symbols once (respect --no-zero) so every wrapped block is consistent.
	var syms []model.IPIStat
	for _, ipi := range v.IPIs {
		if r.opt.NoZero && ipi.Total == 0 {
			continue
		}
		syms = append(syms, ipi)
	}

	const cell, label = 7, 24 // label = SYMBOL(7)+TOTAL(8)+RATE(8)+space(1)
	perRow := (r.opt.Width - label) / cell
	if perRow < 1 {
		perRow = 1
	}

	for start := 0; start < len(cols); start += perRow {
		end := start + perRow
		if end > len(cols) {
			end = len(cols)
		}
		chunk := cols[start:end]

		// Per-block CPU header.
		b.WriteString(r.c(gray, padRight("SYMBOL", 7)+padLeft("TOTAL", 8)+padLeft("RATE/s", 8)+" "))
		for _, i := range chunk {
			cpu := v.CPUList[i]
			lbl := "C" + strconv.Itoa(cpu)
			if r.info.Isolcpus.Contains(cpu) {
				b.WriteString(r.c(magenta+bold, padLeft(lbl+"*", cell)))
			} else {
				b.WriteString(r.c(gray, padLeft(lbl, cell)))
			}
		}
		b.WriteByte('\n')

		for _, ipi := range syms {
			b.WriteString(r.c(bold, padRight(ipi.Name, 7)) +
				padLeft(humanShort(ipi.Total), 8) +
				padLeft(rate(ipi.Rate), 8) + " ")
			for _, i := range chunk {
				var s string
				if v.HasRate {
					if i < len(ipi.PerCPURate) && ipi.PerCPURate[i] > 0 {
						s = rate(ipi.PerCPURate[i])
					} else {
						s = "."
					}
				} else if i < len(ipi.PerCPU) && ipi.PerCPU[i] > 0 {
					s = humanShort(ipi.PerCPU[i])
				} else {
					s = "."
				}
				padded := padLeft(s, cell)
				switch {
				case s == ".":
					b.WriteString(r.c(gray, padded))
				case r.info.Isolcpus.Contains(v.CPUList[i]):
					b.WriteString(r.c(magenta, padded))
				default:
					b.WriteString(padded)
				}
			}
			b.WriteByte('\n')
		}
		if end < len(cols) {
			b.WriteByte('\n')
		}
	}
	b.WriteString(r.c(gray, "* = isolated   LOC=local-timer RES=reschedule CAL=func-call-IPI TLB=shootdown NMI/PMI/IWI=nmi/perf/irq-work\n"))
}

// isolation renders the isolation check: device IRQs whose affinity or activity
// reaches an isolated CPU, plus the residual architectural counters landing
// there. It turns the manual "read AFF vs isolcpus" comparison into a verdict.
func (r *Renderer) isolation(b *strings.Builder, v model.View) {
	b.WriteString(r.c(bold+blue, "── Isolation check ──\n"))
	if v.Isolcpus.Empty() {
		b.WriteString(r.c(gray, "  no isolcpus= configured — no cores to fence off\n"))
		return
	}

	hk := r.info.Housekeeping()
	firing := 0
	for _, lk := range v.Leaks {
		if lk.Firing {
			firing++
		}
	}

	// Summary verdict.
	switch {
	case len(v.Leaks) == 0:
		b.WriteString("  isolcpus=" + r.c(yellow, v.Isolcpus.String()) +
			r.c(green, "  ✓ no device IRQ reaches the isolated cores\n"))
	default:
		msg := strconv.Itoa(len(v.Leaks)) + " IRQ(s) reach isolcpus=" + v.Isolcpus.String()
		if firing > 0 {
			msg += " — " + strconv.Itoa(firing) + " firing now"
		}
		col := yellow
		if firing > 0 {
			col = red
		}
		b.WriteString("  " + r.c(bold+col, msg) + "\n")
	}

	if len(v.Leaks) > 0 {
		// Size mask columns to content (capped unless --no-trunc).
		colW := func(min, cap int, get func(model.IsolationLeak) string) int {
			w := min
			for _, lk := range v.Leaks {
				if l := len(get(lk)); l > w {
					w = l
				}
			}
			if !r.opt.NoTrunc && w > cap {
				w = cap
			}
			return w
		}
		onW := colW(8, 12, func(l model.IsolationLeak) string { return l.OnIsol.String() })
		effW := colW(6, 14, func(l model.IsolationLeak) string { return l.Eff.String() })
		smpW := colW(8, 14, func(l model.IsolationLeak) string { return l.SMP.String() })

		hdr := "  " + padLeft("IRQ", 5) + "  " + padRight("CATEGORY", 14) +
			padRight("ON-ISOL", onW) + padRight("EFF", effW) + padRight("SMP_AFF", smpW) +
			padLeft("ISOL/s", 9) + padLeft("ISOL-TOTAL", 13) + "  ACTIONS"
		b.WriteString(r.c(gray, hdr) + "\n")

		limit := len(v.Leaks)
		if r.opt.Top > 0 && r.opt.Top < limit {
			limit = r.opt.Top
		}
		for i := 0; i < limit; i++ {
			lk := v.Leaks[i]
			mark := " "
			markCol := red
			rateCol := func(s string) string { return s }
			switch {
			case lk.Firing:
				mark = "!"
				rateCol = func(s string) string { return r.c(red, s) }
			case lk.Managed:
				mark = "M" // kernel-managed/pinned: not fixable via smp_affinity
				markCol = cyan
			}
			rateStr := "-"
			if v.HasRate {
				rateStr = rate(lk.IsolRate)
			}
			fields := func(s string, w int) string {
				if !r.opt.NoTrunc {
					s = trunc(s, w)
				}
				return padRight(s, w)
			}
			b.WriteString(r.c(bold+markCol, mark) + " " + padLeft(lk.Name, 4) + "  " +
				padRight(trunc(lk.Category, 14), 14) +
				r.c(yellow, fields(lk.OnIsol.String(), onW)) +
				fields(lk.Eff.String(), effW) +
				fields(lk.SMP.String(), smpW) +
				rateCol(padLeft(rateStr, 9)) +
				padLeft(commaUint(lk.IsolTotal), 13) + "  " +
				trunc(lk.Actions, maxAct(r.opt)) + "\n")
		}
		if limit < len(v.Leaks) {
			b.WriteString(r.c(gray, "  … "+strconv.Itoa(len(v.Leaks)-limit)+" more (use --top to show)\n"))
		}
		b.WriteString(r.c(gray, "  ! = firing on isolated cores now   M = kernel-managed/pinned (can't be re-masked from userspace)\n"))

		// Split remediation: steerable IRQs take a userspace re-mask; managed
		// per-CPU IRQs (nvme/NIC queues) reject the write (-EIO) and need the
		// kernel-side isolcpus=managed_irq instead.
		var steerable, managed []model.IsolationLeak
		for i := 0; i < limit; i++ {
			lk := v.Leaks[i]
			if _, err := strconv.Atoi(lk.Name); err != nil {
				continue // special rows have no /proc/irq/N
			}
			if lk.Managed {
				managed = append(managed, lk)
			} else {
				steerable = append(steerable, lk)
			}
		}
		if !hk.Empty() && len(steerable) > 0 {
			b.WriteString(r.c(gray, "\n  Fix (steerable) — restrict requested affinity to housekeeping ("+hk.String()+"):\n"))
			for _, lk := range steerable {
				b.WriteString(r.c(gray, "    echo "+hk.String()+
					" > /proc/irq/"+lk.Name+"/smp_affinity_list\n"))
			}
			b.WriteString(r.c(gray, "  Or persistently at boot: irqaffinity="+hk.String()+"\n"))
		}
		if len(managed) > 0 {
			names := make([]string, len(managed))
			for i, lk := range managed {
				names[i] = lk.Name
			}
			b.WriteString(r.c(gray, "\n  Managed/pinned (IRQ "+strings.Join(names, ",")+
				") — per-CPU queues; a userspace re-mask is rejected (-EIO).\n"))
			b.WriteString(r.c(gray, "  Keep these off the isolated cores at boot: isolcpus=managed_irq,"+
				v.Isolcpus.String()+"\n"))
		}
	}

	// Architectural counters still hitting isolated cores (affinity can't move
	// these; nohz_full / rcu_nocbs are what quiesce them).
	if len(v.IsolIPIs) > 0 {
		b.WriteString(r.c(gray, "\n  Architectural counters still on isolated cores (need nohz_full/rcu_nocbs, not affinity):\n"))
		b.WriteString(r.c(gray, "  "+padRight("SYM", 6)+padLeft("ISOL/s", 9)+padLeft("ISOL-TOTAL", 13)+"  DESC\n"))
		for _, ii := range v.IsolIPIs {
			rateStr := "-"
			if v.HasRate {
				rateStr = rate(ii.IsolRate)
			}
			b.WriteString("  " + r.c(bold, padRight(ii.Name, 6)) +
				padLeft(rateStr, 9) + padLeft(commaUint(ii.IsolTotal), 13) +
				"  " + ii.Desc + "\n")
		}
	}
}

// maxAct returns the actions column budget for the isolation view.
func maxAct(opt Options) int {
	if opt.NoTrunc {
		return 1 << 30
	}
	w := opt.Width - 70
	if w < 8 {
		w = 8
	}
	return w
}
