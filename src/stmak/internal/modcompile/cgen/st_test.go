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
	"actions",     // named pieces of a body
	"params",      // HAL parameters, top level and inside a block
	"period",      // fperiod in each kind of body
	"durations",   // the TIME type
	"methods",     // methods on an instance
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

// generateSTSource is generateST for a source string rather than a fixture,
// for a case too small to be worth a file of its own.
func generateSTSource(t *testing.T, src string) string {
	t.Helper()
	file, perr := st.Parse("p.st", src)
	if perr != nil {
		t.Fatalf("does not parse:\n%v\n--- source ---\n%s", perr, src)
	}
	pkg, cerr := sema.Check("p", file)
	if cerr != nil {
		t.Fatalf("does not check:\n%v\n--- source ---\n%s", cerr, src)
	}
	var buf bytes.Buffer
	if err := GenerateTo(&buf, pkg, "p.c"); err != nil {
		t.Fatalf("generating C: %v", err)
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

	if !strings.Contains(out, "static void __fb_body_Ramp(inst_t *__comp_inst, Ramp_t *__self)") {
		t.Errorf("block body not emitted with the expected signature:\n%s", out)
	}

	// An invocation writes the inputs into the instance, then runs the body.
	for _, want := range []string{
		"(__comp_inst->r).target = __pin_target;",
		"(__comp_inst->r).rate = __pin_max_rate;",
		"(__comp_inst->r).dt = fperiod;",
		"__fb_body_Ramp(__comp_inst, &(__comp_inst->r));",
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
	if !strings.Contains(out, "for (__i = 0; __i < 4; __i++) __fb_init_Averager(&inst->avg[__i]);") {
		t.Error("array of instances is not initialised element by element")
	}
	// Invoking through a subscript works like any other reference, and the
	// subscript is clamped like any other.
	if !strings.Contains(out, "__fb_body_Averager(__comp_inst, &(__comp_inst->avg)[") {
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

// TestSTActions checks the two shapes an action takes.
//
// An action shares its POU's variables, so it emits as a C function over
// whatever holds them.  For a function block that is the instance and the
// signature is the block body's exactly.  For a PROGRAM the pins are the
// difficulty: they are snapshotted into locals for the cycle, and a local is
// not reachable from another function.
func TestSTActions(t *testing.T) {
	out := generateST(t, "actions")

	// A function block's action takes the instance, like its body.
	for _, want := range []string{
		"static void Counter_clear(inst_t *__comp_inst, Counter_t *__self)",
		"static void Counter_accumulate(inst_t *__comp_inst, Counter_t *__self)",
		"Counter_clear(__comp_inst, __self);",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("function block action missing:\n  %s", want)
		}
	}

	// A program whose actions touch pins holds the snapshot in a struct, so
	// the action sees the same values the body does rather than re-reading
	// shared memory.
	for _, want := range []string{
		"} actions_pins_t;",
		"actions_pins_t __pv;",
		"actions_pins_t *__p = &__pv;",
		"__p->enable = (*(__comp_inst->hal->enable));",
		"static void actions_advance(inst_t *__comp_inst, actions_pins_t *__p)",
		"actions_advance(__comp_inst, __p);",
		"(*(__comp_inst->hal->count)) = __p->count;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("program action pin sharing missing:\n  %s\n--- generated ---\n%s", want, out)
		}
	}

	// The preset form is two statements, and sema lowered it to exactly that.
	if !strings.Contains(out, "(__comp_inst->selected) = (__p->amount);") {
		t.Error("`advance(selected := amount)` did not lower to an assignment before the call")
	}

	// A pin is still read once and written once, whichever shape is used: the
	// struct replaces the locals, it does not add a second read.
	if strings.Count(out, "(*(__comp_inst->hal->enable))") != 1 {
		t.Error("the enable pin is read more than once per cycle")
	}
	if strings.Count(out, "(*(__comp_inst->hal->count)) =") != 1 {
		t.Error("the count pin is written more than once per cycle")
	}
}

// TestSTActionsCostNothingWhenUnused checks that a component without actions is
// emitted exactly as before -- no struct, no indirection.
func TestSTActionsCostNothingWhenUnused(t *testing.T) {
	out := generateST(t, "and2")
	for _, bad := range []string{"_pins_t", "__pv", "__p->"} {
		if strings.Contains(out, bad) {
			t.Errorf("a component with no actions emitted %q", bad)
		}
	}
	if !strings.Contains(out, "const bool __pin_in0 =") {
		t.Error("a component with no actions should still snapshot into plain locals")
	}
}

// TestSTParams checks how a HAL parameter reaches the generated C.
//
// A parameter is the same shared memory as a pin with no signal able to
// connect to it, which decides everything below: inst_hal_t holds the storage
// rather than a pointer to it, so every access is one dereference shallower
// than a pin's, and the body reads it in place rather than from a snapshot.
func TestSTParams(t *testing.T) {
	out := generateST(t, "params")

	// Storage, not a pointer -- the difference a pin's `*` makes.
	for _, want := range []string{
		"    stmak_hal_float_t gain;",
		"    stmak_hal_bit_t active;",
		"    stmak_hal_float_t lim_ceiling;",
		"    stmak_hal_s32_t lim_clamped;",
		"    stmak_hal_float_t *cmd;", // a pin, for contrast
	} {
		if !strings.Contains(out, want) {
			t.Errorf("inst_hal_t is missing:\n  %s\n--- generated ---\n%s", want, out)
		}
	}

	// Registered with HAL's own directions: rw is HAL's to set, ro the
	// component's.
	for _, want := range []string{
		"stmak_hal_param_float_newf(env->hal, STMAK_HAL_RW,",
		"stmak_hal_param_bit_newf(env->hal, STMAK_HAL_RO,",
		"\"%s.gain\", name);",
		"\"%s.active\", name);",
		"\"%s.lim.ceiling\", name);",
		"\"%s.lim.clamped\", name);",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("parameter registration is missing:\n  %s", want)
		}
	}

	// A default is applied before registration, whether the declaration sits
	// at the top level or inside a block.  The block case is the one that has
	// to be carried out to HAL: the member is loaded from shared memory at
	// entry, so a value set only on the instance would be overwritten before
	// the body ever saw it.
	for _, want := range []string{
		"    inst->hal->gain = 1.0;",
		"    inst->hal->lim_ceiling = 100.0;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("parameter default is missing:\n  %s", want)
		}
	}

	// The body reads a top-level parameter in place.  It is not snapshotted:
	// a pin is driven by a thread and could change under the body, while a
	// parameter is changed by a person typing setp.
	for _, want := range []string{
		"(__pin_cmd * (__comp_inst->hal->gain))",
		"if ((__comp_inst->hal->invert)) {",
		"(__comp_inst->hal->active) = ((__comp_inst->hal->gain) < 1.0);",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("body access to a parameter is missing:\n  %s\n--- generated ---\n%s", want, out)
		}
	}
	if strings.Contains(out, "__pin_gain") {
		t.Error("a parameter was snapshotted into a local; only pins are")
	}

	// A parameter inside a function block is loaded into the instance at entry
	// and stored back at exit, as a published pin is -- but without the
	// dereference, because the parameter is the storage.
	for _, want := range []string{
		"    __comp_inst->lim.ceiling = (__comp_inst->hal->lim_ceiling);",
		"    (__comp_inst->hal->lim_clamped) = __comp_inst->lim.hits;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("function block parameter binding is missing:\n  %s\n--- generated ---\n%s", want, out)
		}
	}
	// An rw parameter is HAL's to write, so it is never stored back; an ro one
	// is the component's, so it is never loaded.
	if strings.Contains(out, "(__comp_inst->hal->lim_ceiling) = __comp_inst->lim.ceiling;") {
		t.Error("an rw parameter was stored back, which would clobber a setp")
	}
	if strings.Contains(out, "__comp_inst->lim.hits = (__comp_inst->hal->lim_clamped);") {
		t.Error("an ro parameter was loaded from HAL, which the component alone writes")
	}
}

