// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// WatchFunc is called periodically by the watch server to produce a snapshot.
// Generated code produces these from the registered callbacks.
type WatchFunc func() (json.RawMessage, error)

// WatchFactory is called ONCE at subscribe time with the client's args.
// It returns a per-connection WatchFunc (stateful closure with its own diff state).
// This avoids expensive serialization on every tick — the closure can diff at the
// source data level and only serialize changed values.
type WatchFactory func(args json.RawMessage) (WatchFunc, error)

// BinaryWatchFunc is called periodically by the watch server to produce a
// binary snapshot. Used for bulk data (e.g. scope sample buffers) where JSON
// would be too large. The uint64 is a generation counter for change detection.
type BinaryWatchFunc func() ([]byte, uint64, error)

// CommandFunc handles a command sent by the client over the WebSocket.
// req is the JSON-encoded arguments; returns the JSON-encoded response.
type CommandFunc func(req json.RawMessage) (json.RawMessage, error)

// WatchFuncMeta describes a watchable function.
type WatchFuncMeta struct {
	Name        string          // e.g. "get_status"
	DefaultRate time.Duration   // e.g. 50ms
	Watch       WatchFunc       // JSON watch — shared across connections (no per-conn state)
	Factory     WatchFactory    // Per-connection watch factory (mutually exclusive with Watch)
	BinaryWatch BinaryWatchFunc // Binary watch — sent as binary frames
	Delta       bool            // If true, diff JSON top-level keys per connection.
}

// CommandMeta describes a command callable over the WebSocket.
type CommandMeta struct {
	Name    string // e.g. "jog_start"
	Handler CommandFunc
}

// CommandsFromAPI creates CommandMeta entries for every dispatchable function
// in the given registered API. This exposes a REST API's functions as WS
// commands without manual per-function wiring.
func CommandsFromAPI(api *RegisteredAPI) []CommandMeta {
	if api == nil {
		return nil
	}
	// RESTFuncs, not Meta.Funcs: for a Go provider this is the direct handler
	// set, so a WS command reports the provider's error instead of the zero
	// value the C trampoline would substitute.
	funcs := api.RESTFuncs()
	if funcs == nil {
		return nil
	}
	cmds := make([]CommandMeta, 0, len(funcs))
	for _, fn := range funcs {
		if fn.Dispatch == nil {
			continue
		}
		fn := fn // capture loop variable
		cb := api.Callbacks
		cmds = append(cmds, CommandMeta{
			Name: fn.Name,
			Handler: func(req json.RawMessage) (json.RawMessage, error) {
				res, err := fn.Dispatch(cb, []byte(req))
				return json.RawMessage(res), err
			},
		})
	}
	return cmds
}

// WatchAPI holds registered watch functions and commands for one API instance.
type WatchAPI struct {
	APIName  string
	Instance string
	Watches  []WatchFuncMeta
	Commands []CommandMeta
}

// WatchRegistry tracks all registered watch APIs.
type WatchRegistry struct {
	mu   sync.RWMutex
	apis map[string]*WatchAPI // key: "apiname/instance"

	// Running push-loop subscriptions, tracked so UnregisterByInstance can
	// cancel them AND wait them out. Deleting the registry entries alone only
	// stops NEW subscribes from resolving — a connected client's loops kept
	// calling the module's callbacks through and after Destroy, which with a
	// module that frees C state on destroy is a use-after-free, not a leak.
	// Guarded by the same mu as apis, so a subscribe that resolved an API
	// before an unload cannot slip its tracking in after the sweep.
	subSeq int
	subs   map[string]map[int]context.CancelFunc // key: instance
	subWGs map[string]*sync.WaitGroup            // key: instance; loops in flight
}

// NewWatchRegistry creates a new watch registry.
func NewWatchRegistry() *WatchRegistry {
	return &WatchRegistry{
		apis:   make(map[string]*WatchAPI),
		subs:   make(map[string]map[int]context.CancelFunc),
		subWGs: make(map[string]*sync.WaitGroup),
	}
}

