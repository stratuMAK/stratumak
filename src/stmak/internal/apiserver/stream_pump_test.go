// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package apiserver

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// pumpConn records what the pumps write and replays reads. block, while
// non-nil, holds every write until it is closed: a client that has stopped
// reading.
type pumpConn struct {
	mu    sync.Mutex
	msgs  [][]byte
	block chan struct{}
	fail  error
	done  chan struct{}
	reads [][]byte // returned by ReadBinary in order, then io.EOF

	readClosed bool // CloseRead was called
	readAfter  bool // ReadBinary was called after CloseRead
}

func newPumpConn() *pumpConn { return &pumpConn{done: make(chan struct{})} }

func (c *pumpConn) WriteBinary(data []byte) error {
	c.mu.Lock()
	block := c.block
	c.mu.Unlock()
	if block != nil {
		<-block
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail != nil {
		return c.fail
	}
	c.msgs = append(c.msgs, append([]byte(nil), data...))
	return nil
}

func (c *pumpConn) ReadBinary() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readClosed {
		c.readAfter = true
	}
	if len(c.reads) == 0 {
		return nil, io.EOF
	}
	d := c.reads[0]
	c.reads = c.reads[1:]
	return d, nil
}

func (c *pumpConn) Done() <-chan struct{} { return c.done }

func (c *pumpConn) CloseRead() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readClosed = true
}

func (c *pumpConn) unblock(fail error) {
	c.mu.Lock()
	c.fail = fail
	close(c.block)
	c.block = nil
	c.mu.Unlock()
}

func (c *pumpConn) messages() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]byte(nil), c.msgs...)
}

// counterProducer hands out an endless byte sequence 0,1,2,... in polls of up
// to chunk bytes, and ends the stream once limit bytes are out.
type counterProducer struct {
	next  atomic.Int64
	chunk int
	limit int64
	polls atomic.Int64
}

func (p *counterProducer) poll(buf []byte) int {
	p.polls.Add(1)
	start := p.next.Load()
	if start >= p.limit {
		return -1
	}
	n := min(int64(p.chunk), int64(len(buf)), p.limit-start)
	for i := int64(0); i < n; i++ {
		buf[i] = byte(start + i)
	}
	p.next.Store(start + n)
	return int(n)
}

// wantSequence checks that msgs are the producer's polls, one message each, in
// order and complete.
func wantSequence(t *testing.T, msgs [][]byte, chunk, total int) {
	t.Helper()
	pos := 0
	for i, m := range msgs {
		if want := min(chunk, total-pos); len(m) != want {
			t.Fatalf("message %d has %d bytes, want %d: poll results were merged or split", i, len(m), want)
		}
		for j, b := range m {
			if b != byte(pos+j) {
				t.Fatalf("message %d byte %d = %d, want %d: data reordered or lost", i, j, b, byte(pos+j))
			}
		}
		pos += len(m)
	}
	if pos != total {
		t.Fatalf("received %d bytes, want %d", pos, total)
	}
}

func TestPumpTransmitDeliversInOrder(t *testing.T) {
	c := newPumpConn()
	p := &counterProducer{chunk: 100, limit: 1 << 20}
	PumpTransmit(c, p.poll)
	wantSequence(t, c.messages(), 100, 1<<20)
}

// A transmit stream reads nothing, and must say so: otherwise the client's
// close frame is never answered and its close handshake times out.
func TestPumpTransmitClosesRead(t *testing.T) {
	c := newPumpConn()
	readClosed := false
	poll := func(buf []byte) int {
		c.mu.Lock()
		readClosed = c.readClosed
		c.mu.Unlock()
		return -1
	}
	PumpTransmit(c, poll)
	if !readClosed {
		t.Error("CloseRead was not called before the first poll")
	}
	if c.readAfter {
		t.Error("ReadBinary called after CloseRead")
	}
}

// A receive stream keeps reading, so it must not close its read side.
func TestPumpReceiveKeepsRead(t *testing.T) {
	c := newPumpConn()
	PumpReceive(c, func([]byte) int { return 0 })
	if c.readClosed {
		t.Error("PumpReceive called CloseRead")
	}
}