// TestSTPeriod checks the two routes to fperiod.
//
// It is one value -- the period of the thread this body is running in -- and
// the backend picks how to reach it: through the HAL function's own argument
// where that is in scope, and through the RT task where it is not, which is a
// block body or an action of either kind.  The two agree by construction,
// because a HAL function is invoked by its own thread.
func TestSTPeriod(t *testing.T) {
	out := generateST(t, "period")

	// The exported body has the argument, so it uses the macro cgen already
	// emits for .comp -- which is what keeps every shipped component
	// generating exactly the C it did before.
	if !strings.Contains(out, "((__p->elapsed) + fperiod)") {
		t.Errorf("the exported body should reach fperiod through the period argument\n--- generated ---\n%s", out)
	}

	// A block body and an action are each their own C function and were never
	// handed it.
	for _, want := range []string{
		"static double __st_fperiod(inst_t *__comp_inst) STMAK_NONBLOCKING;",
		"int __task = __rt->task_self(__rt->ctx);",
		"return (double)__rt->task_period(__rt->ctx, __task) * 1e-9;",
		"((__self->rate) * __st_fperiod(__comp_inst))",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("runtime fperiod is missing:\n  %s\n--- generated ---\n%s", want, out)
		}
	}

	// Outside an RT task there is no cycle, and zero is the truth rather than
	// an error nobody can act on.
	if !strings.Contains(out, "if (__task < 0) return 0.0;") {
		t.Error("the helper does not handle being called outside an RT task")
	}
}

