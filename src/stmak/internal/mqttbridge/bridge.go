// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package mqttbridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
)

// --- Pin holder (mirrors haljson pattern) ---

type mqttPin struct {
	name   string
	typ    PinType
	dir    TopicDir
	bitPin *hal.Pin[bool]
	fltPin *hal.Pin[float64]
	s32Pin *hal.Pin[int32]
	u32Pin *hal.Pin[uint32]
}

func (p *mqttPin) read() interface{} {
	switch p.typ {
	case PinTypeBit:
		if p.bitPin != nil {
			return p.bitPin.Get()
		}
	case PinTypeFloat:
		if p.fltPin != nil {
			return p.fltPin.Get()
		}
	case PinTypeS32:
		if p.s32Pin != nil {
			return p.s32Pin.Get()
		}
	case PinTypeU32:
		if p.u32Pin != nil {
			return p.u32Pin.Get()
		}
	}
	return nil
}

func (p *mqttPin) write(raw json.RawMessage) error {
	switch p.typ {
	case PinTypeBit:
		if p.bitPin == nil {
			return nil
		}
		var v bool
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		p.bitPin.Set(v)
	case PinTypeFloat:
		if p.fltPin == nil {
			return nil
		}
		var v float64
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		p.fltPin.Set(v)
	case PinTypeS32:
		if p.s32Pin == nil {
			return nil
		}
		var v int32
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		p.s32Pin.Set(v)
	case PinTypeU32:
		if p.u32Pin == nil {
			return nil
		}
		var v uint32
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		p.u32Pin.Set(v)
	}
	return nil
}

func (p *mqttPin) writeString(s string) error {
	switch p.typ {
	case PinTypeBit:
		if p.bitPin == nil {
			return nil
		}
		v := s == "1" || s == "true" || s == "TRUE"
		p.bitPin.Set(v)
	case PinTypeFloat:
		if p.fltPin == nil {
			return nil
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		p.fltPin.Set(v)
	case PinTypeS32:
		if p.s32Pin == nil {
			return nil
		}
		v, err := strconv.ParseInt(s, 10, 32)
		if err != nil {
			return err
		}
		p.s32Pin.Set(int32(v))
	case PinTypeU32:
		if p.u32Pin == nil {
			return nil
		}
		v, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return err
		}
		p.u32Pin.Set(uint32(v))
	}
	return nil
}

// --- Topic handler ---

type topicHandler struct {
	cfg  TopicConfig
	pins []*mqttPin // single pin for ModePin, multiple for ModeJSON
	// Shadow values for change detection (publish mode)
	shadow []interface{}
	// errLogged suppresses repeat publish-failure logs while a failure streak
	// lasts: publishLoop ticks every cfg.Rate, so a disconnected broker would
	// otherwise flood the log. Only touched by this topic's publishLoop.
	errLogged bool
}

func (t *topicHandler) hasChanged() bool {
	for i, pin := range t.pins {
		cur := pin.read()
		if cur != t.shadow[i] {
			return true
		}
	}
	return false
}

func (t *topicHandler) updateShadow() {
	for i, pin := range t.pins {
		t.shadow[i] = pin.read()
	}
}

func (t *topicHandler) buildPayload() []byte {
	switch t.cfg.Mode {
	case ModePin:
		// Raw value as string
		v := t.pins[0].read()
		switch val := v.(type) {
		case bool:
			if val {
				return []byte("true")
			}
			return []byte("false")
		case float64:
			return []byte(strconv.FormatFloat(val, 'f', -1, 64))
		case int32:
			return []byte(strconv.FormatInt(int64(val), 10))
		case uint32:
			return []byte(strconv.FormatUint(uint64(val), 10))
		default:
			return []byte("null")
		}
	case ModeJSON:
		return t.buildJSONPayload()
	}
	return nil
}

func (t *topicHandler) buildJSONPayload() []byte {
	obj := make(map[string]interface{}, len(t.pins))
	switch t.cfg.PublishMode {
	case PublishDelta:
		for i, pin := range t.pins {
			cur := pin.read()
			if cur != t.shadow[i] {
				obj[pin.name] = cur
			}
		}
	default: // Full or Always
		for _, pin := range t.pins {
			obj[pin.name] = pin.read()
		}
	}
	data, _ := json.Marshal(obj)
	return data
}

