// Package model aggregates raw IRQ snapshots into the per-category and
// per-core views the UI renders, and computes rates between two samples.
package model

import (
	"sort"
	"time"

	"github.com/IonutNechita/irq-monitor/internal/cpuset"
	"github.com/IonutNechita/irq-monitor/internal/irq"
	"github.com/IonutNechita/irq-monitor/internal/sysinfo"
)

// Sample wraps a snapshot with the wall-clock time it was taken.
type Sample struct {
	Time time.Time
	Snap *irq.Snapshot
}

// Take reads a snapshot and timestamps it.
func Take(withAffinity bool) (Sample, error) {
	s, err := irq.Read(withAffinity)
	return Sample{Time: time.Now(), Snap: s}, err
}

// TakeFile reads a snapshot from a saved /proc/interrupts capture instead of the
// live host. Used by the one-shot --from path; carries no rate information.
func TakeFile(path string) (Sample, error) {
	s, err := irq.ReadFile(path)
	return Sample{Time: time.Now(), Snap: s}, err
}

// CategoryStat aggregates all IRQs sharing a category (driver/device).
type CategoryStat struct {
	Name      string
	NumIRQs   int
	Total     uint64
	Delta     uint64
	Rate      float64    // interrupts/sec since previous sample
	EffCPUs   cpuset.Set // union of effective affinity across member IRQs
	HotCPU    int        // column index with most activity in this period (-1 none)
	HotCPURat float64
}

// CoreStat aggregates activity landing on one CPU.
type CoreStat struct {
	CPU        int
	Role       sysinfo.Role
	Flags      string
	Node       int
	CoreID     int    // physical core id (shared id = SMT siblings)
	Class      string // "P"/"E" on hybrid, else ""
	SMT        bool   // shares its physical core with sibling threads
	ActiveIRQs int    // IRQs with non-zero lifetime count on this CPU
	AffIRQs    int    // IRQs whose effective affinity targets this CPU
	Total      uint64
	Delta      uint64
	Rate       float64
}

// IPIStat is one architectural per-CPU counter row from the bottom of
// /proc/interrupts (LOC, RES, CAL, TLB, NMI, IWI, ...). These are the timer
// tick and inter-processor interrupts that reveal whether CPU isolation and
// nohz_full are actually quiescing a core.
type IPIStat struct {
	Name       string    // symbol, e.g. "LOC"
	Desc       string    // human description, e.g. "Local timer interrupts"
	PerCPU     []uint64  // lifetime counts, aligned with View.CPUList
	PerCPURate []float64 // per-CPU interrupts/sec for this period
	Total      uint64
	Rate       float64
}

// IsolationLeak is a steerable IRQ whose effective affinity reaches an isolated
// CPU, or that has actually fired on one. It is the core finding of the
// isolation view: on an RT/NUMA host, device interrupts should be fenced off the
// isolcpus set, and these rows are the ones that are not.
type IsolationLeak struct {
	Name      string     // IRQ number/label
	Category  string     // driver/device category
	Actions   string     // joined action names
	Eff       cpuset.Set // effective_affinity_list (where the kernel delivers)
	SMP       cpuset.Set // smp_affinity_list (requested)
	OnIsol    cpuset.Set // isolated CPUs the effective affinity targets
	IsolTotal uint64     // lifetime interrupts that landed on isolated CPUs
	IsolDelta uint64     // those interrupts since the previous sample
	IsolRate  float64    // interrupts/sec on isolated CPUs this period
	Firing    bool       // currently delivering interrupts on an isolated CPU
	Managed   bool       // requested mask is confined to isolated CPUs — almost
	// always a kernel-managed per-CPU IRQ (nvme/NIC queue) that rejects a
	// userspace smp_affinity write (-EIO); fix only via isolcpus=managed_irq.
}

// View is the fully aggregated, render-ready picture for one frame.
type View struct {
	Interval   time.Duration
	HasRate    bool
	TotalIRQs  int
	GrandTotal uint64
	GrandRate  float64
	Categories []CategoryStat
	Cores      []CoreStat
	CPUList    []int
	Cur        *irq.Snapshot
	IRQRate    map[string]float64 // per-IRQ interrupts/sec, keyed by IRQ.Name
	IPIs       []IPIStat          // architectural per-CPU counters (LOC/RES/CAL/...)
	Isolcpus   cpuset.Set         // isolated CPUs (from isolcpus=), for the isolation view
	Leaks      []IsolationLeak    // IRQs reaching isolated CPUs, worst first
	IsolIPIs   []IsolationIPI     // residual IPI/timer activity on isolated CPUs
}

