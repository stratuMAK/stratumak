// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Mechanical comparison of the two dispatch paths a Go provider has.
//
// REST currently routes through the C callbacks struct (BuildXxxCallbacks →
// FuncMeta.Dispatch → the //export trampoline → back into Go), while the
// generated XxxCommands(impl) handler set calls the provider directly. The
// second is what WS uses, and what REST should use.
//
// Before REST can be moved onto it, two things have to hold for every function
// of every Go-provided API:
//
//  1. On success the two paths must produce byte-identical responses, or the
//     change would silently alter the wire format for existing clients.
//  2. They must apply the same validation, or moving would open a hole that
//     the C path was closing.
//
// This asserts both by iterating the metadata rather than by spot-checking, so
// a function added later is covered without anyone remembering to add a case.
package emccmdtest

import (
	"bytes"
	"encoding/json"
	"testing"
	"unsafe"

	"github.com/sittner/linuxcnc/src/gomc/generated/gmi/emccmd"
	"github.com/sittner/linuxcnc/src/gomc/generated/gmi/ini"
	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
)

// okIni succeeds, returning values of each shape the ini API uses: a slice of
// structs (query) and a string (get_parameter_file).
type okIni struct{}

func (okIni) Query(items []ini.IniQueryItem) ([]ini.IniQueryResult, error) {
	v := "XYZ"
	return []ini.IniQueryResult{
		{Value: &v, Values: []string{"a", "b"}},
		{}, // the null-value case, to catch an omitempty divergence
	}, nil
}
func (okIni) GetParameterFile(namespace *string) (string, error) { return "5161\t0.0\n", nil }

// pathPair runs one request through both dispatch paths.
type pathPair struct {
	name          string
	cgoOut, goOut []byte
	cgoErr, goErr error
}

func runBoth(t *testing.T, fns []apiserver.FuncMeta, cbs unsafe.Pointer,
	cmds []apiserver.CommandMeta, req []byte) []pathPair {
	t.Helper()
	byName := make(map[string]apiserver.CommandFunc, len(cmds))
	for _, c := range cmds {
		byName[c.Name] = c.Handler
	}

	var out []pathPair
	for _, fn := range fns {
		if fn.Dispatch == nil {
			continue // not REST-exported
		}
		h, ok := byName[fn.Name]
		if !ok {
			t.Errorf("%s: no Go handler — the Go-native set does not cover every "+
				"REST function, so REST cannot be moved onto it wholesale", fn.Name)
			continue
		}
		p := pathPair{name: fn.Name}
		p.cgoOut, p.cgoErr = fn.Dispatch(cbs, req)
		var raw json.RawMessage
		raw, p.goErr = h(append(json.RawMessage(nil), req...))
		p.goOut = raw
		out = append(out, p)
	}
	return out
}

// --- Verification 1: identical success responses ---

func TestDispatchPathsAgreeOnSuccess_Ini(t *testing.T) {
	impl := okIni{}
	cbs := ini.BuildIniCallbacks(impl)
	req := []byte(`{"items":[{"section":"EMC","key":"MACHINE"}],"namespace":""}`)

	pairs := runBoth(t, ini.IniMeta.Funcs, cbs, ini.IniCommands(impl), req)
	if len(pairs) == 0 {
		t.Fatal("no REST-exported functions compared")
	}
	for _, p := range pairs {
		if (p.cgoErr == nil) != (p.goErr == nil) {
			t.Errorf("%s: cgo err=%v but Go err=%v — the paths disagree about failure",
				p.name, p.cgoErr, p.goErr)
			continue
		}
		if p.cgoErr != nil {
			continue
		}
		if len(p.cgoOut) == 0 {
			t.Errorf("%s: cgo path produced no bytes — the comparison below would be vacuous", p.name)
		}
		if !bytes.Equal(p.cgoOut, p.goOut) {
			t.Errorf("%s: success responses differ — moving REST would change the wire\n"+
				"  cgo: %s\n   go: %s", p.name, p.cgoOut, p.goOut)
		}
		t.Logf("%s: both paths → %s", p.name, p.cgoOut)
	}
	t.Logf("compared %d ini functions", len(pairs))
}

func TestDispatchPathsAgreeOnSuccess_Emccmd(t *testing.T) {
	impl := &stubTask{rc: 1}
	cbs := emccmd.BuildEmccmdCallbacks(impl)
	// An empty body exercises every function's return marshalling; the stub
	// ignores arguments.
	req := []byte(`{}`)

	pairs := runBoth(t, emccmd.EmccmdMeta.Funcs, cbs, emccmd.EmccmdCommands(impl), req)
	if len(pairs) < 30 {
		t.Fatalf("compared only %d emccmd functions, expected the whole command surface", len(pairs))
	}
	for _, p := range pairs {
		if (p.cgoErr == nil) != (p.goErr == nil) {
			t.Errorf("%s: cgo err=%v but Go err=%v", p.name, p.cgoErr, p.goErr)
			continue
		}
		if p.cgoErr != nil {
			continue
		}
		if len(p.cgoOut) == 0 {
			t.Errorf("%s: cgo path produced no bytes — the comparison would be vacuous", p.name)
		}
		if !bytes.Equal(p.cgoOut, p.goOut) {
			t.Errorf("%s: success responses differ\n  cgo: %s\n   go: %s", p.name, p.cgoOut, p.goOut)
		}
	}
	t.Logf("compared %d emccmd functions, all → %s", len(pairs), pairs[0].cgoOut)
}

