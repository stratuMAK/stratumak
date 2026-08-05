// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pyvcpmodule

import (
	"encoding/xml"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
)

// HAL type/direction constants matching LinuxCNC conventions.
const (
	halBit   = 1
	halFloat = 2
	halS32   = 3
	halU32   = 4

	halIn  = 16
	halOut = 32
)

// widgetType mirrors the WidgetType enum in pyvcp.gmi.
type widgetType int

const (
	wtLED         widgetType = 1
	wtRectLED     widgetType = 2
	wtNumber      widgetType = 3
	wtU32         widgetType = 4
	wtS32         widgetType = 5
	wtTimer       widgetType = 6
	wtBar         widgetType = 7
	wtMeter       widgetType = 8
	wtMultilabel  widgetType = 9
	wtImageBit    widgetType = 10
	wtImageU32    widgetType = 11
	wtButton      widgetType = 12
	wtCheckbutton widgetType = 13
	wtRadiobutton widgetType = 14
	wtScale       widgetType = 15
	wtSpinbox     widgetType = 16
	wtDial        widgetType = 17
	wtJogwheel    widgetType = 18
)

// eventType mirrors the EventType enum in pyvcp.gmi.
type eventType int

const (
	evPress     eventType = 1
	evRelease   eventType = 2
	evToggle    eventType = 3
	evSelect    eventType = 4
	evSet       eventType = 5
	evIncrement eventType = 6
)

// pinRef holds a HAL pin handle (type-tagged).
type pinRef struct {
	halType  int
	dir      int
	bitPin   *hal.Pin[bool]
	floatPin *hal.Pin[float64]
	s32Pin   *hal.Pin[int32]
	u32Pin   *hal.Pin[uint32]
}

// The accessors are nil-safe on the receiver as well as the inner pin: a
// missing map lookup (w.pins[role]) yields a nil *pinRef, and defense in depth
// keeps a malformed panel or an unexpected role from panicking the controller.
func (p *pinRef) readBit() bool {
	if p != nil && p.bitPin != nil {
		return p.bitPin.Get()
	}
	return false
}

func (p *pinRef) readFloat() float64 {
	if p != nil && p.floatPin != nil {
		return p.floatPin.Get()
	}
	return 0
}

func (p *pinRef) readS32() int32 {
	if p != nil && p.s32Pin != nil {
		return p.s32Pin.Get()
	}
	return 0
}

func (p *pinRef) readU32() uint32 {
	if p != nil && p.u32Pin != nil {
		return p.u32Pin.Get()
	}
	return 0
}

func (p *pinRef) writeBit(v bool) {
	if p != nil && p.bitPin != nil {
		p.bitPin.Set(v)
	}
}

func (p *pinRef) writeFloat(v float64) {
	if p != nil && p.floatPin != nil {
		p.floatPin.Set(v)
	}
}

func (p *pinRef) writeS32(v int32) {
	if p != nil && p.s32Pin != nil {
		p.s32Pin.Set(v)
	}
}

// toS32 converts a float64 to int32 without Go's implementation-defined
// out-of-range conversion result: values outside the int32 range saturate.
func toS32(v float64) int32 {
	if math.IsNaN(v) {
		return 0
	}
	if v >= math.MaxInt32 {
		return math.MaxInt32
	}
	if v <= math.MinInt32 {
		return math.MinInt32
	}
	return int32(v)
}

// widgetDef describes a widget with its constraints, HAL pins, and current state.
type widgetDef struct {
	id         string
	autoBase   string // auto-generated base name (for param_pin naming)
	paramName  string // explicit param-pin name from the "halparam" attribute
	wtype      widgetType
	min        float64 // NaN = no lower limit
	max        float64 // NaN = no upper limit
	resolution float64 // 0 = no quantization
	choices    []string
	format     string
	text       string

	// HAL pins owned by this widget (keyed by role).
	// Roles are widget-specific: "value", "state", "disable", "changepin",
	// "reset", "run", "scale", "param", "choice.N", "legend.N", "-i", "-f"
	pins map[string]*pinRef

	// Current widget value (server-authoritative, protected by panel.mu).
	value float64
	state bool
	index int32

	// Edge detection / last-value tracking for input pins.
	lastChangepin bool    // previous changepin state (for rising-edge detection)
	lastParam     float64 // previous param_pin value (for change detection)
	timerReset    bool    // timer reset pin is active
}

