// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package ads

import (
	"net"
	"sync"
	"testing"
	"time"
)

// newTestServer starts a Server on a loopback ephemeral port and returns it
// with its actual address. The caller must Stop() it.
func newTestServer(t *testing.T, maxConns, maxSubs int, st *SymbolTable) (*Server, string) {
	t.Helper()
	s := NewServer("127.0.0.1:0", AMSNetID{1, 2, 3, 4, 1, 1}, 851, st, maxConns, maxSubs, false, nil)
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return s, s.listener.Addr().String()
}

// TestConnectionCapRejects verifies A7: with maxConns=2 the server accepts two
// connections and refuses (closes) the third.
func TestConnectionCapRejects(t *testing.T) {
	st := NewSymbolTable()
	st.Register("v", &mockPin{data: []byte{0}, size: 1})
	s, addr := newTestServer(t, 2, 256, st)
	defer s.Stop()

	// Two connections that stay open (the server blocks reading from them; the
	// deferred close keeps each conn referenced until the test ends).
	for i := 0; i < 2; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		defer func(c net.Conn) { _ = c.Close() }(c)
	}

	// Wait until both are registered so the third dial is genuinely over the cap.
	waitConns(t, s, 2)

	// The third connection is accepted then immediately closed by the server; a
	// read must observe EOF/closed quickly (not a timeout).
	c3, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial 3: %v", err)
	}
	defer func() { _ = c3.Close() }()
	_ = c3.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := c3.Read(buf); err == nil {
		t.Fatal("third connection returned data; expected it to be refused (closed)")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("third connection timed out; expected the server to close it (connection cap)")
	}

	// The connection count must never have exceeded the cap.
	s.connsMu.Lock()
	n := len(s.conns)
	s.connsMu.Unlock()
	if n > 2 {
		t.Fatalf("live connections = %d, want <= 2", n)
	}
}

func waitConns(t *testing.T, s *Server, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.connsMu.Lock()
		n := len(s.conns)
		s.connsMu.Unlock()
		if n >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d connections", want)
}

// TestStopJoinsWithIdleConnection verifies A5: Stop() force-closes a live idle
// connection and joins its goroutine, returning in bounded time (true join, no
// silent cap). If Stop() returned while the handler still ran, this would be an
// unsound free-barrier before Destroy() frees the HAL pins.
func TestStopJoinsWithIdleConnection(t *testing.T) {
	st := NewSymbolTable()
	st.Register("v", &mockPin{data: []byte{0}, size: 1})
	s, addr := newTestServer(t, 8, 256, st)

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	waitConns(t, s, 1)

	done := make(chan struct{})
	go func() {
		s.Stop() // must join the handler goroutine of the idle connection
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() did not return; true join blocked on an idle connection")
	}

	// After a true join, no connection goroutine remains registered.
	s.connsMu.Lock()
	n := len(s.conns)
	s.connsMu.Unlock()
	if n != 0 {
		t.Fatalf("after Stop() live connections = %d, want 0", n)
	}
}

// TestSubscriptionCap verifies A7: notifyManager.add refuses past the cap.
func TestSubscriptionCap(t *testing.T) {
	st := NewSymbolTable()
	st.Register("v", &mockPin{data: []byte{0}, size: 1})
	s := &Server{netID: AMSNetID{1, 2, 3, 4, 1, 1}, port: 851, symbols: st, maxSubs: 2}

	c1, c2 := net.Pipe()
	defer func() { _ = c1.Close() }()
	defer func() { _ = c2.Close() }()
	nm := newNotifyManager(s, c1)

	if _, ec := nm.add(IdxGrpProcessImageRW, 0, 1, ADSTransModeCyclic, 100*time.Millisecond); ec != ErrNoError {
		t.Fatalf("sub 1: errCode=0x%X, want 0", ec)
	}
	if _, ec := nm.add(IdxGrpProcessImageRW, 0, 1, ADSTransModeCyclic, 100*time.Millisecond); ec != ErrNoError {
		t.Fatalf("sub 2: errCode=0x%X, want 0", ec)
	}
	if _, ec := nm.add(IdxGrpProcessImageRW, 0, 1, ADSTransModeCyclic, 100*time.Millisecond); ec != ErrDeviceNoMemory {
		t.Fatalf("sub 3: errCode=0x%X, want ErrDeviceNoMemory(0x%X)", ec, ErrDeviceNoMemory)
	}
}

// TestHandleCap verifies A14: CreateHandle refuses once the live-handle map is
// at maxHandles. Pre-fill the map directly (same package) to avoid allocating
// 65536 real handles.
func TestHandleCap(t *testing.T) {
	st := NewSymbolTable()
	sym := st.Register("v", &mockPin{data: []byte{0}, size: 1})

	st.mu.Lock()
	for i := uint32(0); i < maxHandles; i++ {
		st.handles[i+1] = sym
	}
	st.mu.Unlock()

	if _, ec := st.CreateHandle("v"); ec != ErrDeviceNoMemory {
		t.Fatalf("CreateHandle at cap: errCode=0x%X, want ErrDeviceNoMemory(0x%X)", ec, ErrDeviceNoMemory)
	}

	// Releasing one makes room again.
	if ec := st.ReleaseHandle(1); ec != ErrNoError {
		t.Fatalf("ReleaseHandle: 0x%X", ec)
	}
	if _, ec := st.CreateHandle("v"); ec != ErrNoError {
		t.Fatalf("CreateHandle after release: errCode=0x%X, want 0", ec)
	}
}

// TestConcurrentProcessImageWritesNoLostUpdate verifies A13: concurrent
// overlapping process-image writes must not lose an update. Each goroutine
// owns a distinct byte of one symbol and hammers it via writeProcessImageRange
// (a read-modify-write); every byte must survive. Run under -race.
func TestConcurrentProcessImageWritesNoLostUpdate(t *testing.T) {
	const n = 16
	st := NewSymbolTable()
	st.RegisterAt("img", 0, &mockPin{data: make([]byte, n), size: n})

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx uint32) {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				if ec := st.writeProcessImageRange(idx, []byte{byte(idx + 1)}); ec != ErrNoError {
					t.Errorf("writeProcessImageRange byte %d: 0x%X", idx, ec)
					return
				}
			}
		}(uint32(i))
	}
	wg.Wait()

	img, ec := st.readProcessImageRange(0, n)
	if ec != ErrNoError {
		t.Fatalf("readProcessImageRange: 0x%X", ec)
	}
	for i := 0; i < n; i++ {
		if img[i] != byte(i+1) {
			t.Errorf("byte %d = 0x%02X, want 0x%02X (lost update)", i, img[i], byte(i+1))
		}
	}
}