// --- Bridge ---

// mqttClient is the subset of mqtt.Client the bridge actually uses. Depending
// on the interface rather than the concrete client lets the tests drive the
// publish/subscribe failure paths (disconnected client, errored token) without
// a broker; mqtt.Client satisfies it.
type mqttClient interface {
	Disconnect(quiesce uint)
	IsConnected() bool
	Publish(topic string, qos byte, retained bool, payload interface{}) mqtt.Token
	Subscribe(topic string, qos byte, callback mqtt.MessageHandler) mqtt.Token
}

// errNotConnected marks a publish that never left the process because the
// client is disconnected — paho would buffer or drop it.
var errNotConnected = errors.New("MQTT client not connected")

type bridge struct {
	logger   *slog.Logger
	cfg      *Config
	client   mqttClient
	handlers []*topicHandler
	stopCh   chan struct{}
	wg       sync.WaitGroup

	// dryrun skips the real broker connection: publish ticks run and the
	// publish-count liveness pin advances, but nothing is sent. Used for
	// offline testing/diagnostics (mirrors the classic mqtt-publisher --dryrun).
	dryrun bool
	// pubCount is a HAL output pin (<name>.publish-count) incremented on every
	// publish tick whose payload was actually handed to a connected client — a
	// liveness signal a supervisor (or a test) can watch to confirm the bridge
	// is publishing. It deliberately does NOT advance for a payload that was
	// rejected or that never left the process (see publishPayload), so a
	// supervisor cannot be told "still publishing" while the broker is gone.
	pubCount *hal.Pin[uint32]
	// pubErrCount is a HAL output pin (<name>.publish-error-count) counting the
	// publish attempts that failed. Paired with pubCount it gives a supervisor
	// the full picture: a stalled pubCount with a rising pubErrCount means the
	// bridge is alive but the broker is not.
	pubErrCount *hal.Pin[uint32]
	// pubCounter/pubErrCounter back the two pins. Every DirOut topic runs its
	// own publishLoop goroutine, so the old Get()+1/Set() read-modify-write on
	// the pin lost increments (and raced). Atomic Add keeps it lossless; the pin
	// Set is serialized by the Pin's own mutex.
	pubCounter    atomic.Uint32
	pubErrCounter atomic.Uint32
}

func newBridge(comp *hal.Component, compName string, cfg *Config, logger *slog.Logger, dryrun bool) (*bridge, error) {
	b := &bridge{
		logger: logger,
		cfg:    cfg,
		stopCh: make(chan struct{}),
		dryrun: dryrun,
	}

	// Liveness pin: advances each time a topic publishes (real or dryrun).
	// hal.NewPin prefixes the component name, so pass the bare pin name.
	pc, err := hal.NewPin[uint32](comp, "publish-count", hal.Out)
	if err != nil {
		return nil, fmt.Errorf("creating pin %q: %w", compName+".publish-count", err)
	}
	b.pubCount = pc

	pec, err := hal.NewPin[uint32](comp, "publish-error-count", hal.Out)
	if err != nil {
		return nil, fmt.Errorf("creating pin %q: %w", compName+".publish-error-count", err)
	}
	b.pubErrCount = pec

	// Create HAL pins and topic handlers.
	for i := range cfg.Topics {
		tc := &cfg.Topics[i]
		th := &topicHandler{cfg: *tc}

		switch tc.Mode {
		case ModePin:
			// Single pin; derive HAL pin name from topic path.  hal.NewPin
			// already prefixes the component name, so pass the bare name (the
			// old compName+"." prefix here double-prefixed every pin).
			halName := topicToHalName(tc.Path)
			pin, err := createPin(comp, halName, tc.HalType, tc.Dir)
			if err != nil {
				return nil, err
			}
			th.pins = []*mqttPin{pin}
			th.shadow = []interface{}{nil}

		case ModeJSON:
			// Multiple pins under a topic slug prefix.  hal.NewPin adds the
			// component-name prefix; use just slug.pin here.
			slug := topicToHalName(tc.Path)
			for _, pc := range tc.Pins {
				pinName := slug + "." + pc.Name
				dir := pc.Dir
				// For subscribe topics, pins are outputs (component writes them).
				// For publish topics, pins are inputs (component reads them).
				pin, err := createPin(comp, pinName, pc.Type, dir)
				if err != nil {
					return nil, err
				}
				th.pins = append(th.pins, pin)
			}
			th.shadow = make([]interface{}, len(th.pins))
		}

		b.handlers = append(b.handlers, th)
	}

	return b, nil
}

