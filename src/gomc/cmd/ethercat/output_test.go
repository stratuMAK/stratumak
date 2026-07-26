// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/generated/gmi/ethercatclient"
)

// These tests exercise the diagnostic-CLI command formatters and their error
// paths without a live gomc-server: each command is driven against an
// httptest server returning canned JSON, so the real generated REST client and
// the real formatting/validation code run. stdout is captured for the
// happy-path output assertions.

// route is one mocked REST response. status 0 means 200.
type route struct {
	status int
	body   interface{} // JSON-encoded; a typed struct, or map[string]string{"error":...}
}

// serveRoutes starts an httptest server answering the given sub-paths (the part
// after /api/v1/<instance>, query string excluded) and returns a client pointed
// at it plus a cleanup func. An unmatched path yields 404 so a missed mock is a
// visible failure, not a silent zero value.
func serveRoutes(t *testing.T, routes map[string]route) (*ethercatclient.EthercatClient, func()) {
	t.Helper()
	const prefix = "/api/v1/ethercat"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, prefix)
		w.Header().Set("Content-Type", "application/json")
		rt, ok := routes[p]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "no mock route for " + p})
			return
		}
		if rt.status != 0 {
			w.WriteHeader(rt.status)
		}
		if rt.body != nil {
			_ = json.NewEncoder(w).Encode(rt.body)
		}
	}))
	client := ethercatclient.NewEthercatClientInstance(srv.URL, "ethercat")
	return client, srv.Close
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what was
// written. fn must not call t.Fatal (that would leave os.Stdout unrestored);
// assign any error to a captured variable and assert it after the call.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	return <-done
}

func testOpts() *GlobalOpts {
	return &GlobalOpts{Masters: "-", Positions: "-", Aliases: "-", Domains: "-", Verbosity: Normal}
}

func mustContain(t *testing.T, what, out, want string) {
	t.Helper()
	if !strings.Contains(out, want) {
		t.Errorf("%s: expected output to contain %q, got:\n%s", what, want, out)
	}
}

// ─── Happy-path formatters (U) ──────────────────────────────────────────────

func TestVersionOutput(t *testing.T) {
	client, cleanup := serveRoutes(t, map[string]route{
		"/module": {body: ethercatclient.ModuleInfo{Version: "1.6", IoctlVersionMagic: 37}},
	})
	defer cleanup()

	var err error
	out := captureStdout(t, func() { err = cmdVersion(client, testOpts(), nil) })
	if err != nil {
		t.Fatalf("cmdVersion: %v", err)
	}
	mustContain(t, "version", out, "IgH EtherCAT master 1.6 (API Version 37)")
}

func TestMasterOutput(t *testing.T) {
	for _, tc := range []struct {
		name      string
		active    bool
		wantPhase string
		wantAct   string
	}{
		{"active", true, "Phase: Operation", "Active: yes"},
		{"idle", false, "Phase: Idle", "Active: no"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, cleanup := serveRoutes(t, map[string]route{
				"/master": {body: ethercatclient.MasterInfo{SlaveCount: 3, Phase: 1, Active: tc.active}},
			})
			defer cleanup()

			var err error
			out := captureStdout(t, func() { err = cmdMaster(client, testOpts(), nil) })
			if err != nil {
				t.Fatalf("cmdMaster: %v", err)
			}
			mustContain(t, "master", out, "Master0")
			mustContain(t, "master", out, "Slaves: 3")
			mustContain(t, "master", out, tc.wantPhase)
			mustContain(t, "master", out, tc.wantAct)
		})
	}
}

func TestSlavesOutput(t *testing.T) {
	client, cleanup := serveRoutes(t, map[string]route{
		"/master":   {body: ethercatclient.MasterInfo{SlaveCount: 2}},
		"/slaves/0": {body: ethercatclient.SlaveInfo{Position: 0, AlState: 0x08, Name: "EL2008"}},
		"/slaves/1": {body: ethercatclient.SlaveInfo{Position: 1, AlState: 0x02, Name: "EL1008"}},
	})
	defer cleanup()

	var err error
	out := captureStdout(t, func() { err = cmdSlaves(client, testOpts(), nil) })
	if err != nil {
		t.Fatalf("cmdSlaves: %v", err)
	}
	mustContain(t, "slaves", out, "0  0:0  OP")
	mustContain(t, "slaves", out, "EL2008")
	mustContain(t, "slaves", out, "PREOP")
	mustContain(t, "slaves", out, "EL1008")
}

func TestDomainsOutput(t *testing.T) {
	client, cleanup := serveRoutes(t, map[string]route{
		"/master":    {body: ethercatclient.MasterInfo{DomainCount: 1}},
		"/domains/0": {body: ethercatclient.DomainInfo{Index: 0, DataSize: 6, WorkingCounter: [2]uint16{3, 0}, ExpectedWorkingCounter: 3}},
	})
	defer cleanup()

	var err error
	out := captureStdout(t, func() { err = cmdDomains(client, testOpts(), nil) })
	if err != nil {
		t.Fatalf("cmdDomains: %v", err)
	}
	mustContain(t, "domains", out, "Domain0: LogBaseAddr 0x00000000, Size   6, WorkingCounter 3/3")
}

