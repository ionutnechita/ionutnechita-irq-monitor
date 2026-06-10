# irq-monitor

A live, **dependency-free** (pure Go stdlib) interrupt monitor for Linux, built
for debugging and tuning IRQ placement on real-time / NUMA / network servers.

It answers, at a glance:

- How many interrupts each **CPU core** takes, and the rate/sec right now.
- How interrupts break down **by driver/device category** (e.g. `ice` 200 vectors,
  `nvme` 64 vectors, `i40e`, `iavf`, `mlx5_core`).
- Which cores are **isolated** (`isolcpus`) or carry `nohz_full` / `rcu_nocbs`,
  so you can spot interrupts that still land on cores that should be quiet.
- Per-IRQ **requested vs effective affinity** — to catch masks that don't take
  effect or vectors pinned to the wrong core.

## Build

```sh
go build -o irq-monitor .
```

No modules to download — it only uses the standard library, so it builds on an
air-gapped box.

## Usage

```
irq-monitor [flags]
```

| Flag | Default | Meaning |
|------|---------|---------|
| `--live` | off | continuous dashboard, refresh until Ctrl-C |
| `--interval` | `1s` | sample window for rates (`0` = instant snapshot, no rates) |
| `--view` | `all` | `all` \| `category` \| `core` \| `irq` \| `ipi` \| `isolation` |
| `--filter` | — | comma list of categories to keep, e.g. `ice,i40e,nvme` |
| `--cpu` | — | restrict to CPUs (list form), e.g. `12-14,0` |
| `--top` | `40` | max rows in the `irq` view (`0` = all) |
| `--no-zero` | off | hide rows with zero interrupts |
| `--no-affinity` | off | skip per-IRQ affinity reads (faster on huge IRQ counts) |
| `--no-color` | off | disable ANSI colors |
| `--json` | off | machine-readable output |
| `--width` | auto | output width for the load bars |
| `--from` | — | analyse a saved `/proc/interrupts` capture from another host (one-shot, no rates, no local sysfs) |

### Examples

```sh
irq-monitor --live                          # continuous dashboard
irq-monitor --view category                 # aggregate by driver
irq-monitor --view core --no-zero           # per-CPU load, skip idle cores
irq-monitor --view irq --filter ice --top 50    # busiest ice vectors
irq-monitor --cpu 12-14 --view irq          # what still fires on isolated cores
irq-monitor --view ipi                      # per-CPU timer/IPI matrix
irq-monitor --view ipi --cpu 12-14          # isolation health on isolated cores
irq-monitor --view isolation                # which IRQs reach isolcpus + fix commands
irq-monitor --json --interval 2s            # 2s rate window, JSON
irq-monitor --from irq.txt --view ipi       # analyse a capture from another host
```

`--filter` matches either a full category (`iavf-mbx`) or the base driver
(`ice` selects `ice-queue`, `ice-ctrl`, `ice-ctrl-txrx`).

### Offline captures (`--from`)

On a remote host, save the table and analyse it anywhere:

```sh
cat /proc/interrupts > irq.txt          # on the target host
irq-monitor --from irq.txt --view category   # later, on your machine
```

A capture is a single point in time, so there are no per-second rates, and
isolation/NUMA/driver names are *not* read from the local `/sys` (which would
describe the wrong machine) — categories come straight from the capture's
chip/action text. A leading echoed `cat /proc/interrupts` line is tolerated.

## What the columns mean

**Per-CPU view**

- `ROLE` — `HK` (housekeeping) or `ISOL` (in `isolcpus`).
- `FLAGS` — `i`=isolcpus, `n`=nohz_full, `r`=rcu_nocbs (`.` when unset).
- `TYPE` — `P`/`E` on Intel hybrid hosts, `ht` when the CPU has an SMT sibling,
  `-` otherwise.
- `CORE` — physical core id; logical CPUs sharing a `CORE` are SMT siblings, so
  you can spot two hot IRQs colliding on one physical core's threads.
- `NODE` — NUMA node of the CPU.
- `ACT` — number of IRQs with non-zero lifetime count on this CPU.
- `AFF` — number of IRQs whose **effective** affinity targets this CPU.
- `TOTAL` / `RATE/s` — lifetime count and current rate.

**Category view**

