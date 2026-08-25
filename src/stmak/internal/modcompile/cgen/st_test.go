// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package cgen

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/st"
	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/st/sema"
)

// stCorpus is the first-party ST fixture directory, relative to this package.
const stCorpus = "../st/testdata/accept"

// stLowers lists the fixtures the frontend lowers today.  It mirrors the ledger
// in st/sema/corpus_test.go; the two are kept in step by
// TestSTLedgersAgree below, so a phase landing cannot update one and forget
// the other.
var stLowers = []string{
	"and2", "scale", "literals", "loops", // phase 2: scalars
	"arrays", "structs", "statemachine", "types", // phase 4: types and arrays
	"ramplimit", "fbnest", "funcs", // phase 5: function blocks and functions
	"twoprograms", // phase 6: shared state and several programs
	"atmap",       // explicit pin mapping with AT
	"constants",   // named constants as bounds, limits and labels
	"documented",  // every documentation field
}

// compile turns one fixture into C.
func generateST(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(stCorpus, name+".st")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	file, perr := st.Parse(path, string(src))
	if perr != nil {
		t.Fatalf("%s does not parse:\n%v", name, perr)
	}
	pkg, cerr := sema.Check(name, file)
	if cerr != nil {
		t.Fatalf("%s does not check:\n%v", name, cerr)
	}
	var buf bytes.Buffer
	if err := GenerateTo(&buf, pkg, name+".c"); err != nil {
		t.Fatalf("%s: generating C: %v", name, err)
	}
	return buf.String()
}

// TestSTGeneratesValidC compiles every lowered fixture with the real compiler
// flags.  Emitting plausible-looking C that does not compile is the failure
// mode a string-comparison test cannot catch, and the one that matters.
func TestSTGeneratesValidC(t *testing.T) {
	cflags, ok := stCFlags(t)
	if !ok {
		t.Skip("modcompile --cflags unavailable (running outside a built tree)")
	}

	for _, name := range stLowers {
		t.Run(name, func(t *testing.T) {
			out := generateST(t, name)

			dir := t.TempDir()
			cPath := filepath.Join(dir, name+".c")
			if err := os.WriteFile(cPath, []byte(out), 0o644); err != nil {
				t.Fatalf("writing: %v", err)
			}

			// The flags modcompile itself passes, not a stricter invented
			// set: --cflags already carries -Wall -Werror and the cmod
			// include paths, so this compiles the file exactly as the real
			// build would.  Adding -Wextra here would fail on the FUNCTION
			// macro's unused `period`, which a .comp body does not use
			// either -- a test stricter than the build tests the test.
			args := append([]string{"-fsyntax-only"}, cflags...)
			args = append(args, cPath)
			cmd := exec.Command("gcc", args...)
			if diag, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("generated C does not compile:\n%s\n--- generated ---\n%s", diag, out)
			}
		})
	}
}

// stCFlags asks the built modcompile for the cmod include paths, which is the
// same question an out-of-tree module's Makefile asks.
func stCFlags(t *testing.T) ([]string, bool) {
	t.Helper()
	bin, err := filepath.Abs("../../../../../bin/modcompile")
	if err != nil {
		return nil, false
	}
	if _, err := os.Stat(bin); err != nil {
		return nil, false
	}
	out, err := exec.Command(bin, "--cflags").Output()
	if err != nil {
		return nil, false
	}
	return strings.Fields(string(out)), true
}

// TestSTPinSnapshotting checks the cycle semantics of §3.2: a pin is read once
// into a local, an input's local is const so a stray assignment cannot compile,
// and outputs are written back at the end.
func TestSTPinSnapshotting(t *testing.T) {
	out := generateST(t, "and2")

	for _, want := range []string{
		"const bool __pin_in0 = (*(__comp_inst->hal->in0));",
		"const bool __pin_in1 = (*(__comp_inst->hal->in1));",
		"bool __pin_out = (*(__comp_inst->hal->out));",
		"(*(__comp_inst->hal->out)) = __pin_out;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated C is missing:\n  %s\n--- generated ---\n%s", want, out)
		}
	}

	// An output's local is seeded from the pin, not left uninitialised: a body
	// that assigns it only on some paths must leave the previous value alone,
	// which is what a PLC programmer expects.
	if strings.Contains(out, "bool __pin_out;\n") {
		t.Error("output local is uninitialised; an unassigned output would emit garbage")
	}
}