// trackSub records a live subscription's cancel under the instance it
// watches. It returns the untrack closure (bookkeeping removal — call on
// unsubscribe/supersede/close) and loopDone, which the push GOROUTINE must
// call when it exits: UnregisterByInstance waits on that, because a cancelled
// loop may still be inside a module callback, and the caller is about to
// destroy the module. If the API is no longer registered — the subscribe
// raced an unload past Get — the subscription is refused: cancel is called
// and ok is false.
func (r *WatchRegistry) trackSub(apiName, instance string, cancel context.CancelFunc) (untrack, loopDone func(), ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.apis[apiName+"/"+instance] == nil {
		cancel()
		return nil, nil, false
	}
	r.subSeq++
	id := r.subSeq
	set := r.subs[instance]
	if set == nil {
		set = make(map[int]context.CancelFunc)
		r.subs[instance] = set
	}
	set[id] = cancel
	wg := r.subWGs[instance]
	if wg == nil {
		wg = &sync.WaitGroup{}
		r.subWGs[instance] = wg
	}
	wg.Add(1)
	untrack = func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if set := r.subs[instance]; set != nil {
			delete(set, id)
			if len(set) == 0 {
				delete(r.subs, instance)
			}
		}
	}
	return untrack, wg.Done, true
}

// Register adds a watch API to the registry.
func (r *WatchRegistry) Register(api *WatchAPI) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := api.APIName + "/" + api.Instance
	r.apis[key] = api
}

// Get returns a watch API by name and instance.
func (r *WatchRegistry) Get(apiName, instance string) *WatchAPI {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.apis[apiName+"/"+instance]
}

// UnregisterByInstance removes all watch APIs registered under the given
// instance name, returning the number removed. This MUST be called on module
// unload (alongside Registry.UnregisterByInstance) before the module's Destroy
// frees its HAL pins: a WatchAPI's Factory/Watch closures capture the module's
// pins, so leaving it registered lets a later WS subscribe resolve it and read
// freed/recycled HAL memory (and leaks the registration entry indefinitely).
func (r *WatchRegistry) UnregisterByInstance(instance string) int {
	r.mu.Lock()
	removed := 0
	for key, api := range r.apis {
		if api.Instance == instance {
			delete(r.apis, key)
			removed++
		}
	}
	// Cancel the push loops already running against this instance; the
	// registry deletion above only starves new subscribes. trackSub refuses
	// the race where a subscribe resolved the API before this sweep.
	for _, cancel := range r.subs[instance] {
		cancel()
	}
	delete(r.subs, instance)
	wg := r.subWGs[instance]
	delete(r.subWGs, instance)
	r.mu.Unlock()

	// And wait them out: a cancelled loop may still be inside a module
	// callback, and the caller's next step is Destroy. Bounded, so a wedged
	// callback degrades an unload instead of hanging it forever.
	if wg != nil {
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
	return removed
}

// All returns all registered watch APIs.
func (r *WatchRegistry) All() []*WatchAPI {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*WatchAPI, 0, len(r.apis))
	for _, api := range r.apis {
		result = append(result, api)
	}
	return result
}

// --- WebSocket protocol messages ---

// wsSubscribe is sent by the client to start receiving updates.
type wsSubscribe struct {
	Action   string          `json:"action"`         // "subscribe"
	API      string          `json:"api"`            // "axis"
	Instance string          `json:"instance"`       // "default"
	Func     string          `json:"func"`           // "get_status"
	RateMS   int             `json:"rate_ms"`        // 50
	Args     json.RawMessage `json:"args,omitempty"` // optional args passed to WatchFactory
}

// wsUnsubscribe is sent by the client to stop receiving updates.
type wsUnsubscribe struct {
	Action   string `json:"action"`   // "unsubscribe"
	API      string `json:"api"`      // "axis"
	Instance string `json:"instance"` // "default"
	Func     string `json:"func"`     // "get_status"
}

// wsCall is sent by the client to invoke a command.
type wsCall struct {
	Action   string          `json:"action"`   // "call"
	API      string          `json:"api"`      // "axis"
	Instance string          `json:"instance"` // "default"
	Func     string          `json:"func"`     // "jog_start"
	ID       int             `json:"id"`       // client-assigned request ID
	Args     json.RawMessage `json:"args"`     // function arguments
}

// wsUpdate is sent by the server when a watch function produces new data.
type wsUpdate struct {
	Type     string          `json:"type"`     // "update"
	API      string          `json:"api"`      // "axis"
	Instance string          `json:"instance"` // "default"
	Func     string          `json:"func"`     // "get_status"
	Data     json.RawMessage `json:"data"`
}

