// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Package emccmdtest pins how a rejected machine command reaches a REST client.
//
// This is the surface that starts and stops the machine — MDI, state, jog,
// spindle, home — and it had no test at all. The tracked item "surface RCS
// command errors to clients" described it as returning the RCS code in a
// normal HTTP 200 body, so that a caller discarding the return could not tell
// a refusal from success. These tests establish what it actually does, so the
// contract stops being a matter of recollection.
package emccmdtest

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/generated/gmi/emccmd"
	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
)

// stubTask implements just enough of EmccmdCallbacks to drive the dispatch
// layer. Each method returns whatever the test asked for.
type stubTask struct {
	rc  int32
	err error
}

func (s *stubTask) SetState(int32) (int32, error)                { return s.rc, s.err }
func (s *stubTask) SetMode(int32) (int32, error)                 { return s.rc, s.err }
func (s *stubTask) AutoCmd(emccmd.AutoCmd, int32) (int32, error) { return s.rc, s.err }
func (s *stubTask) Mdi(string) (int32, error)                    { return s.rc, s.err }
func (s *stubTask) Jog(emccmd.JogType, bool, int32, float64, float64) (int32, error) {
	return s.rc, s.err
}
func (s *stubTask) JogStop(bool, int32) (int32, error) { return s.rc, s.err }
func (s *stubTask) Spindle(emccmd.SpindleCmd, float64, int32, int32) (int32, error) {
	return s.rc, s.err
}
func (s *stubTask) Home(int32) (int32, error)                        { return s.rc, s.err }
func (s *stubTask) Unhome(int32) (int32, error)                      { return s.rc, s.err }
func (s *stubTask) OverrideLimits(int32) (int32, error)              { return s.rc, s.err }
func (s *stubTask) TeleopEnable(bool) (int32, error)                 { return s.rc, s.err }
func (s *stubTask) SetFeedOverride(float64) (int32, error)           { return s.rc, s.err }
func (s *stubTask) SetSpindleOverride(float64, int32) (int32, error) { return s.rc, s.err }
func (s *stubTask) SetRapidOverride(float64) (int32, error)          { return s.rc, s.err }
func (s *stubTask) SetMaxVelocity(float64) (int32, error)            { return s.rc, s.err }
func (s *stubTask) SetFoEnable(bool) (int32, error)                  { return s.rc, s.err }
func (s *stubTask) SetFhEnable(bool) (int32, error)                  { return s.rc, s.err }
func (s *stubTask) SetSoEnable(bool, int32) (int32, error)           { return s.rc, s.err }
func (s *stubTask) Flood(bool) (int32, error)                        { return s.rc, s.err }
func (s *stubTask) Mist(bool) (int32, error)                         { return s.rc, s.err }
func (s *stubTask) Brake(bool, int32) (int32, error)                 { return s.rc, s.err }
func (s *stubTask) Lube(bool) (int32, error)                         { return s.rc, s.err }
func (s *stubTask) Abort() (int32, error)                            { return s.rc, s.err }
func (s *stubTask) TaskPlanSynch() (int32, error)                    { return s.rc, s.err }
func (s *stubTask) SetOptionalStop(bool) (int32, error)              { return s.rc, s.err }
func (s *stubTask) SetBlockDelete(bool) (int32, error)               { return s.rc, s.err }
func (s *stubTask) LoadToolTable(string) (int32, error)              { return s.rc, s.err }
func (s *stubTask) ToolUnload() (int32, error)                       { return s.rc, s.err }
func (s *stubTask) ProgramOpen(string) (int32, error)                { return s.rc, s.err }
func (s *stubTask) WaitComplete(float64) (int32, error)              { return s.rc, s.err }
func (s *stubTask) SetDebug(int32) (int32, error)                    { return s.rc, s.err }
func (s *stubTask) SetJogAxis(int32) (int32, error)                  { return s.rc, s.err }
func (s *stubTask) SetJogIncrement(float64) (int32, error)           { return s.rc, s.err }
func (s *stubTask) SetJogSpeed(float64) (int32, error)               { return s.rc, s.err }
func (s *stubTask) SetAjogSpeed(float64) (int32, error)              { return s.rc, s.err }

func post(t *testing.T, ts *httptest.Server, path, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(ts.URL+"/api/v1/emccmd"+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var buf strings.Builder
	if _, err := fmt.Fprint(&buf, readAll(t, resp)); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, buf.String()
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	var sb strings.Builder
	b := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(b)
		sb.Write(b[:n])
		if err != nil {
			break
		}
	}
	return sb.String()
}

