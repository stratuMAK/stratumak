// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// Package pyvcpmodule registers the PyVCP panel module with the stratuMAK module
// registry. When compiled into the stmakd binary, this package's init()
// function registers a factory that creates PyVCP panel instances in response
// to HAL "load pyvcp" commands.
//
// Each instance parses a PyVCP XML file, extracts widget definitions,
// creates a HAL component with the required pins, and provides REST + WebSocket
// endpoints for frontends to display/control the panel.
//
// Protocol: widget-centric
//   - Client sends widget events (press/release/increment/set/toggle/select)
//   - Server owns all state: clamping, quantization, pin derivation (-i from -f)
//   - Watch returns map<widget_id, WidgetState> with delta encoding
//   - Multiple clients sync via shared server-authoritative state
//
// Usage in a HAL file:
//
//	load pyvcp [mypanel] xml=panel.xml
//
// Parameters:
//   - xml=<path>  — path to the PyVCP XML file (required; resolved relative
//     to the INI file directory if not absolute)
package pyvcpmodule

import (
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/pyvcp"
	"github.com/stratuMAK/stratumak/src/stmak/internal/apiserver"
	"github.com/stratuMAK/stratumak/src/stmak/internal/pathres"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/stmak"
)

func init() {
	stmak.RegisterModule("pyvcp", newPyVCPModule)

	// Register REST meta so the HTTP server knows about pyvcp routes.
	apiserver.RegisterMeta(pyvcp.PyvcpMeta)
}

// nanToZero converts NaN to 0 for JSON-safe serialization of constraints.
// Used for WidgetDef min/max/resolution where 0 means "not applicable".
func nanToZero(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return v
}

// nanToNull converts a float64 to *float64, returning nil for NaN.
// Used for WidgetState.Value where null means "not applicable" and 0.0
// is a valid reading.
func nanToNull(v float64) *float64 {
	if math.IsNaN(v) {
		return nil
	}
	return &v
}

// emptyToNull converts a string to *string, returning nil for the empty string.
// Used for the nullable WidgetDef.Format / WidgetDef.Text display hints.
func emptyToNull(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// scanInterval is how often the module processes HAL inputs and accrues timer
// elapsed time, independent of any UI client.
const scanInterval = 100 * time.Millisecond

// pyvcpModule implements stmak.Module for a PyVCP panel.
type pyvcpModule struct {
	logger *slog.Logger
	comp   *hal.Component
	panel  *panel
	stopCh chan struct{} // closed by Stop() to end the scan loop
	doneCh chan struct{} // closed by the scan loop when it exits
}

// Start launches the periodic scan loop. HAL-driven inputs (changepin,
// jogwheel reset, param_pin) and timer elapsed time are processed here so the
// panel behaves correctly whether or not a UI client is connected.
func (m *pyvcpModule) Start() error {
	m.stopCh = make(chan struct{})
	m.doneCh = make(chan struct{})
	m.panel.mu.Lock()
	m.panel.lastScan = time.Now()
	m.panel.mu.Unlock()
	go m.run()
	return nil
}

func (m *pyvcpModule) run() {
	defer close(m.doneCh)
	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.panel.mu.Lock()
			m.panel.scan()
			m.panel.mu.Unlock()
		}
	}
}

// Stop ends the scan loop and waits for it to exit. Idempotent: the launcher
// calls Stop() before Destroy(), and Destroy() calls Stop() again defensively.
func (m *pyvcpModule) Stop() {
	if m.stopCh == nil {
		return // Start() never ran
	}
	select {
	case <-m.stopCh: // already closed
	default:
		close(m.stopCh)
	}
	<-m.doneCh
}

func (m *pyvcpModule) Destroy() {
	m.Stop()

	// Mark the panel closed under mu before freeing the HAL component. An
	// already-open WS connection's watch pushLoop keeps calling the
	// WatchState closure even after the instance is unregistered (the
	// closure was captured at subscribe time, and its context is tied to the
	// connection, not to this module). The closed flag — set here under the
	// same lock that every pin accessor takes — guarantees no goroutine touches
	// a pin after this point, closing the unload use-after-free.
	m.panel.mu.Lock()
	m.panel.closed = true
	m.panel.mu.Unlock()

	panelRegistry.unregister(m.panel.name)

	if m.comp != nil {
		if err := m.comp.Exit(); err != nil {
			m.logger.Debug("pyvcp HAL component exit error", "name", m.panel.name, "error", err)
		}
	}
}