// TestSTPeriodHelperCostsNothingWhenUnused is the -Werror trap.
//
// The helper is a static function, and cmods build with -Werror, so emitting
// one nothing calls fails the build outright.  A component whose only use of
// fperiod is in its exported body must not get it.
func TestSTPeriodHelperCostsNothingWhenUnused(t *testing.T) {
	out := generateST(t, "ramplimit")

	if strings.Contains(out, "__st_fperiod") {
		t.Errorf("ramplimit uses fperiod only in its PROGRAM, so the helper is dead code "+
			"and -Werror=unused-function would reject it\n--- generated ---\n%s", out)
	}
	// ...but it does use fperiod, so this is a real negative and not a fixture
	// that happens to mention it nowhere.
	if !strings.Contains(out, "fperiod") {
		t.Error("ramplimit no longer uses fperiod; pick another fixture for this test")
	}
}

// TestSTDurations checks how a TIME reaches the generated C.
//
// It is int64 nanoseconds -- what get_time() and a task's period already are
// -- and every conversion carries the scale that reconciles that with the unit
// the number is in.
func TestSTDurations(t *testing.T) {
	out := generateST(t, "durations")

	// Storage: int64, in inst_t and inside a STRUCT alike.
	for _, want := range []string{
		"    int64_t pt;",
		"    int64_t errDelay;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("a duration should be held as int64: missing\n  %s", want)
		}
	}

	// The millisecond bridge, which is what a ported CODESYS source depends
	// on: TIME_TO_DINT(T#1s) must be 1000, not 1000000000.
	if !strings.Contains(out, "/ (int64_t)1000000)") {
		t.Errorf("TIME_TO_DINT should divide by a million nanoseconds to give milliseconds"+
			"\n--- generated ---\n%s", out)
	}
	// The seconds bridge, which is stratuMAK's own unit.
	if !strings.Contains(out, "/ 1000000000)") {
		t.Error("TIME_TO_SECONDS should divide by a billion nanoseconds")
	}
	if !strings.Contains(out, "* 1000000000))") {
		t.Error("SECONDS_TO_TIME should multiply up to nanoseconds")
	}

	// Scaling by a whole count stays in int64 and stays exact; scaling by a
	// fraction goes through a double, which is the only way `* 1.5` means
	// what it looks like.
	// Exact, in int64, and guarded like any other integer division -- a
	// constant divisor takes the guard on the numeric path too.
	if !strings.Contains(out, "__st_div_s(__comp_inst, (int64_t)(__comp_inst->pt), (int64_t)((int64_t)2))") {
		t.Errorf("dividing a duration by a count should stay in int64\n--- generated ---\n%s", out)
	}
	if !strings.Contains(out, "((int64_t)(((double)(__comp_inst->warmup)) * 1.5))") {
		t.Error("scaling a duration by a fraction should go through a double")
	}

	// Two durations divided give their ratio, as a number.
	if !strings.Contains(out, "(((double)(__comp_inst->e).et) / ((double)(__comp_inst->pt)))") {
		t.Error("dividing two durations should give their ratio as a double")
	}

	// A literal carries the suffix, because a duration in nanoseconds outgrows
	// a 32-bit long at two seconds.
	if !strings.Contains(out, "90000000000LL") {
		t.Error("T#1m30s should reach the C as 90000000000LL")
	}
}

