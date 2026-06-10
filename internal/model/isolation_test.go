package model

import (
	"testing"

	"github.com/IonutNechita/irq-monitor/internal/cpuset"
	"github.com/IonutNechita/irq-monitor/internal/irq"
	"github.com/IonutNechita/irq-monitor/internal/sysinfo"
)

// TestBuildIsolation checks that the isolation view flags exactly the IRQs that
// reach isolated CPUs, and routes architectural counters into IsolIPIs.
func TestBuildIsolation(t *testing.T) {
	info := &sysinfo.Info{
		Isolcpus:   cpuset.ParseList("2-3"),
		OnlineCPUs: cpuset.ParseList("0-3"),
	}
	snap := &irq.Snapshot{
		CPUList: []int{0, 1, 2, 3},
		IRQs: []*irq.IRQ{
			{ // leaks: effective affinity targets isolated cpu 2, fired there
				Num: 47, Name: "47", Category: "xhci_hcd",
				Actions: []string{"xhci_hcd"},
				Counts:  []uint64{0, 0, 5, 0}, Total: 5,
				EffAff: cpuset.ParseList("2"), SMPAff: cpuset.ParseList("0-3"),
			},
			{ // clean: affinity only on housekeeping, no isolated counts
				Num: 10, Name: "10", Category: "ice-queue",
				Counts: []uint64{3, 3, 0, 0}, Total: 6,
				EffAff: cpuset.ParseList("0-1"), SMPAff: cpuset.ParseList("0-1"),
			},
			{ // managed: per-CPU queue pinned to isolated cpu 3, mask confined there
				Num: 70, Name: "70", Category: "nvme-queue",
				Actions: []string{"nvme0q3"},
				Counts:  []uint64{0, 0, 0, 0}, Total: 0,
				EffAff: cpuset.ParseList("3"), SMPAff: cpuset.ParseList("3"),
			},
			{ // architectural counter still hitting isolated cpus
				Num: -1, Name: "LOC", Special: true,
				Actions: []string{"Local timer interrupts"},
				Counts:  []uint64{100, 100, 7, 7}, Total: 214,
			},
		},
	}

	v := Build(info, Sample{}, Sample{Snap: snap})

	if v.Isolcpus.String() != "2-3" {
		t.Errorf("Isolcpus = %q, want 2-3", v.Isolcpus.String())
	}
	if len(v.Leaks) != 2 {
		t.Fatalf("got %d leaks, want 2: %+v", len(v.Leaks), v.Leaks)
	}
	byName := map[string]IsolationLeak{}
	for _, lk := range v.Leaks {
		byName[lk.Name] = lk
	}

	lk := byName["47"]
	if lk.Category != "xhci_hcd" || lk.OnIsol.String() != "2" || lk.IsolTotal != 5 {
		t.Errorf("leak 47 = %s/%s/%d, want xhci_hcd/2/5", lk.Category, lk.OnIsol.String(), lk.IsolTotal)
	}
	if lk.Firing { // no prev sample => no rate => not "firing now"
		t.Errorf("Firing should be false without a previous sample")
	}
	if lk.Managed { // SMP 0-3 includes housekeeping => steerable
		t.Errorf("IRQ 47 should be steerable, not managed")
	}

	if m := byName["70"]; !m.Managed { // SMP confined to isolated cpu 3 => managed
		t.Errorf("IRQ 70 (nvme0q3, SMP=3) should be flagged managed: %+v", m)
	}

	if len(v.IsolIPIs) != 1 || v.IsolIPIs[0].Name != "LOC" || v.IsolIPIs[0].IsolTotal != 14 {
		t.Errorf("IsolIPIs = %+v, want one LOC with IsolTotal 14", v.IsolIPIs)
	}
}

// TestBuildIsolationNone verifies no leaks are reported without isolcpus.
func TestBuildIsolationNone(t *testing.T) {
	info := &sysinfo.Info{OnlineCPUs: cpuset.ParseList("0-3")}
	snap := &irq.Snapshot{
		CPUList: []int{0, 1, 2, 3},
		IRQs: []*irq.IRQ{
			{Num: 47, Name: "47", Category: "x", Counts: []uint64{1, 1, 1, 1}, Total: 4,
				EffAff: cpuset.ParseList("0-3"), SMPAff: cpuset.ParseList("0-3")},
		},
	}
	v := Build(info, Sample{}, Sample{Snap: snap})
	if len(v.Leaks) != 0 || len(v.IsolIPIs) != 0 {
		t.Errorf("expected no leaks without isolcpus, got %d / %d", len(v.Leaks), len(v.IsolIPIs))
	}
}