func TestSdosOutput(t *testing.T) {
	// sdo_spec in the entry path is int32(SdoIndex): 0x1018 == 4120.
	client, cleanup := serveRoutes(t, map[string]route{
		"/master":                       {body: ethercatclient.MasterInfo{SlaveCount: 1}},
		"/slaves/0":                     {body: ethercatclient.SlaveInfo{SdoCount: 1}},
		"/slaves/0/sdos/0":              {body: ethercatclient.SlaveSdoInfo{SdoIndex: 0x1018, MaxSubindex: 0, Name: "Identity Object"}},
		"/slaves/0/sdos/4120/entries/0": {body: ethercatclient.SdoEntryInfo{DataType: 0x0005, BitLength: 8, ReadAccess: [3]bool{true, true, true}, Description: "Number of entries"}},
	})
	defer cleanup()

	var err error
	out := captureStdout(t, func() { err = cmdSdos(client, testOpts(), nil) })
	if err != nil {
		t.Fatalf("cmdSdos: %v", err)
	}
	mustContain(t, "sdos", out, `SDO 0x1018, "Identity Object"`)
	mustContain(t, "sdos", out, "UNSIGNED8")
	mustContain(t, "sdos", out, "Number of entries")
}

// The reg/sii/foe/soe read commands were previously un-unit-tested (hotspot #3
// deferral). Cover their happy paths here.

func TestRegReadOutput(t *testing.T) {
	client, cleanup := serveRoutes(t, map[string]route{
		// address 0x0130 == 304
		"/slaves/0/registers/304": {body: ethercatclient.RegReadResult{Data: []byte{0x08, 0x00}}},
	})
	defer cleanup()

	var err error
	out := captureStdout(t, func() { err = cmdRegRead(client, testOpts(), []string{"0x0130", "2"}) })
	if err != nil {
		t.Fatalf("cmdRegRead: %v", err)
	}
	mustContain(t, "reg_read", out, "0130  08 00")
}

func TestSiiReadOutput(t *testing.T) {
	client, cleanup := serveRoutes(t, map[string]route{
		"/slaves/0/sii": {body: ethercatclient.SiiData{Words: []byte("SIIBYTES")}},
	})
	defer cleanup()

	var err error
	out := captureStdout(t, func() { err = cmdSiiRead(client, testOpts(), nil) })
	if err != nil {
		t.Fatalf("cmdSiiRead: %v", err)
	}
	if out != "SIIBYTES" {
		t.Errorf("sii_read: expected raw %q, got %q", "SIIBYTES", out)
	}
}

func TestFoeReadOutput(t *testing.T) {
	client, cleanup := serveRoutes(t, map[string]route{
		"/slaves/0/foe/fw.bin": {body: ethercatclient.FoeReadResult{Result: 0, Data: []byte("firmware")}},
	})
	defer cleanup()

	var err error
	out := captureStdout(t, func() { err = cmdFoeRead(client, testOpts(), []string{"fw.bin"}) })
	if err != nil {
		t.Fatalf("cmdFoeRead: %v", err)
	}
	if out != "firmware" {
		t.Errorf("foe_read: expected %q, got %q", "firmware", out)
	}
}

func TestSoeReadOutput(t *testing.T) {
	client, cleanup := serveRoutes(t, map[string]route{
		// idn 0x1000 == 4096
		"/slaves/0/soe/0/4096": {body: ethercatclient.SoeReadResult{ErrorCode: 0, Data: []byte{0x01, 0x00}}},
	})
	defer cleanup()

	var err error
	out := captureStdout(t, func() { err = cmdSoeRead(client, testOpts(), []string{"0", "0x1000"}) })
	if err != nil {
		t.Fatalf("cmdSoeRead: %v", err)
	}
	mustContain(t, "soe_read", out, "0x01 0x00")
}

func TestUploadOutput(t *testing.T) {
	client, cleanup := serveRoutes(t, map[string]route{
		"/slaves/0/sdo/4120/1": {body: ethercatclient.SdoUploadResult{AbortCode: 0, Data: []byte{0x2a, 0x00, 0x00, 0x00}}},
	})
	defer cleanup()

	var err error
	out := captureStdout(t, func() { err = cmdUpload(client, testOpts(), []string{"0x1018", "1"}) })
	if err != nil {
		t.Fatalf("cmdUpload: %v", err)
	}
	mustContain(t, "upload", out, "0x2a")
}

// ─── Fault paths (FP) ───────────────────────────────────────────────────────