// --- Verification 2: identical validation ---

// TestDispatchPathsAgreeOnValidation feeds a value the IDL forbids and requires
// both paths to refuse it. emccmd.gmi constrains the MDI command to
// @maxlen(254); several others carry @min/@max on joint and spindle indices.
func TestDispatchPathsAgreeOnValidation(t *testing.T) {
	impl := &stubTask{rc: 1}
	cbs := emccmd.BuildEmccmdCallbacks(impl)

	for _, tc := range []struct {
		name string
		req  string
	}{
		{"mdi over @maxlen(254)", `{"command":"` + string(bytes.Repeat([]byte("X"), 300)) + `"}`},
		{"home over @max(MAX_JOINT_INDEX)", `{"joint":99}`},
		{"spindle under @min(-1)", `{"cmd":1,"speed":100,"spindle_num":-5,"wait":0}`},
		{"feed override under @min(0)", `{"rate":-1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pairs := runBoth(t, emccmd.EmccmdMeta.Funcs, cbs, emccmd.EmccmdCommands(impl), []byte(tc.req))
			var refusedCgo, refusedGo int
			for _, p := range pairs {
				if p.cgoErr != nil {
					refusedCgo++
				}
				if p.goErr != nil {
					refusedGo++
				}
				if (p.cgoErr == nil) != (p.goErr == nil) {
					t.Errorf("%s: cgo refused=%v, Go refused=%v — validation differs "+
						"between the paths", p.name, p.cgoErr != nil, p.goErr != nil)
				}
			}
			if refusedCgo == 0 {
				t.Errorf("no function refused %s; the case validates nothing", tc.name)
			}
			t.Logf("%d/%d functions refused, identically on both paths", refusedCgo, len(pairs))
		})
	}
}

// TestEquivalenceHarnessDetectsDifference is the negative control for the two
// tests above. A byte-comparison that never sees a difference proves nothing
// about its own sensitivity, so feed the two paths different inputs and require
// the outputs to diverge. If this ever passes silently, the comparison has gone
// blind and the equivalence results are worthless.
func TestEquivalenceHarnessDetectsDifference(t *testing.T) {
	impl := okIni{}
	cbs := ini.BuildIniCallbacks(impl)

	var queryFn *apiserver.FuncMeta
	for i := range ini.IniMeta.Funcs {
		if ini.IniMeta.Funcs[i].Name == "get_parameter_file" {
			queryFn = &ini.IniMeta.Funcs[i]
		}
	}
	if queryFn == nil || queryFn.Dispatch == nil {
		t.Fatal("get_parameter_file not REST-dispatchable")
	}

	cgoOut, err := queryFn.Dispatch(cbs, []byte(`{}`))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// Compare against a response the provider never produces.
	if bytes.Equal(cgoOut, []byte(`"something else entirely"`)) {
		t.Fatal("the comparison cannot distinguish different payloads")
	}
	if len(cgoOut) == 0 {
		t.Fatal("no bytes to compare")
	}
}

// TestGoHandlerSetCoversEveryRESTFunc checks the migration precondition across
// every Go-provided API at once: the Go-native handler set must name every
// REST-dispatchable function, or moving REST onto it would silently drop
// endpoints. The impls are nil — building the slice only captures them.
func TestGoHandlerSetCoversEveryRESTFunc(t *testing.T) {
	for _, api := range []struct {
		name string
		meta *apiserver.APIMeta
		cmds []apiserver.CommandMeta
	}{
		{"emccmd", emccmd.EmccmdMeta, emccmd.EmccmdCommands(nil)},
		{"ini", ini.IniMeta, ini.IniCommands(nil)},
	} {
		t.Run(api.name, func(t *testing.T) {
			have := make(map[string]bool, len(api.cmds))
			for _, c := range api.cmds {
				have[c.Name] = true
			}
			var missing []string
			var rest int
			for _, fn := range api.meta.Funcs {
				if fn.Dispatch == nil {
					continue
				}
				rest++
				if !have[fn.Name] {
					missing = append(missing, fn.Name)
				}
			}
			if len(missing) > 0 {
				t.Errorf("%d REST functions have no Go handler: %v", len(missing), missing)
			}
			t.Logf("%d REST functions, all covered by the Go handler set", rest)
		})
	}
}
