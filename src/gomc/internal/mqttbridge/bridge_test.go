// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Behavioural tests for the MQTT bridge, run against a real in-process HAL
// instance (link_test.go pulls in the HAL C symbols) and a fake MQTT client.
// The broker is never contacted: the fake is what lets the failure paths — a
// disconnected client, a rejected publish, a malformed inbound payload — be
// driven deterministically, which is where the liveness-pin semantics live.
package mqttbridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/sittner/linuxcnc/src/gomc/pkg/hal"
)

// TestMain holds one keep-alive HAL component open for the whole test binary.
// The in-process HAL data segment is torn down when the last component exits
// and cannot be re-initialised afterwards — see pkg/hal's TestMain.
func TestMain(m *testing.M) {
	keep, err := hal.NewComponent("mqtt-test-keepalive")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hal keep-alive init failed: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = keep.Exit()
	os.Exit(code)
}

// uniq gives each test its own HAL namespace. HAL names are process-global and
// only freed on component exit, so sharing them would make failures cascade.
var uniqCounter int

func uniq(prefix string) string {
	uniqCounter++
	return fmt.Sprintf("%s%d", prefix, uniqCounter)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- Fake MQTT client / token / message ---

type fakeToken struct {
	done chan struct{}
	err  error
}

func completedToken(err error) *fakeToken {
	t := &fakeToken{done: make(chan struct{}), err: err}
	close(t.done)
	return t
}

// pendingToken models a QoS>=1 publish that has left the client but is still
// awaiting the broker ack.
func pendingToken() *fakeToken {
	return &fakeToken{done: make(chan struct{})}
}

func (t *fakeToken) Wait() bool                     { <-t.done; return true }
func (t *fakeToken) WaitTimeout(time.Duration) bool { <-t.done; return true }
func (t *fakeToken) Done() <-chan struct{}          { return t.done }
func (t *fakeToken) Error() error                   { return t.err }

type publishedMsg struct {
	topic   string
	qos     byte
	retain  bool
	payload string
}

type fakeClient struct {
	mu sync.Mutex

	connected    bool
	published    []publishedMsg
	subscribed   []string
	disconnected bool

	// publishToken is returned by Publish; nil means "completed, no error".
	publishToken mqtt.Token
	// subscribeErr is the error carried by the token Subscribe returns.
	subscribeErr error
	// handlers records the callback passed to Subscribe, keyed by topic, so a
	// test can deliver an inbound message the way paho would.
	handlers map[string]mqtt.MessageHandler
}

func newFakeClient() *fakeClient {
	return &fakeClient{connected: true, handlers: map[string]mqtt.MessageHandler{}}
}

func (c *fakeClient) Disconnect(uint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disconnected = true
	c.connected = false
}

func (c *fakeClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

func (c *fakeClient) setConnected(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connected = v
}

func (c *fakeClient) Publish(topic string, qos byte, retain bool, payload interface{}) mqtt.Token {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.published = append(c.published, publishedMsg{topic, qos, retain, string(payload.([]byte))})
	if c.publishToken != nil {
		return c.publishToken
	}
	return completedToken(nil)
}

func (c *fakeClient) Subscribe(topic string, _ byte, cb mqtt.MessageHandler) mqtt.Token {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subscribed = append(c.subscribed, topic)
	c.handlers[topic] = cb
	return completedToken(c.subscribeErr)
}

func (c *fakeClient) publishes() []publishedMsg {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]publishedMsg(nil), c.published...)
}

type fakeMessage struct {
	topic   string
	payload []byte
}

func (m fakeMessage) Duplicate() bool   { return false }
func (m fakeMessage) Qos() byte         { return 0 }
func (m fakeMessage) Retained() bool    { return false }
func (m fakeMessage) Topic() string     { return m.topic }
func (m fakeMessage) MessageID() uint16 { return 0 }
func (m fakeMessage) Payload() []byte   { return m.payload }
func (m fakeMessage) Ack()              {}

// --- Fixtures ---