// wsResult is sent by the server in response to a call.
type wsResult struct {
	Type  string          `json:"type"` // "result"
	ID    int             `json:"id"`   // echoed request ID
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

// wsError is sent by the server for protocol errors.
type wsError struct {
	Type    string `json:"type"` // "error"
	Message string `json:"message"`
}

// --- WebSocket handler ---

// WatchHandler handles WebSocket connections for the watch channel.
type WatchHandler struct {
	registry       *WatchRegistry
	logger         *slog.Logger
	ctx            context.Context
	cancel         context.CancelFunc
	originPatterns []string   // WebSocket Origin allow-list; empty = same-origin only
	wsLimit        *wsLimiter // shared with the server's stream endpoint; nil = unlimited
}

// NewWatchHandler creates a new WebSocket watch handler.
func NewWatchHandler(registry *WatchRegistry) *WatchHandler {
	ctx, cancel := context.WithCancel(context.Background())
	return &WatchHandler{registry: registry, logger: slog.Default(), ctx: ctx, cancel: cancel}
}

// Close cancels all active WebSocket connections managed by this handler.
// Call this during server shutdown to ensure goroutines exit cleanly.
func (h *WatchHandler) Close() {
	h.cancel()
}

// SetLogger sets the logger for the watch handler.
func (h *WatchHandler) SetLogger(logger *slog.Logger) {
	h.logger = logger
}

// ServeHTTP upgrades the connection to WebSocket and runs the watch loop.
func (h *WatchHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Refuse before upgrading: every accepted connection can subscribe to the
	// registered watch functions, each of which runs a ticker goroutine calling
	// into generated/cgo code (see limits.go).
	if !h.wsLimit.acquire() {
		h.logger.Warn("watch websocket refused: at the WebSocket connection cap",
			"open", h.wsLimit.count())
		writeErrorJSON(w, http.StatusServiceUnavailable, "too many WebSocket connections")
		return
	}
	defer h.wsLimit.release()

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Empty OriginPatterns → same-origin only (secure default). This blocks
		// cross-site WebSocket hijacking — a malicious page in the operator's
		// browser cannot open this socket and issue `call` commands. Widen via
		// Server.SetWSOriginPatterns for a cross-origin HMI.
		OriginPatterns: h.originPatterns,
	})
	if err != nil {
		h.logger.Warn("websocket accept failed", "error", err)
		return
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()

	c := &wsConn{
		conn:    conn,
		handler: h,
		ctx:     ctx,
		cancel:  cancel,
		subs:    make(map[string]wsSub),
	}
	defer c.dropAll()

	// Liveness keepalive. Watch pushes are change-driven, so a dead peer
	// (pulled cable, powered-off HMI, half-open NAT) keeps the TCP connection
	// ESTABLISHED for minutes with every push "delivered" into the void while
	// the client renders its last values as live. Pings force protocol-level
	// round-trips; on failure the connection is torn down, which is what lets
	// the clients' reconnect logic take over. (The stream handler deliberately
	// has no keepalive: it pushes continuously, so a dead peer surfaces as
	// write backpressure there.)
	go c.keepalive()

	c.readLoop()
}

// Keepalive cadence. Vars, not consts, so tests can shorten them.
var (
	wsPingInterval = 10 * time.Second
	wsPingTimeout  = 5 * time.Second
)

// keepalive pings the peer until the connection dies. Ping requires a
// concurrent Read to process the pong — readLoop provides it (browsers answer
// pongs automatically inside the protocol). Control frames may be written
// concurrently with data frames, so writeMu is not needed here.
func (c *wsConn) keepalive() {
	t := time.NewTicker(wsPingInterval)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(c.ctx, wsPingTimeout)
			err := c.conn.Ping(ctx)
			cancel()
			if err != nil {
				if c.ctx.Err() == nil {
					c.handler.logger.Warn("watch websocket peer unresponsive, closing", "error", err)
				}
				c.cancel()
				_ = c.conn.CloseNow()
				return
			}
		}
	}
}

// wsConn manages one WebSocket client connection.
type wsConn struct {
	conn    *websocket.Conn
	handler *WatchHandler
	ctx     context.Context
	cancel  context.CancelFunc

	writeMu sync.Mutex // serializes writes (coder/websocket supports concurrent writes, but we want ordered JSON)

	mu   sync.Mutex
	subs map[string]wsSub // key: "api/instance/func"
}