// TestTransportError: the server is unreachable → the command surfaces the
// transport error rather than printing a partial/empty result.
func TestTransportError(t *testing.T) {
	client, cleanup := serveRoutes(t, map[string]route{})
	cleanup() // close the listener before the call → connection refused

	if err := cmdMaster(client, testOpts(), nil); err == nil {
		t.Fatal("expected a transport error from an unreachable server, got nil")
	}
}

// TestAPIErrorPropagates: an HTTP 5xx with a JSON error body is surfaced.
func TestAPIErrorPropagates(t *testing.T) {
	client, cleanup := serveRoutes(t, map[string]route{
		"/master": {status: http.StatusInternalServerError, body: map[string]string{"error": "master busy"}},
	})
	defer cleanup()

	err := cmdMaster(client, testOpts(), nil)
	if err == nil {
		t.Fatal("expected an API error, got nil")
	}
	if !strings.Contains(err.Error(), "master busy") {
		t.Errorf("expected error to carry the server message, got: %v", err)
	}
}

// TestSlaveFetchErrorMidIteration: a per-slave GET failing partway through must
// abort the whole command with an error, not print a truncated table.
func TestSlaveFetchErrorMidIteration(t *testing.T) {
	client, cleanup := serveRoutes(t, map[string]route{
		"/master":   {body: ethercatclient.MasterInfo{SlaveCount: 2}},
		"/slaves/0": {body: ethercatclient.SlaveInfo{AlState: 0x08, Name: "EL2008"}},
		"/slaves/1": {status: http.StatusInternalServerError, body: map[string]string{"error": "slave 1 gone"}},
	})
	defer cleanup()

	if err := cmdSlaves(client, testOpts(), nil); err == nil {
		t.Fatal("expected cmdSlaves to fail when a slave fetch errors, got nil")
	}
}

// TestInBandResultErrors: FoE/SoE/SDO carry an in-band failure code in a 200
// response; the command must turn each into an error.
func TestInBandResultErrors(t *testing.T) {
	t.Run("foe-read-result", func(t *testing.T) {
		client, cleanup := serveRoutes(t, map[string]route{
			"/slaves/0/foe/x.bin": {body: ethercatclient.FoeReadResult{Result: 5, ErrorCode: 0x8001}},
		})
		defer cleanup()
		err := cmdFoeRead(client, testOpts(), []string{"x.bin"})
		if err == nil || !strings.Contains(err.Error(), "FoE read failed") {
			t.Fatalf("expected FoE read failure, got: %v", err)
		}
	})
	t.Run("soe-read-errorcode", func(t *testing.T) {
		client, cleanup := serveRoutes(t, map[string]route{
			"/slaves/0/soe/0/4096": {body: ethercatclient.SoeReadResult{ErrorCode: 0x1234}},
		})
		defer cleanup()
		err := cmdSoeRead(client, testOpts(), []string{"0", "0x1000"})
		if err == nil || !strings.Contains(err.Error(), "SoE read error") {
			t.Fatalf("expected SoE read error, got: %v", err)
		}
	})
	t.Run("sdo-upload-abort", func(t *testing.T) {
		client, cleanup := serveRoutes(t, map[string]route{
			"/slaves/0/sdo/4120/1": {body: ethercatclient.SdoUploadResult{AbortCode: 0x06090011}},
		})
		defer cleanup()
		err := cmdUpload(client, testOpts(), []string{"0x1018", "1"})
		if err == nil || !strings.Contains(err.Error(), "SDO abort code") {
			t.Fatalf("expected SDO abort, got: %v", err)
		}
	})
}

// TestArgValidation: malformed arguments are rejected before any REST call. The
// client points at an unreachable server, so a passing case proves validation
// fired first (a reached client would give a different, transport error — but
// we assert the specific validation message).
func TestArgValidation(t *testing.T) {
	client, cleanup := serveRoutes(t, map[string]route{})
	cleanup() // ensure the client is never usable — validation must precede it

	cases := []struct {
		name    string
		fn      func(*ethercatclient.EthercatClient, *GlobalOpts, []string) error
		args    []string
		wantMsg string
	}{
		{"slaves-extra-args", cmdSlaves, []string{"x"}, "takes no arguments"},
		{"states-no-arg", cmdStates, nil, "exactly one argument"},
		{"states-invalid", cmdStates, []string{"BOGUS"}, "invalid state"},
		{"upload-missing-arg", cmdUpload, []string{"0x1018"}, "usage: upload"},
		{"reg-read-missing-arg", cmdRegRead, []string{"0x130"}, "usage: reg_read"},
		{"reg-read-bad-addr", cmdRegRead, []string{"zz", "2"}, "invalid address"},
		{"foe-read-missing-arg", cmdFoeRead, nil, "usage: foe_read"},
		{"soe-read-missing-arg", cmdSoeRead, []string{"0"}, "usage: soe_read"},
		{"debug-missing-level", cmdDebug, nil, "debug level required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fn(client, testOpts(), tc.args)
			if err == nil {
				t.Fatalf("expected a validation error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("expected error to contain %q, got: %v", tc.wantMsg, err)
			}
		})
	}
}