// TestSTNoNameMacros guards the collision that broke the first .so: the .comp
// convenience macros are named after pins, and a macro named `in0` expands
// inside `__comp_inst->hal->in0` and produces uncompilable C.
func TestSTNoNameMacros(t *testing.T) {
	out := generateST(t, "and2")
	for _, bad := range []string{"#define in0 ", "#define out ", "#undef in0"} {
		if strings.Contains(out, bad) {
			t.Errorf("ST component emitted a name macro (%q); "+
				"it would expand inside the member access the body uses", bad)
		}
	}
	// The macros ST does need are still there.
	for _, want := range []string{"#define FUNCTION(", "#define fperiod", "#define STMAK_LOG_ERR"} {
		if !strings.Contains(out, want) {
			t.Errorf("generated C is missing %q, which ST bodies rely on", want)
		}
	}
}

// TestSTLoopBoundsAreEmitted checks that the §4 cap reaches the generated code
// rather than staying an idea in the frontend.
func TestSTLoopBoundsAreEmitted(t *testing.T) {
	out := generateST(t, "loops")

	if !strings.Contains(out, "unsigned long __st_faults;") {
		t.Error("inst_t has no fault counter")
	}
	if !strings.Contains(out, "__st_fault(inst_t *__comp_inst, const char *what)") {
		t.Error("fault helper not emitted")
	}
	if !strings.Contains(out, "__ST_FAULT_LOG_EVERY") {
		t.Error("fault logging is not rate-limited")
	}
	for _, want := range []string{
		`if (++__iter1 > 50UL) { __st_fault(__comp_inst, "WHILE loop exceeded its 50 iteration bound"); break; }`,
		`if (++__iter2 > 10UL) { __st_fault(__comp_inst, "REPEAT loop exceeded its 10 iteration bound"); break; }`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing loop bound check:\n  %s", want)
		}
	}
	// REPEAT runs until its condition holds, which is the negation of C's
	// do/while.  Getting this backwards inverts every REPEAT ever written.
	if !strings.Contains(out, "} while (!(") {
		t.Error("REPEAT was not emitted as do/while over the negated condition")
	}
}

// TestSTGuardsOnlyWhatIsUsed: the build runs with -Werror=unused-function, so
// an emitted helper nothing calls fails the compile.  This is the defect the
// literals fixture found.
func TestSTGuardsOnlyWhatIsUsed(t *testing.T) {
	// and2 divides nothing, so no guard and no counter.
	out := generateST(t, "and2")
	for _, bad := range []string{"__st_div_s", "__st_mod_u", "__st_faults"} {
		if strings.Contains(out, bad) {
			t.Errorf("a component with no guarded operation emitted %s", bad)
		}
	}

	// literals uses MOD on a signed integer, and nothing else.
	out = generateST(t, "literals")
	if !strings.Contains(out, "__st_mod_s(") {
		t.Error("signed MOD guard not emitted for a component that uses MOD")
	}
	for _, bad := range []string{"__st_div_u(", "__st_mod_u("} {
		if strings.Contains(out, "static uint64_t "+strings.TrimSuffix(bad, "(")) {
			t.Errorf("unused guard %s emitted; -Werror=unused-function would reject it", bad)
		}
	}
}

// TestSTLineDirectivesPointAtTheSource checks that a diagnostic in a generated
// body names the .st the author edits, and that attribution returns to the
// generated file for the prologue and epilogue nobody wrote.
func TestSTLineDirectivesPointAtTheSource(t *testing.T) {
	out := generateST(t, "and2")
	// The directive carries the path the file was parsed with, not a bare
	// name: gdb and gcov have to be able to open it.
	if !strings.Contains(out, `#line 20 "`) || !strings.Contains(out, `and2.st"`) {
		t.Errorf("no #line naming line 20 of the .st:\n%s", out)
	}
	if !strings.Contains(out, `"and2.c"`) {
		t.Error("attribution never returns to the generated file")
	}

	// Without an output name there is nothing to return to, so no directives
	// at all -- a half-mapped file is worse than an unmapped one.
	path := filepath.Join(stCorpus, "and2.st")
	src, _ := os.ReadFile(path)
	file, _ := st.Parse(path, string(src))
	pkg, _ := sema.Check("and2", file)
	var buf bytes.Buffer
	if err := Generate(&buf, pkg); err != nil {
		t.Fatalf("generating: %v", err)
	}
	if strings.Contains(buf.String(), "#line") {
		t.Error("emitted #line directives with no output name to return to")
	}
}