// TestSTDurationDivisionIsGuarded is a regression.
//
// A duration is not on the numeric lattice -- IsInteger is false for it -- so
// it slipped past the zero-divisor guard, and dividing one by a runtime zero
// would have raised SIGFPE and taken the whole RT process with it.  The float
// case needs no guard: dividing by 0.0 gives infinity, not a signal.
func TestSTDurationDivisionIsGuarded(t *testing.T) {
	out := generateSTSource(t, `{attribute 'summary' := 'x'}
PROGRAM p
  VAR_INPUT
    n : DINT;
  END_VAR
  VAR_OUTPUT
    out : LREAL;
  END_VAR
  VAR
    pt : TIME := T#1s;
  END_VAR
  pt := pt / n;
  out := TIME_TO_SECONDS(pt);
END_PROGRAM`)

	if !strings.Contains(out, "__st_div_s(__comp_inst,") {
		t.Errorf("dividing a duration by a runtime value must be guarded; "+
			"unguarded it is a SIGFPE in the RT thread\n--- generated ---\n%s", out)
	}
}

// TestSTStringAssignment is a regression, and the last of the STRING paths.
//
// A STRING is a char array, and C has no assignment for one: `b := a` emitted
// `(inst->b) = (inst->a);`, which does not compile. It is copied instead,
// bounded by the destination -- IEC assigns a longer string to a shorter one
// by truncating, so the two need not be the same width -- and a copy that
// loses characters records a counted fault, on the same terms as a clamped
// index. Truncation is allowed; silent truncation is not.
func TestSTStringAssignment(t *testing.T) {
	out := generateSTSource(t, `{attribute 'summary' := 'x'}
PROGRAM p
  VAR_OUTPUT
    q : DINT;
  END_VAR
  VAR
    small : STRING[4];
    big   : STRING[40] := 'a much longer string';
  END_VAR
  small := big;
  q := 1;
END_PROGRAM`)

	// Sized, terminator included, and initialised by copying rather than by
	// filling every element with the same value.
	for _, want := range []string{
		"char small[5];",
		"char big[41];",
		`snprintf(inst->big, sizeof(inst->big), "%s", "a much longer string");`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing:\n  %s\n--- generated ---\n%s", want, out)
		}
	}

	// The assignment is a bounded copy, sized from the destination.
	if !strings.Contains(out,
		"__st_strcpy(__comp_inst, (__comp_inst->small), (int)sizeof((__comp_inst->small)), (__comp_inst->big),") {
		t.Errorf("string assignment is not a bounded copy\n--- generated ---\n%s", out)
	}
	// ...and losing characters is reported rather than passing quietly.
	if !strings.Contains(out, "if (src[__i] != '\\0') __st_fault(__comp_inst, what);") {
		t.Error("a truncated copy does not record a fault")
	}
	// The helper needs the counter, so it has to exist here even though
	// nothing else in this component would have asked for one.
	if !strings.Contains(out, "__st_faults") {
		t.Error("the fault counter is missing, so the copy helper would not link")
	}
}

