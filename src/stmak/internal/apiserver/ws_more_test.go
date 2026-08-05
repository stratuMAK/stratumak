// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// WebSocket protocol coverage: the malformed-message paths, the per-connection
// watch factory, delta encoding, binary watches, and the panic/error containment
// in the push goroutines. Those goroutines run outside net/http's per-request
// recovery, so a panic or an unhandled error there would take the controller
// down rather than just the subscription — which is what these tests pin.
package apiserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/coder/websocket"
)

// wsClient dials a watch handler and returns a connection plus a read helper.
type wsClient struct {
	t    *testing.T
	conn *websocket.Conn
	ctx  context.Context
}

func dialWatch(t *testing.T, reg *WatchRegistry) (*wsClient, *WatchHandler) {
	t.Helper()
	handler := NewWatchHandler(reg)
	handler.SetLogger(quietLogger())
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })
	return &wsClient{t: t, conn: conn, ctx: ctx}, handler
}

func (c *wsClient) send(v interface{}) {
	c.t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		c.t.Fatalf("marshal: %v", err)
	}
	if err := c.conn.Write(c.ctx, websocket.MessageText, data); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

func (c *wsClient) sendRaw(s string) {
	c.t.Helper()
	if err := c.conn.Write(c.ctx, websocket.MessageText, []byte(s)); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

func (c *wsClient) readText() []byte {
	c.t.Helper()
	_, data, err := c.conn.Read(c.ctx)
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	return data
}

// readErrorMessage reads one frame and asserts it is a protocol error.
func (c *wsClient) readErrorMessage() string {
	c.t.Helper()
	var e wsError
	if err := json.Unmarshal(c.readText(), &e); err != nil {
		c.t.Fatalf("unmarshal: %v", err)
	}
	if e.Type != "error" {
		c.t.Fatalf("frame type = %q, want error", e.Type)
	}
	return e.Message
}

// --- CommandsFromAPI ---

func TestCommandsFromAPI(t *testing.T) {
	if got := CommandsFromAPI(nil); got != nil {
		t.Errorf("CommandsFromAPI(nil) = %v, want nil", got)
	}
	if got := CommandsFromAPI(&RegisteredAPI{APIName: "x"}); got != nil {
		t.Errorf("CommandsFromAPI without meta = %v, want nil", got)
	}

	var sawCallbacks unsafe.Pointer
	api := &RegisteredAPI{
		APIName:   "cmds",
		Instance:  "i",
		Callbacks: fakeCallbacks,
		Meta: &APIMeta{
			Name: "cmds",
			Funcs: []FuncMeta{
				{Name: "echo", Dispatch: func(cb unsafe.Pointer, req []byte) ([]byte, error) {
					sawCallbacks = cb
					return req, nil
				}},
				{Name: "boom", Dispatch: func(unsafe.Pointer, []byte) ([]byte, error) {
					return nil, fmt.Errorf("nope")
				}},
				// Not REST-exported / not dispatchable — must be skipped rather
				// than exposed as a command that nil-panics when called.
				{Name: "undispatchable"},
			},
		},
	}

	cmds := CommandsFromAPI(api)
	if len(cmds) != 2 {
		t.Fatalf("got %d commands, want 2: %+v", len(cmds), cmds)
	}
	byName := map[string]CommandFunc{}
	for _, c := range cmds {
		byName[c.Name] = c.Handler
	}
	res, err := byName["echo"](json.RawMessage(`{"a":1}`))
	if err != nil || string(res) != `{"a":1}` {
		t.Errorf("echo = %s, %v", res, err)
	}
	if sawCallbacks != fakeCallbacks {
		t.Error("the command handler did not pass the API's callbacks to dispatch")
	}
	if _, err := byName["boom"](nil); err == nil {
		t.Error("a failing dispatch must propagate its error")
	}
}

// --- Malformed protocol messages ---

func TestWatchRejectsMalformedMessages(t *testing.T) {
	c, _ := dialWatch(t, NewWatchRegistry())

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"not JSON", `{`, "invalid JSON"},
		{"unknown action", `{"action":"explode"}`, "unknown action"},
		{"bad subscribe", `{"action":"subscribe","rate_ms":"fast"}`, "invalid subscribe message"},
		{"bad unsubscribe", `{"action":"unsubscribe","api":123}`, "invalid unsubscribe message"},
		{"bad call", `{"action":"call","id":"one"}`, "invalid call message"},
	}
	for _, tt := range cases {
		c.sendRaw(tt.raw)
		if msg := c.readErrorMessage(); !strings.Contains(msg, tt.want) {
			t.Errorf("%s: error = %q, want it to mention %q", tt.name, msg, tt.want)
		}
	}

	// The connection must still be usable after all of that — a malformed
	// message drops the message, not the socket.
	c.send(wsSubscribe{Action: "subscribe", API: "nope", Instance: "nope", Func: "nope"})
	if msg := c.readErrorMessage(); !strings.Contains(msg, "unknown API") {
		t.Errorf("error = %q, want it to mention the unknown API", msg)
	}
}