// TestSTRejectsBothBodies checks that cgen refuses a package violating the
// invariant it relies on, rather than silently emitting one body and dropping
// the other.
func TestSTRejectsBothBodies(t *testing.T) {
	pkg := &ast.Package{Component: ast.Component{
		Name:      "hybrid",
		VerbatimC: "FUNCTION(_) { }",
		Functions: []ast.Function{{Name: "_", Body: []ast.Stmt{}}},
	}}
	var buf bytes.Buffer
	if err := Generate(&buf, pkg); err == nil {
		t.Fatal("a package with both verbatim C and an ST body was generated")
	}
}

// TestSTLedgersAgree keeps this file's list in step with sema's ledger, so a
// landed phase cannot enable a fixture in one place and leave it disabled in
// the other.
func TestSTLedgersAgree(t *testing.T) {
	// Recomputed rather than imported: sema's ledger is a test-only symbol.
	// What is checked here is that every fixture this file claims to lower
	// really does, and that no other fixture lowers silently.
	files, err := filepath.Glob(filepath.Join(stCorpus, "*.st"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures under %s", stCorpus)
	}

	claimed := make(map[string]bool, len(stLowers))
	for _, n := range stLowers {
		claimed[n] = true
	}

	for _, path := range files {
		name := strings.TrimSuffix(filepath.Base(path), ".st")
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading: %v", err)
		}
		file, perr := st.Parse(path, string(src))
		if perr != nil {
			t.Fatalf("%s does not parse: %v", name, perr)
		}
		_, cerr := sema.Check(name, file)

		switch {
		case cerr == nil && !claimed[name]:
			t.Errorf("%s now lowers but is not in stLowers; add it so its C is checked", name)
		case cerr != nil && claimed[name]:
			t.Errorf("%s is in stLowers but no longer lowers: %v", name, cerr)
		}
	}
}

// TestSTTypedefs checks the user-defined types reach the generated C in a
// usable form: constants as macros, structs dependency-ordered so each is
// declared before its first use, and aliases resolved away.
func TestSTTypedefs(t *testing.T) {
	out := generateST(t, "statemachine")
	for _, want := range []string{
		"#define STATE_IDLE 0",
		"#define STATE_RUNNING 1",
		"#define STATE_FAULTED 2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing enumeration constant:\n  %s", want)
		}
	}
	// Resolving a constant to its value is the defect that made the
	// standalone prototype's own enum example uncompilable: it emitted
	// `inst->RED`.
	if strings.Contains(out, "->IDLE") || strings.Contains(out, "->RUNNING") {
		t.Error("an enumeration constant was emitted as a struct member")
	}
	if !strings.Contains(out, "case STATE_IDLE:") {
		t.Errorf("CASE arm not labelled by the resolved constant:\n%s", out)
	}

	out = generateST(t, "structs")
	pointAt := strings.Index(out, "} Point_t;")
	rectAt := strings.Index(out, "} Rect_t;")
	if pointAt < 0 || rectAt < 0 {
		t.Fatalf("struct typedefs missing:\n%s", out)
	}
	if pointAt > rectAt {
		t.Error("Rect_t is declared before the Point_t it contains; C requires the other order")
	}
	if !strings.Contains(out, "Point_t topLeft;") {
		t.Error("nested struct field not emitted")
	}

	// An alias resolves away; nothing is emitted for it.
	out = generateST(t, "types")
	if strings.Contains(out, "Celsius_t") || strings.Contains(out, "Counter_t") {
		t.Error("an alias produced a typedef; it should resolve to its target")
	}
}

// TestSTArrayShapeIsKept checks that a multi-dimensional array stays
// multi-dimensional in the generated C rather than being flattened, and that a
// non-zero lower bound is subtracted at the index site.
func TestSTArrayShapeIsKept(t *testing.T) {
	out := generateST(t, "arrays")

	if !strings.Contains(out, "double grid[2][3];") {
		t.Errorf("multi-dimensional array was not emitted with its shape:\n%s", out)
	}
	if !strings.Contains(out, "double window[4];") {
		t.Error("ARRAY[1..4] should be a C array of four")
	}
	// The declared lower bound is subtracted at the index, not stored.
	if !strings.Contains(out, `, 1, 4, "window"`) {
		t.Error("the declared bounds 1..4 did not reach the index check")
	}
	// A multi-dimensional fault message names which dimension went wrong.
	if !strings.Contains(out, `"grid dimension 2"`) {
		t.Error("a multi-dimensional index fault does not name the dimension")
	}
}