// widgetState returns the state snapshot for this widget (for the watch).
type widgetState struct {
	Value    float64 // NaN = not applicable
	State    bool
	Index    int32 // -1 = not applicable
	Disabled bool
	Reset    bool // timer reset active
}

// readState reads the current widget state from HAL pins and returns the state.
func (w *widgetDef) readState() widgetState {
	s := widgetState{
		Value: math.NaN(),
		Index: -1,
	}

	switch w.wtype {
	case wtLED, wtRectLED:
		s.State = w.pins["state"].readBit()
		if dp, ok := w.pins["disable"]; ok {
			s.Disabled = dp.readBit()
		}

	case wtButton:
		s.State = w.pins["state"].readBit()
		if dp, ok := w.pins["disable"]; ok {
			s.Disabled = dp.readBit()
		}

	case wtCheckbutton:
		s.State = w.pins["state"].readBit()

	case wtRadiobutton:
		s.Index = 0
		for i := range w.choices {
			key := "choice." + strconv.Itoa(i)
			if p, ok := w.pins[key]; ok && p.readBit() {
				s.Index = int32(i)
				break
			}
		}

	case wtScale:
		s.Value = w.pins["-f"].readFloat()

	case wtSpinbox:
		s.Value = w.pins["value"].readFloat()

	case wtDial:
		s.Value = w.pins["value"].readFloat()

	case wtJogwheel:
		s.Value = w.pins["count"].readFloat()

	case wtNumber:
		s.Value = w.pins["value"].readFloat()

	case wtU32:
		s.Value = float64(w.pins["value"].readU32())

	case wtS32:
		s.Value = float64(w.pins["value"].readS32())

	case wtBar:
		s.Value = w.pins["value"].readFloat()

	case wtMeter:
		s.Value = w.pins["value"].readFloat()

	case wtMultilabel:
		s.Index = 0
		for i := 0; i < 6; i++ {
			key := "legend." + strconv.Itoa(i)
			if p, ok := w.pins[key]; ok && p.readBit() {
				s.Index = int32(i)
				break
			}
		}
		if dp, ok := w.pins["disable"]; ok {
			s.Disabled = dp.readBit()
		}

	case wtImageBit:
		s.Index = 0
		if w.pins["value"].readBit() {
			s.Index = 1
		}

	case wtImageU32:
		s.Index = int32(w.pins["value"].readU32())

	case wtTimer:
		// Server-authoritative elapsed seconds (accrued in scan()); the run
		// state and reset flag are advisory for the client's display.
		s.Value = w.value
		s.State = w.state
		s.Reset = w.timerReset
	}

	return s
}