// TestSTStringHelperCostsNothingWhenUnused is the -Werror trap again.
func TestSTStringHelperCostsNothingWhenUnused(t *testing.T) {
	out := generateST(t, "and2")
	if strings.Contains(out, "__st_strcpy") {
		t.Error("a component that assigns no string got the copy helper; " +
			"-Werror=unused-function would reject it")
	}
}

// TestSTStringComparison covers comparing two STRINGs.
//
// `a = b` in C compares two addresses and is always false, so the characters
// are compared instead. Declared widths do not enter into it: how much room
// each was given to hold its characters is not part of the question.
func TestSTStringComparison(t *testing.T) {
	out := generateSTSource(t, `{attribute 'summary' := 'x'}
PROGRAM p
  VAR_OUTPUT
    same   : BOOL;
    before : BOOL;
  END_VAR
  VAR
    a : STRING[8]  := 'abc';
    b : STRING[16] := 'abd';
  END_VAR
  same   := a = b;
  before := a < b;
END_PROGRAM`)

	for _, want := range []string{
		"__pin_same = (__st_strcmp((__comp_inst->a), (__comp_inst->b)) == 0)",
		"__pin_before = (__st_strcmp((__comp_inst->a), (__comp_inst->b)) < 0)",
		"return (int)(unsigned char)*a - (int)(unsigned char)*b;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing:\n  %s\n--- generated ---\n%s", want, out)
		}
	}

	// Comparing cannot fault, so a component that only compares must not get
	// the counter or the reporter -- an emitted __st_fault nothing calls is
	// -Werror=unused-function, which is how this was found.
	if strings.Contains(out, "__st_fault") {
		t.Errorf("string comparison pulled in the fault reporter, which nothing here calls")
	}
	if strings.Contains(out, "__st_faults") {
		t.Error("string comparison pulled in the fault counter")
	}
}

// TestSTStringCompareHelperCostsNothingWhenUnused is the -Werror trap.
func TestSTStringCompareHelperCostsNothingWhenUnused(t *testing.T) {
	if out := generateST(t, "and2"); strings.Contains(out, "__st_strcmp") {
		t.Error("a component that compares no strings got the compare helper")
	}
}

// TestSTDayParts covers splitting a point in time into its halves.
//
// Neither half is a scaling of the other: the day is a floor to midnight and
// the time of day is what remains. Both have to be right below the epoch,
// where C's remainder takes the sign of the left operand -- which would put
// the day a day late and run the clock backwards.
func TestSTDayParts(t *testing.T) {
	out := generateSTSource(t, `{attribute 'summary' := 'x'}
PROGRAM p
  VAR_OUTPUT
    q : BOOL;
  END_VAR
  VAR
    stamp : DATE_AND_TIME := DT#2026-08-26-14:45:00;
    day   : DATE;
    clock : TIME_OF_DAY;
  END_VAR
  day   := DT_TO_DATE(stamp);
  clock := DT_TO_TOD(stamp);
  q := clock > TOD#08:00:00;
END_PROGRAM`)

	for _, want := range []string{
		"(__comp_inst->day) = __st_day((__comp_inst->stamp));",
		"(__comp_inst->clock) = __st_tod((__comp_inst->stamp));",
		"if (d < 0) d += __ST_NS_PER_DAY;",
		// All three are nanoseconds in an int64, so a literal carries the
		// suffix or a 32-bit long silently truncates it.
		"inst->stamp = 1787755500000000000LL;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing:\n  %s\n--- generated ---\n%s", want, out)
		}
	}

	// Splitting cannot fault, so it must not drag in the reporter.
	if strings.Contains(out, "__st_fault") {
		t.Error("splitting a point in time pulled in the fault reporter")
	}
}