// TestSTIndexClamping is the §4 rule for subscripts: an index the compiler
// could not see is clamped, because an out-of-range access inside the
// real-time process corrupts stmakd rather than the component that caused it.
func TestSTIndexClamping(t *testing.T) {
	out := generateST(t, "arrays")

	if !strings.Contains(out, "static int __st_idx(inst_t *__comp_inst, int64_t i, int lo, int hi, const char *what)") {
		t.Fatalf("index clamp helper not emitted:\n%s", out)
	}
	if !strings.Contains(out, "__st_fault(__comp_inst, what); return 0;") {
		t.Error("an index below the lower bound is not clamped")
	}
	if !strings.Contains(out, "__st_fault(__comp_inst, what); return hi - lo;") {
		t.Error("an index above the upper bound is not clamped")
	}

	// A constant index was already proved in range by sema, so it costs no
	// check and no arithmetic.
	if !strings.Contains(out, "__pin_channel[0]") && !strings.Contains(out, "pts[0]") {
		t.Log("no constant subscript in this fixture to check")
	}
	out = generateST(t, "structs")
	if strings.Contains(out, "__st_idx(__comp_inst, (int64_t)0,") {
		t.Error("a constant index was emitted with a runtime range check")
	}
}

// TestSTArrayPinNaming checks that an array pin gets HAL's indexed names.
// cgen builds them from a run of '#' in the pin name, which an .st declaration
// does not carry -- so sema has to add one, and a mismatch here emits a printf
// with an unused argument that fails the build.
func TestSTArrayPinNaming(t *testing.T) {
	out := generateST(t, "arrays")
	if !strings.Contains(out, `"%s.channel.%01d", name, j`) {
		t.Errorf("array pin does not get an indexed HAL name:\n%s", out)
	}
	// The copy loop is what keeps an array pin on the same footing as a
	// scalar: read once at entry, written back at exit.
	if !strings.Contains(out, "__pin_channel[__pi] = (*(__comp_inst->hal->channel[__pi]));") {
		t.Error("array pin is not snapshotted")
	}
}

// TestSTFunctionBlocks checks that a block's instance holds its I/O as well as
// its state, that the body is a plain function over that instance, and that an
// invocation writes the inputs before running it.
func TestSTFunctionBlocks(t *testing.T) {
	out := generateST(t, "ramplimit")

	// Inputs and outputs persist between calls, so they sit in the instance
	// rather than being parameters.  This is what makes `r.out` a read of
	// retained state.
	for _, want := range []string{
		"typedef struct {",
		"/* VAR_INPUT */",
		"double target;",
		"/* VAR_OUTPUT */",
		"double out;",
		"} Ramp_t;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("instance struct is missing %q:\n%s", want, out)
		}
	}

	// The instance is a plain member of inst_t: no allocation, no indirection.
	if !strings.Contains(out, "Ramp_t r;") {
		t.Error("the instance is not a member of inst_t")
	}

	if !strings.Contains(out, "static void Ramp_body(inst_t *__comp_inst, Ramp_t *__self)") {
		t.Errorf("block body not emitted with the expected signature:\n%s", out)
	}

	// An invocation writes the inputs into the instance, then runs the body.
	for _, want := range []string{
		"(__comp_inst->r).target = __pin_target;",
		"(__comp_inst->r).rate = __pin_max_rate;",
		"(__comp_inst->r).dt = fperiod;",
		"Ramp_body(__comp_inst, &(__comp_inst->r));",
		"__pin_out = (__comp_inst->r).out;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("invocation is missing:\n  %s\n--- generated ---\n%s", want, out)
		}
	}

	// A block body reaches its own members through the instance, never
	// through inst_t.
	if !strings.Contains(out, "(__self->step) = ((__self->rate) * (__self->dt));") {
		t.Error("block body does not reach its members through the instance")
	}
}