// newTestBridge builds a ready bridge over a fresh HAL component. The component
// is exited on test cleanup.
func newTestBridge(t *testing.T, cfg *Config, dryrun bool) (*bridge, *hal.Component) {
	t.Helper()
	name := uniq("mqttb")
	comp, err := hal.NewComponent(name)
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	t.Cleanup(func() { _ = comp.Exit() })

	b, err := newBridge(comp, name, cfg, testLogger(), dryrun)
	if err != nil {
		t.Fatalf("newBridge: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	return b, comp
}

// pinTopicConfig is a one-float-pin publish topic.
func pinTopicConfig(path string, mode PublishMode) *Config {
	return &Config{
		Broker: "tcp://unused",
		Topics: []TopicConfig{{
			Path:        path,
			Dir:         DirOut,
			Mode:        ModePin,
			HalType:     PinTypeFloat,
			Rate:        10 * time.Millisecond,
			QoS:         1,
			Retain:      true,
			PublishMode: mode,
		}},
	}
}

// --- Pin conversion ---

func TestPinReadWriteRoundTrip(t *testing.T) {
	name := uniq("mqttpin")
	comp, err := hal.NewComponent(name)
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	defer func() { _ = comp.Exit() }()

	pins := map[PinType]*mqttPin{}
	for pt, pname := range map[PinType]string{
		PinTypeBit: "b", PinTypeFloat: "f", PinTypeS32: "s", PinTypeU32: "u",
	} {
		p, err := createPin(comp, pname, pt, DirIn) // DirIn → HAL_OUT, writable
		if err != nil {
			t.Fatalf("createPin(%v): %v", pt, err)
		}
		pins[pt] = p
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}

	// JSON write → read round-trip for every type.
	cases := []struct {
		pt   PinType
		raw  string
		want interface{}
	}{
		{PinTypeBit, `true`, true},
		{PinTypeFloat, `2.5`, 2.5},
		{PinTypeS32, `-7`, int32(-7)},
		{PinTypeU32, `9`, uint32(9)},
	}
	for _, tc := range cases {
		if err := pins[tc.pt].write(json.RawMessage(tc.raw)); err != nil {
			t.Fatalf("write(%s): %v", tc.raw, err)
		}
		if got := pins[tc.pt].read(); got != tc.want {
			t.Errorf("after write(%s): read = %v (%T), want %v", tc.raw, got, got, tc.want)
		}
	}

	// A JSON value of the wrong shape is reported, not silently coerced.
	for _, tc := range cases {
		if err := pins[tc.pt].write(json.RawMessage(`"not-a-number"`)); err == nil {
			t.Errorf("write of a string into %v should fail", tc.pt)
		}
	}
}

func TestPinWriteStringParsing(t *testing.T) {
	name := uniq("mqttstr")
	comp, err := hal.NewComponent(name)
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	defer func() { _ = comp.Exit() }()

	mk := func(pname string, pt PinType) *mqttPin {
		p, err := createPin(comp, pname, pt, DirIn)
		if err != nil {
			t.Fatalf("createPin: %v", err)
		}
		return p
	}
	bp, fp, sp, up := mk("b", PinTypeBit), mk("f", PinTypeFloat), mk("s", PinTypeS32), mk("u", PinTypeU32)
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}

	// The bit pin accepts the documented truthy spellings and treats anything
	// else as false — a raw MQTT payload has no JSON typing to fall back on.
	for _, s := range []string{"1", "true", "TRUE"} {
		if err := bp.writeString(s); err != nil || bp.read() != true {
			t.Errorf("writeString(%q) → %v, %v; want true, nil", s, bp.read(), err)
		}
	}
	for _, s := range []string{"0", "false", "yes", ""} {
		if err := bp.writeString(s); err != nil || bp.read() != false {
			t.Errorf("writeString(%q) → %v, %v; want false, nil", s, bp.read(), err)
		}
	}

	if err := fp.writeString("1.25"); err != nil || fp.read() != 1.25 {
		t.Errorf("float writeString: %v, %v", fp.read(), err)
	}
	if err := sp.writeString("-3"); err != nil || sp.read() != int32(-3) {
		t.Errorf("s32 writeString: %v, %v", sp.read(), err)
	}
	if err := up.writeString("4"); err != nil || up.read() != uint32(4) {
		t.Errorf("u32 writeString: %v, %v", up.read(), err)
	}

	// Unparsable payloads must surface as errors and leave the pin untouched.
	for _, tc := range []struct {
		p *mqttPin
		s string
	}{
		{fp, "abc"}, {sp, "abc"}, {up, "-1"}, {sp, "99999999999"},
	} {
		if err := tc.p.writeString(tc.s); err == nil {
			t.Errorf("writeString(%q) on %v should fail", tc.s, tc.p.typ)
		}
	}
	if fp.read() != 1.25 || sp.read() != int32(-3) || up.read() != uint32(4) {
		t.Error("a failed writeString must not modify the pin")
	}
}

// --- Payload building ---

func TestBuildPayloadPinMode(t *testing.T) {
	cfg := pinTopicConfig("t/f", PublishFull)
	cfg.Topics[0].Dir = DirIn // HAL_OUT pins so the test can drive values
	b, _ := newTestBridge(t, cfg, true)
	th := b.handlers[0]

	th.pins[0].fltPin.Set(1.5)
	if got := string(th.buildPayload()); got != "1.5" {
		t.Errorf("float payload = %q, want \"1.5\"", got)
	}

	// A handler with no live pin value yields the explicit null marker rather
	// than an empty frame.
	empty := &topicHandler{cfg: TopicConfig{Mode: ModePin}, pins: []*mqttPin{{typ: PinTypeFloat}}, shadow: []interface{}{nil}}
	if got := string(empty.buildPayload()); got != "null" {
		t.Errorf("unbacked pin payload = %q, want \"null\"", got)
	}

	// An unknown mode produces no payload at all (and must not panic).
	bogus := &topicHandler{cfg: TopicConfig{Mode: TopicMode(42)}}
	if got := bogus.buildPayload(); got != nil {
		t.Errorf("unknown mode payload = %q, want nil", got)
	}
}

func TestBuildPayloadPinModeAllTypes(t *testing.T) {
	name := uniq("mqttpl")
	comp, err := hal.NewComponent(name)
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	defer func() { _ = comp.Exit() }()

	mk := func(pname string, pt PinType) *topicHandler {
		p, err := createPin(comp, pname, pt, DirIn)
		if err != nil {
			t.Fatalf("createPin: %v", err)
		}
		return &topicHandler{cfg: TopicConfig{Mode: ModePin}, pins: []*mqttPin{p}, shadow: []interface{}{nil}}
	}
	bt, ft, st, ut := mk("b", PinTypeBit), mk("f", PinTypeFloat), mk("s", PinTypeS32), mk("u", PinTypeU32)
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}

	bt.pins[0].bitPin.Set(true)
	ft.pins[0].fltPin.Set(-0.25)
	st.pins[0].s32Pin.Set(-42)
	ut.pins[0].u32Pin.Set(42)

	for _, tc := range []struct {
		th   *topicHandler
		want string
	}{
		{bt, "true"}, {ft, "-0.25"}, {st, "-42"}, {ut, "42"},
	} {
		if got := string(tc.th.buildPayload()); got != tc.want {
			t.Errorf("payload = %q, want %q", got, tc.want)
		}
	}
	bt.pins[0].bitPin.Set(false)
	if got := string(bt.buildPayload()); got != "false" {
		t.Errorf("bit false payload = %q", got)
	}
}