func (b *bridge) start() error {
	if b.dryrun {
		// Skip the broker entirely; just run the publish loops so the
		// publish-count pin advances and pin plumbing is exercised.
		b.logger.Info("MQTT dryrun: not connecting to a broker", "broker", b.cfg.Broker)
		for _, th := range b.handlers {
			if th.cfg.Dir == DirOut {
				b.wg.Add(1)
				go b.publishLoop(th)
			}
		}
		return nil
	}

	// Configure MQTT client.
	opts := mqtt.NewClientOptions()
	opts.AddBroker(b.cfg.Broker)
	opts.SetClientID(b.cfg.ClientID)
	if b.cfg.Username != "" {
		opts.SetUsername(b.cfg.Username)
		opts.SetPassword(b.cfg.Password)
	}
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)
	opts.SetOnConnectHandler(b.onConnect)
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		b.logger.Warn("MQTT connection lost", "error", err)
	})

	client := mqtt.NewClient(opts)
	b.client = client
	token := client.Connect()
	token.Wait()
	if err := token.Error(); err != nil {
		return fmt.Errorf("MQTT connect: %w", err)
	}

	b.logger.Info("MQTT connected", "broker", b.cfg.Broker)

	// Start publish goroutines.
	for _, th := range b.handlers {
		if th.cfg.Dir == DirOut {
			b.wg.Add(1)
			go b.publishLoop(th)
		}
	}

	return nil
}

func (b *bridge) stop() {
	close(b.stopCh)
	b.wg.Wait()
	if b.client != nil && b.client.IsConnected() {
		b.client.Disconnect(1000)
	}
}

// onConnect is called when MQTT connects (or reconnects). Subscribe to all
// input topics here so subscriptions survive reconnection.
func (b *bridge) onConnect(_ mqtt.Client) {
	for _, th := range b.handlers {
		if th.cfg.Dir == DirIn {
			handler := th // capture
			tok := b.client.Subscribe(th.cfg.Path, th.cfg.QoS, func(_ mqtt.Client, msg mqtt.Message) {
				b.handleMessage(handler, msg)
			})
			// onConnect runs once per (re)connect, not in a hot path — wait so a
			// failed subscription (input topic that would silently never deliver)
			// is surfaced rather than dropped.
			if tok.Wait() && tok.Error() != nil {
				b.logger.Warn("MQTT subscribe failed", "topic", th.cfg.Path, "error", tok.Error())
			} else {
				b.logger.Debug("subscribed", "topic", th.cfg.Path)
			}
		}
	}
}

func (b *bridge) handleMessage(th *topicHandler, msg mqtt.Message) {
	// Runs in a paho callback goroutine, outside net/http's recover. A panic
	// here (e.g. a future pin-shape assumption breaking on unexpected input)
	// would otherwise kill the whole controller; drop the message instead.
	defer func() {
		if r := recover(); r != nil {
			b.logger.Error("MQTT message handler panic", "topic", th.cfg.Path, "panic", r)
		}
	}()

	payload := msg.Payload()

	switch th.cfg.Mode {
	case ModePin:
		// Raw value → single pin.
		if err := th.pins[0].writeString(string(payload)); err != nil {
			b.logger.Warn("MQTT pin write error",
				"topic", th.cfg.Path, "error", err)
		}
	case ModeJSON:
		// JSON object → multiple pins.
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(payload, &obj); err != nil {
			b.logger.Warn("MQTT JSON parse error",
				"topic", th.cfg.Path, "error", err)
			return
		}
		for _, pin := range th.pins {
			raw, ok := obj[pin.name]
			if !ok {
				continue
			}
			if err := pin.write(raw); err != nil {
				b.logger.Warn("MQTT pin write error",
					"topic", th.cfg.Path, "pin", pin.name, "error", err)
			}
		}
	}
}

