// Package sysinfo gathers static system context for interpreting interrupt
// activity: CPU topology, NUMA layout, kernel realtime flavour and the CPU
// isolation knobs (isolcpus / nohz_full / rcu_nocbs / irqaffinity).
package sysinfo

import (
	"os"
	"strconv"
	"strings"

	"github.com/IonutNechita/irq-monitor/internal/cpuset"
)

// Role classifies a CPU for IRQ-placement purposes.
type Role int

const (
	RoleHousekeeping Role = iota // available for IRQs / general work
	RoleIsolated                 // listed in isolcpus
)

func (r Role) String() string {
	if r == RoleIsolated {
		return "ISOL"
	}
	return "HK"
}

// Info is the collected static picture of the host.
type Info struct {
	Kernel     string
	Realtime   bool // PREEMPT_RT
	OnlineCPUs cpuset.Set
	MaxCPU     int

	NodeCPUs map[int]cpuset.Set // numa node id -> cpus
	CPUNode  map[int]int        // cpu -> numa node id
	CoreID   map[int]int        // cpu -> physical core id (shared id = SMT siblings)
	Sibling  map[int]cpuset.Set // cpu -> its SMT thread siblings (excluding itself)

	Hybrid bool       // Intel P/E hybrid topology present
	PCores cpuset.Set // performance cores (cpu_core PMU)
	ECores cpuset.Set // efficiency cores (cpu_atom PMU)

	Isolcpus    cpuset.Set
	NohzFull    cpuset.Set
	RcuNocbs    cpuset.Set
	IrqAffinity cpuset.Set // from cmdline irqaffinity=
	DefaultAff  cpuset.Set // /proc/irq/default_smp_affinity

	Cmdline string
}

// Offline returns a neutral Info for analysing a /proc/interrupts capture taken
// on another host, where local topology and isolation do not apply. It knows
// only the CPU columns present in the capture: every such CPU is reported online
// and housekeeping, with unknown NUMA node/core and no isolation flags.
func Offline(cpus []int, label string) *Info {
	online := cpuset.New()
	maxCPU := -1
	for _, c := range cpus {
		online.Add(c)
		if c > maxCPU {
			maxCPU = c
		}
	}
	return &Info{
		Kernel:      label,
		OnlineCPUs:  online,
		MaxCPU:      maxCPU,
		NodeCPUs:    map[int]cpuset.Set{},
		CPUNode:     map[int]int{},
		CoreID:      map[int]int{},
		Sibling:     map[int]cpuset.Set{},
		Isolcpus:    cpuset.New(),
		NohzFull:    cpuset.New(),
		RcuNocbs:    cpuset.New(),
		IrqAffinity: cpuset.New(),
		DefaultAff:  cpuset.New(),
	}
}

// Housekeeping returns online CPUs that are not isolated.
func (i *Info) Housekeeping() cpuset.Set {
	return i.OnlineCPUs.Sub(i.Isolcpus)
}

// RoleOf returns the placement role of a cpu.
func (i *Info) RoleOf(cpu int) Role {
	if i.Isolcpus.Contains(cpu) {
		return RoleIsolated
	}
	return RoleHousekeeping
}

