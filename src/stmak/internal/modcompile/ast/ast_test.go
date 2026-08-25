// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package ast

import (
	"strings"
	"testing"
)

// pkgWithC is the shape a .comp parses into: verbatim C, no ST declarations.
func pkgWithC() *Package {
	return &Package{
		Component: Component{
			Name:      "and2",
			Functions: []Function{{Name: "_", FP: false}},
			VerbatimC: "FUNCTION(_) { out = in0 && in1; }",
		},
	}
}

// pkgWithST is the shape an .st lowers into: statement bodies, no verbatim C.
func pkgWithST() *Package {
	body := []Stmt{
		&AssignStmt{
			Target: &VarRef{Name: "out", Store: StoragePin, Typ: Type{Kind: TypeBool}},
			Value:  &BoolLit{Value: true},
		},
	}
	return &Package{
		Component: Component{
			Name:      "ramplimit",
			Functions: []Function{{Name: "_", FP: true, Body: body}},
		},
		Blocks: []FunctionBlock{{Name: "Ramp"}},
	}
}

func TestValidateAcceptsEitherFrontend(t *testing.T) {
	if err := pkgWithC().Validate(); err != nil {
		t.Errorf(".comp-shaped package rejected: %v", err)
	}
	if err := pkgWithST().Validate(); err != nil {
		t.Errorf(".st-shaped package rejected: %v", err)
	}
	// An empty component is not this invariant's business.
	if err := (&Package{Component: Component{Name: "empty"}}).Validate(); err != nil {
		t.Errorf("declaration-only package rejected: %v", err)
	}
}

// TestValidateRejectsBothBodies guards the one invariant cgen actually relies
// on.  It picks its body emission path from verbatim-C-versus-statements, so a
// package carrying both would silently emit one and drop the other -- a
// component that compiles and does half of what its source says.
func TestValidateRejectsBothBodies(t *testing.T) {
	p := pkgWithST()
	p.Component.VerbatimC = "FUNCTION(_) { out = 1; }"

	err := p.Validate()
	if err == nil {
		t.Fatal("a package with both verbatim C and an ST body was accepted")
	}
	if !strings.Contains(err.Error(), "ramplimit") {
		t.Errorf("diagnostic does not name the component: %v", err)
	}
}

func TestValidateRejectsPartialLowering(t *testing.T) {
	// Half-lowered is a frontend bug, and it fails in the backend as a missing
	// function body rather than as anything a reader could diagnose.  Catch it
	// at the boundary instead.
	p := pkgWithST()
	p.Component.Functions = append(p.Component.Functions, Function{Name: "read", FP: true})

	err := p.Validate()
	if err == nil {
		t.Fatal("a package with one lowered and one unlowered function was accepted")
	}
	if !strings.Contains(err.Error(), "read") {
		t.Errorf("diagnostic does not name the offending function: %v", err)
	}
}

func TestValidateRejectsOrphanedSTDeclarations(t *testing.T) {
	// Types, blocks and functions with nothing to run means the frontend
	// dropped the program body on the floor.
	p := &Package{
		Component: Component{Name: "orphan"},
		Types:     []TypeDef{{Name: "Light", Kind: DefEnum}},
	}
	if err := p.Validate(); err == nil {
		t.Fatal("ST declarations with no body were accepted")
	}
}

func TestHasSTBody(t *testing.T) {
	if pkgWithC().HasSTBody() {
		t.Error(".comp-shaped package reports an ST body")
	}
	if !pkgWithST().HasSTBody() {
		t.Error(".st-shaped package reports no ST body")
	}
	// A declared function with an empty-but-present body still counts: an
	// empty PROGRAM is a legal, if pointless, component.
	p := &Package{Component: Component{
		Name:      "nop",
		Functions: []Function{{Name: "_", Body: []Stmt{}}},
	}}
	if !p.HasSTBody() {
		t.Error("an empty (non-nil) body should still count as lowered")
	}
}

