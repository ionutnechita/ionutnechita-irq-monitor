// Package cpuset parses and represents sets of CPUs in the two formats the
// kernel exposes them: human "list" form (e.g. "0-3,12-14") found in
// *_affinity_list / cpulist files and the hex bitmask form (e.g. "00ff,ffffffff")
// found in smp_affinity / default_smp_affinity.
package cpuset

import (
	"sort"
	"strconv"
	"strings"
)

// Set is an immutable-ish set of CPU ids. The zero value is an empty set.
type Set struct {
	m map[int]bool
}

// New returns an empty Set.
func New() Set { return Set{m: map[int]bool{}} }

// ParseList parses a kernel cpu list such as "0-3,7,12-14". An empty or "-1"
// string yields an empty set (some kernels write that for "none").
func ParseList(s string) Set {
	out := New()
	s = strings.TrimSpace(s)
	if s == "" || s == "-1" {
		return out
	}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if lo, hi, ok := strings.Cut(part, "-"); ok {
			a, err1 := strconv.Atoi(strings.TrimSpace(lo))
			b, err2 := strconv.Atoi(strings.TrimSpace(hi))
			if err1 != nil || err2 != nil {
				continue
			}
			for c := a; c <= b; c++ {
				out.m[c] = true
			}
			continue
		}
		if c, err := strconv.Atoi(part); err == nil {
			out.m[c] = true
		}
	}
	return out
}

// ParseMask parses a kernel hex affinity mask such as "00000000,000000ff".
// Groups are comma-separated 32-bit hex words, most-significant group first.
func ParseMask(s string) Set {
	out := New()
	s = strings.TrimSpace(s)
	if s == "" {
		return out
	}
	groups := strings.Split(s, ",")
	n := len(groups)
	for gi, g := range groups {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		v, err := strconv.ParseUint(g, 16, 64)
		if err != nil {
			continue
		}
		// Rightmost group holds the lowest CPUs.
		base := (n - 1 - gi) * 32
		for b := 0; b < 32; b++ {
			if v&(1<<uint(b)) != 0 {
				out.m[base+b] = true
			}
		}
	}
	return out
}

// Add inserts a cpu.
func (s Set) Add(cpu int) {
	if s.m != nil {
		s.m[cpu] = true
	}
}

// Contains reports membership.
func (s Set) Contains(cpu int) bool { return s.m != nil && s.m[cpu] }

// Len returns the number of CPUs in the set.
func (s Set) Len() int { return len(s.m) }

// Empty reports whether the set has no members.
func (s Set) Empty() bool { return len(s.m) == 0 }

// Slice returns the sorted CPU ids.
func (s Set) Slice() []int {
	out := make([]int, 0, len(s.m))
	for c := range s.m {
		out = append(out, c)
	}
	sort.Ints(out)
	return out
}

// Union returns a new set containing members of both.
func (s Set) Union(o Set) Set {
	out := New()
	for c := range s.m {
		out.m[c] = true
	}
	for c := range o.m {
		out.m[c] = true
	}
	return out
}

// Intersect returns the members present in both s and o.
func (s Set) Intersect(o Set) Set {
	out := New()
	for c := range s.m {
		if o.Contains(c) {
			out.m[c] = true
		}
	}
	return out
}

// Sub returns the members of s not in o.
func (s Set) Sub(o Set) Set {
	out := New()
	for c := range s.m {
		if !o.Contains(c) {
			out.m[c] = true
		}
	}
	return out
}

// String renders the set in compact list form ("0-3,12-14"). "-" for empty.
func (s Set) String() string {
	cpus := s.Slice()
	if len(cpus) == 0 {
		return "-"
	}
	var b strings.Builder
	for i := 0; i < len(cpus); {
		j := i
		for j+1 < len(cpus) && cpus[j+1] == cpus[j]+1 {
			j++
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		if j == i {
			b.WriteString(strconv.Itoa(cpus[i]))
		} else {
			b.WriteString(strconv.Itoa(cpus[i]))
			b.WriteByte('-')
			b.WriteString(strconv.Itoa(cpus[j]))
		}
		i = j + 1
	}
	return b.String()
}