// Flags returns single-letter isolation flags for a cpu:
// i=isolcpus n=nohz_full r=rcu_nocbs. Dots for unset, e.g. "inr" or "i.." .
func (i *Info) Flags(cpu int) string {
	var b strings.Builder
	put := func(set cpuset.Set, ch byte) {
		if set.Contains(cpu) {
			b.WriteByte(ch)
		} else {
			b.WriteByte('.')
		}
	}
	put(i.Isolcpus, 'i')
	put(i.NohzFull, 'n')
	put(i.RcuNocbs, 'r')
	return b.String()
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// cmdlineParam extracts the value of key=value from a kernel cmdline string.
func cmdlineParam(cmdline, key string) string {
	for _, tok := range strings.Fields(cmdline) {
		if v, ok := strings.CutPrefix(tok, key+"="); ok {
			return v
		}
	}
	return ""
}

// Collect reads all static system information.
func Collect() *Info {
	i := &Info{
		NodeCPUs: map[int]cpuset.Set{},
		CPUNode:  map[int]int{},
		CoreID:   map[int]int{},
		Sibling:  map[int]cpuset.Set{},
	}

	i.Kernel = readTrim("/proc/sys/kernel/osrelease")
	version := readTrim("/proc/version")
	i.Realtime = strings.Contains(version, "PREEMPT_RT") ||
		strings.Contains(version, "PREEMPT RT") ||
		readTrim("/sys/kernel/realtime") == "1"

	i.OnlineCPUs = cpuset.ParseList(readTrim("/sys/devices/system/cpu/online"))
	for _, c := range i.OnlineCPUs.Slice() {
		if c > i.MaxCPU {
			i.MaxCPU = c
		}
	}

	i.Cmdline = readTrim("/proc/cmdline")
	// nohz_full=auto and friends are non-list values; ParseList ignores them.
	i.Isolcpus = parseIsolcpus(cmdlineParam(i.Cmdline, "isolcpus"))
	i.NohzFull = cpuset.ParseList(cmdlineParam(i.Cmdline, "nohz_full"))
	i.RcuNocbs = cpuset.ParseList(cmdlineParam(i.Cmdline, "rcu_nocbs"))
	i.IrqAffinity = cpuset.ParseList(cmdlineParam(i.Cmdline, "irqaffinity"))
	i.DefaultAff = cpuset.ParseMask(readTrim("/proc/irq/default_smp_affinity"))

	collectNUMA(i)
	collectTopology(i)
	return i
}

func collectTopology(i *Info) {
	for _, c := range i.OnlineCPUs.Slice() {
		base := "/sys/devices/system/cpu/cpu" + strconv.Itoa(c) + "/topology/"
		if id := atoiSafe(readTrim(base + "core_id")); id >= 0 {
			i.CoreID[c] = id
		}
		sib := cpuset.ParseList(readTrim(base + "thread_siblings_list"))
		i.Sibling[c] = sib.Sub(singleton(c)) // siblings excluding self
	}
	// Intel hybrid: the two PMUs expose the P/E core masks directly.
	i.PCores = cpuset.ParseList(readTrim("/sys/devices/cpu_core/cpus"))
	i.ECores = cpuset.ParseList(readTrim("/sys/devices/cpu_atom/cpus"))
	i.Hybrid = !i.PCores.Empty() && !i.ECores.Empty()
}

func singleton(c int) cpuset.Set {
	s := cpuset.New()
	s.Add(c)
	return s
}

// CoreOf returns the physical core id of a cpu, or -1 if unknown.
func (i *Info) CoreOf(cpu int) int {
	if v, ok := i.CoreID[cpu]; ok {
		return v
	}
	return -1
}

// HasSMT reports whether the cpu shares its physical core with sibling threads.
func (i *Info) HasSMT(cpu int) bool { return i.Sibling[cpu].Len() > 0 }

// CPUClass returns "P"/"E" on Intel hybrid hosts, else "" (unknown/uniform).
func (i *Info) CPUClass(cpu int) string {
	if i.Hybrid {
		switch {
		case i.PCores.Contains(cpu):
			return "P"
		case i.ECores.Contains(cpu):
			return "E"
		}
	}
	return ""
}

// parseIsolcpus handles the "isolcpus=[flag,...,]<cpulist>" syntax where the
// value may be prefixed by flags like "managed_irq,domain,".
func parseIsolcpus(v string) cpuset.Set {
	if v == "" {
		return cpuset.New()
	}
	parts := strings.Split(v, ",")
	// Drop leading non-numeric flag tokens.
	start := 0
	for start < len(parts) {
		p := parts[start]
		if p == "" {
			start++
			continue
		}
		c := p[0]
		if (c >= '0' && c <= '9') || c == '-' {
			break
		}
		start++
	}
	return cpuset.ParseList(strings.Join(parts[start:], ","))
}

func collectNUMA(i *Info) {
	entries, err := os.ReadDir("/sys/devices/system/node")
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "node") {
			continue
		}
		idStr := strings.TrimPrefix(name, "node")
		id := atoiSafe(idStr)
		if id < 0 {
			continue
		}
		set := cpuset.ParseList(readTrim("/sys/devices/system/node/" + name + "/cpulist"))
		i.NodeCPUs[id] = set
		for _, c := range set.Slice() {
			i.CPUNode[c] = id
		}
	}
}

func atoiSafe(s string) int {
	n := 0
	if s == "" {
		return -1
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return -1
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// NodeOf returns the NUMA node of a cpu, or -1 if unknown.
func (i *Info) NodeOf(cpu int) int {
	if n, ok := i.CPUNode[cpu]; ok {
		return n
	}
	return -1
}
