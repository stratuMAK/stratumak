// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package comp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// Root of the linuxcnc source tree, relative to this test file.
func sourceRoot() string {
	dir, _ := os.Getwd()
	return filepath.Join(dir, "..", "..", "..", "..", "..")
}

func findCompDir() string {
	return filepath.Join(sourceRoot(), "src", "hal", "components")
}

func findDriverCompDir() string {
	return filepath.Join(sourceRoot(), "src", "hal", "drivers")
}

func findUserCompDir() string {
	return filepath.Join(sourceRoot(), "src", "hal", "user_comps")
}

func TestParseSimpleComp(t *testing.T) {
	src := `component and2 "Two-input AND gate";
pin in bit in0;
pin in bit in1;
pin out bit out "out is computed from the value of in0 and in1";
function _ nofp;
license "GPL";
;;
FUNCTION(_) { out = in0 && in1; }
`
	pkg, err := Parse("and2.comp", src)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	c := pkg.Component
	if c.Name != "and2" {
		t.Errorf("Name = %q, want %q", c.Name, "and2")
	}
	if c.Summary != "Two-input AND gate" {
		t.Errorf("Summary = %q, want %q", c.Summary, "Two-input AND gate")
	}
	if c.License != "GPL" {
		t.Errorf("License = %q, want %q", c.License, "GPL")
	}
	if len(c.Pins) != 3 {
		t.Fatalf("len(Pins) = %d, want 3", len(c.Pins))
	}
	if c.Pins[0].Name != "in0" || c.Pins[0].Dir != ast.PinIn || c.Pins[0].Type != ast.HALBit {
		t.Errorf("Pin[0] = %+v", c.Pins[0])
	}
	if c.Pins[2].Name != "out" || c.Pins[2].Dir != ast.PinOut {
		t.Errorf("Pin[2] = %+v", c.Pins[2])
	}
	if len(c.Functions) != 1 {
		t.Fatalf("len(Functions) = %d, want 1", len(c.Functions))
	}
	if c.Functions[0].Name != "_" || c.Functions[0].FP != false {
		t.Errorf("Function[0] = %+v", c.Functions[0])
	}
	if !strings.Contains(c.VerbatimC, "FUNCTION(_)") {
		t.Errorf("VerbatimC doesn't contain FUNCTION: %q", c.VerbatimC)
	}
}

func TestParsePersonalityPin(t *testing.T) {
	src := `component test "test";
pin in bit hall1 if personality & 0x01 "Hall sensor";
function _;
license "GPL";
;;
`
	pkg, err := Parse("test.comp", src)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(pkg.Component.Pins) != 1 {
		t.Fatalf("len(Pins) = %d, want 1", len(pkg.Component.Pins))
	}
	pin := pkg.Component.Pins[0]
	if pin.Personality != "personality & 0x01" {
		t.Errorf("Personality = %q, want %q", pin.Personality, "personality & 0x01")
	}
}

func TestParseArrayPin(t *testing.T) {
	src := `component test "test";
pin out bit bit-##[32:personality] = false "Output bits";
function _;
license "GPL";
;;
`
	pkg, err := Parse("test.comp", src)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	pin := pkg.Component.Pins[0]
	if pin.Name != "bit-##" {
		t.Errorf("Name = %q, want %q", pin.Name, "bit-##")
	}
	if pin.ArraySize != 32 {
		t.Errorf("ArraySize = %d, want 32", pin.ArraySize)
	}
	if pin.ArrayPersonality != "personality" {
		t.Errorf("ArrayPersonality = %q, want %q", pin.ArrayPersonality, "personality")
	}
	if pin.Default != "false" {
		t.Errorf("Default = %q, want %q", pin.Default, "false")
	}
}

func TestParseOptions(t *testing.T) {
	src := `component test "test";
pin out bit x;
function _;
option extra_setup;
option data internal;
license "GPL";
;;
`
	pkg, err := Parse("test.comp", src)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	opts := pkg.Component.Options
	// A bare flag option normalizes to "yes" — the spelling docgen tests for,
	// while cgen tests mere presence.
	if opts["extra_setup"] != "yes" {
		t.Errorf("extra_setup = %q", opts["extra_setup"])
	}
	if opts["data"] != "internal" {
		t.Errorf("data = %q", opts["data"])
	}
}

