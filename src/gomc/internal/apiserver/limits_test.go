// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Connection-cap tests (finding N9). The caps bound two different resources —
// accepted TCP connections and live WebSocket connections — so each is tested
// for both halves of its contract: the cap actually refuses, and a released
// slot is actually reusable (a leaked slot would wedge the server for good,
// which is worse than the DoS the cap prevents).
package apiserver

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestWSLimiterAcquireRelease(t *testing.T) {
	l := newWSLimiter(2)
	for i := 0; i < 2; i++ {
		if !l.acquire() {
			t.Fatalf("acquire #%d must succeed at a cap of 2", i)
		}
	}
	if l.acquire() {
		t.Fatal("the third acquire must fail at a cap of 2")
	}
	if got := l.count(); got != 2 {
		t.Errorf("count = %d, want 2 (a refused acquire must not leak a slot)", got)
	}
	l.release()
	if !l.acquire() {
		t.Error("a released slot must be reusable")
	}

	// Zero and negative mean unlimited, and a nil limiter must be usable so an
	// unconfigured server is not accidentally capped at zero connections.
	for _, l := range []*wsLimiter{newWSLimiter(0), newWSLimiter(-1), nil} {
		for i := 0; i < 100; i++ {
			if !l.acquire() {
				t.Fatalf("an unlimited limiter refused acquire #%d", i)
			}
		}
		l.release()
	}
}

func TestWSLimiterConcurrent(t *testing.T) {
	const cap = 8
	l := newWSLimiter(cap)

	var wg sync.WaitGroup
	var mu sync.Mutex
	granted := 0
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.acquire() {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// The cap must hold exactly under contention — an over-grant would mean the
	// check and the increment are not atomic.
	if granted != cap {
		t.Errorf("granted %d slots, want exactly %d", granted, cap)
	}
	if got := l.count(); got != cap {
		t.Errorf("count = %d, want %d", got, cap)
	}
}

func TestNewServerDefaultsAreApplied(t *testing.T) {
	s := NewServer(NewRegistry(), "127.0.0.1:0")
	if s.maxConns != defaultMaxConnections {
		t.Errorf("maxConns = %d, want the default %d", s.maxConns, defaultMaxConnections)
	}
	if s.wsLimit == nil || s.wsLimit.max != defaultMaxWSConnections {
		t.Errorf("wsLimit = %+v, want the default %d", s.wsLimit, defaultMaxWSConnections)
	}
	// The WS cap must stay below the overall cap: a WebSocket holds an accept
	// slot for its whole life, so an equal cap would let watch clients starve
	// plain REST entirely.
	if defaultMaxWSConnections >= defaultMaxConnections {
		t.Error("the default WS cap is not below the default overall cap")
	}
}

// --- Overall connection cap ---

func TestLimitListenerBoundsAcceptedConnections(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = base.Close() }()

	ln := limitListener(base, 2)
	addr := ln.Addr().String()

	accepted := make(chan net.Conn, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()

	// Two connections get through.
	var clients, serverSide []net.Conn
	defer func() {
		for _, c := range append(clients, serverSide...) {
			_ = c.Close()
		}
	}()
	for i := 0; i < 2; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		clients = append(clients, c)
		select {
		case a := <-accepted:
			serverSide = append(serverSide, a)
		case <-time.After(3 * time.Second):
			t.Fatalf("connection %d was not accepted", i)
		}
	}

	// A third connects at the TCP level (the kernel backlog takes it) but must
	// not be accepted while the cap is reached.
	third, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial 3: %v", err)
	}
	defer func() { _ = third.Close() }()
	select {
	case <-accepted:
		t.Fatal("a third connection was accepted above the cap of 2")
	case <-time.After(250 * time.Millisecond):
	}

	// Closing an accepted connection frees its slot, and the queued connection
	// goes through — the slot must be released on Close, not leaked.
	_ = serverSide[0].Close()
	select {
	case a := <-accepted:
		_ = a.Close()
	case <-time.After(3 * time.Second):
		t.Fatal("the queued connection was not accepted after a slot freed")
	}
}

func TestLimitListenerUnlimitedPassesThrough(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = base.Close() }()

	// A zero or negative cap must return the listener untouched rather than
	// wrapping it with a zero-capacity semaphore, which would accept nothing.
	if got := limitListener(base, 0); got != base {
		t.Error("limitListener(0) wrapped the listener")
	}
	if got := limitListener(base, -1); got != base {
		t.Error("limitListener(-1) wrapped the listener")
	}
}

