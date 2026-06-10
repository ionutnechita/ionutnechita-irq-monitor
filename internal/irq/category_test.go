package irq

import "testing"

// TestDeriveFromAction guards the action-name fallback used when a chip has no
// resolvable PCI driver (legacy IO-APIC lines etc). The key regression: digits
// that are part of a device's identity (i8042, i915) must be preserved, while
// genuine per-queue suffixes (nvme0q1) are peeled.
func TestDeriveFromAction(t *testing.T) {
	cases := map[string]string{
		"i8042":                "i8042", // PS/2 controller — digits are identity
		"i915":                 "i915",  // GPU — digits are identity
		"nvme0q1":              "nvme",  // controller index + queue -> driver
		"nvme0q0":              "nvme",
		"ice-enp1s0f0-TxRx-12": "ice",
		"iavf-eth1:mbx":        "iavf",
		"idma64.0":             "idma64",
		"snd_hda_intel:card0":  "snd_hda_intel",
		"xhci_hcd:usb1":        "xhci_hcd",
		"timer":                "timer",
		"enp2s0":               "enp2s0", // netdev name, no queue suffix
		"i801_smbus":           "i801_smbus",
	}
	for in, want := range cases {
		if got := deriveFromAction(in); got != want {
			t.Errorf("deriveFromAction(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDeriveFunction checks the queue/ctrl/mbx classification that splits NIC
// vectors into data-plane vs control-plane categories.
func TestDeriveFunction(t *testing.T) {
	cases := map[string]string{
		"ice-enp1s0f0-TxRx-12": "queue",
		"nvme0q1":              "queue",
		"mlx5_comp3@pci:x":     "queue",
		"iavf-eth1:mbx":        "mbx",
		"ice-eth0:misc":        "ctrl",
		"i40e-eth0:misc":       "ctrl",
		"i8042":                "", // no recognizable function
		"timer":                "",
	}
	for in, want := range cases {
		if got := deriveFunction(in); got != want {
			t.Errorf("deriveFunction(%q) = %q, want %q", in, got, want)
		}
	}
}