func serve(t *testing.T, stub *stubTask) *httptest.Server {
	t.Helper()
	reg := apiserver.NewRegistry()
	apiserver.RegisterMeta(emccmd.EmccmdMeta)
	if err := emccmd.RegisterEmccmdAPI(reg, "emccmd", stub); err != nil {
		t.Fatalf("RegisterEmccmdAPI: %v", err)
	}
	srv := apiserver.NewServer(reg, "")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// TestRejectedCommandOverREST documents what a refused machine command looks
// like to a REST client today. It is not what the code reads like: the handler
// set in emccmd_bridge.go propagates the provider's error, but that set is not
// what REST uses.
//
// RegisterEmccmdAPI wraps even a pure-Go provider into the C callbacks struct
// (BuildEmccmdCallbacks), so REST dispatch is C-ABI-mediated for every module,
// Go or C. The C signature for these commands is `int32_t (*)(...)`, which has
// no error channel, so the //export trampoline collapses the Go error to -1 and
// the dispatch marshals that -1 as an ordinary result. The client gets HTTP 200.
//
// Consequence: a caller that checks the HTTP status — the normal thing to do —
// cannot tell a refused MDI from an executed one, and the reason ("Cannot issue
// MDI command when not homed") is destroyed at the C boundary.
//
// This asserts the current shape rather than the desired one, so that whoever
// fixes it sees these tests fail and updates them deliberately. Tracked in
// PRODUCTION_READINESS.md as "Surface RCS command errors to clients".
func TestRejectedCommandOverREST(t *testing.T) {
	ts := serve(t, &stubTask{rc: 3, err: errors.New("Cannot issue MDI command when not homed")})

	for _, path := range []string{"/mdi", "/state", "/wait-complete"} {
		t.Run(path, func(t *testing.T) {
			code, body := post(t, ts, path, `{"command":"G0 X1","state":1,"timeout":1}`)
			if code != http.StatusOK {
				t.Fatalf("status %d — a refusal now has its own status; "+
					"the RCS-error gap may be fixed, update this test", code)
			}
			if strings.TrimSpace(body) != "-1" {
				t.Errorf("body %q, want the flattened -1", body)
			}
			if strings.Contains(body, "not homed") {
				t.Error("the reason survived to the client; the RCS-error gap may be " +
					"fixed, update this test")
			}
		})
	}
}

// TestRejectedCommandErrnoIsAlsoFlattened: the apiserver maps a provider errno
// to an HTTP status (EBUSY → 409) for every API that dispatches in Go. These
// commands never reach that code, because the errno is already a -1 by the time
// dispatch returns.
func TestRejectedCommandErrnoIsAlsoFlattened(t *testing.T) {
	ts := serve(t, &stubTask{rc: 3, err: syscall.EBUSY})
	code, body := post(t, ts, "/mdi", `{"command":"G0 X1"}`)
	if code != http.StatusOK || strings.TrimSpace(body) != "-1" {
		t.Errorf("EBUSY rendered as %d/%s; the errno mapping now reaches these commands, "+
			"update this test", code, body)
	}
}

// TestAcceptedCommandReturnsRC pins the success shape: HTTP 200 whose body is
// the bare RCS code.
func TestAcceptedCommandReturnsRC(t *testing.T) {
	ts := serve(t, &stubTask{rc: 1})
	code, body := post(t, ts, "/mdi", `{"command":"G0 X1"}`)
	if code != http.StatusOK {
		t.Fatalf("an accepted command returned %d: %s", code, body)
	}
	var rc int32
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &rc); err != nil {
		t.Fatalf("body %q is not a bare rc: %v", body, err)
	}
	if rc != 1 {
		t.Errorf("rc = %d, want 1 (RCS_DONE)", rc)
	}
}

// TestValidationRejectionIsBadRequest pins the third refusal shape: a value the
// IDL constrains never reaches the task at all.
func TestValidationRejectionIsBadRequest(t *testing.T) {
	ts := serve(t, &stubTask{rc: 1})
	// emccmd.gmi constrains the MDI command to @maxlen(254).
	code, body := post(t, ts, "/mdi", fmt.Sprintf(`{"command":%q}`, strings.Repeat("X", 300)))
	if code != http.StatusBadRequest {
		t.Errorf("an over-long MDI returned %d: %s; want 400", code, body)
	}
}