// wsSub is one live subscription: its push-loop cancel and the closure that
// takes it back out of the registry's per-instance tracking.
type wsSub struct {
	cancel  context.CancelFunc
	untrack func()
}

// dropAll cancels and untracks every subscription; called when the
// connection goes away (the ctx cancel alone would leak the tracking).
func (c *wsConn) dropAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, s := range c.subs {
		s.cancel()
		if s.untrack != nil {
			s.untrack()
		}
		delete(c.subs, key)
	}
}

func (c *wsConn) writeJSON(v interface{}) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.conn.Write(c.ctx, websocket.MessageText, data)
}

func (c *wsConn) writeBinary(data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.Write(c.ctx, websocket.MessageBinary, data)
}

// writeUpdate marshals and writes a watch update, but only if subCtx is still
// active. The subCtx check is done under writeMu, which serializes all writes.
//
// handleSubscribe cancels a superseded subscription's context BEFORE it creates
// the replacement subscription's goroutine. Combined with the check-under-lock
// here, this guarantees a cancelled subscription can never write after its
// successor: if the successor has written, the predecessor's ctx was already
// cancelled, so its check fails and it drops the message. Without this, a
// cancelled subscription's immediate first-poll snapshot (a full meta message)
// could still be scheduled and arrive AFTER the new subscription's snapshot,
// clobbering it on the client — which made newly-added watch items show up with
// no value/type until a page reload.
func (c *wsConn) writeUpdate(subCtx context.Context, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := subCtx.Err(); err != nil {
		return err // subscription superseded or connection gone — drop
	}
	return c.conn.Write(subCtx, websocket.MessageText, data)
}

func (c *wsConn) sendError(msg string) {
	_ = c.writeJSON(wsError{Type: "error", Message: msg})
}

func (c *wsConn) readLoop() {
	for {
		_, data, err := c.conn.Read(c.ctx)
		if err != nil {
			return // connection closed
		}

		// Peek at the action field
		var msg struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			c.sendError("invalid JSON")
			continue
		}

		switch msg.Action {
		case "subscribe":
			var sub wsSubscribe
			if err := json.Unmarshal(data, &sub); err != nil {
				c.sendError("invalid subscribe message")
				continue
			}
			c.handleSubscribe(sub)

		case "unsubscribe":
			var unsub wsUnsubscribe
			if err := json.Unmarshal(data, &unsub); err != nil {
				c.sendError("invalid unsubscribe message")
				continue
			}
			c.handleUnsubscribe(unsub)

		case "call":
			var call wsCall
			if err := json.Unmarshal(data, &call); err != nil {
				c.sendError("invalid call message")
				continue
			}
			c.handleCall(call)

		default:
			c.sendError(fmt.Sprintf("unknown action: %q", msg.Action))
		}
	}
}