// TestSTNestedAndArrayedInstances checks the property that follows from a
// block having no HAL identity: an instance nests, and an array of them is
// unremarkable.
func TestSTNestedAndArrayedInstances(t *testing.T) {
	out := generateST(t, "fbnest")

	// A block contained by another is declared first, since C needs the
	// complete type.
	accAt := strings.Index(out, "} Accumulator_t;")
	avgAt := strings.Index(out, "} Averager_t;")
	if accAt < 0 || avgAt < 0 {
		t.Fatalf("instance structs missing:\n%s", out)
	}
	if accAt > avgAt {
		t.Error("Averager_t is declared before the Accumulator_t it contains")
	}
	if !strings.Contains(out, "Accumulator_t acc;") {
		t.Error("nested instance is not a member of its container")
	}

	// An array of instances.
	if !strings.Contains(out, "Averager_t avg[4];") {
		t.Errorf("array of instances not emitted:\n%s", out)
	}
	// Each element is initialised, not just the first.
	if !strings.Contains(out, "for (__i = 0; __i < 4; __i++) Averager_init(&inst->avg[__i]);") {
		t.Error("array of instances is not initialised element by element")
	}
	// Invoking through a subscript works like any other reference, and the
	// subscript is clamped like any other.
	if !strings.Contains(out, "Averager_body(__comp_inst, &(__comp_inst->avg)[") {
		t.Errorf("invocation through a subscript not emitted:\n%s", out)
	}
}

// TestSTPureFunctions checks that a function is a plain C function returning
// its result, and is callable from an expression.
func TestSTPureFunctions(t *testing.T) {
	out := generateST(t, "funcs")

	if !strings.Contains(out, "static double st_Clamp(inst_t *__comp_inst, double value, double low, double high)") {
		t.Errorf("function not emitted with the expected signature:\n%s", out)
	}
	// The st_ prefix keeps an ST function called Clamp or Round from colliding
	// with the C library, which is linked into every module.
	if strings.Contains(out, "\ndouble Clamp(") || strings.Contains(out, "static double Clamp(") {
		t.Error("function emitted without the st_ prefix; it could collide with libc")
	}

	// ST returns by assigning a name matching the function.
	if !strings.Contains(out, "double Clamp = 0;") {
		t.Error("result variable not declared and zeroed")
	}
	if !strings.Contains(out, "return Clamp;") {
		t.Error("function does not return its result variable")
	}

	// Called from an expression, with the instance threaded through so a
	// guard inside can record a fault.
	if !strings.Contains(out, "st_Clamp(__comp_inst,") {
		t.Errorf("function call not emitted:\n%s", out)
	}
	// A function calling another.
	if !strings.Contains(out, "st_Lerp(__comp_inst,") {
		t.Error("nested function call not emitted")
	}
}

// TestSTMultipleProgramsExportSeparately checks that each PROGRAM in a file
// becomes its own HAL function, with its own pins and its own fp flag.
func TestSTMultipleProgramsExportSeparately(t *testing.T) {
	out := generateST(t, "twoprograms")

	if !strings.Contains(out, `snprintf(fname, sizeof(fname), "%s.read", name);`) ||
		!strings.Contains(out, `snprintf(fname, sizeof(fname), "%s.write", name);`) {
		t.Fatalf("both programs are not exported as HAL functions:\n%s", out)
	}

	// The fp flag is inferred per function, not per component: read touches no
	// floating point and can run in a thread without FPU save and restore,
	// while write cannot.
	if !strings.Contains(out, "funct_read, inst, 0, 0, inst->comp_id") {
		t.Error("read should export with fp = 0; it touches no floating point")
	}
	if !strings.Contains(out, "funct_write, inst, 1, 0, inst->comp_id") {
		t.Error("write should export with fp = 1; it uses LREAL")
	}

	// VAR_GLOBAL is shared state in the one instance struct.
	for _, want := range []string{"int32_t latched;", "bool ready;"} {
		if !strings.Contains(out, want) {
			t.Errorf("shared state missing from inst_t: %s", want)
		}
	}

	// Each function snapshots only its own pins.  Copying another's would
	// cost for nothing; writing another's outputs back would fight whichever
	// thread that one runs in.
	readBody := between(out, "FUNCTION(read) {", "\n}")
	writeBody := between(out, "FUNCTION(write) {", "\n}")
	if readBody == "" || writeBody == "" {
		t.Fatalf("could not find both bodies:\n%s", out)
	}
	if strings.Contains(readBody, "__pin_gain") || strings.Contains(readBody, "__pin_scaled") {
		t.Error("read snapshots pins belonging to write")
	}
	if strings.Contains(writeBody, "__pin_raw") || strings.Contains(writeBody, "__pin_strobe") {
		t.Error("write snapshots pins belonging to read")
	}
	if !strings.Contains(readBody, "(__comp_inst->latched) = __pin_raw;") {
		t.Error("read does not write the shared state")
	}
	if !strings.Contains(writeBody, "(__comp_inst->ready)") {
		t.Error("write does not read the shared state")
	}
}

