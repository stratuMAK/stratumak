// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package classicladder

import (
	"strings"
	"testing"
)

// The PLC arrays are allocated to exactly the configured counts (2.9 sized its
// shared memory the same way), so the variable-region packing has to be right
// for every size combination, not just the defaults every other test uses.

// TestAllocRefusesBadSizes: the allocator is the last line of defense behind
// the argument parser; nonsense counts must yield nil, not a short array.
func TestAllocRefusesBadSizes(t *testing.T) {
	cases := []struct {
		name  string
		sizes map[string]int
	}{
		{"zero rungs", map[string]int{"numRungs": 0}},
		{"zero sections", map[string]int{"numSections": 0}},
		{"negative bits", map[string]int{"numBits": -5}},
		{"bits beyond the sanity cap", map[string]int{"numBits": clSizeLimit + 1}},
		{"negative phys inputs", map[string]int{"numPhysInputs": -1}},
	}
	for _, tc := range cases {
		if rt := allocSizedRT(tc.sizes); rt != nil {
			rt.free()
			t.Errorf("%s: allocator accepted %v", tc.name, tc.sizes)
		}
	}
}

// Odd, pairwise-distinct sizes so a region packed against the wrong neighbor
// count cannot land on the right offset by coincidence.
var oddSizes = map[string]int{
	"numBits":        7,
	"numPhysInputs":  3,
	"numPhysOutputs": 5,
	"numWords":       9,
	"numS32in":       2,
	"numS32out":      4,
	"numFloatIn":     1,
	"numFloatOut":    6,
}

// bitRegions/wordRegions describe the packed layout under oddSizes, in order.
// numErrorBits has no load argument; the helper leaves it at 10.
func bitRegions() []struct {
	varType int
	size    int
} {
	return []struct {
		varType int
		size    int
	}{
		{varMemBit, 7},
		{varPhysInput, 3},
		{varPhysOutput, 5},
		{varStepActivity, maxSteps},
		{varErrorBit, 10},
	}
}

func wordRegions() []struct {
	varType int
	size    int
} {
	return []struct {
		varType int
		size    int
	}{
		{varMemWord, 9},
		{varPhysWordInput, 2},
		{varPhysWordOutput, 4},
		{varStepTime, maxSteps},
	}
}

// TestVarRegionsAtOddSizes sets one variable at a time — the first and last
// offset of every region — and then reads back every offset of every region:
// exactly the one written cell may be set. Any packing overlap or off-by-one
// in the region bases shows up as a phantom neighbor.
func TestVarRegionsAtOddSizes(t *testing.T) {
	l := allocSizedRT(oddSizes)
	if l == nil {
		t.Fatal("allocator refused the odd sizes")
	}
	defer l.free()

	regions := bitRegions()
	for _, target := range regions {
		for _, offset := range []int{0, target.size - 1} {
			l.writeVar(target.varType, offset, 1)
			for _, r := range regions {
				for o := 0; o < r.size; o++ {
					want := 0
					if r.varType == target.varType && o == offset {
						want = 1
					}
					if got := l.readVar(r.varType, o); got != want {
						t.Fatalf("after setting type %d offset %d: type %d offset %d = %d, want %d",
							target.varType, offset, r.varType, o, got, want)
					}
				}
			}
			l.writeVar(target.varType, offset, 0)
		}
	}

	words := wordRegions()
	for i, target := range words {
		for _, offset := range []int{0, target.size - 1} {
			l.writeVar(target.varType, offset, 1000+i)
			for _, r := range words {
				for o := 0; o < r.size; o++ {
					want := 0
					if r.varType == target.varType && o == offset {
						want = 1000 + i
					}
					if got := l.readVar(r.varType, o); got != want {
						t.Fatalf("after setting type %d offset %d: type %d offset %d = %d, want %d",
							target.varType, offset, r.varType, o, got, want)
					}
				}
			}
			l.writeVar(target.varType, offset, 0)
		}
	}

	// Out-of-range offsets are refused outright: the write is dropped and the
	// read yields 0 — in particular the write must not land in the next region.
	l.writeVar(varMemBit, 7, 1)
	if l.readVar(varPhysInput, 0) != 0 || l.readVar(varMemBit, 7) != 0 {
		t.Error("out-of-range %B7 write was not dropped")
	}
	l.writeVar(varMemWord, 9, 42)
	if l.readVar(varPhysWordInput, 0) != 0 || l.readVar(varMemWord, 9) != 0 {
		t.Error("out-of-range %W9 write was not dropped")
	}
}

// TestScanAtOddSizes runs a real scan against the odd layout, wiring the LAST
// physical input to the LAST physical output — the offsets a packing bug
// corrupts first.
func TestScanAtOddSizes(t *testing.T) {
	l := allocSizedRT(oddSizes)
	if l == nil {
		t.Fatal("allocator refused the odd sizes")
	}
	defer l.free()

	rtRungs(l.rt)[0].used = 1
	rtRungs(l.rt)[0].next_rung = 0
	l.setMainSection(0, 0, 0)
	l.put(0, 0, 0, eleInput, varPhysInput, 2)
	for x := 1; x < 9; x++ {
		l.put(0, x, 0, eleConnection, 0, 0)
	}
	l.put(0, 9, 0, eleOutput, varPhysOutput, 4)

	l.input(2, true)
	l.scan(1)
	if !l.output(4) {
		t.Error("last input did not drive last output")
	}
	l.input(2, false)
	l.scan(1)
	if l.output(4) {
		t.Error("output stuck after input dropped")
	}
}

func TestParseModuleArgs(t *testing.T) {
	t.Run("defaults and overrides", func(t *testing.T) {
		sizes, file, port, err := parseArgsForTest([]string{"numRungs=42", "numBits=7", "modbus_port=1502", "prog.clp"})
		if err != nil {
			t.Fatal(err)
		}
		if sizes["numRungs"] != 42 || sizes["numBits"] != 7 {
			t.Errorf("overrides not applied: %v", sizes)
		}
		if sizes["numWords"] != 100 {
			t.Errorf("default numWords = %d, want 100", sizes["numWords"])
		}
		if file != "prog.clp" || port != 1502 {
			t.Errorf("file=%q port=%d", file, port)
		}
	})

	refusals := []struct {
		name string
		args []string
		want string
	}{
		{"unknown key", []string{"numbits=7"}, "unknown argument"},
		{"garbage value", []string{"numBits=lots"}, "not a number"},
		{"negative", []string{"numBits=-1"}, "out of range"},
		{"beyond cap", []string{"numBits=100001"}, "out of range"},
		{"zero rungs", []string{"numRungs=0"}, "at least 1"},
		{"zero sections", []string{"numSections=0"}, "at least 1"},
		{"modbus port zero", []string{"modbus_port=0"}, "out of range"},
		{"modbus port huge", []string{"modbus_port=65536"}, "out of range"},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := parseArgsForTest(tc.args)
			if err == nil {
				t.Fatalf("accepted %v", tc.args)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}
