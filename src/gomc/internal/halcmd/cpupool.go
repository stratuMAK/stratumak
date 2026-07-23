// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package halcmd

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// cpuPool manages a pool of isolated physical CPU cores for automatic
// thread-to-CPU assignment.  The pool is initialized once at startup via
// InitCPUPool(). Each call to CreateThreadCPU with cpu=-1 pops the next
// available core from the pool; once the pool is exhausted, further threads
// co-locate onto the last-assigned isolated core rather than floating onto the
// non-isolated housekeeping cores (see acquireCPU). Explicit cpu=N is the
// caller's deliberate choice and is honoured for any online CPU — isolated or
// not — with a warning when it isn't isolated.
type cpuPool struct {
	mu           sync.Mutex
	online       []int // all online CPUs, ascending; empty if topology is unknown
	isolated     []int // all isolated physical cores, sorted descending
	available    []int // remaining unassigned isolated cores, sorted descending
	lastAssigned int   // most recently assigned isolated core, -1 if none yet
	logger       *slog.Logger
	posixRT      bool // true if running with SCHED_FIFO (real RT)
}

var pool cpuPool

// InitCPUPool detects isolated physical CPU cores and prepares the
// auto-assignment pool. Must be called once after RtapiAppInit().
// The logger is used for pool-exhaustion warnings; pass nil to suppress.
func InitCPUPool(logger *slog.Logger) error {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}

	topo, err := detectTopology()
	if err != nil {
		return err
	}

	// Build pool: isolated CPUs, preferring physical cores but including HT
	// siblings when both siblings of a core are isolated.  If only the
	// non-primary sibling is isolated (common: isolcpus=2,3 on a 4-logical-CPU
	// system where 0,2 and 1,3 are sibling pairs), we still use it — the
	// kernel has already removed it from the general scheduler.
	avail := append([]int(nil), topo.isolated...)
	sort.Sort(sort.Reverse(sort.IntSlice(avail)))

	posixRT := rtapiIsRealtime()

	pool.mu.Lock()
	pool.online = append([]int(nil), topo.online...)
	pool.isolated = append([]int(nil), avail...)
	pool.available = avail
	pool.lastAssigned = -1
	pool.logger = logger
	pool.posixRT = posixRT
	pool.mu.Unlock()

	if len(avail) > 0 {
		logger.Info("CPU pool initialized", "isolated_cores", avail, "posix_rt", posixRT)
	}
	return nil
}

// acquireCPU obtains a CPU for a newthread command.
//   - cpu=-1: auto-assign next free isolated core; once the pool is exhausted,
//     co-locate onto the last-assigned isolated core; -1 (no affinity) only if
//     there are no isolated cores at all (warn in RT mode).
//   - cpu>=0: an explicit pin request. Any online CPU is accepted — pinning is
//     the caller's deliberate choice and a machine without isolcpus must still
//     be able to place a thread. An isolated core is removed from the free list
//     (or co-located onto if already handed out); a non-isolated core is honoured
//     with a warning and does NOT become lastAssigned, so later auto-assigned
//     threads never inherit a non-isolated core. Only a CPU that is not online
//     is an error (hal_create_thread_cpu ignores the affinity return value, so an
//     out-of-range core would silently no-op).
//
// Co-location mirrors the classic base+servo-on-one-CPU model: threads are
// created fastest-first with descending priority, so a slower thread stacked
// onto a faster thread's core is simply preempted by it (rate monotonic) while
// still running on an isolated core, rather than floating onto the noisy
// non-isolated housekeeping cores.
func acquireCPU(threadName string, cpu int) (int, error) {
	pool.mu.Lock()
	defer pool.mu.Unlock()

	if cpu < 0 {
		// Auto-assign the next free isolated core.
		if len(pool.available) > 0 {
			assigned := pool.available[0]
			pool.available = pool.available[1:]
			pool.lastAssigned = assigned
			return assigned, nil
		}
		// Pool exhausted but at least one isolated core exists — co-locate
		// onto the last one handed out instead of dropping affinity.
		if pool.lastAssigned >= 0 {
			if pool.logger != nil {
				pool.logger.Info("isolated CPU pool exhausted, co-locating thread onto isolated core",
					"thread", threadName, "cpu", pool.lastAssigned)
			}
			return pool.lastAssigned, nil
		}
		// No isolated cores at all — run without affinity.
		if pool.posixRT && pool.logger != nil {
			pool.logger.Warn("no isolated CPU available for thread, running without affinity",
				"thread", threadName)
		}
		return -1, nil
	}

	// Explicit cpu=N requested — remove it from the free list if still there.
	for i, c := range pool.available {
		if c == cpu {
			pool.available = append(pool.available[:i], pool.available[i+1:]...)
			pool.lastAssigned = cpu
			return cpu, nil
		}
	}
	// Already handed out but still an isolated core — co-locate onto it (an
	// explicit request is the caller's deliberate choice).
	for _, c := range pool.isolated {
		if c == cpu {
			pool.lastAssigned = cpu
			return cpu, nil
		}
	}
	// Not isolated. Refuse only if we know the topology and the CPU isn't online.
	if len(pool.online) > 0 && !containsInt(pool.online, cpu) {
		return 0, fmt.Errorf("newthread %s: cpu=%d is not an online CPU (online: %v)",
			threadName, cpu, pool.online)
	}
	if pool.logger != nil {
		pool.logger.Warn("thread pinned to a non-isolated CPU, realtime performance will depend on other load on that core",
			"thread", threadName, "cpu", cpu, "isolated", pool.isolated)
	}
	// Deliberately not recorded as lastAssigned: auto-assigned threads must not
	// spill onto a non-isolated core just because someone pinned one here.
	return cpu, nil
}

