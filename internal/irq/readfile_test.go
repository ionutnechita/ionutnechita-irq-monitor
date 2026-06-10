package irq

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadFileCapture checks that ReadFile parses a saved /proc/interrupts:
// it skips a leading shell-command line, finds the CPU header, parses numeric
// and special rows, and categorises without touching local sysfs.
func TestReadFileCapture(t *testing.T) {
	const capture = `cat /proc/interrupts
           CPU0       CPU1       CPU2
  4:       3811          0        183  IR-IO-APIC    4-edge      ttyS0
447:    3260746          0          0  IR-PCI-MSIX-0000:13:00.1  0-edge  ice-0000:13:00.1:misc
2449:     50786          0          0  IR-PCI-MSI-0000:13:07.7   0-edge  iavf-0000:13:07.7:mbx
 LOC:  104118942   92641875        849   Local timer interrupts
 ERR:          0
`
	dir := t.TempDir()
	path := filepath.Join(dir, "irq.txt")
	if err := os.WriteFile(path, []byte(capture), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.CPUList) != 3 {
		t.Fatalf("CPUList = %v, want 3 columns", s.CPUList)
	}
	if len(s.IRQs) != 5 {
		t.Fatalf("got %d IRQs, want 5", len(s.IRQs))
	}

	want := map[string]struct {
		cat   string
		total uint64
	}{
		"4":    {"ttyS0", 3994},
		"447":  {"ice-ctrl", 3260746}, // action-derived, not via sysfs
		"2449": {"iavf-mbx", 50786},   // sysfs would mis-map the BDF locally
		"LOC":  {"kernel", 104118942 + 92641875 + 849},
	}
	for key, w := range want {
		q := s.Lookup(key)
		if q == nil {
			t.Errorf("missing IRQ %q", key)
			continue
		}
		if q.Category != w.cat {
			t.Errorf("IRQ %q category = %q, want %q", key, q.Category, w.cat)
		}
		if q.Total != w.total {
			t.Errorf("IRQ %q total = %d, want %d", key, q.Total, w.total)
		}
	}

	// ERR is a single-column global counter; it must not steal description tokens.
	if q := s.Lookup("ERR"); q == nil || q.Total != 0 || len(q.Counts) != 1 {
		t.Errorf("ERR row mis-parsed: %+v", q)
	}
}

// TestReadFileNoHeader ensures a non-capture file is rejected clearly.
func TestReadFileNoHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.txt")
	os.WriteFile(path, []byte("hello\nworld\n"), 0o644)
	if _, err := ReadFile(path); err == nil {
		t.Fatal("expected error for file with no CPU header")
	}
}
