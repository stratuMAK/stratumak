// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Package emccmdtest pins how a *provider error* reaches a REST client.
//
// The primary case is emccmd — the surface that starts and stops the machine
// (MDI, state, jog, spindle, home), which had no test at all. scope_probe_test.go
// then shows the same defect on a second, differently-shaped API, because the
// cause is the shared dispatch path rather than anything specific to emccmd.
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

// TestRejectedCommandOverREST: a command the task refuses must not come back as
// a success status, and the reason must travel with it.
//
// This used to be HTTP 200 with body -1. REST dispatch went through the C
// callbacks struct even for a pure-Go provider, and that ABI has no error
// channel, so the //export trampoline substituted -1 and the client was told the
// call succeeded. A Go provider now serves REST directly.
func TestRejectedCommandOverREST(t *testing.T) {
	ts := serve(t, &stubTask{rc: 3, err: errors.New("Cannot issue MDI command when not homed")})

	for _, path := range []string{"/mdi", "/state", "/wait-complete"} {
		t.Run(path, func(t *testing.T) {
			code, body := post(t, ts, path, `{"command":"G0 X1","state":1,"timeout":1}`)
			if code == http.StatusOK {
				t.Fatalf("a refused command returned 200 with body %s", body)
			}
			if !strings.Contains(body, "not homed") {
				t.Errorf("status %d body %s does not carry the refusal reason — "+
					"an rc alone cannot say why", code, body)
			}
		})
	}
}

// TestRejectedCommandMapsErrno: a provider errno becomes the matching HTTP
// status rather than a blanket 500. EBUSY is the common one here — a command
// issued while the previous one is still running.
func TestRejectedCommandMapsErrno(t *testing.T) {
	ts := serve(t, &stubTask{rc: 3, err: syscall.EBUSY})
	code, body := post(t, ts, "/mdi", `{"command":"G0 X1"}`)
	if code != http.StatusConflict {
		t.Errorf("EBUSY rendered as %d (%s), want %d", code, body, http.StatusConflict)
	}
}

// TestWrappedErrnoStillMaps: a provider that wraps its errno for context is
// doing the normal thing, and the status must survive it. The mapping used a
// bare `switch err`, so every wrapped errno silently became a 500 — the
// opposite of what wrapping is for.
func TestWrappedErrnoStillMaps(t *testing.T) {
	ts := serve(t, &stubTask{rc: 3, err: fmt.Errorf("opening tool table: %w", syscall.ENOENT)})
	code, body := post(t, ts, "/mdi", `{"command":"G0 X1"}`)
	if code != http.StatusNotFound {
		t.Errorf("a wrapped ENOENT rendered as %d (%s), want %d", code, body, http.StatusNotFound)
	}
	if !strings.Contains(body, "tool table") {
		t.Errorf("the wrapping context was lost: %s", body)
	}
}

// TestFaultKindsMapToStatus pins the classification. A refusal is not a
// controller malfunction — reporting it as 500 tells a client the machine
// broke, invites a retry against a controller presumed sick, and is what
// monitoring escalates on.
func TestFaultKindsMapToStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind apiserver.FaultKind
		want int
	}{
		{"state forbids it", apiserver.FaultState, http.StatusConflict},
		{"module not ready", apiserver.FaultNotReady, http.StatusServiceUnavailable},
		{"not found", apiserver.FaultNotFound, http.StatusNotFound},
		{"genuine internal failure", apiserver.FaultInternal, http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := serve(t, &stubTask{rc: 3,
				err: apiserver.NewFault(tc.kind, errors.New("must be in MDI mode"))})
			code, body := post(t, ts, "/mdi", `{"command":"G0 X1"}`)
			if code != tc.want {
				t.Errorf("%v rendered as %d, want %d (%s)", tc.kind, code, tc.want, body)
			}
			if !strings.Contains(body, "must be in MDI mode") {
				t.Errorf("classifying lost the reason: %s", body)
			}
		})
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