func TestWatchSubscribeUnknownFunc(t *testing.T) {
	reg := NewWatchRegistry()
	reg.Register(&WatchAPI{APIName: "a", Instance: "i", Watches: []WatchFuncMeta{{Name: "known"}}})
	c, _ := dialWatch(t, reg)

	c.send(wsSubscribe{Action: "subscribe", API: "a", Instance: "i", Func: "unknown"})
	if msg := c.readErrorMessage(); !strings.Contains(msg, "unknown watch function") {
		t.Errorf("error = %q", msg)
	}
}

func TestWatchCallUnknownAPIAndCommand(t *testing.T) {
	reg := NewWatchRegistry()
	reg.Register(&WatchAPI{APIName: "a", Instance: "i", Commands: []CommandMeta{{
		Name: "ok", Handler: func(json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`1`), nil },
	}}})
	c, _ := dialWatch(t, reg)

	c.send(wsCall{Action: "call", API: "missing", Instance: "i", Func: "ok", ID: 1})
	var res wsResult
	if err := json.Unmarshal(c.readText(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// A failed call must come back as a result carrying the ID, not as a bare
	// protocol error — the client correlates responses by ID.
	if res.Type != "result" || res.ID != 1 || !strings.Contains(res.Error, "unknown API") {
		t.Errorf("result = %+v", res)
	}

	c.send(wsCall{Action: "call", API: "a", Instance: "i", Func: "missing", ID: 2})
	if err := json.Unmarshal(c.readText(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.ID != 2 || !strings.Contains(res.Error, "unknown command") {
		t.Errorf("result = %+v", res)
	}
}

// --- Per-connection factory ---

func TestWatchFactorySubscription(t *testing.T) {
	reg := NewWatchRegistry()
	reg.Register(&WatchAPI{
		APIName:  "f",
		Instance: "i",
		Watches: []WatchFuncMeta{
			{
				Name:        "good",
				DefaultRate: 20 * time.Millisecond,
				Factory: func(args json.RawMessage) (WatchFunc, error) {
					// The factory sees the client's args verbatim.
					var a struct {
						Tag string `json:"tag"`
					}
					_ = json.Unmarshal(args, &a)
					n := 0
					return func() (json.RawMessage, error) {
						n++
						return json.Marshal(map[string]interface{}{"tag": a.Tag, "n": n})
					}, nil
				},
			},
			{
				Name:        "bad",
				DefaultRate: 20 * time.Millisecond,
				Factory: func(json.RawMessage) (WatchFunc, error) {
					return nil, fmt.Errorf("names required")
				},
			},
		},
	})
	c, _ := dialWatch(t, reg)

	// A factory that rejects the args must fail the subscription with a
	// message, and must not leave a dangling entry in the connection's subs.
	c.send(wsSubscribe{Action: "subscribe", API: "f", Instance: "i", Func: "bad"})
	if msg := c.readErrorMessage(); !strings.Contains(msg, "names required") {
		t.Errorf("error = %q", msg)
	}

	c.send(wsSubscribe{
		Action: "subscribe", API: "f", Instance: "i", Func: "good",
		RateMS: 20, Args: json.RawMessage(`{"tag":"hello"}`),
	})
	var up wsUpdate
	if err := json.Unmarshal(c.readText(), &up); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var data struct {
		Tag string `json:"tag"`
		N   int    `json:"n"`
	}
	if err := json.Unmarshal(up.Data, &data); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if data.Tag != "hello" {
		t.Errorf("the factory did not receive the subscription args: %+v", data)
	}
}

// TestWatchRateClamped pins the 10ms floor: a client asking for a 0/1ms rate
// would otherwise spin a ticker fast enough to starve the controller.
func TestWatchRateClamped(t *testing.T) {
	var polls atomic.Int64
	reg := NewWatchRegistry()
	reg.Register(&WatchAPI{
		APIName: "r", Instance: "i",
		Watches: []WatchFuncMeta{{
			Name: "w", DefaultRate: time.Second,
			Watch: func() (json.RawMessage, error) {
				n := polls.Add(1)
				return json.Marshal(map[string]int64{"n": n})
			},
		}},
	})
	c, _ := dialWatch(t, reg)

	c.send(wsSubscribe{Action: "subscribe", API: "r", Instance: "i", Func: "w", RateMS: 1})
	_ = c.readText() // immediate first poll

	time.Sleep(120 * time.Millisecond)
	if n := polls.Load(); n > 40 {
		t.Errorf("%d polls in ~120ms — the rate floor is not being applied", n)
	}
}

// --- Delta encoding ---

func TestDeltaEncode(t *testing.T) {
	c := &wsConn{}
	var prev map[string]json.RawMessage

	// First message is always the full snapshot — the client has no baseline.
	full := json.RawMessage(`{"a":1,"b":2}`)
	if got := c.deltaEncode(full, &prev); string(got) != string(full) {
		t.Errorf("first deltaEncode = %s, want the full snapshot", got)
	}

	// Unchanged → nil, so pushLoop skips the send entirely.
	if got := c.deltaEncode(json.RawMessage(`{"a":1,"b":2}`), &prev); got != nil {
		t.Errorf("unchanged deltaEncode = %s, want nil", got)
	}

	// Only the changed key travels.
	got := c.deltaEncode(json.RawMessage(`{"a":1,"b":3}`), &prev)
	var delta map[string]int
	if err := json.Unmarshal(got, &delta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(delta) != 1 || delta["b"] != 3 {
		t.Errorf("delta = %v, want only b", delta)
	}

	// A new key counts as changed. (json.Unmarshal merges into a non-nil map,
	// so this must decode into a fresh one.)
	got = c.deltaEncode(json.RawMessage(`{"a":1,"b":3,"c":9}`), &prev)
	var delta2 map[string]int
	if err := json.Unmarshal(got, &delta2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(delta2) != 1 || delta2["c"] != 9 {
		t.Errorf("delta = %v, want only c", delta2)
	}

	// Unparsable input falls back to sending the payload whole rather than
	// dropping the update.
	raw := json.RawMessage(`[1,2,3]`)
	if got := c.deltaEncode(raw, &prev); string(got) != string(raw) {
		t.Errorf("non-object deltaEncode = %s, want it passed through", got)
	}
}

func TestWatchDeltaSubscriptionSendsOnlyChanges(t *testing.T) {
	var b atomic.Int64
	reg := NewWatchRegistry()
	reg.Register(&WatchAPI{
		APIName: "d", Instance: "i",
		Watches: []WatchFuncMeta{{
			Name: "w", DefaultRate: 15 * time.Millisecond, Delta: true,
			Watch: func() (json.RawMessage, error) {
				return json.Marshal(map[string]int64{"a": 1, "b": b.Load()})
			},
		}},
	})
	c, _ := dialWatch(t, reg)

	c.send(wsSubscribe{Action: "subscribe", API: "d", Instance: "i", Func: "w", RateMS: 15})

	var up wsUpdate
	if err := json.Unmarshal(c.readText(), &up); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var first map[string]int64
	if err := json.Unmarshal(up.Data, &first); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("first update = %v, want the full snapshot", first)
	}

	// Change one key: the next update must carry only that key.
	b.Store(7)
	if err := json.Unmarshal(c.readText(), &up); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var second map[string]int64
	if err := json.Unmarshal(up.Data, &second); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if len(second) != 1 || second["b"] != 7 {
		t.Errorf("second update = %v, want only the changed key", second)
	}
}

// --- Binary watches ---

func TestBinaryWatchFramesAndGenerationDedup(t *testing.T) {
	var gen atomic.Uint64
	var calls atomic.Int64
	gen.Store(1)

	reg := NewWatchRegistry()
	reg.Register(&WatchAPI{
		APIName: "bin", Instance: "i",
		Watches: []WatchFuncMeta{{
			Name: "samples", DefaultRate: 15 * time.Millisecond,
			BinaryWatch: func() ([]byte, uint64, error) {
				n := calls.Add(1)
				g := gen.Load()
				if g == 0 {
					return nil, 0, fmt.Errorf("no capture yet")
				}
				return []byte{byte(n)}, g, nil
			},
		}},
	})
	c, _ := dialWatch(t, reg)

	c.send(wsSubscribe{Action: "subscribe", API: "bin", Instance: "i", Func: "samples", RateMS: 15})

	typ, data, err := c.conn.Read(c.ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("frame type = %v, want binary", typ)
	}
	// Frame format is funcName + NUL + payload so a client can demux several
	// binary subscriptions on one socket.
	prefix := append([]byte("samples"), 0)
	if !strings.HasPrefix(string(data), string(prefix)) {
		t.Fatalf("frame %q does not start with the func-name prefix", data)
	}
	if len(data) != len(prefix)+1 {
		t.Errorf("frame length = %d, want prefix + 1 payload byte", len(data))
	}

	// While the generation is unchanged nothing more is sent; bumping it
	// produces the next frame.
	gen.Store(2)
	ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
	defer cancel()
	if _, _, err := c.conn.Read(ctx); err != nil {
		t.Fatalf("no frame after the generation changed: %v", err)
	}
}

// --- Failure containment in the push goroutines ---

// TestPushLoopSurvivesWatchPanic covers the N2 recover(): the push goroutine is
// spawned by handleSubscribe, outside net/http's per-request recovery, so a
// panic inside a generated/cgo watch function would otherwise kill the process.
func TestPushLoopSurvivesWatchPanic(t *testing.T) {
	reg := NewWatchRegistry()
	reg.Register(&WatchAPI{
		APIName: "p", Instance: "i",
		Watches: []WatchFuncMeta{
			{Name: "boom", DefaultRate: 15 * time.Millisecond,
				Watch: func() (json.RawMessage, error) { panic("watch exploded") }},
			{Name: "fine", DefaultRate: 15 * time.Millisecond,
				Watch: func() (json.RawMessage, error) { return json.RawMessage(`{"ok":1}`), nil }},
		},
	})
	c, _ := dialWatch(t, reg)

	c.send(wsSubscribe{Action: "subscribe", API: "p", Instance: "i", Func: "boom", RateMS: 15})
	time.Sleep(50 * time.Millisecond)

	// The panicking subscription is dropped, but the connection lives on and
	// other subscriptions keep working.
	c.send(wsSubscribe{Action: "subscribe", API: "p", Instance: "i", Func: "fine", RateMS: 15})
	var up wsUpdate
	if err := json.Unmarshal(c.readText(), &up); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if up.Func != "fine" {
		t.Errorf("update func = %q, want fine", up.Func)
	}
}

func TestPushLoopSurvivesBinaryWatchPanic(t *testing.T) {
	reg := NewWatchRegistry()
	reg.Register(&WatchAPI{
		APIName: "pb", Instance: "i",
		Watches: []WatchFuncMeta{
			{Name: "boom", DefaultRate: 15 * time.Millisecond,
				BinaryWatch: func() ([]byte, uint64, error) { panic("binary watch exploded") }},
			{Name: "fine", DefaultRate: 15 * time.Millisecond,
				Watch: func() (json.RawMessage, error) { return json.RawMessage(`{"ok":1}`), nil }},
		},
	})
	c, _ := dialWatch(t, reg)

	c.send(wsSubscribe{Action: "subscribe", API: "pb", Instance: "i", Func: "boom", RateMS: 15})
	time.Sleep(50 * time.Millisecond)

	c.send(wsSubscribe{Action: "subscribe", API: "pb", Instance: "i", Func: "fine", RateMS: 15})
	var up wsUpdate
	if err := json.Unmarshal(c.readText(), &up); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if up.Func != "fine" {
		t.Errorf("update func = %q, want fine", up.Func)
	}
}

// TestPushLoopRetriesAfterWatchError verifies a watch error is transient: the
// subscription must recover rather than be silently dropped, since a watch can
// legitimately fail while a module is mid-reload.
func TestPushLoopRetriesAfterWatchError(t *testing.T) {
	var failing atomic.Bool
	failing.Store(true)
	var n atomic.Int64

	reg := NewWatchRegistry()
	reg.Register(&WatchAPI{
		APIName: "e", Instance: "i",
		Watches: []WatchFuncMeta{{
			Name: "w", DefaultRate: 15 * time.Millisecond,
			Watch: func() (json.RawMessage, error) {
				if failing.Load() {
					return nil, fmt.Errorf("temporarily unavailable")
				}
				return json.Marshal(map[string]int64{"n": n.Add(1)})
			},
		}},
	})
	c, _ := dialWatch(t, reg)

	c.send(wsSubscribe{Action: "subscribe", API: "e", Instance: "i", Func: "w", RateMS: 15})
	time.Sleep(60 * time.Millisecond)
	failing.Store(false)

	var up wsUpdate
	if err := json.Unmarshal(c.readText(), &up); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if up.Func != "w" {
		t.Errorf("update = %+v", up)
	}
}

// TestPushLoopSkipsNilData covers a watch that has nothing new: it must skip the
// tick rather than push an empty frame.
//
// The two halves use separate connections on purpose — a Read whose context
// expires tears the coder/websocket connection down, so the silence check
// cannot be followed by more reads on the same socket.
func TestPushLoopSkipsNilData(t *testing.T) {
	var ready atomic.Bool
	newReg := func() *WatchRegistry {
		reg := NewWatchRegistry()
		reg.Register(&WatchAPI{
			APIName: "n", Instance: "i",
			Watches: []WatchFuncMeta{{
				Name: "w", DefaultRate: 15 * time.Millisecond,
				Watch: func() (json.RawMessage, error) {
					if !ready.Load() {
						return nil, nil
					}
					return json.RawMessage(`{"v":1}`), nil
				},
			}},
		})
		return reg
	}

	silent, _ := dialWatch(t, newReg())
	silent.send(wsSubscribe{Action: "subscribe", API: "n", Instance: "i", Func: "w", RateMS: 15})
	quiet, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, _, err := silent.conn.Read(quiet); err == nil {
		t.Fatal("a nil watch result was pushed to the client")
	}

	ready.Store(true)
	live, _ := dialWatch(t, newReg())
	live.send(wsSubscribe{Action: "subscribe", API: "n", Instance: "i", Func: "w", RateMS: 15})
	var up wsUpdate
	if err := json.Unmarshal(live.readText(), &up); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if up.Func != "w" {
		t.Errorf("update = %+v", up)
	}
}

// TestWatchHandlerCloseEndsConnections covers the shutdown path: Close cancels
// the handler context so every connection's read/push goroutines exit instead
// of lingering while modules are destroyed.
func TestWatchHandlerCloseEndsConnections(t *testing.T) {
	reg := NewWatchRegistry()
	reg.Register(&WatchAPI{
		APIName: "c", Instance: "i",
		Watches: []WatchFuncMeta{{
			Name: "w", DefaultRate: 15 * time.Millisecond,
			Watch: func() (json.RawMessage, error) {
				return json.Marshal(map[string]int64{"t": time.Now().UnixNano()})
			},
		}},
	})
	c, handler := dialWatch(t, reg)

	c.send(wsSubscribe{Action: "subscribe", API: "c", Instance: "i", Func: "w", RateMS: 15})
	_ = c.readText() // confirm it is live

	handler.Close()

	// Reading must now fail (the server side has gone away) within a bounded
	// time rather than hanging.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, _, err := c.conn.Read(ctx); err != nil {
			return // expected
		}
		if time.Now().After(deadline) {
			t.Fatal("the connection kept delivering updates after Close()")
		}
	}
}
