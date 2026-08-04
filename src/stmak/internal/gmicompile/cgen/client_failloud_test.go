// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package cgen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/gmicompile/parser"
)

// TestClientCFailsLoudOnUnsupported verifies the --client-c generator errors
// (rather than silently dropping fields) on constructs it cannot faithfully
// emit: a doubly-nested struct on the receive path, and a slice-of-struct on
// the send path. See client.go failf.
func TestClientCFailsLoudOnUnsupported(t *testing.T) {
	// Depth-2 nested struct in a REST response — the parser only inlines one
	// level of primitive-scalar nested struct, so this must fail loud.
	deepNest := `@api deeptest
@version 1
@rest_export true

type Inner { a: i32 }
type Mid { inner: Inner }
type Outer { mid: Mid }

@method "GET"
@path "/outer"
func get_outer() -> Outer
`
	if err := genClientSource(t, deepNest); err == nil {
		t.Error("depth-2 nested struct: expected a fail-loud error, got nil")
	} else if !strings.Contains(err.Error(), "--client-c") {
		t.Errorf("error should be attributed to --client-c, got: %v", err)
	}

	// Slice-of-struct request body — emitSliceToJson only handles []string.
	sliceStructBody := `@api sendtest
@version 1
@rest_export true

type Item { name: string }

@method "POST"
@path "/items"
func add_items(items: []Item) -> i32
`
	if err := genClientSource(t, sliceStructBody); err == nil {
		t.Error("slice-of-struct body: expected a fail-loud error, got nil")
	}

	// Nested struct field of a slice element (depth-2 via a list) must fail too.
	nestedInSlice := `@api slicetest
@version 1
@rest_export true

type Sub { a: i32 }
type Row { sub: Sub }

@method "GET"
@path "/rows"
func get_rows() -> []Row
`
	if err := genClientSource(t, nestedInSlice); err == nil {
		t.Error("nested struct in slice element: expected a fail-loud error, got nil")
	}
}

// TestClientCSupportedShapesSucceed pins that the shapes the generator *does*
// support (primitive scalars, []string, one level of primitive-only nested
// struct, slice-of-primitive) still generate without a fail-loud error — so the
// guards are additive, not a regression.
func TestClientCSupportedShapesSucceed(t *testing.T) {
	ok := `@api oktest
@version 1
@rest_export true

type Addr {
    city: string
    zip: i32
}
type Person {
    name: string
    age: i32
    tags: []string
    addr: Addr
}

@method "GET"
@path "/person"
func get_person() -> Person

@method "GET"
@path "/names"
func get_names() -> []string

@method "POST"
@path "/person"
func set_person(name: string, age: i32) -> i32
`
	if err := genClientSource(t, ok); err != nil {
		t.Errorf("supported shapes should generate cleanly, got: %v", err)
	}
}

// TestClientCRealIDLCharacterization records the true --client-c coverage over
// the real @rest_export IDLs. It is a characterization test, NOT a "must all
// pass" gate: the fail-loud rollout revealed that the generator cannot faithfully
// emit a client for the majority of real REST APIs (deep nesting, slice-of-struct,
// narrow scalars like u8, enum fields) — so before this change those APIs would
// have produced a C client that silently dropped fields on the wire. The
// invariant we CAN pin: every real IDL either generates cleanly or fails with a
// "--client-c:"-attributed error — never a partial/unflagged result or a panic.
// If a full recursive rewrite (G-L7/A) ever lands, the clean set grows and this
// log documents the before/after. Skips gracefully if the IDL dir is unreachable.
func TestClientCRealIDLCharacterization(t *testing.T) {
	idlDir := filepath.Join("..", "..", "..", "..", "gmi", "idl")
	entries, err := os.ReadDir(idlDir)
	if err != nil {
		t.Skipf("IDL dir not reachable (%v); skipping real-IDL sweep", err)
	}
	var clean, failLoud int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".gmi") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(idlDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		api, perrs := parser.Parse(e.Name(), string(src))
		if len(perrs) > 0 || !api.RestExport {
			continue // parse issues covered elsewhere; --client-c needs @rest_export
		}
		var buf bytes.Buffer
		err = GenerateClientSource(&buf, api)
		if err == nil {
			clean++
			continue
		}
		failLoud++
		// The one hard invariant: an unsupported construct must fail loud with a
		// --client-c-attributed error, never silently.
		if !strings.Contains(err.Error(), "--client-c") {
			t.Errorf("%s: unsupported construct produced a non-attributed error: %v", e.Name(), err)
		}
		t.Logf("fail-loud: %s — %v", e.Name(), err)
	}
	t.Logf("--client-c coverage over real @rest_export IDLs: %d generate cleanly, %d fail loud (unsupported shapes)", clean, failLoud)
	if clean+failLoud == 0 {
		t.Error("no @rest_export IDLs were exercised; sweep is not actually running")
	}
}

func genClientSource(t *testing.T, src string) error {
	t.Helper()
	api, perrs := parser.Parse("test.gmi", src)
	if len(perrs) > 0 {
		t.Fatalf("unexpected parse errors: %v", perrs)
	}
	var buf bytes.Buffer
	return GenerateClientSource(&buf, api)
}