// TestServeHonoursConnectionCap drives the cap through a real HTTP server: with
// a cap of 1, a second client on its own connection is not served while the
// first holds the only slot.
//
// Slot *release* is asserted at the listener level in
// TestLimitListenerBoundsAcceptedConnections instead: once a request has timed
// out here its transport still owns the queued TCP connection, so the freed
// slot goes to that leftover rather than to a fresh client, and the assertion
// would be about net/http's pooling rather than about the cap.
func TestServeHonoursConnectionCap(t *testing.T) {
	s := NewServer(NewRegistry(), "127.0.0.1:0")
	s.SetLogger(quietLogger())
	s.SetMaxConnections(1)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- s.Serve(ln) }()

	url := "http://" + ln.Addr().String() + "/api/v1/nope"

	// Client 1 keeps its connection alive after the response. The body must be
	// drained, or net/http closes the connection instead of pooling it and the
	// slot would free itself.
	tr1 := &http.Transport{}
	c1 := &http.Client{Transport: tr1}
	resp, err := c1.Get(url)
	if err != nil {
		t.Fatalf("client 1: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	// Client 2 needs its own connection and must not be served.
	tr2 := &http.Transport{}
	c2 := &http.Client{Transport: tr2, Timeout: 500 * time.Millisecond}
	if _, err := c2.Get(url); err == nil {
		t.Fatal("a second connection was served while the cap of 1 was held")
	}
	tr2.CloseIdleConnections()

	// Shutdown must still complete while the cap is saturated — a blocked
	// Accept must not keep Serve() alive, or a shutdown under load would hang.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown while saturated: %v", err)
	}
	tr1.CloseIdleConnections()
	select {
	case err := <-served:
		if err != http.ErrServerClosed {
			t.Errorf("Serve returned %v, want ErrServerClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after Shutdown while the cap was saturated")
	}
}

// --- WebSocket cap ---

func TestWatchEndpointHonoursWSCap(t *testing.T) {
	s := NewServer(NewRegistry(), "127.0.0.1:0")
	s.SetLogger(quietLogger())
	s.SetMaxWSConnections(1)
	s.AddWatchEndpoint(NewWatchRegistry())

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/watch"

	first, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("first dial: %v", err)
	}

	// The second upgrade must be refused with a clear status, not hang or be
	// accepted and immediately dropped.
	_, resp, err := websocket.Dial(ctx, wsURL, nil)
	if err == nil {
		t.Fatal("a second WebSocket was accepted above the cap of 1")
	}
	if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("refusal status = %v, want 503", resp)
	}

	// Closing the first connection frees the slot. The server releases it when
	// its ServeHTTP returns, which happens asynchronously after the close.
	_ = first.Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, _, err := websocket.Dial(ctx, wsURL, nil)
		if err == nil {
			_ = conn.Close(websocket.StatusNormalClosure, "")
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the WebSocket slot was never released: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestStreamEndpointSharesWSCap pins that the cap bounds the TOTAL number of
// WebSocket connections: watch and stream share one limiter, so a client cannot
// double the budget by using both endpoints.
func TestStreamEndpointSharesWSCap(t *testing.T) {
	s := NewServer(NewRegistry(), "127.0.0.1:0")
	s.SetLogger(quietLogger())
	s.SetMaxWSConnections(1)
	s.AddWatchEndpoint(NewWatchRegistry())
	s.RegisterStream("bulk", "inst", streamServerFunc(func(conn StreamConn) { <-conn.Done() }))

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	base := "ws" + strings.TrimPrefix(srv.URL, "http")

	watch, _, err := websocket.Dial(ctx, base+"/api/v1/watch", nil)
	if err != nil {
		t.Fatalf("watch dial: %v", err)
	}
	defer func() { _ = watch.Close(websocket.StatusNormalClosure, "") }()

	_, resp, err := websocket.Dial(ctx, base+"/api/v1/stream/bulk/inst", nil)
	if err == nil {
		t.Fatal("a stream connection was accepted while the shared cap was held by a watch")
	}
	if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("refusal status = %v, want 503", resp)
	}
}

func TestWSCapUnlimitedByDefaultWhenDisabled(t *testing.T) {
	s := NewServer(NewRegistry(), "127.0.0.1:0")
	s.SetLogger(quietLogger())
	s.SetMaxWSConnections(0) // explicit opt-out
	s.AddWatchEndpoint(NewWatchRegistry())

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/watch"

	var conns []*websocket.Conn
	defer func() {
		for _, c := range conns {
			_ = c.Close(websocket.StatusNormalClosure, "")
		}
	}()
	for i := 0; i < 12; i++ {
		c, _, err := websocket.Dial(ctx, wsURL, nil)
		if err != nil {
			t.Fatalf("dial %d with the cap disabled: %v", i, err)
		}
		conns = append(conns, c)
	}
}

// TestRefusedUpgradeDoesNotLeakASlot covers the ordering that matters most: the
// limiter is acquired before the upgrade, so a refusal for any *other* reason
// (here a cross-origin request) must still release it — otherwise a page
// hammering the endpoint would exhaust the cap permanently.
func TestRefusedUpgradeDoesNotLeakASlot(t *testing.T) {
	s := NewServer(NewRegistry(), "127.0.0.1:0")
	s.SetLogger(quietLogger())
	s.SetMaxWSConnections(2)
	s.AddWatchEndpoint(NewWatchRegistry())

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/watch"

	for i := 0; i < 10; i++ {
		if _, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
			HTTPHeader: http.Header{"Origin": []string{"http://evil.example"}},
		}); err == nil {
			t.Fatal("a cross-origin upgrade was accepted")
		}
	}

	if got := s.wsLimit.count(); got != 0 {
		t.Fatalf("%d slots still held after 10 rejected upgrades", got)
	}
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("a legitimate client was locked out by leaked slots: %v", err)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
}
