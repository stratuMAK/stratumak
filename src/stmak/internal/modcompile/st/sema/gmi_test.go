// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package sema

import (
	"strings"
	"testing"

	gmiast "github.com/stratuMAK/stratumak/src/stmak/internal/gmicompile/ast"
	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/st"
)

// tinyAPI is one rt-safe function taking a string and an integer.
func tinyAPI() map[string]*gmiast.API {
	prim := func(n string) gmiast.TypeRef {
		return gmiast.TypeRef{Kind: gmiast.TypePrimitive, Name: n}
	}
	ret := prim("i32")
	return map[string]*gmiast.API{"tiny": {
		Name: "tiny", Prefix: "tiny", Version: 1,
		Funcs: []gmiast.Func{{
			Name: "lookup", RTSafe: true, Return: &ret,
			Params: []gmiast.Param{
				{Name: "name", Type: prim("string")},
				{Name: "want", Type: prim("i32")},
			},
		}},
	}}
}

func checkWithTiny(t *testing.T, src string) error {
	t.Helper()
	f, perr := st.Parse("p.st", src)
	if perr != nil {
		t.Fatalf("does not parse:\n%v\n--- source ---\n%s", perr, src)
	}
	_, err := CheckWith("p", f, tinyAPI())
	return err
}

// TestProviderTakesString covers a provider whose interface hands it a string.
//
// This was refused, on the grounds that the interface passes a pointer into
// the caller's memory while an ST STRING is a value of fixed size, so
// receiving one would mean a bounded copy and a truncation rule.  That stopped
// being true when a STRING parameter became `const char *` and read-only:
// which is exactly what the callback provides, so the thunk hands the pointer
// over and there is nothing to decide.
func TestProviderTakesString(t *testing.T) {
	const src = `{attribute 'summary' := 'x'}
{attribute 'gmi_provide' := 'tiny'}

FUNCTION_BLOCK Impl IMPLEMENTS tiny
  VAR_OUTPUT
    calls : DINT;
  END_VAR
  calls := calls;
METHOD lookup : DINT
  VAR_INPUT
    name : STRING[64];
    want : DINT;
  END_VAR
  calls := calls + 1;
  lookup := want;
END_METHOD
END_FUNCTION_BLOCK

PROGRAM p
  VAR_OUTPUT
    q : DINT;
  END_VAR
  VAR
    i : Impl;
  END_VAR
  i();
  q := i.calls;
END_PROGRAM`

	if err := checkWithTiny(t, src); err != nil {
		t.Fatalf("a provider taking a string should be accepted:\n%v", err)
	}
}

// TestProviderStringShapeStillChecked is the other half: accepting a string
// does not mean accepting anything in its place.
func TestProviderStringShapeStillChecked(t *testing.T) {
	const src = `{attribute 'summary' := 'x'}
{attribute 'gmi_provide' := 'tiny'}

FUNCTION_BLOCK Impl IMPLEMENTS tiny
  VAR_OUTPUT
    calls : DINT;
  END_VAR
  calls := calls;
METHOD lookup : DINT
  VAR_INPUT
    name : DINT;
    want : DINT;
  END_VAR
  lookup := want;
END_METHOD
END_FUNCTION_BLOCK

PROGRAM p
  VAR_OUTPUT
    q : DINT;
  END_VAR
  VAR
    i : Impl;
  END_VAR
  i();
  q := i.calls;
END_PROGRAM`

	err := checkWithTiny(t, src)
	if err == nil || !strings.Contains(err.Error(), "parameter 1 is STRING name") {
		t.Errorf("a DINT where the interface says string gave %v, want a refusal naming it", err)
	}
}
