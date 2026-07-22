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

// TestSliceReturningProviderErrorBecomesEmptyResult shows the RCS-error gap is
// not specific to emccmd's i32 returns. For a slice-returning func the
// trampoline's error branch returns the zero C slice, so a provider failure
// arrives as HTTP 200 with an empty array — indistinguishable from a
// legitimately empty answer, and strictly worse than emccmd's -1 because it
// looks like ordinary data.
func TestSliceReturningProviderErrorBecomesEmptyResult(t *testing.T) {
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

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d — a provider error now reaches the client; "+
			"the gap may be fixed, update this test", resp.StatusCode)
	}
	if strings.Contains(body, "not loaded") {
		t.Fatal("the reason survived; the gap may be fixed, update this test")
	}
	t.Logf("a failing provider returned HTTP %d with body %q", resp.StatusCode, strings.TrimSpace(body))
}