// scan processes input pins (param_pin, changepin, reset, scale), accrues timer
// elapsed time, and updates output pins. It runs on the module's own periodic
// ticker (see pyvcpModule.run) so HAL-driven inputs are processed continuously,
// independent of whether any UI client is currently watching. Caller must hold
// p.mu.
func (p *panel) scan() {
	if p.closed {
		return
	}

	// Wall-clock delta since the previous scan, used to accrue timer elapsed
	// time. Guard the first tick (zero lastScan) and any long pause (suspend,
	// scheduling stall) so a single huge dt can't jump every running timer.
	now := time.Now()
	dt := now.Sub(p.lastScan)
	p.lastScan = now
	if dt < 0 || dt > time.Second {
		dt = 0
	}

	for _, w := range p.widgets {
		// Handle changepin (rising edge toggles checkbutton state).
		if w.wtype == wtCheckbutton {
			if cp, ok := w.pins["changepin"]; ok {
				cur := cp.readBit()
				if cur && !w.lastChangepin {
					// Rising edge — toggle the output.
					w.state = !w.pins["state"].readBit()
					w.pins["state"].writeBit(w.state)
				}
				w.lastChangepin = cur
			}
		}

		// Handle jogwheel reset pin (clear count on rising edge).
		if w.wtype == wtJogwheel {
			if rp, ok := w.pins["reset"]; ok {
				if rp.readBit() {
					w.value = 0
					w.pins["count"].writeFloat(0)
				}
			}
		}

		// Timer is server-authoritative: accrue elapsed seconds into w.value
		// while the run pin is high, zero it while reset is high. Clients just
		// render the value, so a client that connects mid-run sees the correct
		// elapsed time (the old client-computed model showed zero/garbage).
		if w.wtype == wtTimer {
			reset := false
			if rp, ok := w.pins["reset"]; ok {
				reset = rp.readBit()
			}
			w.timerReset = reset
			run := w.pins["run"].readBit()
			w.state = run
			if reset {
				w.value = 0
			} else if run {
				w.value += dt.Seconds()
			}
		}

		// Handle param_pin (external value override for scales/spinboxes/dials).
		if pp, ok := w.pins["param"]; ok {
			cur := pp.readFloat()
			if cur != w.lastParam {
				w.lastParam = cur
				switch w.wtype {
				case wtScale:
					v := w.clamp(cur)
					w.pins["-f"].writeFloat(v)
					w.pins["-i"].writeS32(toS32(v))
					w.value = v
				case wtSpinbox:
					v := w.clamp(w.quantize(cur))
					w.pins["value"].writeFloat(v)
					w.value = v
				case wtDial:
					v := w.clamp(cur)
					w.pins["value"].writeFloat(v)
					w.value = v
				}
			}
		}
	}
}

// handleEvent processes a widget event and updates HAL pins.
// Returns true if the event was accepted.
func (w *widgetDef) handleEvent(ev eventType, value float64, increment int32, index int32) bool {
	switch w.wtype {
	case wtButton:
		switch ev {
		case evPress:
			w.pins["state"].writeBit(true)
			w.state = true
			return true
		case evRelease:
			w.pins["state"].writeBit(false)
			w.state = false
			return true
		}

	case wtCheckbutton:
		if ev == evToggle {
			w.state = !w.pins["state"].readBit()
			w.pins["state"].writeBit(w.state)
			return true
		}

	case wtRadiobutton:
		if ev == evSelect {
			idx := int(index)
			if idx < 0 || idx >= len(w.choices) {
				return false
			}
			for i := range w.choices {
				key := "choice." + strconv.Itoa(i)
				if p, ok := w.pins[key]; ok {
					p.writeBit(i == idx)
				}
			}
			w.index = int32(idx)
			return true
		}

	case wtScale:
		switch ev {
		case evSet:
			if math.IsNaN(value) {
				return false
			}
			v := w.clamp(value)
			w.pins["-f"].writeFloat(v)
			w.pins["-i"].writeS32(toS32(v))
			w.value = v
			return true
		case evIncrement:
			if increment == 0 || w.resolution == 0 {
				return false
			}
			v := w.pins["-f"].readFloat() + float64(increment)*w.resolution
			v = w.clamp(v)
			w.pins["-f"].writeFloat(v)
			w.pins["-i"].writeS32(toS32(v))
			w.value = v
			return true
		}

	case wtSpinbox:
		switch ev {
		case evSet:
			if math.IsNaN(value) {
				return false
			}
			v := w.clamp(w.quantize(value))
			w.pins["value"].writeFloat(v)
			w.value = v
			return true
		case evIncrement:
			if increment == 0 || w.resolution == 0 {
				return false
			}
			v := w.pins["value"].readFloat() + float64(increment)*w.resolution
			v = w.clamp(v)
			w.pins["value"].writeFloat(v)
			w.value = v
			return true
		}

	case wtDial:
		switch ev {
		case evSet:
			if math.IsNaN(value) {
				return false
			}
			v := w.clamp(value)
			w.pins["value"].writeFloat(v)
			w.value = v
			return true
		case evIncrement:
			if increment == 0 || w.resolution == 0 {
				return false
			}
			v := w.pins["value"].readFloat() + float64(increment)*w.resolution
			v = w.clamp(v)
			w.pins["value"].writeFloat(v)
			w.value = v
			return true
		}

	case wtJogwheel:
		if ev == evIncrement {
			if increment == 0 {
				return false
			}
			v := w.pins["count"].readFloat() + float64(increment)
			w.pins["count"].writeFloat(v)
			w.value = v
			return true
		}
	}

	return false
}