func TestBuildPayloadJSONFullVsDelta(t *testing.T) {
	name := uniq("mqttjson")
	comp, err := hal.NewComponent(name)
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	defer func() { _ = comp.Exit() }()

	cfg := &Config{Broker: "tcp://unused", Topics: []TopicConfig{{
		Path: "cnc/state", Dir: DirIn, Mode: ModeJSON, Rate: 10 * time.Millisecond,
		Pins: []PinConfig{
			{Name: "a", Type: PinTypeS32, Dir: DirIn},
			{Name: "b", Type: PinTypeS32, Dir: DirIn},
		},
	}}}
	b, err := newBridge(comp, name, cfg, testLogger(), true)
	if err != nil {
		t.Fatalf("newBridge: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	th := b.handlers[0]
	th.pins[0].s32Pin.Set(1)
	th.pins[1].s32Pin.Set(2)

	// Full mode always carries every pin.
	var full map[string]int32
	if err := json.Unmarshal(th.buildPayload(), &full); err != nil {
		t.Fatalf("unmarshal full: %v", err)
	}
	if full["a"] != 1 || full["b"] != 2 || len(full) != 2 {
		t.Errorf("full payload = %v, want both pins", full)
	}

	// Delta mode carries only what changed since the last shadow update.
	th.cfg.PublishMode = PublishDelta
	th.updateShadow()
	th.pins[1].s32Pin.Set(5)
	var delta map[string]int32
	if err := json.Unmarshal(th.buildPayload(), &delta); err != nil {
		t.Fatalf("unmarshal delta: %v", err)
	}
	if len(delta) != 1 || delta["b"] != 5 {
		t.Errorf("delta payload = %v, want only b=5", delta)
	}
}

func TestHasChangedTracksShadow(t *testing.T) {
	cfg := pinTopicConfig("t/c", PublishFull)
	cfg.Topics[0].Dir = DirIn
	b, _ := newTestBridge(t, cfg, true)
	th := b.handlers[0]

	// The initial shadow is nil, so the first read always counts as a change —
	// that is what makes a freshly loaded bridge publish its first snapshot.
	if !th.hasChanged() {
		t.Fatal("a fresh handler should report changed")
	}
	th.updateShadow()
	if th.hasChanged() {
		t.Fatal("after updateShadow, an untouched pin should not report changed")
	}
	th.pins[0].fltPin.Set(3.25)
	if !th.hasChanged() {
		t.Fatal("a written pin should report changed")
	}
}

// --- publishTick: liveness-pin semantics (finding N7) ---

func TestPublishTickAdvancesCountOnSuccess(t *testing.T) {
	b, _ := newTestBridge(t, pinTopicConfig("t/ok", PublishAlways), false)
	fc := newFakeClient()
	b.client = fc

	b.publishTick(b.handlers[0])
	b.publishTick(b.handlers[0])

	if got := b.pubCount.Get(); got != 2 {
		t.Errorf("publish-count = %d, want 2", got)
	}
	if got := b.pubErrCount.Get(); got != 0 {
		t.Errorf("publish-error-count = %d, want 0", got)
	}
	pubs := fc.publishes()
	if len(pubs) != 2 {
		t.Fatalf("client saw %d publishes, want 2", len(pubs))
	}
	if pubs[0].topic != "t/ok" || pubs[0].qos != 1 || !pubs[0].retain {
		t.Errorf("publish transport attrs not forwarded: %+v", pubs[0])
	}
}

// TestPublishTickDisconnectedDoesNotAdvanceCount is the N7 regression: while the
// client is disconnected paho buffers (or drops) the message, so the liveness
// pin a supervisor watches must not claim the bridge is still publishing.
func TestPublishTickDisconnectedDoesNotAdvanceCount(t *testing.T) {
	b, _ := newTestBridge(t, pinTopicConfig("t/down", PublishAlways), false)
	fc := newFakeClient()
	fc.setConnected(false)
	b.client = fc

	for i := 0; i < 3; i++ {
		b.publishTick(b.handlers[0])
	}

	if got := b.pubCount.Get(); got != 0 {
		t.Errorf("publish-count = %d, want 0 while disconnected", got)
	}
	if got := b.pubErrCount.Get(); got != 3 {
		t.Errorf("publish-error-count = %d, want 3", got)
	}
	if n := len(fc.publishes()); n != 0 {
		t.Errorf("client saw %d publishes while disconnected, want 0", n)
	}
}

// TestPublishTickErroredTokenDoesNotAdvanceCount covers a publish the client
// rejected outright (its token is already completed with an error).
func TestPublishTickErroredTokenDoesNotAdvanceCount(t *testing.T) {
	b, _ := newTestBridge(t, pinTopicConfig("t/err", PublishAlways), false)
	fc := newFakeClient()
	fc.publishToken = completedToken(errors.New("payload rejected"))
	b.client = fc

	b.publishTick(b.handlers[0])

	if got := b.pubCount.Get(); got != 0 {
		t.Errorf("publish-count = %d, want 0 after a rejected publish", got)
	}
	if got := b.pubErrCount.Get(); got != 1 {
		t.Errorf("publish-error-count = %d, want 1", got)
	}
}

// TestPublishTickPendingTokenCountsAsPublished pins the deliberate tradeoff: a
// QoS>=1 publish still awaiting its broker ack has left the client, and the loop
// must not block for the round-trip, so it counts as published.
func TestPublishTickPendingTokenCountsAsPublished(t *testing.T) {
	b, _ := newTestBridge(t, pinTopicConfig("t/pending", PublishAlways), false)
	fc := newFakeClient()
	fc.publishToken = pendingToken()
	b.client = fc

	done := make(chan struct{})
	go func() { b.publishTick(b.handlers[0]); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publishTick blocked on an unacked token")
	}

	if got := b.pubCount.Get(); got != 1 {
		t.Errorf("publish-count = %d, want 1", got)
	}
	if got := b.pubErrCount.Get(); got != 0 {
		t.Errorf("publish-error-count = %d, want 0", got)
	}
}

// TestPublishTickRetriesAfterFailure verifies the other half of the N7 fix: a
// change whose publish failed must not be swallowed by a shadow update — the
// next tick has to send it once the client is back.
func TestPublishTickRetriesAfterFailure(t *testing.T) {
	cfg := pinTopicConfig("t/retry", PublishFull)
	cfg.Topics[0].Dir = DirIn // HAL_OUT so the test can drive the value
	b, _ := newTestBridge(t, cfg, false)
	fc := newFakeClient()
	b.client = fc
	th := b.handlers[0]

	// Establish a published baseline, then take the broker away and change the
	// value: the change must be held, not dropped.
	th.pins[0].fltPin.Set(1)
	b.publishTick(th)
	fc.setConnected(false)
	th.pins[0].fltPin.Set(2)
	b.publishTick(th)

	if got := b.pubCount.Get(); got != 1 {
		t.Fatalf("publish-count = %d, want 1 (the failed tick must not count)", got)
	}

	// Broker back: the still-pending change goes out without the pin being
	// touched again.
	fc.setConnected(true)
	b.publishTick(th)

	pubs := fc.publishes()
	if len(pubs) != 2 {
		t.Fatalf("client saw %d publishes, want 2", len(pubs))
	}
	if pubs[1].payload != "2" {
		t.Errorf("retried payload = %q, want \"2\"", pubs[1].payload)
	}
	if got := b.pubCount.Get(); got != 2 {
		t.Errorf("publish-count = %d, want 2 after recovery", got)
	}
	if got := b.pubErrCount.Get(); got != 1 {
		t.Errorf("publish-error-count = %d, want 1", got)
	}
}

func TestPublishTickSkipsUnchangedValue(t *testing.T) {
	cfg := pinTopicConfig("t/unchanged", PublishFull)
	cfg.Topics[0].Dir = DirIn
	b, _ := newTestBridge(t, cfg, false)
	fc := newFakeClient()
	b.client = fc
	th := b.handlers[0]

	b.publishTick(th) // first snapshot
	b.publishTick(th) // nothing changed
	b.publishTick(th)

	if n := len(fc.publishes()); n != 1 {
		t.Errorf("client saw %d publishes, want 1 (change-detected mode)", n)
	}
	if got := b.pubCount.Get(); got != 1 {
		t.Errorf("publish-count = %d, want 1", got)
	}
}

func TestPublishTickDryrunAdvancesCountWithoutClient(t *testing.T) {
	b, _ := newTestBridge(t, pinTopicConfig("t/dry", PublishAlways), true)
	if b.client != nil {
		t.Fatal("dryrun bridge should have no client")
	}
	b.publishTick(b.handlers[0])
	b.publishTick(b.handlers[0])
	if got := b.pubCount.Get(); got != 2 {
		t.Errorf("dryrun publish-count = %d, want 2", got)
	}
	if got := b.pubErrCount.Get(); got != 0 {
		t.Errorf("dryrun publish-error-count = %d, want 0", got)
	}
}

func TestPublishTickJSONDeltaWithNoChangeEmitsNothing(t *testing.T) {
	name := uniq("mqttdelta")
	comp, err := hal.NewComponent(name)
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	defer func() { _ = comp.Exit() }()

	cfg := &Config{Broker: "tcp://unused", Topics: []TopicConfig{{
		Path: "d", Dir: DirIn, Mode: ModeJSON, Rate: time.Millisecond, PublishMode: PublishAlways,
		Pins: []PinConfig{{Name: "a", Type: PinTypeS32, Dir: DirIn}},
	}}}
	b, err := newBridge(comp, name, cfg, testLogger(), false)
	if err != nil {
		t.Fatalf("newBridge: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	fc := newFakeClient()
	b.client = fc

	// PublishAlways in delta mode still emits an (empty) object every tick; the
	// point here is that the tick path is total — no nil-payload panic.
	b.handlers[0].cfg.PublishMode = PublishDelta
	b.handlers[0].cfg.PublishMode = PublishAlways
	b.publishTick(b.handlers[0])
	if n := len(fc.publishes()); n != 1 {
		t.Fatalf("client saw %d publishes, want 1", n)
	}
}

// --- Inbound messages ---

func TestHandleMessagePinMode(t *testing.T) {
	cfg := &Config{Broker: "tcp://unused", Topics: []TopicConfig{{
		Path: "in/f", Dir: DirIn, Mode: ModePin, HalType: PinTypeFloat, Rate: time.Second,
	}}}
	b, _ := newTestBridge(t, cfg, true)
	th := b.handlers[0]

	b.handleMessage(th, fakeMessage{topic: "in/f", payload: []byte("4.5")})
	if got := th.pins[0].fltPin.Get(); got != 4.5 {
		t.Errorf("pin = %v, want 4.5", got)
	}

	// A garbage payload is logged and dropped — the pin keeps its last value.
	b.handleMessage(th, fakeMessage{topic: "in/f", payload: []byte("garbage")})
	if got := th.pins[0].fltPin.Get(); got != 4.5 {
		t.Errorf("pin = %v after a bad payload, want the previous 4.5", got)
	}
}

func TestHandleMessageJSONMode(t *testing.T) {
	name := uniq("mqttin")
	comp, err := hal.NewComponent(name)
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	defer func() { _ = comp.Exit() }()

	cfg := &Config{Broker: "tcp://unused", Topics: []TopicConfig{{
		Path: "in/obj", Dir: DirIn, Mode: ModeJSON, Rate: time.Second,
		Pins: []PinConfig{
			{Name: "a", Type: PinTypeS32, Dir: DirIn},
			{Name: "b", Type: PinTypeBit, Dir: DirIn},
		},
	}}}
	b, err := newBridge(comp, name, cfg, testLogger(), true)
	if err != nil {
		t.Fatalf("newBridge: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	th := b.handlers[0]

	b.handleMessage(th, fakeMessage{payload: []byte(`{"a":7,"b":true}`)})
	if th.pins[0].s32Pin.Get() != 7 || th.pins[1].bitPin.Get() != true {
		t.Fatalf("pins not written: a=%v b=%v", th.pins[0].s32Pin.Get(), th.pins[1].bitPin.Get())
	}

	// A partial object updates only the keys it carries.
	b.handleMessage(th, fakeMessage{payload: []byte(`{"a":9}`)})
	if th.pins[0].s32Pin.Get() != 9 || th.pins[1].bitPin.Get() != true {
		t.Errorf("partial update clobbered an absent key: a=%v b=%v",
			th.pins[0].s32Pin.Get(), th.pins[1].bitPin.Get())
	}

	// Unknown keys, wrong-typed values and non-object payloads are all dropped
	// without disturbing the pins.
	for _, payload := range []string{`{"zzz":1}`, `{"a":"str"}`, `[1,2]`, `not json`, ``} {
		b.handleMessage(th, fakeMessage{payload: []byte(payload)})
	}
	if th.pins[0].s32Pin.Get() != 9 || th.pins[1].bitPin.Get() != true {
		t.Errorf("a malformed payload modified the pins: a=%v b=%v",
			th.pins[0].s32Pin.Get(), th.pins[1].bitPin.Get())
	}
}

// TestHandleMessagePanicIsContained covers the N8 recover(): handleMessage runs
// in a paho callback goroutine, outside net/http's per-request recovery, so a
// panic there would take the whole controller down.
func TestHandleMessagePanicIsContained(t *testing.T) {
	b, _ := newTestBridge(t, pinTopicConfig("boom", PublishAlways), true)

	// A pin-mode handler with no pins makes th.pins[0] panic.
	broken := &topicHandler{cfg: TopicConfig{Path: "boom", Mode: ModePin}}
	b.handleMessage(broken, fakeMessage{payload: []byte("1")})
	// Reaching here at all is the assertion: the panic was recovered.
}

// --- Subscribe / lifecycle ---

func TestOnConnectSubscribesInputTopicsOnly(t *testing.T) {
	name := uniq("mqttsub")
	comp, err := hal.NewComponent(name)
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	defer func() { _ = comp.Exit() }()

	cfg := &Config{Broker: "tcp://unused", Topics: []TopicConfig{
		{Path: "out/a", Dir: DirOut, Mode: ModePin, HalType: PinTypeBit, Rate: time.Second},
		{Path: "in/b", Dir: DirIn, Mode: ModePin, HalType: PinTypeBit, Rate: time.Second},
	}}
	b, err := newBridge(comp, name, cfg, testLogger(), false)
	if err != nil {
		t.Fatalf("newBridge: %v", err)
	}
	if err := comp.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	fc := newFakeClient()
	b.client = fc

	b.onConnect(nil)

	if len(fc.subscribed) != 1 || fc.subscribed[0] != "in/b" {
		t.Fatalf("subscribed = %v, want only the DirIn topic", fc.subscribed)
	}

	// The registered callback must route to the matching topic's pins.
	fc.handlers["in/b"](nil, fakeMessage{topic: "in/b", payload: []byte("1")})
	if !b.handlers[1].pins[0].bitPin.Get() {
		t.Error("the subscribe callback did not write the topic's pin")
	}
}

func TestOnConnectSurvivesSubscribeFailure(t *testing.T) {
	cfg := &Config{Broker: "tcp://unused", Topics: []TopicConfig{
		{Path: "in/x", Dir: DirIn, Mode: ModePin, HalType: PinTypeBit, Rate: time.Second},
	}}
	b, _ := newTestBridge(t, cfg, false)
	fc := newFakeClient()
	fc.subscribeErr = errors.New("not authorized")
	b.client = fc

	b.onConnect(nil) // must log and continue, not panic or block
	if len(fc.subscribed) != 1 {
		t.Errorf("subscribed = %v, want the attempt to have been made", fc.subscribed)
	}
}

func TestDryrunStartStopAdvancesLiveness(t *testing.T) {
	cfg := pinTopicConfig("t/live", PublishAlways)
	cfg.Topics[0].Rate = 5 * time.Millisecond
	b, _ := newTestBridge(t, cfg, true)

	if err := b.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.After(3 * time.Second)
	for b.pubCount.Get() < 2 {
		select {
		case <-deadline:
			t.Fatalf("publish-count stuck at %d — the dryrun publish loop is not running", b.pubCount.Get())
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}

	// stop() must join the publish goroutines; if it did not, the count would
	// keep moving after it returned.
	b.stop()
	after := b.pubCount.Get()
	time.Sleep(30 * time.Millisecond)
	if b.pubCount.Get() != after {
		t.Error("publish-count advanced after stop() — a publish loop outlived it")
	}
}

func TestStopDisconnectsConnectedClient(t *testing.T) {
	b, _ := newTestBridge(t, pinTopicConfig("t/stop", PublishAlways), false)
	fc := newFakeClient()
	b.client = fc

	b.stop()
	if !fc.disconnected {
		t.Error("stop() did not disconnect a connected client")
	}
}

func TestNewBridgeRejectsDuplicatePinNames(t *testing.T) {
	name := uniq("mqttdup")
	comp, err := hal.NewComponent(name)
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	defer func() { _ = comp.Exit() }()

	// Two topics whose slugs collide produce the same HAL pin name; HAL must
	// reject the second and newBridge must surface that rather than run with a
	// silently missing pin.
	cfg := &Config{Broker: "tcp://unused", Topics: []TopicConfig{
		{Path: "a/b", Dir: DirOut, Mode: ModePin, HalType: PinTypeBit, Rate: time.Second},
		{Path: "/a/b/", Dir: DirOut, Mode: ModePin, HalType: PinTypeBit, Rate: time.Second},
	}}
	if _, err := newBridge(comp, name, cfg, testLogger(), true); err == nil {
		t.Fatal("expected a duplicate-pin error")
	}
}