func newPyVCPModule(ini *inifile.IniFile, logger *slog.Logger, name string, args []string) (stmak.Module, error) {
	xmlPath := ""
	for _, arg := range args {
		k, v, ok := strings.Cut(arg, "=")
		if !ok {
			continue
		}
		if k == "xml" {
			xmlPath = v
		}
	}
	if xmlPath == "" {
		return nil, fmt.Errorf("pyvcp: missing required xml= parameter")
	}

	// Configuration paths are server-side paths resolved by the shared rule
	// (config dir, then HALLIB_PATH, contained within them) — see
	// internal/pathres.
	xmlPath, err := pathres.Resolve(xmlPath, pathres.Read)
	if err != nil {
		return nil, fmt.Errorf("pyvcp: xml=: %w", err)
	}

	logger = logger.With("module", "pyvcp", "name", name)
	logger.Info("loading PyVCP panel", "xml", xmlPath)

	// Parse XML and extract widget definitions.
	p, err := parsePanel(name, xmlPath)
	if err != nil {
		return nil, fmt.Errorf("pyvcp %q: %w", name, err)
	}

	// Create HAL component.
	comp, err := hal.NewComponent(name)
	if err != nil {
		return nil, fmt.Errorf("pyvcp %q: creating HAL component: %w", name, err)
	}

	// Create all HAL pins for all widgets.
	if err := p.createPins(comp); err != nil {
		return nil, fmt.Errorf("pyvcp %q: creating pins: %w", name, err)
	}

	if err := comp.Ready(); err != nil {
		return nil, fmt.Errorf("pyvcp %q: hal ready: %w", name, err)
	}

	// Apply initial values to OUT pins.
	p.applyInitialValues()

	// Register with the panel registry for REST/WS access.
	panelRegistry.register(p)

	// Register the API instance with the apiserver registry.
	cb := &pyvcpCallbacks{panel: p, comp: comp, logger: logger}
	if err := pyvcp.RegisterPyvcpAPI(apiserver.DefaultRegistry(), name, cb); err != nil {
		return nil, fmt.Errorf("pyvcp %q: api register: %w", name, err)
	}

	// Register the WebSocket watch API. watch_state is declared in the IDL
	// (map[string]WidgetState, @watch_delta), so registration and the
	// Delta:true per-connection change tracking are generated code.
	if apiserver.DefaultWatchRegistry() == nil {
		apiserver.SetDefaultWatchRegistry(apiserver.NewWatchRegistry())
	}
	pyvcp.RegisterPyvcpWatch(apiserver.DefaultWatchRegistry(), name, cb, pyvcp.PyvcpCommands(cb))

	logger.Info("PyVCP panel initialized", "name", name, "widgets", len(p.widgets))

	return &pyvcpModule{
		logger: logger,
		comp:   comp,
		panel:  p,
	}, nil
}

// --- Panel registry (shared across all pyvcp instances) ---

var panelRegistry = newPanelRegistry()

type panelRegistry_ struct {
	mu     sync.RWMutex
	panels map[string]*panel
}

func newPanelRegistry() *panelRegistry_ {
	return &panelRegistry_{panels: make(map[string]*panel)}
}

func (r *panelRegistry_) register(p *panel) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.panels[p.name] = p
}

func (r *panelRegistry_) unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.panels, name)
}

func (r *panelRegistry_) get(name string) *panel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.panels[name]
}

func (r *panelRegistry_) list() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.panels))
	for name := range r.panels {
		names = append(names, name)
	}
	return names
}

// pyvcpCallbacks holds the state for one panel's API callbacks. It implements
// the generated pyvcp.PyvcpCallbacks (REST + widget_event command) and the
// generated pyvcp.PyvcpWatchCallbacks (watch_state, map[string]WidgetState).
type pyvcpCallbacks struct {
	panel  *panel
	comp   *hal.Component
	logger *slog.Logger
}

// --- PyvcpCallbacks implementation (REST + WS commands) ---

func (cb *pyvcpCallbacks) ListPanels() ([]string, error) {
	return panelRegistry.list(), nil
}

func (cb *pyvcpCallbacks) GetPanel(name string) (*pyvcp.PanelInfo, error) {
	p := panelRegistry.get(name)
	if p == nil {
		return nil, fmt.Errorf("panel %q not found", name)
	}

	widgets := make([]pyvcp.WidgetDef, len(p.widgets))
	for i, w := range p.widgets {
		widgets[i] = pyvcp.WidgetDef{
			Id:   w.id,
			Type: pyvcp.WidgetType(w.wtype),
			// min/max are nullable: null means "no limit", which is distinct
			// from a real limit of 0 (a 0..100 scale, a -100..0 bar).
			Min:        nanToNull(w.min),
			Max:        nanToNull(w.max),
			Resolution: nanToZero(w.resolution), // 0 = continuous (unambiguous)
			Choices:    w.choices,
			// format/text are nullable strings (null = not set).
			Format: emptyToNull(w.format),
			Text:   emptyToNull(w.text),
		}
	}

	return &pyvcp.PanelInfo{
		Name:    p.name,
		Xml:     p.xml,
		Widgets: widgets,
	}, nil
}

func (cb *pyvcpCallbacks) WidgetEvent(event pyvcp.WidgetEvent) (bool, error) {
	p := cb.panel
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return false, fmt.Errorf("panel %q is shutting down", p.name)
	}

	w, ok := p.byID[event.Widget]
	if !ok {
		return false, fmt.Errorf("widget %q not found", event.Widget)
	}

	accepted := w.handleEvent(eventType(event.Event), event.Value, event.Increment, event.Index)
	if !accepted {
		cb.logger.Debug("widget event rejected",
			"widget", event.Widget,
			"event", event.Event,
		)
	}
	return accepted, nil
}

// --- WebSocket watch (PyvcpWatchCallbacks, registered via RegisterPyvcpWatch) ---

// WatchState returns the current state of all widgets, keyed by widget ID.
// The generated registration marshals it and the @watch_delta contract sends
// only changed widgets after the initial full snapshot. Input processing is
// done by the module's own scan loop (see run), not here, so state is pushed
// even with no client.
func (cb *pyvcpCallbacks) WatchState() (map[string]pyvcp.WidgetState, error) {
	p := cb.panel
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.closed {
		// The instance is being torn down; a pushLoop from an already-open
		// connection may still call this. Return an empty map — never touch
		// a pin, which may already be freed.
		return map[string]pyvcp.WidgetState{}, nil
	}

	states := make(map[string]pyvcp.WidgetState, len(p.widgets))
	for _, w := range p.widgets {
		ws := w.readState()
		states[w.id] = pyvcp.WidgetState{
			Value:    nanToNull(ws.Value),
			State:    ws.State,
			Index:    ws.Index,
			Disabled: ws.Disabled,
			Reset:    ws.Reset,
		}
	}
	return states, nil
}