// The point of the pump: a client that stops reading must not stop the drain.
// Before, poll and write alternated on one goroutine, so a stalled write left
// the producer's ring to overflow.
func TestPumpTransmitDrainsWhileWriteBlocks(t *testing.T) {
	c := newPumpConn()
	c.block = make(chan struct{})
	p := &counterProducer{chunk: 1000, limit: 1 << 20}

	finished := make(chan struct{})
	go func() {
		PumpTransmit(c, p.poll)
		close(finished)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for p.next.Load() < 1<<20 {
		if time.Now().After(deadline) {
			t.Fatalf("drained %d of %d bytes while the write was blocked", p.next.Load(), 1<<20)
		}
		time.Sleep(time.Millisecond)
	}

	c.unblock(nil)
	<-finished
	wantSequence(t, c.messages(), 1000, 1<<20)
}

// Past pumpMaxQueued the drain stops polling instead of growing without bound;
// the producer's own overflow handling takes over from there.
func TestPumpTransmitBoundsBacklog(t *testing.T) {
	for _, chunk := range []int{pumpPollSize, 16} {
		c := newPumpConn()
		c.block = make(chan struct{})
		p := &counterProducer{chunk: chunk, limit: 1 << 40}

		finished := make(chan struct{})
		go func() {
			PumpTransmit(c, p.poll)
			close(finished)
		}()

		time.Sleep(300 * time.Millisecond)
		// Messages of chunk bytes, each costing chunk+overhead against the cap.
		held := p.next.Load()
		maxMsgs := int64(pumpMaxQueued / (chunk + pumpMsgOverhead))
		if held > (maxMsgs+1)*int64(chunk) {
			t.Errorf("chunk %d: drained %d bytes against a stalled client, want at most %d",
				chunk, held, (maxMsgs+1)*int64(chunk))
		}

		c.unblock(errors.New("client gone"))
		<-finished
	}
}

// A failed write ends the stream, and poll is not called once PumpTransmit
// has returned: the module behind it may be destroyed right after.
func TestPumpTransmitStopsOnWriteError(t *testing.T) {
	c := newPumpConn()
	c.fail = errors.New("client gone")
	p := &counterProducer{chunk: 10, limit: 1 << 40}
	PumpTransmit(c, p.poll)

	polls := p.polls.Load()
	time.Sleep(20 * time.Millisecond)
	if after := p.polls.Load(); after != polls {
		t.Errorf("poll called %d times after PumpTransmit returned", after-polls)
	}
}

func TestPumpTransmitStopsOnDone(t *testing.T) {
	c := newPumpConn()
	idle := func(buf []byte) int { return 0 }

	finished := make(chan struct{})
	go func() {
		PumpTransmit(c, idle)
		close(finished)
	}()
	close(c.done)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("PumpTransmit did not return after Done")
	}
}

// What was drained before the producer ended still reaches the client.
func TestPumpTransmitFlushesAtEnd(t *testing.T) {
	c := newPumpConn()
	var calls int
	poll := func(buf []byte) int {
		calls++
		if calls > 1 {
			return -1
		}
		return copy(buf, []byte("tail"))
	}
	PumpTransmit(c, poll)
	if msgs := c.messages(); len(msgs) != 1 || !bytes.Equal(msgs[0], []byte("tail")) {
		t.Errorf("received %q, want one message %q", msgs, "tail")
	}
}

func TestPumpReceive(t *testing.T) {
	c := newPumpConn()
	c.reads = [][]byte{[]byte("a"), {}, []byte("bc")}
	var got []string
	PumpReceive(c, func(data []byte) int {
		got = append(got, string(data))
		return 0
	})
	if len(got) != 2 || got[0] != "a" || got[1] != "bc" {
		t.Errorf("delivered %q, want [a bc] (empty messages skipped)", got)
	}

	// A refusal from the module ends the connection.
	c.reads = [][]byte{[]byte("x"), []byte("y")}
	got = nil
	PumpReceive(c, func(data []byte) int {
		got = append(got, string(data))
		return -1
	})
	if len(got) != 1 {
		t.Errorf("delivered %q after the module refused, want only [x]", got)
	}
}
