// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package apiserver

import (
	"context"
	"net/http"
	"sync"

	"github.com/coder/websocket"
)

// handleStreamUpgrade upgrades the HTTP connection to WebSocket and
// dispatches to the StreamServer's ServeConn in a goroutine.
func (s *Server) handleStreamUpgrade(w http.ResponseWriter, r *http.Request, server StreamServer) {
	// Refuse before upgrading: a stream connection holds a ServeConn goroutine
	// making cgo calls for as long as it lives (see limits.go).
	if !s.wsLimit.acquire() {
		s.logger.Warn("stream websocket refused: at the WebSocket connection cap",
			"open", s.wsLimit.count(), "path", r.URL.Path)
		writeErrorJSON(w, http.StatusServiceUnavailable, "too many WebSocket connections")
		return
	}
	defer s.wsLimit.release()

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Empty OriginPatterns → same-origin only (secure default). See
		// Server.wsOriginPatterns.
		OriginPatterns: s.wsOriginPatterns,
	})
	if err != nil {
		s.logger.Warn("stream websocket accept failed", "error", err)
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	sc := &streamConn{
		conn:   conn,
		ctx:    ctx,
		cancel: cancel,
	}

	s.streamMu.Lock()
	if s.streamConns == nil {
		s.streamConns = make(map[*streamConn]struct{})
	}
	s.streamConns[sc] = struct{}{}
	// Add to the WaitGroup INSIDE the lock, together with the map insert, so
	// Shutdown() (which takes streamMu then streamWg.Wait()) can never observe a
	// registered conn with a not-yet-incremented counter — which would let
	// Wait() return while ServeConn is about to make cgo calls into a
	// being-destroyed module (UAF), or race Add() against a returned Wait().
	s.streamWg.Add(1)
	s.streamMu.Unlock()

	// ServeConn blocks until the stream is done (poll_transmit returns <=0
	// or a write error occurs).
	server.ServeConn(sc)

	s.streamWg.Done()
	s.streamMu.Lock()
	delete(s.streamConns, sc)
	s.streamMu.Unlock()

	_ = conn.Close(websocket.StatusNormalClosure, "")
	cancel()
}

// streamConn implements StreamConn backed by a coder/websocket connection.
type streamConn struct {
	conn    *websocket.Conn
	ctx     context.Context
	cancel  context.CancelFunc
	writeMu sync.Mutex
}

func (c *streamConn) WriteBinary(data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.Write(c.ctx, websocket.MessageBinary, data)
}

func (c *streamConn) ReadBinary() ([]byte, error) {
	_, data, err := c.conn.Read(c.ctx)
	return data, err
}

func (c *streamConn) Done() <-chan struct{} {
	return c.ctx.Done()
}