func TestParseSingletonRejected(t *testing.T) {
	src := `component test "test";
pin out bit x;
function _;
option singleton yes;
license "GPL";
;;
`
	_, err := Parse("test.comp", src)
	if err == nil {
		t.Fatal("expected error for 'option singleton'")
	}
	if !strings.Contains(err.Error(), "singleton") {
		t.Errorf("error should mention singleton: %v", err)
	}
}

func TestParseVariable(t *testing.T) {
	src := `component test "test";
pin out bit x;
function _;
variable double counter = 0;
variable int old_pattern = -1;
variable unsigned *ptr;
variable unsigned data[8];
license "GPL";
;;
`
	pkg, err := Parse("test.comp", src)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	vars := pkg.Component.Variables
	if len(vars) != 4 {
		t.Fatalf("len(Variables) = %d, want 4", len(vars))
	}
	if vars[0].CType != "double" || vars[0].Name != "counter" || vars[0].Default != "0" {
		t.Errorf("var[0] = %+v", vars[0])
	}
	if vars[1].CType != "int" || vars[1].Name != "old_pattern" || vars[1].Default != "-1" {
		t.Errorf("var[1] = %+v", vars[1])
	}
	if vars[2].CType != "unsigned" || vars[2].Name != "*ptr" {
		t.Errorf("var[2] = %+v", vars[2])
	}
	if vars[3].CType != "unsigned" || vars[3].Name != "data" || vars[3].Array != 8 {
		t.Errorf("var[3] = %+v", vars[3])
	}
}

func TestParseModparam(t *testing.T) {
	src := `component test "test";
pin out bit x;
function _;
modparam dummy cfg "configuration string";
modparam int test_encoder = 0 "test encoder";
license "GPL";
;;
`
	pkg, err := Parse("test.comp", src)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	mps := pkg.Component.Modparams
	if len(mps) != 2 {
		t.Fatalf("len(Modparams) = %d, want 2", len(mps))
	}
	if mps[0].Type != "dummy" || mps[0].Name != "cfg" || mps[0].Doc != "configuration string" {
		t.Errorf("modparam[0] = %+v", mps[0])
	}
	if mps[1].Type != "int" || mps[1].Name != "test_encoder" || mps[1].Default != "0" {
		t.Errorf("modparam[1] = %+v", mps[1])
	}
}

func TestParseInclude(t *testing.T) {
	src := `component test "test";
pin out bit x;
function _;
include "rtapi_math.h";
license "GPL";
;;
`
	pkg, err := Parse("test.comp", src)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(pkg.Component.Includes) != 1 {
		t.Fatalf("len(Includes) = %d, want 1", len(pkg.Component.Includes))
	}
	if pkg.Component.Includes[0] != `"rtapi_math.h"` {
		t.Errorf("Include[0] = %q", pkg.Component.Includes[0])
	}
}

func TestParseTripleQuotedString(t *testing.T) {
	src := `component test """This is a
multi-line description""";
pin out bit x;
function _;
license "GPL";
;;
`
	pkg, err := Parse("test.comp", src)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if !strings.Contains(pkg.Component.Summary, "multi-line") {
		t.Errorf("Summary = %q", pkg.Component.Summary)
	}
}

// TestParseAllComponentFiles discovers and parses all .comp files
// under src/hal/components/ and src/hal/drivers/. This validates the
// parser handles real-world input.
func TestParseAllComponentFiles(t *testing.T) {
	dirs := []struct {
		name string
		path string
	}{
		{"components", findCompDir()},
		{"drivers", findDriverCompDir()},
	}
	for _, d := range dirs {
		if _, err := os.Stat(d.path); os.IsNotExist(err) {
			t.Skipf("%s directory not found at %s", d.name, d.path)
		}
		entries, err := os.ReadDir(d.path)
		if err != nil {
			t.Fatalf("ReadDir(%s): %v", d.path, err)
		}
		parsed := 0
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".comp") {
				continue
			}
			path := filepath.Join(d.path, e.Name())
			t.Run(d.name+"/"+e.Name(), func(t *testing.T) {
				src, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("ReadFile: %v", err)
				}
				pkg, err := Parse(e.Name(), string(src))
				if err != nil {
					// Skip files with unsupported features (singleton, RTAPI_MP_ARRAY_*).
					if strings.Contains(err.Error(), "singleton") ||
						strings.Contains(err.Error(), "RTAPI_MP_ARRAY_") {
						t.Skipf("unsupported feature: %v", err)
					}
					t.Fatalf("Parse error: %v", err)
				}
				if pkg.Component.Name == "" {
					t.Error("Component name is empty")
				}
			})
			parsed++
		}
		t.Logf("%s: parsed %d .comp files", d.name, parsed)
	}
}

