// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package emccmdtest

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/generated/gmi/ini"
	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
)

// failingIni fails every call, the way inirest does when no INI is loaded.
type failingIni struct{}

func (failingIni) Query(items []ini.IniQueryItem) ([]ini.IniQueryResult, error) {
	return nil, errors.New("INI file not loaded")
}
func (failingIni) GetParameterFile(namespace *string) (string, error) {
	return "", errors.New("INI file not loaded")
}

// TestSliceReturningProviderErrorReachesClient covers the shape that was worst
// affected. For a slice-returning func the C trampoline's error branch returned
// the zero C slice, so a provider failure arrived as HTTP 200 with an empty
// array — indistinguishable from a legitimately empty answer, and worse than
// emccmd's -1 because it looked like ordinary data.
func TestSliceReturningProviderErrorReachesClient(t *testing.T) {
	reg := apiserver.NewRegistry()
	apiserver.RegisterMeta(ini.IniMeta)
	if err := ini.RegisterIniAPI(reg, "ini", failingIni{}); err != nil {
		t.Fatalf("RegisterIniAPI: %v", err)
	}
	srv := apiserver.NewServer(reg, "")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Post(ts.URL+"/api/v1/ini/query", "application/json",
		strings.NewReader(`{"items":[{"section":"EMC","key":"MACHINE"}]}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body := readAll(t, resp)

	if resp.StatusCode == http.StatusOK {
		t.Fatalf("a failing provider returned 200 with body %q — "+
			"indistinguishable from an empty result set", strings.TrimSpace(body))
	}
	if !strings.Contains(body, "not loaded") {
		t.Errorf("status %d body %q does not carry the reason", resp.StatusCode, body)
	}
}