func (b *bridge) publishLoop(th *topicHandler) {
	defer b.wg.Done()
	// Spawned goroutine — not covered by net/http recover. A panic here would
	// crash the controller; log and exit this loop instead.
	defer func() {
		if r := recover(); r != nil {
			b.logger.Error("MQTT publish loop panic", "topic", th.cfg.Path, "panic", r)
		}
	}()
	ticker := time.NewTicker(th.cfg.Rate)
	defer ticker.Stop()

	for {
		select {
		case <-b.stopCh:
			return
		case <-ticker.C:
			b.publishTick(th)
		}
	}
}

func (b *bridge) publishTick(th *topicHandler) {
	switch th.cfg.PublishMode {
	case PublishAlways:
		// Always publish regardless of changes.
	case PublishFull, PublishDelta:
		if !th.hasChanged() {
			return
		}
	}

	payload := th.buildPayload()
	if payload == nil {
		th.updateShadow()
		return
	}

	if err := b.publishPayload(th, payload); err != nil {
		// The shadow is deliberately NOT updated: leaving it stale makes the
		// next tick see the same change again and retry it, instead of silently
		// dropping the value that failed to go out.
		b.pubErrCount.Set(b.pubErrCounter.Add(1))
		if !th.errLogged {
			b.logger.Warn("MQTT publish failed", "topic", th.cfg.Path, "error", err)
			th.errLogged = true
		}
		return
	}
	if th.errLogged {
		b.logger.Info("MQTT publish recovered", "topic", th.cfg.Path)
		th.errLogged = false
	}

	th.updateShadow()
	// Advance the liveness counter for both real and dryrun publishes.
	b.pubCount.Set(b.pubCounter.Add(1))
}

// publishPayload hands one payload to the MQTT client.
//
// It stays fire-and-forget — blocking on a QoS>=1 token would stall publishLoop
// for a full broker round-trip on every tick — but it does report the failures
// that are knowable without blocking: a client that is disconnected (paho would
// queue or drop the message, and the liveness pin must not claim otherwise) and
// a token that has already completed with an error. A token still in flight has
// left the client and counts as published.
func (b *bridge) publishPayload(th *topicHandler, payload []byte) error {
	if b.dryrun || b.client == nil {
		return nil
	}
	if !b.client.IsConnected() {
		return errNotConnected
	}
	tok := b.client.Publish(th.cfg.Path, th.cfg.QoS, th.cfg.Retain, payload)
	if tok == nil {
		return nil
	}
	select {
	case <-tok.Done():
		return tok.Error()
	default:
		return nil
	}
}

// --- Helpers ---

// topicToHalName converts an MQTT topic path to a HAL-safe pin name prefix.
// "cnc/spindle/speed" → "cnc-spindle-speed"
func topicToHalName(topic string) string {
	return strings.ReplaceAll(strings.Trim(topic, "/"), "/", "-")
}

// createPin creates a HAL pin with the appropriate type and direction.
func createPin(comp *hal.Component, name string, pt PinType, dir TopicDir) (*mqttPin, error) {
	p := &mqttPin{
		name: name[strings.LastIndex(name, ".")+1:], // JSON key is the last segment
		typ:  pt,
		dir:  dir,
	}

	// HAL direction: for publish topics (DirOut), the component reads the pin → HAL_IN.
	// For subscribe topics (DirIn), the component writes the pin → HAL_OUT.
	var halDir hal.Direction
	if dir == DirOut {
		halDir = hal.In
	} else {
		halDir = hal.Out
	}

	var err error
	switch pt {
	case PinTypeBit:
		p.bitPin, err = hal.NewPin[bool](comp, name, halDir)
	case PinTypeFloat:
		p.fltPin, err = hal.NewPin[float64](comp, name, halDir)
	case PinTypeS32:
		p.s32Pin, err = hal.NewPin[int32](comp, name, halDir)
	case PinTypeU32:
		p.u32Pin, err = hal.NewPin[uint32](comp, name, halDir)
	}
	if err != nil {
		return nil, fmt.Errorf("creating pin %q: %w", name, err)
	}
	return p, nil
}