// TestParseUserComps tests parsing the userspace .comp files.
func TestParseUserComps(t *testing.T) {
	baseDir := findUserCompDir()
	if _, err := os.Stat(baseDir); os.IsNotExist(err) {
		t.Skipf("user_comps directory not found at %s", baseDir)
	}
	var files []string
	if err := filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(info.Name(), ".comp") {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", baseDir, err)
	}
	if len(files) == 0 {
		t.Skip("no .comp files found in user_comps")
	}
	for _, path := range files {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			pkg, err := Parse(name, string(src))
			if err != nil {
				// Skip files with unsupported features (singleton, RTAPI_MP_ARRAY_*).
				if strings.Contains(err.Error(), "singleton") ||
					strings.Contains(err.Error(), "RTAPI_MP_ARRAY_") {
					t.Skipf("unsupported feature: %v", err)
				}
				t.Fatalf("Parse error: %v", err)
			}
			if pkg.Component.Name == "" {
				t.Error("Component name is empty")
			}
		})
	}
}

func TestParseGMIProvideConsume(t *testing.T) {
	src := `component mykins "Custom kinematics";
pin out s32 fpin;
function fdemo;
gmi_provide kins;
gmi_consume tp;
license "GPL";
;;
// user code
`
	pkg, err := Parse("mykins.comp", src)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	c := pkg.Component
	if len(c.GMIProvide) != 1 || c.GMIProvide[0] != "kins" {
		t.Errorf("GMIProvide = %v, want [kins]", c.GMIProvide)
	}
	if len(c.GMIConsume) != 1 || c.GMIConsume[0].API != "tp" {
		t.Errorf("GMIConsume = %v, want [{tp }]", c.GMIConsume)
	}
	if c.GMIConsume[0].From != "" {
		t.Errorf("GMIConsume[0].From = %q, want empty", c.GMIConsume[0].From)
	}
}

func TestParseGMIConsumeFrom(t *testing.T) {
	src := `component homecomp "Homing";
pin out s32 fpin;
function fdemo;
gmi_provide home;
gmi_consume mot from motmod;
license "GPL";
;;
`
	pkg, err := Parse("homecomp.comp", src)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	c := pkg.Component
	if len(c.GMIConsume) != 1 {
		t.Fatalf("GMIConsume length = %d, want 1", len(c.GMIConsume))
	}
	if c.GMIConsume[0].API != "mot" {
		t.Errorf("GMIConsume[0].API = %q, want \"mot\"", c.GMIConsume[0].API)
	}
	if c.GMIConsume[0].From != "motmod" {
		t.Errorf("GMIConsume[0].From = %q, want \"motmod\"", c.GMIConsume[0].From)
	}
}

func TestParseArch(t *testing.T) {
	src := `component pcl720 "Port I/O card";
pin out bit x;
function _ nofp;
arch x86_64 i386;
license "GPL";
;;
`
	pkg, err := Parse("pcl720.comp", src)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if got, want := pkg.Component.Archs, []string{"x86_64", "i386"}; !equalStrings(got, want) {
		t.Errorf("Archs = %v, want %v", got, want)
	}
}

func TestParseArchUnknownRejected(t *testing.T) {
	src := `component test "test";
pin out bit x;
function _ nofp;
arch sparc64;
license "GPL";
;;
`
	_, err := Parse("test.comp", src)
	if err == nil {
		t.Fatal("expected error for unknown arch")
	}
	if !strings.Contains(err.Error(), "sparc64") {
		t.Errorf("error should mention the offending arch: %v", err)
	}
}