// TestSTDayPartHelpersCostNothingWhenUnused is the -Werror trap.
func TestSTDayPartHelpersCostNothingWhenUnused(t *testing.T) {
	if out := generateST(t, "durations"); strings.Contains(out, "__st_day") {
		t.Error("a component that splits no point in time got the helpers")
	}
}

// TestSTTimeConversionDirection pins which way each conversion scales.
//
// It was wrong for three of them and nothing noticed: a conversion *into* a
// point in time divided instead of multiplying, so UDINT_TO_DT(1787733000)
// came out as 1 nanosecond past the epoch. Every test at the time converted
// only outwards, where the direction happens to be the same.
//
// The rule is one line: towards nanoseconds, multiply; away from them, divide.
// The cases below are one per type in each direction, so a fifth nanosecond
// type added without teaching the emitter shows up here.
func TestSTTimeConversionDirection(t *testing.T) {
	out := generateSTSource(t, `{attribute 'summary' := 'x'}
PROGRAM p
  VAR_OUTPUT
    q : BOOL;
  END_VAR
  VAR
    n : LINT;
    pt : TIME;
    stamp : DATE_AND_TIME;
    day : DATE;
    clock : TIME_OF_DAY;
  END_VAR
  pt    := LINT_TO_TIME(n);
  stamp := LINT_TO_DT(n);
  day   := LINT_TO_DATE(n);
  clock := UDINT_TO_TOD(1);
  n     := TIME_TO_LINT(pt);
  n     := DT_TO_LINT(stamp);
  n     := DATE_TO_LINT(day);
  q     := TRUE;
END_PROGRAM`)

	for _, tt := range []struct{ what, want string }{
		// Into nanoseconds: multiply, through a double so a fraction survives.
		{"LINT_TO_TIME", "(__comp_inst->pt) = ((int64_t)((double)(__comp_inst->n) * 1000000))"},
		{"LINT_TO_DT", "(__comp_inst->stamp) = ((int64_t)((double)(__comp_inst->n) * 1000000000))"},
		{"LINT_TO_DATE", "(__comp_inst->day) = ((int64_t)((double)(__comp_inst->n) * 1000000000))"},
		{"UDINT_TO_TOD", "(__comp_inst->clock) = ((int64_t)((double)1u * 1000000))"},
		// Out of them: divide, in int64 so it stays exact.
		{"TIME_TO_LINT", "((int64_t)((__comp_inst->pt) / (int64_t)1000000))"},
		{"DT_TO_LINT", "((int64_t)((__comp_inst->stamp) / (int64_t)1000000000))"},
		{"DATE_TO_LINT", "((int64_t)((__comp_inst->day) / (int64_t)1000000000))"},
	} {
		if !strings.Contains(out, tt.want) {
			t.Errorf("%s scales the wrong way; wanted\n  %s\n--- generated ---\n%s",
				tt.what, tt.want, out)
		}
	}
}