// clamp applies min/max constraints.
func (w *widgetDef) clamp(v float64) float64 {
	if !math.IsNaN(w.min) && v < w.min {
		v = w.min
	}
	if !math.IsNaN(w.max) && v > w.max {
		v = w.max
	}
	return v
}

// quantize rounds to the nearest resolution step.
func (w *widgetDef) quantize(v float64) float64 {
	if w.resolution == 0 {
		return v
	}
	return math.Round(v/w.resolution) * w.resolution
}

// panel holds the parsed state of a PyVCP XML panel.
type panel struct {
	mu       sync.RWMutex
	name     string
	xml      string
	widgets  []*widgetDef
	byID     map[string]*widgetDef
	lastScan time.Time // wall-clock of the previous scan() (timer elapsed accrual)
	// closed is set under mu by the module's Destroy(). Once set, scan(),
	// readState() and handleEvent() must not touch HAL pins — the component is
	// about to be (or has been) freed. A watch pushLoop from an already-open
	// WS connection keeps its cb.WatchState closure after the instance is
	// unregistered, so this flag (plus the mu barrier in Destroy) is what
	// prevents a use-after-free on unload.
	closed bool
}

// createPins creates HAL pins on the component for all widgets.
func (p *panel) createPins(comp *hal.Component) error {
	for _, w := range p.widgets {
		for role, pin := range w.pins {
			dir := hal.In
			if pin.dir == halOut {
				dir = hal.Out
			}
			pinName := p.pinName(w, role)
			switch pin.halType {
			case halBit:
				h, err := hal.NewPin[bool](comp, pinName, dir)
				if err != nil {
					return fmt.Errorf("widget %q pin %q: %w", w.id, role, err)
				}
				pin.bitPin = h
			case halFloat:
				h, err := hal.NewPin[float64](comp, pinName, dir)
				if err != nil {
					return fmt.Errorf("widget %q pin %q: %w", w.id, role, err)
				}
				pin.floatPin = h
			case halS32:
				h, err := hal.NewPin[int32](comp, pinName, dir)
				if err != nil {
					return fmt.Errorf("widget %q pin %q: %w", w.id, role, err)
				}
				pin.s32Pin = h
			case halU32:
				h, err := hal.NewPin[uint32](comp, pinName, dir)
				if err != nil {
					return fmt.Errorf("widget %q pin %q: %w", w.id, role, err)
				}
				pin.u32Pin = h
			default:
				return fmt.Errorf("widget %q pin %q: unknown type %d", w.id, role, pin.halType)
			}
		}
	}
	return nil
}