func (c *wsConn) handleSubscribe(sub wsSubscribe) {
	api := c.handler.registry.Get(sub.API, sub.Instance)
	if api == nil {
		c.sendError(fmt.Sprintf("unknown API: %s/%s", sub.API, sub.Instance))
		return
	}

	var watchMeta *WatchFuncMeta
	for i := range api.Watches {
		if api.Watches[i].Name == sub.Func {
			watchMeta = &api.Watches[i]
			break
		}
	}
	if watchMeta == nil {
		c.sendError(fmt.Sprintf("unknown watch function: %s", sub.Func))
		return
	}

	rate := watchMeta.DefaultRate
	if sub.RateMS > 0 {
		requested := time.Duration(sub.RateMS) * time.Millisecond
		// Clamp to minimum 10ms
		if requested < 10*time.Millisecond {
			requested = 10 * time.Millisecond
		}
		rate = requested
	}

	key := sub.API + "/" + sub.Instance + "/" + sub.Func

	// Cancel existing subscription for this key
	c.mu.Lock()
	if old, ok := c.subs[key]; ok {
		old.cancel()
		if old.untrack != nil {
			old.untrack()
		}
	}
	subCtx, cancelFn := context.WithCancel(c.ctx)
	untrack, loopDone, ok := c.handler.registry.trackSub(sub.API, sub.Instance, cancelFn)
	if !ok {
		// The API was unregistered between Get and here (a racing unload).
		delete(c.subs, key)
		c.mu.Unlock()
		c.sendError(fmt.Sprintf("unknown API: %s/%s", sub.API, sub.Instance))
		return
	}
	c.subs[key] = wsSub{cancel: cancelFn, untrack: untrack}
	c.mu.Unlock()

	// Start the push goroutine. loopDone signals the registry when the loop
	// has actually exited — UnregisterByInstance waits on it before the
	// module behind the callbacks is destroyed.
	if watchMeta.BinaryWatch != nil {
		go func() {
			defer loopDone()
			c.pushLoopBinary(subCtx, sub.Func, rate, watchMeta.BinaryWatch)
		}()
	} else if watchMeta.Factory != nil {
		watchFn, err := watchMeta.Factory(sub.Args)
		if err != nil {
			c.sendError(fmt.Sprintf("watch factory error: %v", err))
			cancelFn()
			loopDone()
			c.mu.Lock()
			if s, ok := c.subs[key]; ok {
				if s.untrack != nil {
					s.untrack()
				}
				delete(c.subs, key)
			}
			c.mu.Unlock()
			return
		}
		go func() {
			defer loopDone()
			c.pushLoop(subCtx, sub.API, sub.Instance, sub.Func, rate, watchFn, watchMeta.Delta)
		}()
	} else {
		go func() {
			defer loopDone()
			c.pushLoop(subCtx, sub.API, sub.Instance, sub.Func, rate, watchMeta.Watch, watchMeta.Delta)
		}()
	}
}

func (c *wsConn) handleUnsubscribe(unsub wsUnsubscribe) {
	key := unsub.API + "/" + unsub.Instance + "/" + unsub.Func

	c.mu.Lock()
	if s, ok := c.subs[key]; ok {
		s.cancel()
		if s.untrack != nil {
			s.untrack()
		}
		delete(c.subs, key)
	}
	c.mu.Unlock()
}

func (c *wsConn) handleCall(call wsCall) {
	api := c.handler.registry.Get(call.API, call.Instance)
	if api == nil {
		_ = c.writeJSON(wsResult{
			Type:  "result",
			ID:    call.ID,
			Error: fmt.Sprintf("unknown API: %s/%s", call.API, call.Instance),
		})
		return
	}

	var cmdMeta *CommandMeta
	for i := range api.Commands {
		if api.Commands[i].Name == call.Func {
			cmdMeta = &api.Commands[i]
			break
		}
	}
	if cmdMeta == nil {
		_ = c.writeJSON(wsResult{
			Type:  "result",
			ID:    call.ID,
			Error: fmt.Sprintf("unknown command: %s", call.Func),
		})
		return
	}

	result, err := cmdMeta.Handler(call.Args)
	if err != nil {
		_ = c.writeJSON(wsResult{
			Type:  "result",
			ID:    call.ID,
			Error: err.Error(),
		})
		return
	}

	_ = c.writeJSON(wsResult{
		Type: "result",
		ID:   call.ID,
		Data: result,
	})
}