// TestSTStringFunctionBuffers covers how a string result reaches its caller.
//
// C returns no arrays, so each call builds its result in a buffer the call
// site owns and returns that buffer. What has to hold is that the buffers are
// distinct: CONCAT(CONCAT(a, b), c) is the shape these appear in, and a shared
// one would have the inner call overwrite what the outer call is reading.
func TestSTStringFunctionBuffers(t *testing.T) {
	out := generateSTSource(t, `{attribute 'summary' := 'x'}
PROGRAM p
  VAR_OUTPUT
    q : BOOL;
  END_VAR
  VAR
    s : STRING[8] := 'ab';
    msg : STRING[64];
  END_VAR
  msg := CONCAT(CONCAT('drive=', s), '!');
  q := TRUE;
END_PROGRAM`)

	// Three call sites, three buffers, each sized from its own arguments:
	// 6+8 for the inner, that plus 1 for the outer.
	for _, want := range []string{
		"char __st_s0[16];",
		"char __st_s1[15];",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing buffer declaration %q\n--- generated ---\n%s", want, out)
		}
	}
	if strings.Count(out, "char __st_s") != 2 {
		t.Errorf("wanted one buffer per call site, got %d\n--- generated ---\n%s",
			strings.Count(out, "char __st_s"), out)
	}
	// Nested as an expression, not hoisted into statements: that is what keeps
	// a call inside a loop or a short-circuit evaluating when it should.
	if !strings.Contains(out, "__st_concat(__comp_inst, __st_s0, (int)sizeof(__st_s0), __st_concat(__comp_inst, __st_s1,") {
		t.Errorf("the calls did not nest\n--- generated ---\n%s", out)
	}
	// And the result is copied into the destination under its own bound,
	// because the buffer is sized from the arguments and the variable is not.
	if !strings.Contains(out, "__st_strcpy(__comp_inst, (__comp_inst->msg)") {
		t.Errorf("the result was not copied into the destination\n--- generated ---\n%s", out)
	}
}

// TestSTStringHelpersAreGated is the -Werror trap for the string runtime.
//
// The build runs with -Wall -Werror, so a helper nothing calls fails the
// compile just as surely as a wrong one. FIND is the case that pins it: it
// counts characters rather than building any, so it needs neither the append
// primitive nor the fault reporter that a truncated result would use.
func TestSTStringHelpersAreGated(t *testing.T) {
	find := generateSTSource(t, `{attribute 'summary' := 'x'}
PROGRAM p
  VAR_OUTPUT
    q : BOOL;
  END_VAR
  VAR
    s : STRING[8] := 'ab';
  END_VAR
  q := FIND(s, 'b') = 2;
END_PROGRAM`)
	if !strings.Contains(find, "static int __st_find") {
		t.Error("FIND did not get its helper")
	}
	for _, unwanted := range []string{"__st_app", "__st_fault", "__st_len", "__st_concat"} {
		if strings.Contains(find, unwanted) {
			t.Errorf("a component that only searches got %s", unwanted)
		}
	}

	// And a component that uses none of them gets none of it.
	if plain := generateST(t, "durations"); strings.Contains(plain, "__st_") &&
		strings.Contains(plain, "__st_concat") {
		t.Error("a component with no string functions got the string runtime")
	}
}

// TestSTToStringGating is the -Werror trap for the writers.
//
// Several source types share one writer -- every signed integer is written the
// same way once it is an int64 -- so the gating is on the writer and not on
// the type, and a component that writes one kind of value must not carry the
// rest.
func TestSTToStringGating(t *testing.T) {
	// Written out and measured, not stored: an assignment into a STRING
	// variable would bring the copy helper and its fault with it, and what is
	// being pinned here is that the conversion itself needs neither.
	ints := generateSTSource(t, `{attribute 'summary' := 'x'}
PROGRAM p
  VAR_OUTPUT
    n : INT;
  END_VAR
  n := LEN(INT_TO_STRING(-1));
END_PROGRAM`)
	for _, want := range []string{"static int __st_num", "static char *__st_str_i"} {
		if !strings.Contains(ints, want) {
			t.Errorf("writing an integer out did not emit %q", want)
		}
	}
	for _, unwanted := range []string{
		"__st_str_f", "__st_str_t", "__st_civil", "__st_word", "__st_unum",
		// Nothing can truncate here: every buffer holds the widest result
		// its type gives, so the reporter would be a helper nothing calls.
		"__st_fault",
	} {
		if strings.Contains(ints, unwanted) {
			t.Errorf("a component that only writes integers got %s", unwanted)
		}
	}

	// A date pulls in the calendar, and with it the split that makes a day
	// before 1970 land on the day it is.
	dates := generateSTSource(t, `{attribute 'summary' := 'x'}
PROGRAM p
  VAR_OUTPUT
    n : INT;
  END_VAR
  n := LEN(DATE_TO_STRING(D#2026-08-26));
END_PROGRAM`)
	for _, want := range []string{
		"static void __st_civil", "static int64_t __st_day", "__ST_NS_PER_DAY",
	} {
		if !strings.Contains(dates, want) {
			t.Errorf("writing a date out did not emit %q\n--- generated ---\n%s", want, dates)
		}
	}
	// A date has no clock, so the clock writer is not reached.
	if strings.Contains(dates, "__st_clock_at") {
		t.Error("writing a date out pulled in the clock")
	}
}