// pinName returns the HAL pin name for a widget role.
// The naming must match the legacy pyvcp convention for HAL net compatibility.
func (p *panel) pinName(w *widgetDef, role string) string {
	// The widget ID IS the base HAL pin name (e.g. "button.0", "scale.0")
	switch role {
	case "value", "state", "count":
		// Primary pin — some widgets use the base name directly.
		switch w.wtype {
		case wtLED, wtRectLED:
			return w.id // led pin is just the ID
		case wtButton:
			return w.id
		case wtCheckbutton:
			return w.id
		case wtSpinbox:
			return w.id
		case wtNumber, wtBar, wtMeter:
			return w.id
		case wtU32, wtS32:
			return w.id
		case wtImageBit, wtImageU32:
			return w.id
		case wtDial:
			if w.id == w.autoBase {
				return w.id + ".out"
			}
			return w.id // explicit halpin used directly
		case wtJogwheel:
			if w.id == w.autoBase {
				return w.id + ".count"
			}
			return w.id // explicit halpin used directly
		}
		return w.id
	case "disable":
		return w.id + ".disable"
	case "changepin":
		return w.id + ".changepin"
	case "-f":
		return w.id + "-f"
	case "-i":
		return w.id + "-i"
	case "param":
		if w.paramName != "" {
			return w.paramName // explicit "halparam" name
		}
		if w.autoBase != "" {
			return w.autoBase + ".param_pin"
		}
		return w.id + ".param_pin"
	case "reset":
		return w.id + ".reset"
	case "run":
		return w.id + ".run"
	case "scale":
		return w.id + ".scale"
	default:
		// choice.N → w.id + "." + choiceName
		// legend.N → w.id + ".legendN"
		if strings.HasPrefix(role, "choice.") {
			idx, _ := strconv.Atoi(role[7:])
			if idx < len(w.choices) {
				return w.id + "." + w.choices[idx]
			}
		}
		if strings.HasPrefix(role, "legend.") {
			// "legend.0" → "legend0" (no dot between "legend" and number)
			return w.id + ".legend" + role[7:]
		}
		return w.id + "." + role
	}
}

// applyInitialValues writes initial values to OUT pins after component is ready.
func (p *panel) applyInitialValues() {
	for _, w := range p.widgets {
		switch w.wtype {
		case wtCheckbutton:
			if w.state {
				w.pins["state"].writeBit(true)
			}
		case wtRadiobutton:
			for i := range w.choices {
				key := "choice." + strconv.Itoa(i)
				if p, ok := w.pins[key]; ok {
					p.writeBit(i == int(w.index))
				}
			}
		case wtScale:
			w.pins["-f"].writeFloat(w.value)
			w.pins["-i"].writeS32(toS32(w.value))
		case wtSpinbox:
			w.pins["value"].writeFloat(w.value)
		case wtDial:
			w.pins["value"].writeFloat(w.value)
		case wtJogwheel:
			w.pins["count"].writeFloat(w.value)
		}
	}
}

// parsePanel reads a PyVCP XML file and extracts widget definitions.
func parsePanel(name, xmlPath string) (*panel, error) {
	data, err := os.ReadFile(xmlPath)
	if err != nil {
		return nil, fmt.Errorf("reading XML: %w", err)
	}

	p := &panel{
		name: name,
		xml:  string(data),
		byID: make(map[string]*widgetDef),
	}

	// Parse XML and walk the element tree.
	decoder := xml.NewDecoder(strings.NewReader(p.xml))
	if err := p.walkXML(decoder); err != nil {
		return nil, fmt.Errorf("parsing XML: %w", err)
	}

	return p, nil
}

// walkXML traverses the XML document and extracts widget definitions.
func (p *panel) walkXML(decoder *xml.Decoder) error {
	counters := make(map[string]int)

	type element struct {
		name     string
		children map[string]string
		attrs    map[string]string
	}

	var stack []*element

	for {
		tok, err := decoder.Token()
		if err != nil {
			break // EOF
		}

		switch t := tok.(type) {
		case xml.StartElement:
			e := &element{
				name:     t.Name.Local,
				children: make(map[string]string),
				attrs:    make(map[string]string),
			}
			for _, a := range t.Attr {
				e.attrs[a.Name.Local] = a.Value
			}
			stack = append(stack, e)

		case xml.CharData:
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				text := strings.TrimSpace(string(t))
				if text != "" {
					parent.children["_text"] = text
				}
			}

		case xml.EndElement:
			if len(stack) == 0 {
				continue
			}
			e := stack[len(stack)-1]
			stack = stack[:len(stack)-1]

			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				if text, ok := e.children["_text"]; ok {
					parent.children[e.name] = text
				}
			}

			// Check if this element is a known widget.
			w := extractWidget(e.name, e.children, e.attrs, counters)
			if w != nil {
				p.widgets = append(p.widgets, w)
				p.byID[w.id] = w
			}
		}
	}
	return nil
}

// --- Helper functions ---

func getParam(children, attrs map[string]string, key string) string {
	if v, ok := children[key]; ok {
		return stripQuotes(v)
	}
	if v, ok := attrs[key]; ok {
		return stripQuotes(v)
	}
	return ""
}