// between returns the text between the first occurrence of start and the next
// end after it.
func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	rest := s[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// TestSTExplicitPinMapping covers the AT form of §3.1: a plain VAR mapped onto
// a pin, renamed on the way out, and a function block publishing pins of its
// own along the instance path.
func TestSTExplicitPinMapping(t *testing.T) {
	out := generateST(t, "atmap")

	// A program-level AT variable is an ordinary pin, snapshotted like any
	// other -- and renamed where the clause said so.
	for _, want := range []string{
		"const bool __pin_enable = (*(__comp_inst->hal->enable));",
		"bool __pin_active = (*(__comp_inst->hal->active));",
		"double __pin_feed = (*(__comp_inst->hal->feed));",
		"(*(__comp_inst->hal->active)) = __pin_active;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("program-level AT pin missing:\n  %s", want)
		}
	}
	// %I is read-only and %Q write-only, so an input keeps its const and an
	// input is never written back.
	if strings.Contains(out, "(*(__comp_inst->hal->enable)) =") {
		t.Error("an %I pin was written back")
	}

	// A block's published pins are named by the path that reaches the
	// instance, and an array segment becomes the pin's index.
	for _, want := range []string{
		`"%s.x.position", name`,
		`"%s.x.error", name`,
		`"%s.axes.%01d.position", name, j`,
		`"%s.axes.%01d.error", name, j`,
		// Underscores become hyphens on the way to HAL, the same transform a
		// VAR_INPUT pin gets: `at_home` and `at-home` name one pin.
		`"%s.x.at-home", name`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("published pin missing:\n  %s\n--- generated ---\n%s", want, out)
		}
	}

	// They are loaded into the instance at entry and stored back at exit,
	// which gives them the same cycle semantics as the function's own pins.
	for _, want := range []string{
		"__comp_inst->x.home = (*(__comp_inst->hal->x_at_home));",
		"(*(__comp_inst->hal->x_position)) = __comp_inst->x.pos;",
		"for (__pi = 0; __pi < 3; __pi++) __comp_inst->axes[__pi].home = (*(__comp_inst->hal->axes_at_home[__pi]));",
		"for (__pi = 0; __pi < 3; __pi++) (*(__comp_inst->hal->axes_position[__pi])) = __comp_inst->axes[__pi].pos;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("published pin is not moved between HAL and its instance:\n  %s", want)
		}
	}

	// The block body itself is untouched by any of this: it reads a plain
	// member, which is the whole reason the copies happen at the boundary.
	if !strings.Contains(out, "(__self->pos) = 0.0;") {
		t.Errorf("block body does not reach its published member as an ordinary field:\n%s", out)
	}
	if strings.Contains(between(out, "static void Axis_body", "\n}\n"), "hal->") {
		t.Error("block body reaches HAL directly; it should only touch its own instance")
	}
}

// TestSTConstantsFold checks that a named constant reaches the generated C as
// its value.  Real ST names its bounds far more often than it writes them out,
// so a constant that does not fold is a constant that cannot be used where it
// is most wanted -- sizing an array, bounding a loop, labelling a CASE arm.
func TestSTConstantsFold(t *testing.T) {
	out := generateST(t, "constants")

	// A block's array sized by the block's own constant, resolved while the
	// rest of its declarations were still being read.
	if !strings.Contains(out, "double slot[8];") {
		t.Errorf("array not sized by a named constant:\n%s", out)
	}
	// A constant written as an expression is still a value.
	if !strings.Contains(out, "__pin_minutes = 1440;") {
		t.Error("a constant written as (24 * 60) did not fold to its value")
	}
	// And it costs no storage, since every reference folded.
	for _, dead := range []string{"int32_t SLOTS;", "int32_t MINUTES_PER_DAY;", "int32_t WINDOW;"} {
		if strings.Contains(out, dead) {
			t.Errorf("a folded constant still occupies storage: %s", dead)
		}
	}
}

// TestSTQualifiedEnums checks the qualified form two enumerations need when
// they share a constant name -- which is ordinary in real code.
func TestSTQualifiedEnums(t *testing.T) {
	out := generateST(t, "constants")

	// Both enumerations declare OFF, and each keeps its own constant.
	for _, want := range []string{"#define MODE_OFF 0", "#define STATE_OFF 0"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "(__comp_inst->mode) = MODE_OFF;") {
		t.Error("a qualified constant did not resolve to its own enumeration")
	}
	if !strings.Contains(out, "(__comp_inst->step) = STATE_OFF;") {
		t.Error("the other enumeration's OFF did not resolve")
	}
}