// IsolationIPI summarises how much of an architectural counter (LOC/RES/CAL/...)
// still lands on the isolated CPUs — the part isolation cannot steer away, only
// nohz_full/rcu_nocbs can quiesce.
type IsolationIPI struct {
	Name      string
	Desc      string
	IsolTotal uint64
	IsolDelta uint64
	IsolRate  float64
}

// Build aggregates cur (and, if non-nil, the delta against prev) using the
// static system info for roles/flags/node annotations.
func Build(info *sysinfo.Info, prev, cur Sample) View {
	snap := cur.Snap
	v := View{
		Cur:       snap,
		CPUList:   snap.CPUList,
		TotalIRQs: len(snap.IRQs),
		IRQRate:   map[string]float64{},
	}

	var dt float64
	if prev.Snap != nil {
		dt = cur.Time.Sub(prev.Time).Seconds()
		v.Interval = cur.Time.Sub(prev.Time)
		if dt > 0 {
			v.HasRate = true
		}
	}

	nCols := len(snap.CPUList)
	coreTotal := make([]uint64, nCols)
	coreDelta := make([]uint64, nCols)
	coreActive := make([]int, nCols)
	coreAff := make([]int, nCols)

	catIdx := map[string]int{}
	var cats []CategoryStat

	for _, q := range snap.IRQs {
		// Find matching previous IRQ for delta computation.
		var pq *irq.IRQ
		if prev.Snap != nil {
			pq = prev.Snap.Lookup(q.Name)
		}

		ci, ok := catIdx[q.Category]
		if !ok {
			ci = len(cats)
			catIdx[q.Category] = ci
			cats = append(cats, CategoryStat{Name: q.Category, EffCPUs: cpuset.New(), HotCPU: -1})
		}
		cs := &cats[ci]
		cs.NumIRQs++
		cs.Total += q.Total
		for _, c := range q.EffAff.Slice() {
			cs.EffCPUs.Add(c)
		}

		var catHotIdx = -1
		var catHotDelta uint64
		var irqDelta uint64
		for i := 0; i < len(q.Counts) && i < nCols; i++ {
			c := q.Counts[i]
			coreTotal[i] += c
			if c > 0 {
				coreActive[i]++
			}
			if q.EffAff.Contains(snap.CPUList[i]) {
				coreAff[i]++
			}
			if pq != nil && i < len(pq.Counts) && c >= pq.Counts[i] {
				d := c - pq.Counts[i]
				coreDelta[i] += d
				cs.Delta += d
				irqDelta += d
				if d > catHotDelta {
					catHotDelta = d
					catHotIdx = i
				}
			}
		}
		if dt > 0 {
			v.IRQRate[q.Name] = float64(irqDelta) / dt
		}

		if q.Special {
			ipi := IPIStat{
				Name:       q.Name,
				Total:      q.Total,
				PerCPU:     append([]uint64(nil), q.Counts...),
				PerCPURate: make([]float64, len(q.Counts)),
			}
			if len(q.Actions) > 0 {
				ipi.Desc = q.Actions[0]
			}
			if dt > 0 {
				ipi.Rate = float64(irqDelta) / dt
				for i := range q.Counts {
					if pq != nil && i < len(pq.Counts) && q.Counts[i] >= pq.Counts[i] {
						ipi.PerCPURate[i] = float64(q.Counts[i]-pq.Counts[i]) / dt
					}
				}
			}
			v.IPIs = append(v.IPIs, ipi)
		}
		if catHotIdx >= 0 && catHotDelta > 0 {
			cs.HotCPU = snap.CPUList[catHotIdx]
		}
		v.GrandTotal += q.Total
	}

	if dt > 0 {
		for i := range cats {
			cats[i].Rate = float64(cats[i].Delta) / dt
		}
	}
	sort.Slice(cats, func(a, b int) bool {
		if v.HasRate && cats[a].Rate != cats[b].Rate {
			return cats[a].Rate > cats[b].Rate
		}
		return cats[a].Total > cats[b].Total
	})
	v.Categories = cats

	for i, cpu := range snap.CPUList {
		cstat := CoreStat{
			CPU:        cpu,
			Role:       info.RoleOf(cpu),
			Flags:      info.Flags(cpu),
			Node:       info.NodeOf(cpu),
			CoreID:     info.CoreOf(cpu),
			Class:      info.CPUClass(cpu),
			SMT:        info.HasSMT(cpu),
			ActiveIRQs: coreActive[i],
			AffIRQs:    coreAff[i],
			Total:      coreTotal[i],
			Delta:      coreDelta[i],
		}
		if dt > 0 {
			cstat.Rate = float64(coreDelta[i]) / dt
			v.GrandRate += cstat.Rate
		}
		v.Cores = append(v.Cores, cstat)
	}

	buildIsolation(&v, info, prev, dt)
	return v
}