func getBoolParam(children, attrs map[string]string, key string) bool {
	v := getParam(children, attrs, key)
	return v == "1" || strings.EqualFold(v, "true")
}

func stripQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func autoName(widgetType string, counters map[string]int) string {
	n := counters[widgetType]
	counters[widgetType] = n + 1
	return widgetType + "." + strconv.Itoa(n)
}

func parseList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if (s[0] == '[' || s[0] == '(') && len(s) > 1 {
		s = s[1 : len(s)-1]
	}
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = stripQuotes(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func parseFloat(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// extractWidget returns a widgetDef for a known widget element, or nil.
func extractWidget(elemName string, children, attrs map[string]string, counters map[string]int) *widgetDef {
	halpin := getParam(children, attrs, "halpin")

	switch elemName {
	case "led", "rectled":
		if halpin == "" {
			halpin = autoName("led", counters)
		}
		wt := wtLED
		if elemName == "rectled" {
			wt = wtRectLED
		}
		w := &widgetDef{
			id:    halpin,
			wtype: wt,
			pins:  map[string]*pinRef{"state": {halType: halBit, dir: halIn}},
		}
		if getBoolParam(children, attrs, "disable_pin") {
			w.pins["disable"] = &pinRef{halType: halBit, dir: halIn}
		}
		return w

	case "button":
		if halpin == "" {
			halpin = autoName("button", counters)
		}
		w := &widgetDef{
			id:    halpin,
			wtype: wtButton,
			min:   math.NaN(),
			max:   math.NaN(),
			pins:  map[string]*pinRef{"state": {halType: halBit, dir: halOut}},
		}
		if getBoolParam(children, attrs, "disable_pin") || getBoolParam(children, attrs, "disablepin") {
			w.pins["disable"] = &pinRef{halType: halBit, dir: halIn}
		}
		return w

	case "checkbutton":
		if halpin == "" {
			halpin = autoName("checkbutton", counters)
		}
		w := &widgetDef{
			id:    halpin,
			wtype: wtCheckbutton,
			min:   math.NaN(),
			max:   math.NaN(),
			pins: map[string]*pinRef{
				"state":     {halType: halBit, dir: halOut},
				"changepin": {halType: halBit, dir: halIn},
			},
		}
		// Parse initial value.
		initval := getParam(children, attrs, "initval")
		if initval != "" {
			if v, err := strconv.ParseFloat(initval, 64); err == nil && v >= 0.5 {
				w.state = true
			}
		}
		return w

	case "radiobutton":
		if halpin == "" {
			halpin = autoName("radiobutton", counters)
		}
		choices := parseList(getParam(children, attrs, "choices"))
		initval := getParam(children, attrs, "initval")
		initIdx := 0
		if initval != "" {
			if v, err := strconv.Atoi(initval); err == nil {
				initIdx = v
			}
		}
		w := &widgetDef{
			id:      halpin,
			wtype:   wtRadiobutton,
			min:     math.NaN(),
			max:     math.NaN(),
			choices: choices,
			index:   int32(initIdx),
			pins:    make(map[string]*pinRef),
		}
		for i := range choices {
			w.pins["choice."+strconv.Itoa(i)] = &pinRef{halType: halBit, dir: halOut}
		}
		return w

	case "scale":
		autoBase := autoName("scale", counters)
		if halpin == "" {
			halpin = autoBase
		}
		initval := getParam(children, attrs, "initval")
		minVal := getParam(children, attrs, "min_")
		maxVal := getParam(children, attrs, "max_")
		resVal := getParam(children, attrs, "resolution")

		w := &widgetDef{
			id:       halpin,
			autoBase: autoBase,
			wtype:    wtScale,
			pins: map[string]*pinRef{
				"-f": {halType: halFloat, dir: halOut},
				"-i": {halType: halS32, dir: halOut},
			},
		}
		if v, ok := parseFloat(minVal); ok {
			w.min = v
		} else {
			w.min = 0
		}
		if v, ok := parseFloat(maxVal); ok {
			w.max = v
		} else {
			w.max = 10
		}
		if v, ok := parseFloat(resVal); ok {
			w.resolution = v
		} else {
			w.resolution = 1
		}
		if v, ok := parseFloat(initval); ok {
			w.value = v
		}
		if getBoolParam(children, attrs, "param_pin") {
			w.pins["param"] = &pinRef{halType: halFloat, dir: halIn}
			w.paramName = getParam(children, attrs, "halparam")
		}
		return w

	case "spinbox":
		autoBase := autoName("spinbox", counters)
		if halpin == "" {
			halpin = autoBase
		}
		initval := getParam(children, attrs, "initval")
		minVal := getParam(children, attrs, "min_")
		maxVal := getParam(children, attrs, "max_")
		resVal := getParam(children, attrs, "resolution")
		fmtVal := getParam(children, attrs, "format")

		w := &widgetDef{
			id:       halpin,
			autoBase: autoBase,
			wtype:    wtSpinbox,
			format:   fmtVal,
			pins:     map[string]*pinRef{"value": {halType: halFloat, dir: halOut}},
		}
		if v, ok := parseFloat(minVal); ok {
			w.min = v
		} else {
			w.min = 0
		}
		if v, ok := parseFloat(maxVal); ok {
			w.max = v
		} else {
			w.max = 100
		}
		if v, ok := parseFloat(resVal); ok {
			w.resolution = v
		} else {
			w.resolution = 1
		}
		if v, ok := parseFloat(initval); ok {
			w.value = v
		}
		if getBoolParam(children, attrs, "param_pin") {
			w.pins["param"] = &pinRef{halType: halFloat, dir: halIn}
			w.paramName = getParam(children, attrs, "halparam")
		}
		return w

	case "dial":
		autoBase := autoName("dial", counters)
		if halpin == "" {
			halpin = autoBase
		}
		initval := getParam(children, attrs, "initval")
		minVal := getParam(children, attrs, "min_")
		maxVal := getParam(children, attrs, "max_")
		resVal := getParam(children, attrs, "resolution")
		textVal := getParam(children, attrs, "text")

		w := &widgetDef{
			id:       halpin,
			autoBase: autoBase,
			wtype:    wtDial,
			text:     textVal,
			min:      math.NaN(),
			max:      math.NaN(),
			pins: map[string]*pinRef{
				"value": {halType: halFloat, dir: halOut},
			},
		}
		if v, ok := parseFloat(minVal); ok {
			w.min = v
		}
		if v, ok := parseFloat(maxVal); ok {
			w.max = v
		}
		if v, ok := parseFloat(resVal); ok {
			w.resolution = v
		} else {
			w.resolution = 0.1
		}
		if v, ok := parseFloat(initval); ok {
			w.value = v
		}
		// A dial ALWAYS has a param_pin (HAL can set the dial position),
		// matching Python pyvcp — unlike scale/spinbox where it is opt-in.
		w.pins["param"] = &pinRef{halType: halFloat, dir: halIn}
		w.paramName = getParam(children, attrs, "halparam")
		return w

	case "jogwheel":
		autoBase := autoName("jogwheel", counters)
		if halpin == "" {
			halpin = autoBase
		}
		textVal := getParam(children, attrs, "text")

		w := &widgetDef{
			id:       halpin,
			autoBase: autoBase,
			wtype:    wtJogwheel,
			min:      math.NaN(),
			max:      math.NaN(),
			text:     textVal,
			pins:     map[string]*pinRef{"count": {halType: halFloat, dir: halOut}},
		}
		if getBoolParam(children, attrs, "clear_pin") {
			w.pins["reset"] = &pinRef{halType: halBit, dir: halIn}
		}
		if getBoolParam(children, attrs, "scale_pin") {
			w.pins["scale"] = &pinRef{halType: halFloat, dir: halIn}
		}
		return w

	case "meter":
		if halpin == "" {
			halpin = autoName("meter", counters)
		}
		minVal := getParam(children, attrs, "min_")
		maxVal := getParam(children, attrs, "max_")
		textVal := getParam(children, attrs, "subtext")

		w := &widgetDef{
			id:    halpin,
			wtype: wtMeter,
			text:  textVal,
			min:   math.NaN(),
			max:   math.NaN(),
			pins:  map[string]*pinRef{"value": {halType: halFloat, dir: halIn}},
		}
		if v, ok := parseFloat(minVal); ok {
			w.min = v
		}
		if v, ok := parseFloat(maxVal); ok {
			w.max = v
		}
		return w

	case "bar":
		if halpin == "" {
			halpin = autoName("bar", counters)
		}
		minVal := getParam(children, attrs, "min_")
		maxVal := getParam(children, attrs, "max_")

		w := &widgetDef{
			id:    halpin,
			wtype: wtBar,
			min:   math.NaN(),
			max:   math.NaN(),
			pins:  map[string]*pinRef{"value": {halType: halFloat, dir: halIn}},
		}
		if v, ok := parseFloat(minVal); ok {
			w.min = v
		}
		if v, ok := parseFloat(maxVal); ok {
			w.max = v
		}
		return w

	case "number":
		if halpin == "" {
			halpin = autoName("number", counters)
		}
		fmtVal := getParam(children, attrs, "format")
		w := &widgetDef{
			id:     halpin,
			wtype:  wtNumber,
			min:    math.NaN(),
			max:    math.NaN(),
			format: fmtVal,
			pins:   map[string]*pinRef{"value": {halType: halFloat, dir: halIn}},
		}
		return w

	case "u32":
		if halpin == "" {
			halpin = autoName("number", counters) // shares counter with number
		}
		w := &widgetDef{
			id:    halpin,
			wtype: wtU32,
			min:   math.NaN(),
			max:   math.NaN(),
			pins:  map[string]*pinRef{"value": {halType: halU32, dir: halIn}},
		}
		return w

	case "s32":
		if halpin == "" {
			halpin = autoName("number", counters) // shares counter with number
		}
		w := &widgetDef{
			id:    halpin,
			wtype: wtS32,
			min:   math.NaN(),
			max:   math.NaN(),
			pins:  map[string]*pinRef{"value": {halType: halS32, dir: halIn}},
		}
		return w

	case "timer":
		if halpin == "" {
			halpin = autoName("timer", counters)
		}
		w := &widgetDef{
			id:    halpin,
			wtype: wtTimer,
			min:   math.NaN(),
			max:   math.NaN(),
			pins: map[string]*pinRef{
				"reset": {halType: halBit, dir: halIn},
				"run":   {halType: halBit, dir: halIn},
			},
		}
		return w

	case "multilabel":
		if halpin == "" {
			halpin = autoName("multilabel", counters)
		}
		legends := parseList(getParam(children, attrs, "legends"))
		w := &widgetDef{
			id:    halpin,
			wtype: wtMultilabel,
			min:   math.NaN(),
			max:   math.NaN(),
			pins:  make(map[string]*pinRef),
		}
		for i := range legends {
			if i >= 6 {
				break
			}
			w.pins["legend."+strconv.Itoa(i)] = &pinRef{halType: halBit, dir: halIn}
		}
		if getBoolParam(children, attrs, "disable_pin") {
			w.pins["disable"] = &pinRef{halType: halBit, dir: halIn}
		}
		return w

	case "image_bit":
		if halpin == "" {
			halpin = autoName("number", counters) // shares counter
		}
		w := &widgetDef{
			id:    halpin,
			wtype: wtImageBit,
			min:   math.NaN(),
			max:   math.NaN(),
			pins:  map[string]*pinRef{"value": {halType: halBit, dir: halIn}},
		}
		return w

	case "image_u32":
		if halpin == "" {
			halpin = autoName("number", counters) // shares counter
		}
		w := &widgetDef{
			id:    halpin,
			wtype: wtImageU32,
			min:   math.NaN(),
			max:   math.NaN(),
			pins:  map[string]*pinRef{"value": {halType: halU32, dir: halIn}},
		}
		return w

	default:
		return nil
	}
}