func TestParseArchEmptyRejected(t *testing.T) {
	src := `component test "test";
pin out bit x;
function _ nofp;
arch;
license "GPL";
;;
`
	_, err := Parse("test.comp", src)
	if err == nil {
		t.Fatal("expected error for empty arch list")
	}
}

func TestParseMcode(t *testing.T) {
	src := `component test "test";
pin out bit m101_request;
mcode 100;
mcode 101;
license "GPL";
;;
MCODE(100) { return 32; }
MCODE(101) { return 0; }
`
	pkg, err := Parse("test.comp", src)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if !equalInts(pkg.Component.Mcodes, []int{100, 101}) {
		t.Errorf("Mcodes = %v, want [100 101]", pkg.Component.Mcodes)
	}
}

func TestParseMcodeOutOfRange(t *testing.T) {
	src := `component test "test";
mcode 250;
license "GPL";
;;
`
	_, err := Parse("test.comp", src)
	if err == nil {
		t.Fatal("expected error for out-of-range mcode")
	}
	if !strings.Contains(err.Error(), "100-199") {
		t.Errorf("error should mention the valid range: %v", err)
	}
}

func TestParseMcodeDuplicate(t *testing.T) {
	src := `component test "test";
mcode 101;
mcode 101;
license "GPL";
;;
`
	_, err := Parse("test.comp", src)
	if err == nil {
		t.Fatal("expected error for duplicate mcode")
	}
	if !strings.Contains(err.Error(), "more than once") {
		t.Errorf("error should mention the duplicate: %v", err)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// External build dependencies: pkgconfig / cflags / ldflags
// ---------------------------------------------------------------------------

const buildDepsPreamble = `component test "test";
pin out bit x;
function _;
license "GPL";
`

func parseBuildDeps(t *testing.T, decls string) *ast.Package {
	t.Helper()
	pkg, err := Parse("test.comp", buildDepsPreamble+decls+"\n;;\n")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	return pkg
}

func TestParsePkgConfig(t *testing.T) {
	// A bare name, several on one line, and a quoted version constraint. The
	// scanner's ident rule already admits '-' and '.', so libusb-1.0 needs no
	// quotes; the constraint does, because of the operator.
	pkg := parseBuildDeps(t, `pkgconfig libcurl;
pkgconfig libusb-1.0 zlib;
pkgconfig "libfoo >= 1.2.3";`)

	got := pkg.Component.PkgConfig
	want := []string{"libcurl", "libusb-1.0", "zlib", "libfoo >= 1.2.3"}
	if len(got) != len(want) {
		t.Fatalf("got %d deps, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Spec != w {
			t.Errorf("dep %d = %q, want %q", i, got[i].Spec, w)
		}
	}
	// Name() strips the constraint — that is what --deps prints.
	if n := got[3].Name(); n != "libfoo" {
		t.Errorf("Name() = %q, want %q", n, "libfoo")
	}
	if n := got[1].Name(); n != "libusb-1.0" {
		t.Errorf("Name() = %q, want %q", n, "libusb-1.0")
	}
}

func TestParsePkgConfigUnquotedConstraintRejected(t *testing.T) {
	_, err := Parse("test.comp", buildDepsPreamble+"pkgconfig libcurl >= 7.60;\n;;\n")
	if err == nil {
		t.Fatal("expected error for an unquoted version constraint")
	}
	// The message has to name the fix, not just complain about the token.
	if !strings.Contains(err.Error(), "must be") || !strings.Contains(err.Error(), "quoted") {
		t.Errorf("error should say the constraint must be quoted: %v", err)
	}
}

func TestParsePkgConfigEmptyRejected(t *testing.T) {
	if _, err := Parse("test.comp", buildDepsPreamble+"pkgconfig;\n;;\n"); err == nil {
		t.Fatal("expected error for a pkgconfig with no module")
	}
}

func TestParseCFlagsLDFlags(t *testing.T) {
	pkg := parseBuildDeps(t, `cflags "-DVENDOR -I/opt/vendor/include";
ldflags "-L/opt/vendor/lib -lvendor";`)

	if !equalStrings(pkg.Component.CFlags, []string{"-DVENDOR", "-I/opt/vendor/include"}) {
		t.Errorf("CFlags = %+v", pkg.Component.CFlags)
	}
	if !equalStrings(pkg.Component.LDFlags, []string{"-L/opt/vendor/lib", "-lvendor"}) {
		t.Errorf("LDFlags = %+v", pkg.Component.LDFlags)
	}
}

// Shell substitution is refused rather than passed through: `modcompile
// --install` runs as root, so a .comp must stay data, not become a script.
func TestParseBuildFlagsRejectSubstitution(t *testing.T) {
	for _, decl := range []string{
		`cflags "$(pkg-config --cflags libcurl)";`,
		`ldflags "$(pkg-config --libs libcurl)";`,
		"ldflags \"-L`pwd`/lib\";",
		`cflags "-I$HOME/include";`,
	} {
		_, err := Parse("test.comp", buildDepsPreamble+decl+"\n;;\n")
		if err == nil {
			t.Errorf("%s: expected rejection", decl)
			continue
		}
		if !strings.Contains(err.Error(), "pkgconfig") {
			t.Errorf("%s: error should point at pkgconfig: %v", decl, err)
		}
	}
}

func TestParseBuildFlagsRequireString(t *testing.T) {
	if _, err := Parse("test.comp", buildDepsPreamble+"cflags -DFOO;\n;;\n"); err == nil {
		t.Fatal("expected error for an unquoted cflags value")
	}
}

// ---------------------------------------------------------------------------
// Option whitelist
// ---------------------------------------------------------------------------

// The classic manual documents these two; modcompile used to accept and drop
// them, which cost the author an undefined symbol at load time instead of an
// error at compile time.
func TestParseLegacyBuildOptionsRejected(t *testing.T) {
	for opt, want := range map[string]string{
		"extra_compile_args": "cflags",
		"extra_link_args":    "ldflags",
	} {
		src := buildDepsPreamble + "option " + opt + " \"-lcurl\";\n;;\n"
		_, err := Parse("test.comp", src)
		if err == nil {
			t.Errorf("option %s: expected rejection", opt)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("option %s: error should name %q: %v", opt, want, err)
		}
	}
}

func TestParseUnknownOptionRejected(t *testing.T) {
	_, err := Parse("test.comp", buildDepsPreamble+"option nosuchoption yes;\n;;\n")
	if err == nil {
		t.Fatal("expected error for an unknown option")
	}
	// The message lists what is accepted, so a typo is self-correcting.
	if !strings.Contains(err.Error(), "extra_setup") {
		t.Errorf("error should list the supported options: %v", err)
	}
}

// Options that classic halcompile had and stratuMAK ignores stay accepted, so
// existing components keep building — but they warn, because the point of the
// whitelist is that no declaration silently does nothing.
func TestParseIgnoredOptionsWarn(t *testing.T) {
	for _, opt := range []string{"fp yes", "personality", "default_personality 32"} {
		pkg, err := Parse("test.comp", buildDepsPreamble+"option "+opt+";\n;;\n")
		if err != nil {
			t.Errorf("option %s: unexpected error: %v", opt, err)
			continue
		}
		if len(pkg.Warnings) != 1 {
			t.Errorf("option %s: got %d warnings, want 1: %+v", opt, len(pkg.Warnings), pkg.Warnings)
			continue
		}
		if !strings.Contains(pkg.Warnings[0], "ignored") {
			t.Errorf("option %s: warning should say it is ignored: %q", opt, pkg.Warnings[0])
		}
		// Warned, and not stored: an option nothing reads must not surface in
		// --parse output as if it did something.
		if len(pkg.Component.Options) != 0 {
			t.Errorf("option %s: stored despite being ignored: %+v", opt, pkg.Component.Options)
		}
	}
}

// Classic options with no counterpart here fail with a message that names
// what happened to each, not the bare "unknown option" a typo gets: all of
// these parsed silently on earlier releases, so the error is the migration
// pointer.
func TestParseUnsupportedClassicOptionsNamed(t *testing.T) {
	for _, opt := range []string{
		"count_function", "default_count 4", "constructable",
		"rtapi_app no", "userinit", "homemod", "tpmod",
	} {
		_, err := Parse("test.comp", buildDepsPreamble+"option "+opt+";\n;;\n")
		if err == nil {
			t.Errorf("option %s: expected rejection", opt)
			continue
		}
		if !strings.Contains(err.Error(), "not supported") {
			t.Errorf("option %s: error should say it is not supported: %v", opt, err)
		}
		if strings.Contains(err.Error(), "unknown option") {
			t.Errorf("option %s: recognized classic option got the typo message: %v", opt, err)
		}
	}
}

// Flag options must mean what they say: cgen tests presence and docgen tests
// "yes", so an unvalidated value would let `option userspace no;` silently
// build a userspace component. Truthy spellings normalize to "yes", falsy
// ones to absent, anything else is an error.
func TestParseBoolOptionValues(t *testing.T) {
	for _, val := range []string{"", " yes", " 1", " true"} {
		pkg, err := Parse("test.comp", `component test "test";
pin out bit x;
license "GPL";
option userspace`+val+";\n;;\n")
		if err != nil {
			t.Errorf("option userspace%s: unexpected error: %v", val, err)
			continue
		}
		if got := pkg.Component.Options["userspace"]; got != "yes" {
			t.Errorf("option userspace%s: stored as %q, want %q", val, got, "yes")
		}
	}
	for _, val := range []string{" no", " 0", " false"} {
		pkg, err := Parse("test.comp", buildDepsPreamble+"option userspace"+val+";\n;;\n")
		if err != nil {
			t.Errorf("option userspace%s: unexpected error: %v", val, err)
			continue
		}
		if _, ok := pkg.Component.Options["userspace"]; ok {
			t.Errorf("option userspace%s: the explicit default must not be stored", val)
		}
	}
	if _, err := Parse("test.comp", buildDepsPreamble+"option extra_setup maybe;\n;;\n"); err == nil {
		t.Error("expected rejection of a flag option with a non-boolean value")
	}
}

// A data block needs a type; a bare `option data;` would send parseOptValue's
// "1" default into the generated C as a type name.
func TestParseDataOptionNeedsValue(t *testing.T) {
	if _, err := Parse("test.comp", buildDepsPreamble+"option data;\n;;\n"); err == nil {
		t.Error("expected rejection of 'option data' without a type")
	}
}

// The linker's own dynamic string tokens are literals, not shell
// substitution: -Wl,-rpath,$ORIGIN is the standard way a module locates a
// vendor .so shipped beside it, and needs no shell to mean that.
func TestParseBuildFlagsAllowLinkerTokens(t *testing.T) {
	pkg := parseBuildDeps(t, `ldflags "-L/opt/vendor/lib -lvendor -Wl,-rpath,$ORIGIN";
cflags "-DRPATH=${ORIGIN}/lib";`)
	if !equalStrings(pkg.Component.LDFlags,
		[]string{"-L/opt/vendor/lib", "-lvendor", "-Wl,-rpath,$ORIGIN"}) {
		t.Errorf("LDFlags = %+v", pkg.Component.LDFlags)
	}
	// $ORIGINAL is a variable that merely starts like the token.
	if _, err := Parse("test.comp", buildDepsPreamble+`ldflags "-Wl,-rpath,$ORIGINAL";`+"\n;;\n"); err == nil {
		t.Error("expected rejection of $ORIGINAL (a variable, not the $ORIGIN token)")
	}
}

// pkg-config's grammar takes comma lists, so a comma spec would compile — but
// --deps prints one name per declaration and would silently drop the second
// module from the dependency report.
func TestParsePkgConfigCommaRejected(t *testing.T) {
	for _, decl := range []string{
		`pkgconfig "libcurl, zlib";`,
		`pkgconfig "libcurl >= 7.60.0, zlib";`,
	} {
		_, err := Parse("test.comp", buildDepsPreamble+decl+"\n;;\n")
		if err == nil {
			t.Errorf("%s: expected rejection", decl)
			continue
		}
		if !strings.Contains(err.Error(), "one module per spec") {
			t.Errorf("%s: error should name the rule: %v", decl, err)
		}
	}
}