Categories are the bound driver/device, split by IRQ **function** when it can be
recognized from the action name, so NIC vectors separate data-plane from control-
plane: `ice-queue` / `ice-ctrl`, `iavf-queue` / `iavf-mbx`, `i40e-queue`,
`mlx5_core-queue`, `nvme-queue`, etc. (`queue` = TxRx / completion vectors,
`ctrl` = adminq / OICR / misc / async, `mbx` = VF mailbox).

- `EFFECTIVE CPUS` — union of effective affinity across the category's IRQs.
- `HOTCPU` — the CPU that took the most interrupts in the last sample.

**IRQ view**

- `EFF` — `/proc/irq/N/effective_affinity_list` (where it actually fires).
- `SMP_AFF` — `/proc/irq/N/smp_affinity_list` (what was requested). A mismatch,
  or an `EFF` that lands on an isolated core, is usually what you're hunting.
- Lists real device IRQs only; the architectural counters (`LOC`/`RES`/`CAL`/…)
  have no affinity and live in `--view ipi`.

**IPI view** (`--view ipi`)

The architectural per-CPU counters from the bottom of `/proc/interrupts`
(`LOC` local timer, `RES` reschedule IPI, `CAL` function-call IPI, `TLB`
shootdown, `NMI`, `PMI`, `IWI`, …) shown as a CPU matrix. Isolated CPUs are
marked `*` and colored. This is the fastest way to confirm `nohz_full` is
quiescing the tick (`LOC` ≈ 0) and that no stray reschedule / function-call
IPIs land on isolated cores. Cells show rate/s with `--interval > 0`, or
lifetime totals with `--interval 0`. On many-core hosts (e.g. 144-core servers)
the matrix auto-wraps to the terminal width into stacked CPU blocks; use `--cpu`
to focus on specific cores. Terminal width is detected automatically (TIOCGWINSZ)
and tracked across resizes in `--live`.

**Isolation view** (`--view isolation`)

Turns the manual "compare each IRQ's affinity against `isolcpus`" into a verdict.
It lists every steerable device IRQ whose effective affinity reaches an isolated
CPU (`ON-ISOL`), or that is actively firing there (marked `!`, sorted first),
with the per-isolated-core rate (`ISOL/s`) and lifetime count (`ISOL-TOTAL`). It
then prints ready-to-run `echo … > /proc/irq/N/smp_affinity_list` commands to
fence them onto the housekeeping set, and the persistent `irqaffinity=` boot
option. Kernel-managed per-CPU IRQs (nvme/NIC queues pinned to a single core,
marked `M`) are split out — a userspace re-mask is rejected with `-EIO`, so they
are pointed at `isolcpus=managed_irq,<isol>` instead. A final section reports the
architectural counters (`LOC`/`RES`/`CAL`/…) still landing on isolated cores —
those affinity can't move; only `nohz_full` / `rcu_nocbs` quiesce them. A host
with no `isolcpus=` reports nothing to fix.

## Data sources

- `/proc/interrupts` — counts, controller chip, hwirq, action names.
- `/proc/irq/N/{smp_affinity_list,effective_affinity_list,node}` — affinity.
- `/proc/cmdline` — `isolcpus` / `nohz_full` / `rcu_nocbs` / `irqaffinity`.
- `/proc/irq/default_smp_affinity` — default mask.
- `/sys/devices/system/cpu/cpuN/topology/{core_id,thread_siblings_list}` — SMT
  sibling pairing; `/sys/devices/cpu_core/cpus` + `/sys/devices/cpu_atom/cpus`
  for Intel P/E hybrid detection.
- `/sys/devices/system/{cpu,node}` — online CPUs and NUMA topology.
- `/sys/bus/pci/devices/<BDF>/driver` — resolves a vector's chip BDF to its
  driver name (`ice`, `i40e`, `iavf`, `mlx5_core`, `nvme`, …).
- `/proc/version`, `/sys/kernel/realtime` — `PREEMPT_RT` detection.

## Layout

```
main.go                       flags, sampling loop, dispatch
internal/cpuset/              CPU list ("0-3,12-14") and hex mask parsing
internal/sysinfo/             topology, NUMA, isolation knobs, RT detection
internal/irq/                 /proc/interrupts + affinity parsing, categorization
internal/model/               aggregation into category/core views + rate deltas
internal/ui/                  ANSI rendering, live loop, JSON output
```