func (c *wsConn) pushLoop(ctx context.Context, apiName, instance, funcName string, rate time.Duration, watch WatchFunc, delta bool) {
	// This runs in a goroutine spawned by handleSubscribe, so net/http's
	// per-request panic recovery does NOT cover it. watch() calls into
	// generated/cgo code; a panic there would otherwise kill the whole
	// controller. Recover and drop the subscription instead.
	defer func() {
		if r := recover(); r != nil {
			c.handler.logger.Error("watch push goroutine panic",
				"api", apiName, "instance", instance, "func", funcName, "panic", r)
		}
	}()

	ticker := time.NewTicker(rate)
	defer ticker.Stop()

	// Resolve funcName for update messages — strip "get_" prefix for cleaner names
	updateFunc := funcName

	var prevData json.RawMessage           // suppress unchanged sends
	var prevMap map[string]json.RawMessage // per-connection delta state

	// Immediate first poll — deliver data to new subscriber without waiting
	// for the first ticker tick.
	if data, err := watch(); err == nil && data != nil {
		sendData := data
		if delta {
			sendData = c.deltaEncode(data, &prevMap)
		}
		if sendData != nil {
			if err := c.writeUpdate(ctx, wsUpdate{
				Type:     "update",
				API:      apiName,
				Instance: instance,
				Func:     updateFunc,
				Data:     sendData,
			}); err != nil {
				return
			}
			prevData = append(prevData[:0], data...)
		}
	}

	// errLogged suppresses repeat logs while an error persists — this loop
	// ticks every `rate`, so we log the first error of a streak and the
	// recovery, not every tick. A watch error is transient (don't kill the sub).
	errLogged := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			data, err := watch()
			if err != nil {
				if !errLogged {
					c.handler.logger.Warn("watch push error — retrying",
						"api", apiName, "instance", instance, "func", funcName, "error", err)
					errLogged = true
				}
				continue
			}
			if errLogged {
				c.handler.logger.Info("watch push recovered",
					"api", apiName, "instance", instance, "func", funcName)
				errLogged = false
			}
			if data == nil {
				// No data — skip this tick.
				continue
			}

			// Skip if nothing changed since last send
			if bytes.Equal(data, prevData) {
				continue
			}

			sendData := data
			if delta {
				sendData = c.deltaEncode(data, &prevMap)
				if sendData == nil {
					continue // nothing changed
				}
			}

			if err := c.writeUpdate(ctx, wsUpdate{
				Type:     "update",
				API:      apiName,
				Instance: instance,
				Func:     updateFunc,
				Data:     sendData,
			}); err != nil {
				return // write failed or subscription superseded — connection dead
			}
			prevData = append(prevData[:0], data...)
		}
	}
}

// pushLoopBinary pushes binary watch data to the client as binary WebSocket
// frames. The frame format is: funcName + '\0' + payload, so the client can
// demux multiple binary watch subscriptions on one connection.
func (c *wsConn) pushLoopBinary(ctx context.Context, funcName string, rate time.Duration, watch BinaryWatchFunc) {
	// Spawned goroutine — not covered by net/http recover (see pushLoop).
	defer func() {
		if r := recover(); r != nil {
			c.handler.logger.Error("binary watch push goroutine panic", "func", funcName, "panic", r)
		}
	}()

	ticker := time.NewTicker(rate)
	defer ticker.Stop()

	prefix := append([]byte(funcName), 0) // "func_name\0"
	var sentGen uint64

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			payload, gen, err := watch()
			if err != nil || payload == nil {
				continue
			}

			// Skip if generation unchanged since last send
			if gen > 0 && gen == sentGen {
				continue
			}

			frame := make([]byte, len(prefix)+len(payload))
			copy(frame, prefix)
			copy(frame[len(prefix):], payload)

			if err := c.writeBinary(frame); err != nil {
				return // write failed — connection dead
			}
			sentGen = gen
		}
	}
}

// deltaEncode compares current JSON with the per-connection previous state
// and returns only changed top-level keys. Returns nil if nothing changed.
// First call returns the full data.
func (c *wsConn) deltaEncode(data json.RawMessage, prevMap *map[string]json.RawMessage) json.RawMessage {
	var curMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &curMap); err != nil {
		return data // can't parse — send full
	}

	prev := *prevMap
	if prev == nil {
		// First message — send full snapshot.
		*prevMap = curMap
		return data
	}

	// Build delta: only keys whose JSON bytes changed.
	delta := make(map[string]json.RawMessage, len(curMap)/4)
	for k, v := range curMap {
		oldV, ok := prev[k]
		if !ok || string(v) != string(oldV) {
			delta[k] = v
		}
	}

	*prevMap = curMap

	if len(delta) == 0 {
		return nil
	}

	result, err := json.Marshal(delta)
	if err != nil {
		return data
	}
	return result
}

// AddWatchEndpoint registers the WebSocket handler on the server's mux.
func (s *Server) AddWatchEndpoint(registry *WatchRegistry) {
	handler := NewWatchHandler(registry)
	handler.SetLogger(s.logger)
	handler.originPatterns = s.wsOriginPatterns
	// One limiter shared with the stream endpoint, so the cap bounds the total
	// number of WebSocket connections rather than each endpoint separately.
	handler.wsLimit = s.wsLimit
	s.watchHandler = handler
	pattern := strings.TrimSuffix(s.prefix, "/") + "/watch"
	s.mux.Handle(pattern, handler)
}