// TestSTToStringFloatCanFault covers the one conversion in the family that can
// fail to place its value.
//
// Six decimals are held in an int64 scaled by a million, which runs out at
// about nine million million; past an int64 entirely there is nothing to
// place. It says so rather than printing a wrong number, which is why it alone
// takes somewhere to say it.
func TestSTToStringFloatCanFault(t *testing.T) {
	out := generateSTSource(t, `{attribute 'summary' := 'x'}
PROGRAM p
  VAR_OUTPUT
    n : INT;
  END_VAR
  VAR
    msg : STRING[40];
    v : LREAL := 1.5;
  END_VAR
  msg := LREAL_TO_STRING(v);
  n := LEN(msg);
END_PROGRAM`)
	if !strings.Contains(out, "__st_str_f(__comp_inst, __st_s0, (int)sizeof(__st_s0), (__comp_inst->v), \"LREAL_TO_STRING at") {
		t.Errorf("the float writer was not given somewhere to report\n--- generated ---\n%s", out)
	}
	if !strings.Contains(out, "static void __st_fault") {
		t.Error("the float writer can fault but the reporter was not emitted")
	}
	// Trailing zeros go, which is what makes 2.0 read as 2 rather than as
	// 2.000000.
	if !strings.Contains(out, "while (k > 0 && frac[k] == '0') k--;") {
		t.Error("the float writer keeps its trailing zeros")
	}
}

// TestSTExtremeIntegerLiterals covers how the ends of a 64-bit word reach C.
//
// C has no literal for the most negative int64: 9223372036854775808 is past
// what a signed long long holds, so -9223372036854775808 is a unary minus on
// an unsigned constant and gcc refuses it under -Werror. And a ULINT above
// int64 is held as the pattern, which reads as -1 unless the type is asked.
// Both spellings have to be right in the body and in the initialiser, which
// are separate emitters.
func TestSTExtremeIntegerLiterals(t *testing.T) {
	out := generateSTSource(t, `{attribute 'summary' := 'x'}
PROGRAM p
  VAR_OUTPUT
    q : BOOL;
  END_VAR
  VAR
    lo : LINT := -9223372036854775808;
    hi : ULINT := 18446744073709551615;
    a : LINT;
    b : ULINT;
  END_VAR
  a := -9223372036854775808;
  b := 18446744073709551615;
  q := TRUE;
END_PROGRAM`)

	for _, want := range []string{
		// The initialisers, emitted verbatim into the loader.
		"inst->lo = (-9223372036854775807LL - 1);",
		"inst->hi = 18446744073709551615ULL;",
		// And the body, which renders literals through its own path.
		"(__comp_inst->a) = (-9223372036854775807LL - 1);",
		"(__comp_inst->b) = 18446744073709551615ULL;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing:\n  %s\n--- generated ---\n%s", want, out)
		}
	}
	// The spelling C rejects must appear nowhere.
	if strings.Contains(out, "-9223372036854775808") {
		t.Error("emitted -9223372036854775808, which C reads as a minus on an unsigned constant")
	}
}