func TestPackageLookupsAreCaseInsensitive(t *testing.T) {
	p := &Package{
		Types: []TypeDef{
			{Name: "Light", Kind: DefEnum, Consts: []EnumConst{
				{Name: "RED", Value: 0}, {Name: "GREEN", Value: 2},
			}},
			{Name: "Point", Kind: DefStruct, Fields: []Field{
				{Name: "x", Typ: Type{Kind: TypeLReal}},
			}},
		},
		Blocks:    []FunctionBlock{{Name: "Ramp"}},
		Functions: []PureFunction{{Name: "AverageVal"}},
	}

	if td := p.LookupType("LIGHT"); td == nil || td.Name != "Light" {
		t.Error("LookupType is not case-insensitive")
	}
	if b := p.LookupBlock("ramp"); b == nil || b.Name != "Ramp" {
		t.Error("LookupBlock is not case-insensitive")
	}
	if f := p.LookupFunction("averageval"); f == nil || f.Name != "AverageVal" {
		t.Error("LookupFunction is not case-insensitive")
	}
	if p.LookupType("Missing") != nil {
		t.Error("LookupType invented a type")
	}

	// The lookup returns a pointer into the package so a resolved Type.Ref
	// stays in step with later edits rather than being a stale copy.
	td := p.LookupType("Light")
	td.Doc = "traffic light"
	if p.Types[0].Doc != "traffic light" {
		t.Error("LookupType returned a copy, not a reference into the package")
	}
}

func TestTypeDefMemberLookup(t *testing.T) {
	enum := &TypeDef{Name: "Light", Kind: DefEnum, Consts: []EnumConst{
		{Name: "RED", Value: 0}, {Name: "YELLOW", Value: 1}, {Name: "GREEN", Value: 2},
	}}
	// Resolving an enumeration constant to its value is what lets the backend
	// emit the generated macro instead of an undefined identifier -- the
	// defect that made the standalone prototype's own enum example
	// uncompilable.
	c, ok := enum.LookupConst("green")
	if !ok || c.Value != 2 {
		t.Errorf("LookupConst(green) = %+v, %v; want value 2", c, ok)
	}
	if _, ok := enum.LookupConst("BLUE"); ok {
		t.Error("LookupConst invented a constant")
	}

	st := &TypeDef{Name: "Rect", Kind: DefStruct, Fields: []Field{
		{Name: "topLeft", Typ: Type{Kind: TypeStruct, Ref: "Point"}},
		{Name: "width", Typ: Type{Kind: TypeLReal}},
	}}
	f, ok := st.LookupField("WIDTH")
	if !ok || f.Typ.Kind != TypeLReal {
		t.Errorf("LookupField(WIDTH) = %+v, %v", f, ok)
	}
}

func TestFunctionBlockLookupSpansEverySection(t *testing.T) {
	b := &FunctionBlock{
		Name:    "Ramp",
		Inputs:  []VarDecl{{Name: "target", Typ: Type{Kind: TypeLReal}, Store: StorageField}},
		Outputs: []VarDecl{{Name: "out", Typ: Type{Kind: TypeLReal}, Store: StorageField}},
		InOuts:  []VarDecl{{Name: "accum", Typ: Type{Kind: TypeLReal}, Store: StorageField}},
		Vars:    []VarDecl{{Name: "step", Typ: Type{Kind: TypeLReal}, Store: StorageField}},
	}
	for _, name := range []string{"TARGET", "Out", "accum", "STEP"} {
		if _, ok := b.LookupVar(name); !ok {
			t.Errorf("LookupVar(%q) missed a declared name", name)
		}
	}
	if _, ok := b.LookupVar("nothere"); ok {
		t.Error("LookupVar invented a name")
	}
}

func TestTypeDefKindString(t *testing.T) {
	for k, want := range map[TypeDefKind]string{
		DefAlias:  "alias",
		DefEnum:   "enumeration",
		DefStruct: "struct",
	} {
		if got := k.String(); got != want {
			t.Errorf("TypeDefKind(%d).String() = %q, want %q", int(k), got, want)
		}
	}
}