// buildIsolation populates the isolation view: device IRQs whose affinity or
// activity reaches an isolated CPU (Leaks), and the residual architectural
// counters still landing there (IsolIPIs). A host with no isolcpus has none.
func buildIsolation(v *View, info *sysinfo.Info, prev Sample, dt float64) {
	v.Isolcpus = info.Isolcpus
	if info.Isolcpus.Empty() {
		return
	}
	snap := v.Cur
	hk := info.Housekeeping()
	isolCol := make([]bool, len(snap.CPUList))
	for i, cpu := range snap.CPUList {
		isolCol[i] = info.Isolcpus.Contains(cpu)
	}

	for _, q := range snap.IRQs {
		var pq *irq.IRQ
		if prev.Snap != nil {
			pq = prev.Snap.Lookup(q.Name)
		}
		var isolTotal, isolDelta uint64
		for i := 0; i < len(q.Counts) && i < len(isolCol); i++ {
			if !isolCol[i] {
				continue
			}
			isolTotal += q.Counts[i]
			if pq != nil && i < len(pq.Counts) && q.Counts[i] >= pq.Counts[i] {
				isolDelta += q.Counts[i] - pq.Counts[i]
			}
		}

		if q.Special {
			// LOC/RES/CAL/TLB etc. cannot be steered by affinity; report the
			// share still hitting isolated CPUs so nohz_full health is visible.
			if isolTotal == 0 && isolDelta == 0 {
				continue
			}
			ii := IsolationIPI{Name: q.Name, IsolTotal: isolTotal, IsolDelta: isolDelta}
			if len(q.Actions) > 0 {
				ii.Desc = q.Actions[0]
			}
			if dt > 0 {
				ii.IsolRate = float64(isolDelta) / dt
			}
			v.IsolIPIs = append(v.IsolIPIs, ii)
			continue
		}

		onIsol := q.EffAff.Intersect(info.Isolcpus)
		// Report if affinity reaches an isolated CPU (steering problem) or it is
		// actively firing there now. Purely historical firing on a now-clean
		// mask is treated as remediated and omitted to keep the list actionable.
		if onIsol.Empty() && isolDelta == 0 {
			continue
		}
		leak := IsolationLeak{
			Name:      q.Name,
			Category:  q.Category,
			Actions:   q.ActionStr(),
			Eff:       q.EffAff,
			SMP:       q.SMPAff,
			OnIsol:    onIsol,
			IsolTotal: isolTotal,
			IsolDelta: isolDelta,
			Firing:    isolDelta > 0,
			Managed:   !q.SMPAff.Empty() && q.SMPAff.Intersect(hk).Empty(),
		}
		if dt > 0 {
			leak.IsolRate = float64(isolDelta) / dt
		}
		v.Leaks = append(v.Leaks, leak)
	}

	sort.Slice(v.Leaks, func(a, b int) bool {
		la, lb := v.Leaks[a], v.Leaks[b]
		if la.Firing != lb.Firing {
			return la.Firing // currently firing on isolated cores ranks first
		}
		if la.IsolRate != lb.IsolRate {
			return la.IsolRate > lb.IsolRate
		}
		if la.IsolTotal != lb.IsolTotal {
			return la.IsolTotal > lb.IsolTotal
		}
		return la.Name < lb.Name
	})
	sort.Slice(v.IsolIPIs, func(a, b int) bool {
		ia, ib := v.IsolIPIs[a], v.IsolIPIs[b]
		if ia.IsolRate != ib.IsolRate {
			return ia.IsolRate > ib.IsolRate
		}
		return ia.IsolTotal > ib.IsolTotal
	})
}