// poolState captures the assignable part of the pool so a CPU handed out for a
// thread that then fails to be created can be given back (see CreateThreadCPU).
type poolState struct {
	available    []int
	lastAssigned int
}

func snapshotPool() poolState {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return poolState{
		available:    append([]int(nil), pool.available...),
		lastAssigned: pool.lastAssigned,
	}
}

func restorePool(s poolState) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	pool.available = s.available
	pool.lastAssigned = s.lastAssigned
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// --- CPU topology detection (moved from threadcfg/cpu_linux.go) ---

type cpuTopology struct {
	online        []int
	isolated      []int
	physicalCores map[int]bool
	siblingOf     map[int]int
}

func detectTopology() (*cpuTopology, error) {
	topo := &cpuTopology{
		physicalCores: make(map[int]bool),
		siblingOf:     make(map[int]int),
	}

	nCPU := runtime.NumCPU()
	for i := 0; i < nCPU; i++ {
		if cpuOnline(i) {
			topo.online = append(topo.online, i)
		}
	}

	topo.isolated = parseIsolatedCPUs()

	seen := make(map[int]bool)
	for _, cpu := range topo.online {
		if seen[cpu] {
			continue
		}
		siblings := readSiblingsList(cpu)
		if len(siblings) == 0 {
			siblings = []int{cpu}
		}
		sort.Ints(siblings)
		primary := siblings[0]
		for _, s := range siblings {
			seen[s] = true
			topo.siblingOf[s] = primary
		}
		topo.physicalCores[primary] = true
	}

	return topo, nil
}

func cpuOnline(cpu int) bool {
	if cpu == 0 {
		return true
	}
	path := fmt.Sprintf("/sys/devices/system/cpu/cpu%d/online", cpu)
	data, err := os.ReadFile(path)
	if err != nil {
		dir := fmt.Sprintf("/sys/devices/system/cpu/cpu%d", cpu)
		if _, err := os.Stat(dir); err == nil {
			return true
		}
		return false
	}
	return strings.TrimSpace(string(data)) == "1"
}

func parseIsolatedCPUs() []int {
	data, err := os.ReadFile("/sys/devices/system/cpu/isolated")
	if err != nil {
		return nil
	}
	return parseCPUList(strings.TrimSpace(string(data)))
}

func readSiblingsList(cpu int) []int {
	path := filepath.Join(fmt.Sprintf("/sys/devices/system/cpu/cpu%d/topology/thread_siblings_list", cpu))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return parseCPUList(strings.TrimSpace(string(data)))
}

func parseCPUList(s string) []int {
	if s == "" {
		return nil
	}
	var result []int
	for _, token := range strings.Split(s, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if idx := strings.IndexByte(token, '-'); idx >= 0 {
			a, err1 := strconv.Atoi(token[:idx])
			b, err2 := strconv.Atoi(token[idx+1:])
			if err1 != nil || err2 != nil {
				continue
			}
			for i := a; i <= b; i++ {
				result = append(result, i)
			}
		} else {
			n, err := strconv.Atoi(token)
			if err != nil {
				continue
			}
			result = append(result, n)
		}
	}
	sort.Ints(result)
	return result
}
