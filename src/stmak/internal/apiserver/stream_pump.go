// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package apiserver

import (
	"sync"
	"time"
)

// The loops below are the runtime half of every generated stream_server: the
// generator emits only the cgo call into the module's callback (poll_transmit,
// data_received) and hands it here.

const (
	// pumpPollSize is the buffer handed to one poll_transmit call, and so the
	// largest message a transmit stream sends.
	pumpPollSize = 64 << 10
	// pumpMaxQueued bounds the memory a connection holds for a slow client,
	// counting pumpMsgOverhead per message. Past it the drain stops polling and
	// the producer's own overflow handling takes over; the pump never drops a
	// message itself.
	pumpMaxQueued = 4 << 20
	// pumpMsgOverhead is what a queued message costs beyond its bytes, so that a
	// backlog of tiny messages is bounded too.
	pumpMsgOverhead = 64
	// pumpIdle is the wait after a poll that returned nothing.
	pumpIdle = time.Millisecond
)

// pumpQueue holds the messages drained but not yet sent.
type pumpQueue struct {
	mu    sync.Mutex
	msgs  [][]byte
	cost  int           // bytes + overhead of everything queued or being written
	ready chan struct{} // signalled on push; capacity 1
}

func (q *pumpQueue) room() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.cost+pumpPollSize+pumpMsgOverhead <= pumpMaxQueued
}

func (q *pumpQueue) push(msg []byte) {
	q.mu.Lock()
	q.msgs = append(q.msgs, msg)
	q.cost += len(msg) + pumpMsgOverhead
	q.mu.Unlock()
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

// take removes every queued message. Their cost stays counted until release:
// a message being written still occupies memory.
func (q *pumpQueue) take() [][]byte {
	q.mu.Lock()
	defer q.mu.Unlock()
	msgs := q.msgs
	q.msgs = nil
	return msgs
}

func (q *pumpQueue) release(msg []byte) {
	q.mu.Lock()
	q.cost -= len(msg) + pumpMsgOverhead
	q.mu.Unlock()
}

// PumpTransmit serves a poll_transmit stream: it drains the producer through
// poll and sends each poll result to conn as one message, until poll reports
// the end (< 0), a write fails or conn is done.
//
// Draining and sending run on separate goroutines. The producer is typically a
// realtime ring that must not wait for anyone, so how fast it is drained must
// not depend on how fast the client reads: while one message is being written,
// polling goes on and the results queue up, up to pumpMaxQueued.
//
// PumpTransmit returns only after the drain goroutine has exited: poll is
// typically a cgo call into a module, and the caller's return is what lets the
// module be destroyed.
func PumpTransmit(conn StreamConn, poll func(buf []byte) int) {
	// Nothing reads from a transmit stream, so without this the client's
	// close frame would go unanswered and its close handshake would time out
	// (halsampler -n took 5 s longer than its samples).
	conn.CloseRead()

	q := &pumpQueue{ready: make(chan struct{}, 1)}
	stop := make(chan struct{})
	drained := make(chan struct{})

	go func() {
		defer close(drained)
		buf := make([]byte, pumpPollSize)
		for {
			select {
			case <-stop:
				return
			case <-conn.Done():
				return
			default:
			}
			n := 0
			if q.room() {
				n = poll(buf)
				if n < 0 {
					return
				}
				if n > 0 {
					q.push(append([]byte(nil), buf[:n]...))
				}
			}
			if n == 0 {
				select {
				case <-stop:
					return
				case <-conn.Done():
					return
				case <-time.After(pumpIdle):
				}
			}
		}
	}()

	defer func() {
		close(stop)
		<-drained
	}()
	for {
		msgs := q.take()
		if len(msgs) == 0 {
			select {
			case <-q.ready:
				continue
			case <-drained:
				// Whatever was drained before the end still goes out.
				if msgs = q.take(); len(msgs) == 0 {
					return
				}
			}
		}
		for _, m := range msgs {
			err := conn.WriteBinary(m)
			q.release(m)
			if err != nil {
				return
			}
		}
	}
}

// PumpReceive serves a data_received stream: it hands every message the client
// sends to deliver, until reading fails (the client closed) or deliver reports
// an error (< 0). Empty messages are skipped.
func PumpReceive(conn StreamConn, deliver func(data []byte) int) {
	for {
		data, err := conn.ReadBinary()
		if err != nil {
			return
		}
		if len(data) == 0 {
			continue
		}
		if deliver(data) < 0 {
			return
		}
	}
}
