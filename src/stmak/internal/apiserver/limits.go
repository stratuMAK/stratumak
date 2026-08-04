// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package apiserver

import (
	"net"
	"sync"
	"sync/atomic"
)

// Connection limits (finding N9).
//
// Two separate caps, because they bound two different failure modes:
//
//   - The overall cap bounds *accepted TCP connections*. It is the backstop
//     against an accept-storm — a wedged client reconnecting in a tight loop, a
//     scanner — where each connection costs a goroutine and read buffers even if
//     it never sends a complete request. ReadHeaderTimeout/IdleTimeout bound how
//     long any one of them lives; this bounds how many exist at once.
//
//   - The WebSocket cap bounds the connections that hold *standing* work. Every
//     WS connection can subscribe to the registered watch functions, and each
//     subscription runs its own ticker goroutine calling into generated/cgo code
//     on every tick — i.e. straight into modules adjacent to the RT path. That
//     is the axis where "one more client" is not free.
//
// Neither is a security control: without authentication a local process can
// open connections up to the cap regardless. They are blast-radius limits, so
// the defaults are deliberately generous — a machine legitimately runs several
// panels, a pendant, halscope and diagnostic tools at once, and a cap that bites
// normal use would be worse than the DoS it prevents.
//
// A WebSocket connection is a hijacked HTTP connection, so it counts against
// BOTH caps. Keep maxWSConnections comfortably below maxConnections, or WS
// clients can starve plain REST of accept slots.
const (
	// defaultMaxConnections caps concurrently accepted HTTP connections.
	defaultMaxConnections = 256
	// defaultMaxWSConnections caps concurrent WebSocket connections (watch and
	// stream endpoints together).
	defaultMaxWSConnections = 64
)

// wsLimiter bounds the number of concurrent WebSocket connections. A nil
// limiter, or one with max <= 0, is unlimited.
type wsLimiter struct {
	max int64
	n   atomic.Int64
}

func newWSLimiter(max int) *wsLimiter {
	return &wsLimiter{max: int64(max)}
}

// acquire takes a slot, reporting false if the cap is already reached.
func (l *wsLimiter) acquire() bool {
	if l == nil || l.max <= 0 {
		return true
	}
	if l.n.Add(1) > l.max {
		l.n.Add(-1)
		return false
	}
	return true
}

// release returns a slot taken by a successful acquire.
func (l *wsLimiter) release() {
	if l == nil || l.max <= 0 {
		return
	}
	l.n.Add(-1)
}

// count returns the number of slots currently held (for logging).
func (l *wsLimiter) count() int64 {
	if l == nil {
		return 0
	}
	return l.n.Load()
}

// limitListener wraps ln so that at most max connections are accepted at a
// time. Accept blocks while the cap is reached and resumes as connections
// close; further connections wait in the kernel backlog. max <= 0 returns ln
// unchanged.
//
// Blocking (rather than accept-then-close) is deliberate: a burst from a
// legitimate client is served a moment later instead of being met with a reset,
// while a storm simply stops consuming process resources.
func limitListener(ln net.Listener, max int) net.Listener {
	if max <= 0 {
		return ln
	}
	return &limitedListener{
		Listener: ln,
		sem:      make(chan struct{}, max),
		done:     make(chan struct{}),
	}
}

type limitedListener struct {
	net.Listener
	sem       chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func (l *limitedListener) Accept() (net.Conn, error) {
	// The wait for a slot must be abortable. Blocking on the semaphore alone
	// would leave Accept parked after Close — so http.Server.Serve would never
	// return ErrServerClosed while the cap was saturated, and a shutdown
	// triggered under load would hang instead of tearing the machine down in
	// order.
	select {
	case l.sem <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	conn, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	return &limitedConn{Conn: conn, release: func() { <-l.sem }}, nil
}

func (l *limitedListener) Close() error {
	l.closeOnce.Do(func() { close(l.done) })
	return l.Listener.Close()
}

// limitedConn releases its listener slot on the first Close. net/http closes a
// connection it has finished with, and a hijacked (WebSocket) connection is
// closed by the WS layer when the socket goes away — so the slot is held for
// exactly as long as the connection exists, which is what makes the overall cap
// cover WS connections too.
type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
