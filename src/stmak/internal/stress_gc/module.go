// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// Package stress_gc registers a load-generator module that deliberately
// produces Go garbage-collector pressure, for latency testing: it lets you
// measure whether the Go runtime's GC (mark work, assists, stop-the-world
// pauses) leaks into RT scheduling jitter.
//
// It is a plain gomod with no HAL and no INI: loading it turns it on, not
// loading it leaves it off, and its parameters ride on the HAL load line.  It
// therefore only ever runs where a config explicitly asks for it.
//
// Usage in a HAL file:
//
//	loadrt stress_gc [workers=N] [live=MiB]
//
// Parameters:
//   - workers=N  — number of goroutines churning garbage (default 2)
//   - live=MiB   — size of a retained, pointer-heavy live set that the GC must
//     mark every cycle (default 64); 0 disables the live set
package stress_gc

import (
	"fmt"
	"log/slog"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/stmak"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
)

func init() {
	stmak.RegisterModule("stress_gc", newStressGC)
}

const (
	objSize     = 256 // bytes per allocated object
	batch       = 64  // objects allocated per worker iteration
	defaultWork = 2
	defaultLive = 64 // MiB
	maxWorkers  = 256
	// Cap the live-set *data* at 4 GiB so a typo cannot OOM the box.  The
	// retained footprint is ~1.15x this once per-object overhead (the next
	// pointer, slice header) and the []*obj index are counted.
	maxLiveMiB = 4096
)

// obj is intentionally pointer-heavy: the `next` pointer gives the GC real
// references to trace, so marking the live set (and the churn) costs mark time
// rather than being a cheap pointer-free byte scan.
type obj struct {
	next *obj
	data []byte
}

func newObj() *obj { return &obj{data: make([]byte, objSize)} }

type stressGC struct {
	logger    *slog.Logger
	workers   int
	liveMiB   int
	live      []*obj // retained for the module lifetime -> marked every GC
	stopCh    chan struct{}
	wg        sync.WaitGroup
	iters     atomic.Uint64
	startGC   uint32
	started   time.Time
	startedOK bool      // Start() actually ran (workers launched)
	stopOnce  sync.Once // Stop() is safe to call more than once
}

func newStressGC(_ *inifile.IniFile, logger *slog.Logger, name string, args []string) (stmak.Module, error) {
	workers, liveMiB := defaultWork, defaultLive
	for _, a := range args {
		k, v, ok := strings.Cut(a, "=")
		if !ok {
			return nil, fmt.Errorf("stress_gc: bad parameter %q (want key=value)", a)
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return nil, fmt.Errorf("stress_gc: %s=%q is not an integer", k, v)
		}
		switch strings.TrimSpace(k) {
		case "workers":
			workers = n
		case "live":
			liveMiB = n
		default:
			return nil, fmt.Errorf("stress_gc: unknown parameter %q", k)
		}
	}
	if workers < 1 {
		workers = 1
	}
	if workers > maxWorkers {
		workers = maxWorkers
	}
	if liveMiB < 0 {
		liveMiB = 0
	}
	if liveMiB > maxLiveMiB {
		liveMiB = maxLiveMiB
	}

	return &stressGC{
		logger:  logger.With("module", name),
		workers: workers,
		liveMiB: liveMiB,
		stopCh:  make(chan struct{}),
	}, nil
}

func (m *stressGC) Start() error {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	m.startGC = ms.NumGC
	m.started = time.Now()

	// Build the retained, pointer-heavy live set once.  Chaining the objects
	// through `next` keeps them referenced and gives the GC a real graph to
	// mark on every cycle.
	if m.liveMiB > 0 {
		n := m.liveMiB * 1024 * 1024 / objSize
		m.live = make([]*obj, n)
		for i := range m.live {
			m.live[i] = newObj()
			if i > 0 {
				m.live[i].next = m.live[i-1]
			}
		}
	}

	m.logger.Info("stress_gc started", "workers", m.workers, "liveMiB", m.liveMiB)
	for i := 0; i < m.workers; i++ {
		m.wg.Add(1)
		go m.churn()
	}
	m.startedOK = true
	return nil
}

// churn allocates short-lived, pointer-heavy garbage as fast as it can, driving
// frequent GC cycles.  `keep` retains the current batch for one iteration so
// escape analysis cannot elide the allocations; the previous batch becomes
// garbage each iteration.
func (m *stressGC) churn() {
	defer m.wg.Done()
	var keep []*obj
	for {
		select {
		case <-m.stopCh:
			runtime.KeepAlive(keep)
			return
		default:
		}
		junk := make([]*obj, batch)
		for i := range junk {
			junk[i] = newObj()
			if i > 0 {
				junk[i].next = junk[i-1]
			}
		}
		keep = junk
		m.iters.Add(1)
	}
}

func (m *stressGC) Stop() {
	m.stopOnce.Do(func() {
		// The launcher stops every loaded module even if a peer's Start()
		// failed first, so Stop() can run without a matching Start().  Skip
		// the teardown (and its bogus 2000-year "elapsed" / whole-process GC
		// count) when nothing was started.
		if !m.startedOK {
			return
		}
		close(m.stopCh)
		m.wg.Wait()
		m.live = nil

		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		m.logger.Info("stress_gc stopped",
			"gcCycles", ms.NumGC-m.startGC,
			"batches", m.iters.Load(),
			"elapsed", time.Since(m.started).Round(time.Millisecond).String())
	})
}

func (m *stressGC) Destroy() {} // no HAL component to tear down
