package irq

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// bdfRe matches a PCI address (domain:bus:device.function) embedded in a chip
// name such as "IR-PCI-MSIX-0000:03:00.0".
var bdfRe = regexp.MustCompile(`[0-9a-fA-F]{4}:[0-9a-fA-F]{2}:[0-9a-fA-F]{2}\.[0-9a-fA-F]`)

// driverCache memoises PCI BDF -> driver name lookups for the process lifetime.
var driverCache = map[string]string{}

// pciDriver resolves the bound kernel driver of a PCI device by reading the
// /sys/bus/pci/devices/<bdf>/driver symlink (e.g. ice, i40e, iavf, mlx5_core,
// nvme). Returns "" when the device has no driver or sysfs is unavailable.
func pciDriver(bdf string) string {
	if d, ok := driverCache[bdf]; ok {
		return d
	}
	link := "/sys/bus/pci/devices/" + bdf + "/driver"
	target, err := os.Readlink(link)
	drv := ""
	if err == nil {
		drv = filepath.Base(target)
	}
	driverCache[bdf] = drv
	return drv
}

// Categorize assigns a category to an interrupt: the base driver/device name
// suffixed with the IRQ function when one is recognizable, so NIC vectors split
// by role. Recognised suffixes (priority order, most specific first):
//
//	mbx        VF mailbox (iavf)
//	ll_ts      low-latency timestamp / PHC (ice ll_ts vector)
//	fdir       flow director queue (i40e/ice fdir-TxRx)
//	ctrl-txrx  control-plane TxRx pair (ice ctrl-TxRx-0)
//	ctrl       admin queue: adminq / aenq / async / event / misc / bare "ctrl"
//	queue      data-plane TxRx, mlx5 comp, nvme qN
//
// The base name is resolved, in priority order, from:
//  1. the bound PCI driver (most accurate: ice/i40e/iavf/mlx5_core/nvme/...),
//  2. a name derived from the first action (e.g. "ice-...-TxRx-3" -> "ice"),
//  3. a name derived from the controller chip (IO-APIC, IOMMU, ...).
//
// sysfs is true only for a live read of the local host; pass false for an
// offline capture, where the local /sys PCI map does not match the capture's
// devices (driver names are then derived from the chip/action text instead).
func Categorize(chip string, actions []string, sysfs bool) string {
	base := baseCategory(chip, actions, sysfs)
	if len(actions) > 0 {
		if fn := deriveFunction(actions[0]); fn != "" {
			return base + "-" + fn
		}
	}
	return base
}

func baseCategory(chip string, actions []string, sysfs bool) string {
	if sysfs {
		if bdf := bdfRe.FindString(chip); bdf != "" {
			if drv := pciDriver(strings.ToLower(bdf)); drv != "" {
				return drv
			}
		}
	}
	if len(actions) > 0 && actions[0] != "" {
		if c := deriveFromAction(actions[0]); c != "" {
			return c
		}
	}
	return deriveFromChip(chip)
}

// qSuffixRe matches a trailing per-queue index such as the "q1" in "nvme0q1".
var qSuffixRe = regexp.MustCompile(`q[0-9]+$`)

// deriveFunction classifies an IRQ by role from its action name. Returns "" when
// no specific function is recognized (so the category stays the plain driver).
//
// The ORDER of checks matters: more specific patterns must precede more generic
// ones. "ctrl-TxRx-0" contains both "ctrl" and "-txrx-", so we test the
// composite "ctrl-txrx" first; "fdir-TxRx-0" must be classified as fdir, not as
// a regular data queue; "ll_ts" must not be swallowed by anything else.
func deriveFunction(action string) string {
	a := strings.ToLower(action)
	switch {
	case strings.Contains(a, "mbx"):
		return "mbx"
	case strings.Contains(a, "ll_ts"),
		strings.HasSuffix(a, "_ts"),
		strings.HasSuffix(a, ":ts"):
		return "ll_ts"
	case strings.Contains(a, "fdir"):
		return "fdir"
	case strings.Contains(a, "ctrl-txrx"),
		strings.Contains(a, "ctrl-tx-"),
		strings.Contains(a, "ctrl-rx-"):
		return "ctrl-txrx"
	case strings.Contains(a, "oicr"),
		strings.Contains(a, "aenq"),
		strings.Contains(a, "adminq"),
		strings.Contains(a, "async"),
		strings.Contains(a, "ctrl"),
		strings.Contains(a, "event"),
		strings.Contains(a, "misc"):
		return "ctrl"
	case strings.Contains(a, "-txrx-"),
		strings.Contains(a, "-tx-"),
		strings.Contains(a, "-rx-"),
		strings.Contains(a, "comp"),
		qSuffixRe.MatchString(a):
		return "queue"
	default:
		return ""
	}
}

// deriveFromAction extracts a driver-ish prefix from an action/device name.
func deriveFromAction(a string) string {
	// Cut at the first separator: queues/channels live after these
	// (e.g. "ice-...-TxRx-3" -> "ice", "idma64.0" -> "idma64").
	if idx := strings.IndexAny(a, "-@: ."); idx >= 0 {
		a = a[:idx]
	}
	// Strip only a "<idx>q<n>" style per-queue suffix (e.g. "nvme0q1" -> "nvme").
	// Crucially, do NOT strip trailing digits otherwise: they are part of the
	// identity for names like "i8042" (PS/2) or "i915" (GPU).
	if loc := qSuffixRe.FindStringIndex(a); loc != nil {
		a = strings.TrimRight(a[:loc[0]], "0123456789")
	}
	if a == "" {
		return "misc"
	}
	return a
}

// deriveFromChip produces a coarse category from a controller chip name.
func deriveFromChip(chip string) string {
	l := strings.ToLower(chip)
	switch {
	case strings.Contains(l, "io-apic"):
		return "ioapic"
	case strings.Contains(l, "iommu"):
		return "iommu"
	case strings.Contains(l, "its"):
		return "its"
	case chip == "":
		return "misc"
	default:
		// First whitespace-separated token, lowercased.
		if f := strings.Fields(chip); len(f) > 0 {
			return strings.ToLower(f[0])
		}
		return "misc"
	}
}
