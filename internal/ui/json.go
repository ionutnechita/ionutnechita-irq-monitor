package ui

import (
	"encoding/json"

	"github.com/IonutNechita/irq-monitor/internal/model"
	"github.com/IonutNechita/irq-monitor/internal/sysinfo"
)

// jsonOut is the machine-readable shape emitted by --json.
type jsonOut struct {
	Kernel       string         `json:"kernel"`
	Realtime     bool           `json:"realtime"`
	OnlineCPUs   string         `json:"online_cpus"`
	Isolcpus     string         `json:"isolcpus"`
	NohzFull     string         `json:"nohz_full"`
	RcuNocbs     string         `json:"rcu_nocbs"`
	Housekeeping string         `json:"housekeeping"`
	IntervalSec  float64        `json:"interval_sec"`
	HasRate      bool           `json:"has_rate"`
	TotalIRQs    int            `json:"total_irqs"`
	GrandTotal   uint64         `json:"grand_total"`
	GrandRate    float64        `json:"grand_rate"`
	Categories   []jsonCategory `json:"categories"`
	Cores        []jsonCore     `json:"cores"`
	IRQs         []jsonIRQ      `json:"irqs,omitempty"`
	Leaks        []jsonLeak     `json:"isolation_leaks,omitempty"`
}

type jsonLeak struct {
	Num       string  `json:"irq"`
	Category  string  `json:"category"`
	OnIsol    string  `json:"on_isolcpus"`
	Eff       string  `json:"effective_affinity"`
	SMP       string  `json:"smp_affinity"`
	IsolTotal uint64  `json:"isol_total"`
	IsolRate  float64 `json:"isol_rate"`
	Firing    bool    `json:"firing"`
	Managed   bool    `json:"managed"`
	Actions   string  `json:"actions"`
}

type jsonCategory struct {
	Name    string  `json:"name"`
	NumIRQs int     `json:"num_irqs"`
	Total   uint64  `json:"total"`
	Rate    float64 `json:"rate"`
	EffCPUs string  `json:"effective_cpus"`
	HotCPU  int     `json:"hot_cpu"`
}

type jsonCore struct {
	CPU        int     `json:"cpu"`
	Role       string  `json:"role"`
	Flags      string  `json:"flags"`
	Class      string  `json:"class"` // "P"/"E"/""
	CoreID     int     `json:"core_id"`
	SMT        bool    `json:"smt"`
	Node       int     `json:"node"`
	ActiveIRQs int     `json:"active_irqs"`
	AffIRQs    int     `json:"affinity_irqs"`
	Total      uint64  `json:"total"`
	Rate       float64 `json:"rate"`
}

type jsonIRQ struct {
	Num      string  `json:"irq"`
	Category string  `json:"category"`
	Eff      string  `json:"effective_affinity"`
	SMP      string  `json:"smp_affinity"`
	Node     int     `json:"node"`
	Total    uint64  `json:"total"`
	Rate     float64 `json:"rate"`
	Chip     string  `json:"chip"`
	Actions  string  `json:"actions"`
}

// JSON renders the view as indented JSON. includeIRQs adds the per-IRQ array.
func JSON(info *sysinfo.Info, v model.View, includeIRQs bool) ([]byte, error) {
	o := jsonOut{
		Kernel:       info.Kernel,
		Realtime:     info.Realtime,
		OnlineCPUs:   info.OnlineCPUs.String(),
		Isolcpus:     info.Isolcpus.String(),
		NohzFull:     info.NohzFull.String(),
		RcuNocbs:     info.RcuNocbs.String(),
		Housekeeping: info.Housekeeping().String(),
		IntervalSec:  v.Interval.Seconds(),
		HasRate:      v.HasRate,
		TotalIRQs:    v.TotalIRQs,
		GrandTotal:   v.GrandTotal,
		GrandRate:    v.GrandRate,
	}
	for _, c := range v.Categories {
		o.Categories = append(o.Categories, jsonCategory{
			Name: c.Name, NumIRQs: c.NumIRQs, Total: c.Total,
			Rate: c.Rate, EffCPUs: c.EffCPUs.String(), HotCPU: c.HotCPU,
		})
	}
	for _, cs := range v.Cores {
		o.Cores = append(o.Cores, jsonCore{
			CPU: cs.CPU, Role: cs.Role.String(), Flags: cs.Flags,
			Class: cs.Class, CoreID: cs.CoreID, SMT: cs.SMT, Node: cs.Node,
			ActiveIRQs: cs.ActiveIRQs, AffIRQs: cs.AffIRQs, Total: cs.Total, Rate: cs.Rate,
		})
	}
	if includeIRQs {
		for _, q := range v.Cur.IRQs {
			o.IRQs = append(o.IRQs, jsonIRQ{
				Num: q.Name, Category: q.Category, Eff: q.EffAff.String(),
				SMP: q.SMPAff.String(), Node: q.Node, Total: q.Total,
				Rate: v.IRQRate[q.Name], Chip: q.Chip, Actions: q.ActionStr(),
			})
		}
	}
	for _, lk := range v.Leaks {
		o.Leaks = append(o.Leaks, jsonLeak{
			Num: lk.Name, Category: lk.Category, OnIsol: lk.OnIsol.String(),
			Eff: lk.Eff.String(), SMP: lk.SMP.String(), IsolTotal: lk.IsolTotal,
			IsolRate: lk.IsolRate, Firing: lk.Firing, Managed: lk.Managed, Actions: lk.Actions,
		})
	}
	return json.MarshalIndent(o, "", "  ")
}
