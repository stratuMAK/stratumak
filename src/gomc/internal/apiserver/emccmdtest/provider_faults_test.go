// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Status mapping for the non-command providers.
//
// Once provider errors became reachable at all, every one of these landed on
// HTTP 500 — the controller reporting itself broken while working correctly.
// These pin the classification each was given, through the real REST stack
// rather than by asserting on the error value, because the status is the part a
// client acts on.
package emccmdtest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/generated/gmi/ini"
	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
	"github.com/sittner/linuxcnc/src/gomc/internal/inirest"
	"github.com/sittner/linuxcnc/src/gomc/pkg/inifile"
)

func iniServer(t *testing.T, parsed *inifile.IniFile) *httptest.Server {
	t.Helper()
	reg := apiserver.NewRegistry()
	apiserver.RegisterMeta(ini.IniMeta)
	if err := inirest.Register(reg, parsed); err != nil {
		t.Fatalf("inirest.Register: %v", err)
	}
	srv := apiserver.NewServer(reg, "")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func postTo(t *testing.T, ts *httptest.Server, path, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(ts.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, readAll(t, resp)
}

// TestIniNotLoadedIsConflict: an INI-less launcher (halrun mode) is a permanent
// condition for the process, so it is a state conflict rather than a 503
// inviting a retry that can never succeed.
func TestIniNotLoadedIsConflict(t *testing.T) {
	ts := iniServer(t, nil)

	for _, tc := range []struct{ path, body string }{
		{"/api/v1/ini/query", `{"items":[{"section":"EMC","key":"MACHINE"}]}`},
		{"/api/v1/ini/parameter-file", `{}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			code, body := postTo(t, ts, tc.path, tc.body)
			if code != http.StatusConflict {
				t.Errorf("status %d, want %d (%s)", code, http.StatusConflict, body)
			}
			if !strings.Contains(body, "not loaded") {
				t.Errorf("reason lost: %s", body)
			}
		})
	}
}

// TestParameterFileMissingIsNotFound: unset, unresolvable and unreadable are one
// answer to a client — there is no parameter file — and none is a controller
// failure. The reason still travels, which is what an operator debugging their
// INI needs.
func TestParameterFileMissingIsNotFound(t *testing.T) {
	for _, tc := range []struct {
		name, ini, want string
	}{
		{"not configured", "[EMC]\nMACHINE = t\n", "PARAMETER_FILE not set"},
		{"configured but absent", "[RS274NGC]\nPARAMETER_FILE = nosuch.var\n", "paramfile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := inifile.ParseString(tc.ini)
			if err != nil {
				t.Fatal(err)
			}
			ts := iniServer(t, parsed)
			code, body := postTo(t, ts, "/api/v1/ini/parameter-file", `{}`)
			if code != http.StatusNotFound {
				t.Errorf("status %d, want %d (%s)", code, http.StatusNotFound, body)
			}
			if !strings.Contains(body, tc.want) {
				t.Errorf("reason lost: %s", body)
			}
		})
	}
}

// TestFaultCapacityIsServiceUnavailable pins the kind added for persist_sqlite's
// namespace cap. It is separate from FaultNotReady on purpose: the module there
// is running and healthy, it is simply full, and conflating the two would make
// the name lie in logs.
func TestFaultCapacityIsServiceUnavailable(t *testing.T) {
	ts := serve(t, &stubTask{rc: 3,
		err: apiserver.Faultf(apiserver.FaultCapacity, "too many open namespaces (limit 256)")})
	code, body := post(t, ts, "/mdi", `{"command":"G0 X1"}`)
	if code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want %d (%s)", code, http.StatusServiceUnavailable, body)
	}
	if !strings.Contains(body, "limit 256") {
		t.Errorf("the limit must be named so an operator can act: %s", body)
	}
}
