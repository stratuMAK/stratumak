// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// modcompile compiles .comp files into cmod .so plugins for gomc-server,
// and manages the package registry for compiled-in Go modules.
//
// Usage:
//
//	modcompile [options] file.comp...
//
// Options:
//
//	--help           Show this help message.
//	--parse          Parse only — print the parsed AST and exit.
//	--preprocess     Preprocess only — emit generated C to stdout.
//	--document       Generate man page documentation.
//	--view-doc       Generate and display man page.
//	--compile        Compile to .so in the current directory.
//	--install        Compile and install to EMC2_CMOD_DIR.
//	-o FILE          Write output to FILE (for --preprocess, --document).
//
// Package registry commands:
//
//	list             List registered packages.
//	rebuild          Regenerate imports_generated.go and rebuild gomc-server.
//	add-gomod        Copy a Go package into gomc and rebuild gomc-server.
//	rm-gomod         Remove a Go package and rebuild gomc-server.
//
// Environment query options (for external Makefiles):
//
//	--cflags         Print compiler flags for cmod components.
//	--ldflags        Print linker flags for cmod components.
//	--cmod-dir       Print cmod installation directory.
//	--include-dir    Print cmod headers directory.
//	--gomc-dir       Print gomc Go module source directory.
//	--go             Print Go binary path used to build LinuxCNC.
//	--print-make-inc Print Makefile include snippet for external projects.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sittner/linuxcnc/src/gomc/internal/config"
	gmiast "github.com/sittner/linuxcnc/src/gomc/internal/gmicompile/ast"
	gmicgen "github.com/sittner/linuxcnc/src/gomc/internal/gmicompile/cgen"
	gmicheck "github.com/sittner/linuxcnc/src/gomc/internal/gmicompile/check"
	gmiparser "github.com/sittner/linuxcnc/src/gomc/internal/gmicompile/parser"
	"github.com/sittner/linuxcnc/src/gomc/internal/modcompile/ast"
	"github.com/sittner/linuxcnc/src/gomc/internal/modcompile/cgen"
	"github.com/sittner/linuxcnc/src/gomc/internal/modcompile/comp"
	"github.com/sittner/linuxcnc/src/gomc/internal/modcompile/docgen"
	"github.com/sittner/linuxcnc/src/gomc/internal/pkgreg"
)

const usageText = `modcompile: Compile .comp files, generate GMI code, and manage gomc-server packages

Usage:
    modcompile [options] file.comp...
    modcompile gmi [options] file.gmi...
    modcompile list | rebuild | add-gomod | rm-gomod
    modcompile --cflags | --ldflags | --cmod-dir | --include-dir
    modcompile --print-make-inc

Compile options (.comp):
    --help           Show this help message
    --parse          Parse only — print the parsed AST as JSON
    --preprocess     Preprocess only — emit generated C code
    --document       Generate man page documentation
    --view-doc       Generate and display man page in terminal
    --compile        Compile .comp to .so in the current directory
    --install        Compile .comp and install to cmod directory
    -o FILE          Write output to FILE (for --preprocess, --document)

GMI code generation (.gmi):
    modcompile gmi --parse file.gmi
    modcompile gmi --server-c file.gmi -o api.h
    modcompile gmi --client-c file.gmi -o client
    modcompile gmi --server-go file.gmi -o api.go
    modcompile gmi --client-go file.gmi -o client.go
    modcompile gmi --client-python file.gmi -o client.py

Package registry commands:
    list             List packages compiled into gomc-server
    rebuild          Regenerate imports + rebuild gomc-server from packages.conf
    add-gomod <dir>  Copy a Go package into gomc, register, and rebuild
    rm-gomod <name>  Unregister, delete source, and rebuild gomc-server

Environment query options (for external Makefiles):
    --cflags         Print compiler flags for cmod components
    --ldflags        Print linker flags for cmod components
    --cmod-dir       Print cmod installation directory
    --include-dir    Print cmod headers directory
    --gomc-dir       Print gomc Go module source directory
    --go             Print Go binary path used to build LinuxCNC
    --print-make-inc Print Makefile include snippet for external projects

Examples:
    # Compile a .comp file
    modcompile --compile mycomp.comp
    modcompile --install mycomp.comp

    # Generate GMI code from .gmi IDL
    modcompile gmi --server-c kins.gmi -o kins_api.h
    modcompile gmi --client-python manualtoolchange.gmi -o mtc_client.py

    # Generate documentation
    modcompile --document -o mycomp.9 mycomp.comp
    modcompile --view-doc mycomp.comp

    # Manage compiled-in Go modules
    modcompile list
    modcompile add-gomod /path/to/galv-formula
    modcompile rm-gomod galv-formula
    modcompile rebuild

    # Use in external Makefile:
    $(eval $(shell modcompile --print-make-inc))
    mycomp.so: mycomp.c
        $(GOMC_CC) $(GOMC_CFLAGS) -o $@ $< $(GOMC_LDFLAGS)
`

// Compiler/linker settings
const (
	defaultCC      = "gcc"
	defaultCXX     = "g++"
	defaultCFlags  = "-fPIC -Os -Wall"
	defaultLDFlags = "-shared -lm"
)

// resolveCC returns the C compiler command: $CC from the environment wins,
// then the configure-time compiler baked into the binary, then plain gcc.
// The result may contain arguments (e.g. "gcc -m32") and is never
// empty/whitespace-only (callers split it with strings.Fields).
func resolveCC() string {
	if cc := strings.TrimSpace(os.Getenv("CC")); cc != "" {
		return cc
	}
	if cc := strings.TrimSpace(config.CCompiler); cc != "" {
		return cc
	}
	return defaultCC
}

// resolveCXX is resolveCC for the C++ compiler (cgo CXX when rebuilding
// gomc-server).
func resolveCXX() string {
	if cxx := strings.TrimSpace(os.Getenv("CXX")); cxx != "" {
		return cxx
	}
	if cxx := strings.TrimSpace(config.CxxCompiler); cxx != "" {
		return cxx
	}
	return defaultCXX
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(1)
	}

	// Handle environment query options first (no files needed)
	switch os.Args[1] {
	case "--cflags":
		fmt.Printf("-I%s %s\n", config.EMC2CmodIncludeDir, defaultCFlags)
		return
	case "--ldflags":
		fmt.Println(defaultLDFlags)
		return
	case "--cmod-dir":
		// The directory an out-of-tree project should install into, which on
		// a packaged system is not the one the package's own modules live in.
		fmt.Println(cmodInstallDir())
		return
	case "--include-dir":
		fmt.Println(config.EMC2CmodIncludeDir)
		return
	case "--gomc-dir", "--launcher-dir":
		fmt.Println(config.GomcDir())
		return
	case "--go":
		fmt.Println(config.GoBinary)
		return
	case "--print-make-inc":
		printMakeInc()
		return

	// Package registry subcommands
	case "list":
		cmdList()
		return
	case "rebuild":
		cmdRebuild()
		return
	case "regenerate-imports":
		cmdRegenerateImports()
		return
	case "regenerate-gomod":
		cmdRegenerateGomod()
		return
	case "add-gomod":
		force := false
		var dir string
		for _, a := range os.Args[2:] {
			if a == "--force" || a == "-f" {
				force = true
			} else if !strings.HasPrefix(a, "-") {
				dir = a
			}
		}
		if dir == "" {
			fmt.Fprintln(os.Stderr, "modcompile add-gomod: missing directory argument")
			os.Exit(1)
		}
		cmdAddGomod(dir, force)
		return
	case "rm-gomod":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "modcompile rm-gomod: missing package name argument")
			os.Exit(1)
		}
		cmdRmGomod(os.Args[2])
		return

	case "add-gmi":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "modcompile add-gmi: missing import path argument")
			os.Exit(1)
		}
		cmdAddGmi(os.Args[2])
		return
	case "rm-gmi":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "modcompile rm-gmi: missing import path argument")
			os.Exit(1)
		}
		cmdRmGmi(os.Args[2])
		return

	// GMI code generation subcommand
	case "gmi":
		cmdGMI(os.Args[2:])
		return

	// Internal: the unprivileged half of a rebuild, reached by re-exec from
	// the privileged half. Not in the usage text — it compiles a tree the
	// caller names into a path the caller names, with the caller's own
	// privileges, so running it by hand gains nobody anything.
	case buildPhaseArg:
		cmdBuildPhase(os.Args[2:])
		return
	case buildCModPhaseArg:
		cmdCompileCModPhase(os.Args[2:])
		return
	}

	// Parse arguments for file-processing modes
	var mode string
	var outputFile string
	var files []string

	// Known file-processing modes. Any other dashed argument is an unknown flag
	// and must be rejected explicitly — not silently absorbed as a "mode" (which
	// let a typo like --compil, or a stray --personalities, either error with a
	// misleading "unknown mode" message or clobber a real mode supplied earlier).
	validModes := map[string]bool{
		"--parse":      true,
		"--preprocess": true,
		"--document":   true,
		"--view-doc":   true,
		"--compile":    true,
		"--install":    true,
	}

	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Print(usageText)
			os.Exit(0)
		case arg == "-o" && i+1 < len(args):
			outputFile = args[i+1]
			i++
		case strings.HasPrefix(arg, "-o"):
			outputFile = arg[2:]
		case validModes[arg]:
			if mode != "" && mode != arg {
				fmt.Fprintf(os.Stderr, "modcompile: conflicting modes %q and %q\n", mode, arg)
				os.Exit(1)
			}
			mode = arg
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(os.Stderr, "modcompile: unknown flag %q\n", arg)
			os.Exit(1)
		default:
			files = append(files, arg)
		}
	}

	if mode == "" {
		mode = "--parse"
	}

	if len(files) == 0 {
		fmt.Fprintf(os.Stderr, "modcompile: no input files\n")
		os.Exit(1)
	}

	for _, path := range files {
		if err := processFile(path, mode, outputFile); err != nil {
			fmt.Fprintf(os.Stderr, "modcompile: %v\n", err)
			os.Exit(1)
		}
	}
}

// cmodInstallDir returns the directory `--install` writes a compiled cmod to.
//
// On a packaged system that is the state directory's cmod directory, not the
// package's own: a local .so dropped among the shipped ones survives upgrades
// (dpkg removes only what it shipped) and can quietly shadow or collide with a
// module of the same name. Keeping the two apart is what lets the launcher say
// so — see resolveCModule.
func cmodInstallDir() string {
	if d := config.LocalCModDir(); d != "" {
		return d
	}
	return config.EMC2CmodDir
}

func processFile(path, mode, outputFile string) error {
	// Handle raw .c files — only --compile and --install are supported.
	if strings.HasSuffix(path, ".c") {
		switch mode {
		case "--compile":
			return compileCFile(path, ".")
		case "--install":
			requireCModInstallPrivilege()
			return compileCFile(path, cmodInstallDir())
		default:
			return fmt.Errorf("%s: .c files only support --compile and --install", path)
		}
	}

	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	pkg, err := comp.Parse(path, string(src))
	if err != nil {
		return err
	}

	// Enforce that the declared component name matches the .comp filename
	// (classic halcompile behaviour — catches copy/paste mistakes where a
	// component keeps the name of the file it was copied from).  HAL component
	// names cannot contain '-', so a filename may use '-' where the name uses
	// '_' (e.g. svd-ps_vfd.comp -> component svd_ps_vfd); normalize before
	// comparing so that is accepted while genuine mismatches are rejected.
	if base := strings.ReplaceAll(strings.TrimSuffix(filepath.Base(path), ".comp"), "-", "_"); pkg.Component.Name != base {
		return fmt.Errorf("%s: component name %q does not match file name (expected %q)", path, pkg.Component.Name, base)
	}

	switch mode {
	case "--parse":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(pkg)

	case "--preprocess":
		if outputFile == "" {
			return cgen.Generate(os.Stdout, pkg)
		}
		f, err := os.Create(outputFile)
		if err != nil {
			return err
		}
		if err := cgen.Generate(f, pkg); err != nil {
			_ = f.Close()
			return err
		}
		// Close checks the flush: a failed close on a written file loses output.
		return f.Close()

	case "--document":
		outName := outputFile
		if outName == "" {
			// Default output filename
			base := strings.TrimSuffix(filepath.Base(path), ".comp")
			section := "9"
			if pkg.Component.Options["userspace"] == "yes" {
				section = "1"
			}
			outName = base + "." + section
		}
		f, err := os.Create(outName)
		if err != nil {
			return err
		}
		if err := docgen.Generate(f, pkg); err != nil {
			_ = f.Close()
			return err
		}
		// Close checks the flush: a failed close on a written file loses output.
		return f.Close()

	case "--view-doc":
		// Generate to temp file and display with man
		tmpFile, err := os.CreateTemp("", "modcompile-*.man")
		if err != nil {
			return err
		}
		tmpName := tmpFile.Name()
		defer func() { _ = os.Remove(tmpName) }()

		if err := docgen.Generate(tmpFile, pkg); err != nil {
			_ = tmpFile.Close()
			return err
		}
		// Flush before man reads the file back.
		if err := tmpFile.Close(); err != nil {
			return err
		}

		// Run man to display
		cmd := exec.Command("man", tmpName)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		return cmd.Run()

	case "--compile":
		return compileComp(path, pkg, ".")

	case "--install":
		requireCModInstallPrivilege()
		return compileComp(path, pkg, cmodInstallDir())

	default:
		return fmt.Errorf("unknown mode %q", mode)
	}
}

// compileToSO compiles a C source file to a shared object in outDir.
// extraIncludes provides additional -I paths (e.g. for GMI API headers).
// soName overrides the output .so base name (without extension); if empty,
// it is derived from the cPath filename.
func compileToSO(cPath string, outDir string, soName string, extraIncludes []string) error {
	if soName == "" {
		soName = strings.TrimSuffix(filepath.Base(cPath), ".c")
	}
	soPath := filepath.Join(outDir, soName+".so")

	// Ensure output directory exists
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	// The compiler command may carry arguments (e.g. "gcc -m32").
	cc := strings.Fields(resolveCC())

	args := append([]string(nil), cc[1:]...)
	args = append(args,
		"-I"+config.EMC2CmodIncludeDir,
		"-I"+filepath.Join(config.EMC2Home, "include"),
	)
	args = append(args, extraIncludes...)
	args = append(args,
		"-fPIC", "-Os", "-Wall",
		// Fortify 3 to match the rest of the build (src/Makefile DEBUG) and
		// the Ubuntu CI runners' builtin default; -U first because Ubuntu's
		// gcc predefines it.
		"-U_FORTIFY_SOURCE", "-D_FORTIFY_SOURCE=3",
		"-shared",
		"-o", soPath,
		cPath,
		"-lm",
	)

	cmd := exec.Command(cc[0], args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("compiling %s: %w", soName, err)
	}

	return nil
}

// compileComp compiles a .comp file to a .so in the given output directory.
func compileComp(compPath string, pkg *ast.Package, outDir string) error {
	// Create temp file for generated C
	tmpFile, err := os.CreateTemp("", "modcompile-*.c")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpCPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpCPath) }()

	// Generate C code
	if err := cgen.Generate(tmpFile, pkg); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("generating C: %w", err)
	}
	// Flush before the compiler reads the temp file back.
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("writing generated C: %w", err)
	}

	// Collect -I paths for GMI APIs referenced (gmi_provide / gmi_consume).
	var gmiIncludes []string
	gmiAPIs := make(map[string]bool)
	for _, api := range pkg.Component.GMIProvide {
		gmiAPIs[api] = true
	}
	for _, entry := range pkg.Component.GMIConsume {
		gmiAPIs[entry.API] = true
	}
	for api := range gmiAPIs {
		gmiIncludes = append(gmiIncludes, "-I"+filepath.Join(config.GomcDir(), "generated", "gmi", api))
	}

	// Add the .comp's own directory so a relative #include "local.h" resolves:
	// the generated C is compiled from a temp dir, so without this the comp's
	// source directory is not on the include path.
	if d := filepath.Dir(compPath); d != "" {
		gmiIncludes = append(gmiIncludes, "-I"+d)
	}

	return compileCMod(tmpCPath, filepath.Dir(compPath), outDir, pkg.Component.Name, gmiIncludes)
}

// compileCMod runs the C compiler over a cmod source and puts the .so in
// outDir, dropping privilege first where the layout calls for it.
//
// One decision in one place, so that .comp and .c cannot end up on different
// sides of it. The condition is "am I root, on a layout that has an
// unprivileged identity to be instead" — not "am I writing to the cmod
// directory": `sudo modcompile --compile` runs the compiler over the same
// source with the same privileges and deserves the same treatment. A
// run-in-place tree has no second identity, and an unprivileged caller is
// already the answer, so both compile straight through.
func compileCMod(cPath, srcDir, outDir, soName string, extraIncludes []string) error {
	if soName == "" {
		soName = strings.TrimSuffix(filepath.Base(cPath), ".c")
	}
	if config.LocalCModDir() == "" || os.Geteuid() != 0 {
		return compileToSO(cPath, outDir, soName, extraIncludes)
	}
	return compileCModStaged(cPath, srcDir, outDir, soName, extraIncludes)
}

// compileCFile compiles a hand-written cmod .c file directly to a .so.
func compileCFile(cPath string, outDir string) error {
	absCPath, err := filepath.Abs(cPath)
	if err != nil {
		return err
	}
	// Add the .c's own directory so a relative #include "local.h" resolves.
	dir := filepath.Dir(absCPath)
	return compileCMod(absCPath, dir, outDir, "", []string{"-I" + dir})
}

// cgoFlags returns the include and link flags a build of the gomc Go packages
// needs, for whichever layout this modcompile was built for.
//
// The gomc packages declare their C includes relative to ${SRCDIR}, which only
// resolves inside the source tree; from the installed tree at
// $(datadir)/linuxcnc/gomc those paths point at directories that do not exist,
// and gcc drops a missing -I without a word. Anything compiling those packages
// outside the source tree -- a rebuild of gomc-server, or a third-party Go
// module importing pkg/hal -- has to be handed the real include directory.
//
// The include half belongs in CGO_CPPFLAGS, not CGO_CFLAGS: several gomc
// packages carry C++ translation units (the interpreter shim above all), and
// CGO_CFLAGS never reaches the C++ compiler. Passing it only as CGO_CFLAGS
// left interp_shim.cc unable to find config.h while the C sources beside it
// compiled perfectly.
//
// One function so the callers cannot drift: the rebuild puts these in the
// build environment, printMakeInc hands them to external Makefiles.
func cgoFlags() (cflags, ldflags string) {
	libDir := filepath.Join(config.EMC2Home, "lib")
	if config.RunInPlace == "yes" {
		srcDir := filepath.Join(config.EMC2Home, "src")
		cflags = fmt.Sprintf("-I%s -I%s/hal -I%s/rtapi -I%s/../include",
			srcDir, srcDir, srcDir, srcDir)
	} else {
		// Two roots. The first is where the C headers are installed. The
		// second is the directory the gomc tree sits in, because a few
		// headers spell their includes from the source root inwards --
		// emc/rs274ngc/canon_interface.hh asks for
		// "gomc/generated/gmi/canon/canon_api.h" -- and that resolves only
		// against a directory that has a "gomc" in it. The run-in-place
		// branch above gets the same thing from -I<src>.
		cflags = "-I" + filepath.Join(config.EMC2Home, "include", "linuxcnc") +
			" -I" + filepath.Dir(config.EMC2GomcDir)
	}
	return cflags, fmt.Sprintf("-L%s -Wl,-rpath,%s", libDir, libDir)
}

// printMakeInc outputs a Makefile snippet for external projects.
// Each variable is wrapped in $(eval) so $(shell) newline→space conversion works.
func printMakeInc() {
	cc := resolveCC()

	libDir := filepath.Join(config.EMC2Home, "lib")
	cgoC, cgoLD := cgoFlags()

	// Each line wrapped in $(eval ...) because $(shell) converts newlines to spaces.
	// The outer $(eval $(shell ...)) then evaluates each inner $(eval) properly.
	fmt.Printf(`$(eval GOMC_CC := %s) $(eval GOMC_CFLAGS := -I%s %s) $(eval GOMC_LDFLAGS := %s) $(eval GOMC_CMOD_DIR := %s) $(eval GOMC_INCLUDE_DIR := %s) $(eval GOMC_DIR := %s) $(eval GOMC_GO := %s) $(eval GOMC_LIB_DIR := %s) $(eval GOMC_CGO_CFLAGS := %s) $(eval GOMC_CGO_CPPFLAGS := %s) $(eval GOMC_CGO_LDFLAGS := %s)`,
		cc,
		config.EMC2CmodIncludeDir, defaultCFlags,
		defaultLDFlags,
		cmodInstallDir(),
		config.EMC2CmodIncludeDir,
		config.GomcDir(),
		config.GoBinary,
		libDir,
		cgoC,
		// Same value under the name that also reaches a C++ translation unit.
		// A project importing pkg/hal needs it as soon as it has one.
		cgoC,
		cgoLD,
	)
}

// regenerate writes imports_generated.go from the registry.
func regenerate(reg *pkgreg.Registry) {
	serverDir := config.BuildTreeDir()

	if err := reg.GenerateImports(serverDir); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile: generating imports: %v\n", err)
		os.Exit(1)
	}
}

// buildServerInPlace compiles the gomc-server binary from serverDir to
// outPath, with whatever privileges the caller has.
//
// On a packaged system the caller is the unprivileged build phase and both
// paths belong to the build identity; in a run-in-place or build tree it is
// the developer, compiling their own sources. Nothing here is privileged, and
// nothing here may become privileged: this is the phase that runs cgo and the
// C compiler over module-supplied source.
func buildServerInPlace(serverDir, outPath string) {
	gobin := config.GoBinary
	if gobin == "" {
		gobin = "go"
	}

	// Build ldflags to inject compile-time config into the new binary.
	// modcompile already has these values baked in, so we propagate them.
	pkg := "github.com/sittner/linuxcnc/src/gomc/internal/config"
	ldflags := fmt.Sprintf(
		"-X '%s.EMC2Home=%s' "+
			"-X '%s.EMC2BinDir=%s' "+
			"-X '%s.EMC2TclDir=%s' "+
			"-X '%s.EMC2HelpDir=%s' "+
			"-X '%s.EMC2RtlibDir=%s' "+
			"-X '%s.EMC2CmodDir=%s' "+
			"-X '%s.EMC2CmodIncludeDir=%s' "+
			"-X '%s.EMC2GomcDir=%s' "+
			"-X '%s.EMC2StateDir=%s' "+
			"-X '%s.EMC2LibexecDir=%s' "+
			"-X '%s.CCompiler=%s' "+
			"-X '%s.CxxCompiler=%s' "+
			"-X '%s.GoBinary=%s' "+
			"-X '%s.EMC2ConfigPath=%s' "+
			"-X '%s.EMC2NCFilesDir=%s' "+
			"-X '%s.EMC2LangDir=%s' "+
			"-X '%s.EMC2ImageDir=%s' "+
			"-X '%s.EMC2TclLibDir=%s' "+
			"-X '%s.HalibDir=%s' "+
			"-X '%s.EMC2WebAppDir=%s' "+
			"-X '%s.EMC2Version=%s' "+
			"-X '%s.RunInPlace=%s' "+
			"-X '%s.ModExt=%s' "+
			"-X '%s.KernelVers=%s'",
		pkg, config.EMC2Home,
		pkg, config.EMC2BinDir,
		pkg, config.EMC2TclDir,
		pkg, config.EMC2HelpDir,
		pkg, config.EMC2RtlibDir,
		pkg, config.EMC2CmodDir,
		pkg, config.EMC2CmodIncludeDir,
		pkg, config.EMC2GomcDir,
		pkg, config.EMC2StateDir,
		pkg, config.EMC2LibexecDir,
		pkg, config.CCompiler,
		pkg, config.CxxCompiler,
		pkg, config.GoBinary,
		pkg, config.EMC2ConfigPath,
		pkg, config.EMC2NCFilesDir,
		pkg, config.EMC2LangDir,
		pkg, config.EMC2ImageDir,
		pkg, config.EMC2TclLibDir,
		pkg, config.HalibDir,
		pkg, config.EMC2WebAppDir,
		pkg, config.EMC2Version,
		pkg, config.RunInPlace,
		pkg, config.ModExt,
		pkg, config.KernelVers,
	)

	// -mod=mod: the build tree is a copy, and the copy is allowed to update
	// its own go.mod/go.sum. The shared tree it was copied from stays
	// root-owned and untouched.
	cmd := exec.Command(gobin, "build", "-mod=mod", "-ldflags", ldflags, "-o", outPath, "./cmd/gomc-server")
	cmd.Dir = serverDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// CGO needs to find headers and libraries; see cgoFlags.
	cgoC, cgoLD := cgoFlags()
	// The tree being compiled comes first, ahead of any installed copy: its
	// parent is what makes a "gomc/generated/..." include resolve, and it must
	// resolve to the sources this build is actually made of. Deriving it from
	// serverDir rather than naming a directory keeps that true wherever the
	// build tree happens to be — a source tree, or the build identity's
	// private copy of one.
	cgoC = "-I" + filepath.Dir(serverDir) + " " + cgoC
	// cgo takes the compiler from the environment (default gcc); pass the
	// configured toolchain through so the rebuild matches the original build.
	// Appending to os.Environ() is right in both callers: in the unprivileged
	// build phase that environment is the deliberately minimal one runBuildPhase
	// handed this process, not whatever the administrator's shell carried.
	cmd.Env = append(os.Environ(),
		// CPPFLAGS reaches the C++ translation units too; see cgoFlags.
		"CGO_CPPFLAGS="+cgoC,
		"CGO_CFLAGS="+cgoC,
		"CGO_LDFLAGS="+cgoLD,
		"CC="+resolveCC(),
		"CXX="+resolveCXX(),
	)

	fmt.Fprintf(os.Stderr, "Building gomc-server...\n")

	// Save file capabilities before rebuild (setcap is lost when binary is
	// replaced).  Only meaningful when this call is the one that installs the
	// binary — on a packaged system installServer does the save/restore around
	// its own replacement, and outPath here is a staging path with no
	// capabilities to lose.
	oldCaps := getFileCaps(outPath)

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile: building gomc-server: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "gomc-server built successfully: %s\n", outPath)

	// Reapply file capabilities if they were set before.
	if oldCaps != "" {
		applyFileCaps(outPath, oldCaps)
	}
}

// getFileCaps returns the capability string for a file, or "" if none set.
//
// The symlink resolution is the whole point. getcap lstat()s its argument and
// silently skips anything that is not a regular file — a symlink produces no
// output and exit status zero, indistinguishable from "this file has no
// capabilities". On a packaged system that is exactly the case that matters:
// before the first local rebuild, $(bindir)/gomc-server is a symlink chain
// onto the package's binary, and reading it directly reported no capabilities
// for a file that had ten. The rebuilt server then came out unable to do
// realtime at all, saying only that there had been nothing to carry over.
func getFileCaps(path string) string {
	resolved := path
	if r, err := filepath.EvalSymlinks(path); err == nil {
		resolved = r
	}

	// Nothing to read from a file that is not there yet — the staging path a
	// build writes to, most often. Not a condition worth a warning.
	if _, err := os.Stat(resolved); err != nil {
		return ""
	}

	out, err := exec.Command("getcap", resolved).Output()
	if err != nil {
		// Distinct from "no capabilities set", which is getcap exiting zero
		// with nothing to say. This is getcap missing from PATH or failing on
		// a file that exists, and silently treating it as "none" is how a
		// server ends up installed without the privileges it needs.
		fmt.Fprintf(os.Stderr,
			"modcompile: warning: could not read the file capabilities of %s: %v\n", resolved, err)
		return ""
	}
	if len(out) == 0 {
		return ""
	}
	// getcap output: "/path/to/bin cap_net_raw,cap_sys_nice=eip"
	s := strings.TrimSpace(string(out))
	if idx := strings.Index(s, " = "); idx >= 0 {
		return s[idx+3:]
	}
	if idx := strings.Index(s, " "); idx >= 0 {
		return s[idx+1:]
	}
	return ""
}

// applyFileCaps sets file capabilities on a binary, escalating through sudo
// only when this process is not already root.
//
// Setting them needs CAP_SETFCAP, which is an independent reason the install
// phase stays privileged even though the build phase does not.
//
// The sudo branch is reachable from exactly one place, and it is worth being
// precise about which, because "modcompile shells out to sudo" reads alarming
// next to a privilege split: a run-in-place tree, where the developer rebuilds
// their own ../bin/gomc-server and `make setuid` had granted it capabilities
// that replacing the file drops. Nothing on the packaged path can reach it.
// installServer is already root and calls setcap directly, and the
// unprivileged build phase writes a staging file that never had capabilities
// to begin with — setting them needs CAP_SETFCAP — so getFileCaps returns ""
// there and this is not called at all.
func applyFileCaps(path, caps string) {
	fmt.Fprintf(os.Stderr, "Reapplying file capabilities (%s)...\n", caps)

	var cmd *exec.Cmd
	if os.Geteuid() == 0 {
		cmd = exec.Command("setcap", caps, path)
	} else {
		cmd = exec.Command("sudo", "setcap", caps, path)
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile: warning: failed to restore capabilities: %v\n", err)
		fmt.Fprintf(os.Stderr, "  Run manually: sudo setcap '%s' %s\n", caps, path)
	}
}

// cmdList lists all packages that would be compiled into gomc-server.
//
// The internal and GMI packages come from the pristine sources, which is what
// a rebuild would start from; the external ones come from the registry, which
// is what a rebuild would add. Reading the derived build tree instead would
// report the last build rather than the next one, and would report nothing at
// all on a system that has never rebuilt.
func cmdList() {
	gomcDir := config.GomcDir()
	confIn := filepath.Join(gomcDir, "packages.conf")
	enabledFlags := pkgreg.ParseBuildFlags(config.BuildFlags)

	reg, err := pkgreg.ReadConfIn(confIn, enabledFlags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "modcompile list: %v\n", err)
		os.Exit(1)
	}
	for _, e := range pkgreg.DiscoverGMI(gomcDir) {
		reg.Add(e)
	}
	if config.DerivedBuild() {
		mods, err := registeredModules()
		if err != nil {
			fmt.Fprintf(os.Stderr, "modcompile list: %v\n", err)
			os.Exit(1)
		}
		for _, name := range mods {
			reg.Add(pkgreg.Entry{Type: pkgreg.TypeGomod, ImportPath: "external/" + name})
		}
	} else {
		for _, e := range pkgreg.DiscoverExternal(gomcDir) {
			reg.Add(e)
		}
	}
	for _, e := range reg.Entries {
		fmt.Printf("%-8s %s\n", e.Type, e.ImportPath)
	}
}

// cmdRebuild regenerates all derived files and rebuilds the server.
//
// rebuildServer is the whole command on a packaged system: it owns the
// privilege split, and regeneration happens inside its root phase, against the
// derived tree, after that tree has been refreshed from the pristine sources.
// Doing it here as well would regenerate a tree that is about to be
// overwritten.
func cmdRebuild() {
	if !config.DerivedBuild() {
		cmdRegenerateGomod()
		cmdRegenerateImports()
	}
	rebuildServer()
}

// cmdRegenerateImports builds a complete Registry by:
//  1. Reading packages.conf and filtering by compiled-in BuildFlags
//  2. Auto-discovering GMI packages in generated/gmi/ and external/*/gmi/
//  3. Auto-discovering external Go modules in external/
//
// Then generates imports_generated.go.  No intermediate packages.conf needed.
//
// Operates on the build tree, which is the gomc source tree itself in an
// in-place layout and the derived tree under the state directory on a packaged
// system.  Either way it is the tree the compiler is about to read.
func cmdRegenerateImports() {
	gomcDir := config.BuildTreeDir()
	confIn := filepath.Join(gomcDir, "packages.conf")

	// Parse build flags from compiled-in config.
	enabledFlags := pkgreg.ParseBuildFlags(config.BuildFlags)

	// 1. Internal gomods from packages.conf (filtered).
	reg, err := pkgreg.ReadConfIn(confIn, enabledFlags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "modcompile: reading packages.conf: %v\n", err)
		hintGomcDir(gomcDir)
		os.Exit(1)
	}

	// 2. Auto-discover GMI packages.
	for _, e := range pkgreg.DiscoverGMI(gomcDir) {
		reg.Add(e)
	}

	// 3. Auto-discover external Go modules.
	for _, e := range pkgreg.DiscoverExternal(gomcDir) {
		reg.Add(e)
	}

	regenerate(reg)
}

// cmdRegenerateGomod merges go.mod.in with go.deps files from external modules
// to produce go.mod.  Only writes if content changed.
func cmdRegenerateGomod() {
	gomcDir := config.BuildTreeDir()
	goModIn := filepath.Join(gomcDir, "go.mod.in")

	base, err := os.ReadFile(goModIn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "modcompile regenerate-gomod: reading go.mod.in: %v\n", err)
		os.Exit(1)
	}

	// Collect extra require lines from external/*/go.deps files.
	var extraRequires []string
	extDir := filepath.Join(gomcDir, "external")
	subs, _ := os.ReadDir(extDir)
	for _, sub := range subs {
		if !sub.IsDir() {
			continue
		}
		depsFile := filepath.Join(extDir, sub.Name(), "go.deps")
		data, err := os.ReadFile(depsFile)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "//") {
				extraRequires = append(extraRequires, "\t"+line)
			}
		}
	}

	var result []byte
	if len(extraRequires) == 0 {
		result = base
	} else {
		// Insert extra requires into the first require block.
		lines := strings.Split(string(base), "\n")
		var out []string
		inserted := false
		for _, line := range lines {
			out = append(out, line)
			// Insert after the opening "require (" line.
			if !inserted && strings.TrimSpace(line) == "require (" {
				out = append(out, extraRequires...)
				inserted = true
			}
		}
		if !inserted {
			// No require block found — append one.
			out = append(out, "", "require (")
			out = append(out, extraRequires...)
			out = append(out, ")")
		}
		result = []byte(strings.Join(out, "\n"))
	}

	goModPath := filepath.Join(gomcDir, "go.mod")
	existing, err := os.ReadFile(goModPath)
	if err == nil && string(existing) == string(result) {
		return // no change
	}
	if err := os.WriteFile(goModPath, result, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile regenerate-gomod: writing go.mod: %v\n", err)
		os.Exit(1)
	}
}

// moduleStoreDir returns the directory that records one copy of each
// registered external module's source.
//
// On a packaged system that is the registry under the state directory, which
// is the source of truth a build tree is derived from. In an in-place layout
// there is nothing to derive, and the store is the external/ directory inside
// the tree itself, exactly as it has always been.
func moduleStoreDir() string {
	if config.DerivedBuild() {
		return config.ModuleRegistryDir()
	}
	return filepath.Join(config.GomcDir(), "external")
}

// cmdAddGomod records an external Go package in the module store and rebuilds.
//
// Recording it is the trust decision: `sudo modcompile add-gomod ~/foo` says
// "I vouch for ~/foo", deliberately, naming its input, attributable to whoever
// ran it. Everything downstream — a build tree derived from the store, a
// compile that is not privileged — follows from that one step.
func cmdAddGomod(dir string, force bool) {
	if config.DerivedBuild() {
		requirePrivilege("add-gomod")
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "modcompile add-gomod: resolving path: %v\n", err)
		os.Exit(1)
	}

	// Validate the directory exists and has a go.mod.
	goModPath := filepath.Join(absDir, "go.mod")
	if _, err := os.Stat(goModPath); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile add-gomod: %s does not exist or has no go.mod\n", absDir)
		os.Exit(1)
	}

	// Package name = directory basename.
	name := filepath.Base(absDir)
	extDir := filepath.Join(moduleStoreDir(), name)
	originFile := filepath.Join(extDir, ".origin")

	// Remove stale external modules whose origin no longer exists.
	// This handles the case where a source directory was renamed/moved and
	// reinstalled under a new name — the old entry would otherwise cause a
	// "duplicate module registration" panic at runtime.
	removeStaleExternals(name)

	// Check for collision.
	if info, err := os.Stat(extDir); err == nil && info.IsDir() {
		originData, _ := os.ReadFile(originFile)
		existingOrigin := strings.TrimSpace(string(originData))
		if existingOrigin == absDir {
			// Same source — auto-force (reinstall).
			fmt.Fprintf(os.Stderr, "Reinstalling %s from %s\n", name, absDir)
		} else if !force {
			fmt.Fprintf(os.Stderr, "modcompile add-gomod: external/%s already installed", name)
			if existingOrigin != "" {
				fmt.Fprintf(os.Stderr, " from %s", existingOrigin)
			}
			fmt.Fprintf(os.Stderr, "\nUse --force to overwrite.\n")
			os.Exit(1)
		} else {
			fmt.Fprintf(os.Stderr, "Overwriting external/%s (--force)\n", name)
		}
	}

	// rsync --delete source into external/<name>/.
	if err := os.MkdirAll(extDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile add-gomod: creating directory: %v\n", err)
		os.Exit(1)
	}

	// Mirror source directory into external/<name>/, excluding build artifacts
	// and module boundary files (the copy becomes a sub-package of the gomc module).
	excludeSet := map[string]bool{
		".git": true, "go.work": true, "go.work.sum": true,
		"go.mod": true, "go.sum": true,
	}
	if err := dirMirror(absDir, extDir, excludeSet); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile add-gomod: copying files: %v\n", err)
		os.Exit(1)
	}

	// Extract third-party dependencies from the external module's go.mod
	// and write them to go.deps for the regenerate-gomod step.
	writeGoDeps(goModPath, extDir)

	// Write .origin to track where the source came from.
	if err := os.WriteFile(originFile, []byte(absDir+"\n"), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile add-gomod: writing .origin: %v\n", err)
		os.Exit(1)
	}

	// The recorded copy must be readable by the identity that will compile it.
	// A module sitting at 0600 in someone's home copies across root-owned and
	// unreadable otherwise, and the build fails on a file the administrator
	// can see perfectly well.
	if config.DerivedBuild() {
		if err := normalizeModes(extDir); err != nil {
			fmt.Fprintf(os.Stderr, "modcompile add-gomod: setting permissions on the recorded copy: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Fprintf(os.Stderr, "Recorded %s → %s\n", absDir, extDir)

	cmdRebuild()
}

// removeStaleExternals removes external module directories whose .origin path
// no longer exists on disk. This prevents "duplicate module registration" panics
// when a source directory is moved/renamed and reinstalled under a new basename.
func removeStaleExternals(skipName string) {
	extBase := moduleStoreDir()
	subs, err := os.ReadDir(extBase)
	if err != nil {
		return
	}
	for _, sub := range subs {
		if !sub.IsDir() || sub.Name() == skipName {
			continue
		}
		originFile := filepath.Join(extBase, sub.Name(), ".origin")
		data, err := os.ReadFile(originFile)
		if err != nil {
			continue // no .origin → leave alone
		}
		origin := strings.TrimSpace(string(data))
		if origin == "" {
			continue
		}
		if _, err := os.Stat(origin); err == nil {
			continue // origin still exists → not stale
		}
		// Origin no longer exists — remove stale entry.
		staleDir := filepath.Join(extBase, sub.Name())
		fmt.Fprintf(os.Stderr, "Removing stale module %s (origin %s no longer exists)\n", sub.Name(), origin)
		_ = os.RemoveAll(staleDir)
	}
}

// writeGoDeps extracts require directives from an external module's go.mod
// and writes them to <extDir>/go.deps for use by regenerate-gomod.
func writeGoDeps(extGoModPath, extDir string) {
	gobin := config.GoBinary
	if gobin == "" {
		gobin = "go"
	}

	// Parse external go.mod.
	editCmd := exec.Command(gobin, "mod", "edit", "-json", extGoModPath)
	editCmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	out, err := editCmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "modcompile add-gomod: reading external go.mod: %v\n", err)
		os.Exit(1)
	}

	var modInfo struct {
		Require []struct {
			Path    string
			Version string
		}
		Replace []struct {
			Old struct{ Path string }
			New struct{ Path string }
		}
	}
	if err := json.Unmarshal(out, &modInfo); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile add-gomod: parsing external go.mod: %v\n", err)
		os.Exit(1)
	}

	// Collect local replace targets to skip (stub dirs, etc.)
	localReplaces := make(map[string]bool)
	for _, r := range modInfo.Replace {
		if strings.HasPrefix(r.New.Path, ".") || strings.HasPrefix(r.New.Path, "/") {
			localReplaces[r.Old.Path] = true
		}
	}

	// Build go.deps file content.
	var deps []string
	for _, req := range modInfo.Require {
		if localReplaces[req.Path] {
			continue
		}
		if strings.HasPrefix(req.Path, "github.com/sittner/linuxcnc/") {
			continue
		}
		deps = append(deps, req.Path+" "+req.Version)
	}

	depsPath := filepath.Join(extDir, "go.deps")
	if len(deps) == 0 {
		// Remove stale go.deps if no deps needed.
		_ = os.Remove(depsPath)
		return
	}

	content := "# Third-party dependencies for this external module.\n" +
		"# Generated by modcompile add-gomod. Do not edit.\n" +
		strings.Join(deps, "\n") + "\n"
	if err := os.WriteFile(depsPath, []byte(content), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile add-gomod: writing go.deps: %v\n", err)
		os.Exit(1)
	}
}

// cmdRmGomod removes an external Go package from the module store and
// rebuilds. The build tree's copy goes with the next rebuild, which derives
// that tree afresh from what the store still holds.
func cmdRmGomod(name string) {
	if config.DerivedBuild() {
		requirePrivilege("rm-gomod")
	}

	extDir := moduleStoreDir()

	// Accept the "external/<name>" form the listing prints as well as the
	// bare name; anything else is a plain name under the store.
	targetDir := filepath.Join(extDir, strings.TrimPrefix(name, "external/"))

	// A module name identifies one directory directly inside the store. The
	// command runs as root and ends in RemoveAll, so a name that resolves
	// anywhere else — "../.." and friends — is refused rather than cleaned
	// into something plausible.
	if filepath.Dir(targetDir) != filepath.Clean(extDir) {
		fmt.Fprintf(os.Stderr, "modcompile rm-gomod: %q is not a module name: it resolves to %s, outside %s\n",
			name, targetDir, extDir)
		os.Exit(1)
	}

	if _, err := os.Stat(targetDir); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile rm-gomod: %s is not registered (looked in %s)\n", name, extDir)
		os.Exit(1)
	}

	if err := os.RemoveAll(targetDir); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile rm-gomod: removing %s: %v\n", targetDir, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Deleted %s\n", targetDir)

	cmdRebuild()
}

// cmdAddGmi adds a GMI package to the registry idempotently.
// importPath is relative to the gomc module, e.g. "generated/gmi/axisui".
// cmdAddGmi is a no-op retained for backward compatibility.
// GMI packages are now auto-discovered from generated/gmi/ and external/*/gmi/
// during regenerate-imports.  The codegen Submakefile still calls this, but it
// does nothing.
func cmdAddGmi(importPath string) {
}

// cmdRmGmi is a no-op retained for backward compatibility.
// GMI packages are auto-discovered; removing the generated directory is sufficient.
func cmdRmGmi(importPath string) {
}

// goModTidy runs "go mod tidy" in the build tree to clean up unused
// dependencies (e.g. after removing an external module).
func goModTidy() { goModTidyIn(config.BuildTreeDir()) }

// goModTidyIn is goModTidy against a named directory.
//
// Dependency resolution belongs to the unprivileged phase: it reaches the
// network, populates a module cache and rewrites go.mod and go.sum, none of
// which root should be doing on a shared tree. On a packaged system the
// directory named here is therefore the build identity's own scratch copy.
func goModTidyIn(dir string) {
	gobin := config.GoBinary
	if gobin == "" {
		gobin = "go"
	}
	cmd := exec.Command(gobin, "mod", "tidy")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile: go mod tidy warning: %v\n", err)
	}
}

// ---------------------------------------------------------------------------
// Directory mirror (pure Go, replaces rsync -a --delete)
// ---------------------------------------------------------------------------

// dirMirror copies srcDir into dstDir, mirroring the contents exactly.
// Files in dstDir that don't exist in srcDir are deleted (except .origin).
// Top-level entries whose name is in exclude are skipped during copy.
func dirMirror(srcDir, dstDir string, exclude map[string]bool) error {
	// Phase 1: copy / update files from src → dst.
	srcSet := make(map[string]bool) // relative paths present in source
	err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(srcDir, path)
		if rel == "." {
			return nil
		}

		// Skip excluded top-level entries.
		topLevel := strings.SplitN(rel, string(filepath.Separator), 2)[0]
		if exclude[topLevel] {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		srcSet[rel] = true
		dst := filepath.Join(dstDir, rel)

		if info.IsDir() {
			return os.MkdirAll(dst, info.Mode()|0700)
		}

		// Copy the file unless the destination already holds exactly it.
		//
		// Content, not size-and-modtime. What this mirror produces is
		// compiled, so a file wrongly judged up to date is not a stale
		// timestamp — it is the previous release's source built into the
		// running server, with nothing to show for it. Size alone misses a
		// same-length edit; adding modtime misses one made within a
		// filesystem tick, and an upgrade can deliver both. Reading a few
		// megabytes of Go source is not measurable next to the build it
		// precedes.
		same, err := sameContent(path, dst, info.Size())
		if err != nil {
			return err
		}
		if same {
			return nil
		}

		return copyFile(path, dst, info.Mode())
	})
	if err != nil {
		return fmt.Errorf("copying: %w", err)
	}

	// Phase 2: delete files in dst that are not in src (except .origin).
	return filepath.Walk(dstDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dstDir, path)
		if rel == "." || rel == ".origin" {
			return nil
		}
		if !srcSet[rel] {
			if info.IsDir() {
				_ = os.RemoveAll(path)
				return filepath.SkipDir
			}
			_ = os.Remove(path)
		}
		return nil
	})
}

// sameContent reports whether dst is a regular file holding exactly the srcSize
// bytes of src. A missing or differently sized destination answers false
// without reading anything.
func sameContent(src, dst string, srcSize int64) (bool, error) {
	dstInfo, err := os.Lstat(dst)
	if err != nil || !dstInfo.Mode().IsRegular() || dstInfo.Size() != srcSize {
		return false, nil
	}

	a, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer func() { _ = a.Close() }()

	b, err := os.Open(dst)
	if err != nil {
		return false, nil // unreadable destination: copy over it
	}
	defer func() { _ = b.Close() }()

	const chunk = 64 * 1024
	bufA := make([]byte, chunk)
	bufB := make([]byte, chunk)
	for {
		nA, errA := io.ReadFull(a, bufA)
		nB, errB := io.ReadFull(b, bufB)
		if nA != nB || !bytes.Equal(bufA[:nA], bufB[:nB]) {
			return false, nil
		}
		if errA == io.EOF || errA == io.ErrUnexpectedEOF {
			// Sizes matched going in, so both streams end together.
			return true, nil
		}
		if errA != nil {
			return false, errA
		}
		if errB != nil {
			return false, errB
		}
	}
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	// Close checks the flush: a failed close on a written file loses data.
	return out.Close()
}

// ---------------------------------------------------------------------------
// GMI code generation (modcompile gmi)
// ---------------------------------------------------------------------------

const gmiUsageText = `modcompile gmi: Compile GMI interface definitions

Usage:
    modcompile gmi [options] file.gmi...

Options:
    --help              Show this help message
    --parse             Parse only — print AST as JSON
    --server-c          Generate C server header only (types, callback typedefs)
    --server-meta       Generate Go META dispatch (cgo types, converters, dispatch, init)
    --server-go         Generate Go provider interface + cbridge
    --client-c          Generate C REST client (header + source). Supports a
                        subset of the type system: primitive scalars, []string,
                        and one level of nested struct. Narrow scalars (u8/i16/
                        f32/...), enum fields, non-string slices, slice-of-struct,
                        and deeper nesting are rejected at generate time (build
                        error, never a silently-broken client) — use --client-go/
                        --client-python/--client-ts for APIs that need them.
    --client-go         Generate Go REST client
    --client-python     Generate Python REST client
    --client-ts         Generate TypeScript REST client
    --client-ts-ws      Generate TypeScript WebSocket watch client
    --stream-server-c   Generate C header for stream_server blocks
    --stream-server-go  Generate Go bridge for stream_server blocks
    -o PATH             Output file or directory
`

type gmiMode int

const (
	gmiModeParse gmiMode = iota
	gmiModeServerC
	gmiModeServerMeta
	gmiModeClientC
	gmiModeServerGo
	gmiModeClientGo
	gmiModeClientPython
	gmiModeClientPythonWS
	gmiModeClientTS
	gmiModeClientTSWS
	gmiModeClientCgo
	gmiModeStreamServerC
	gmiModeStreamServerGo
)

func cmdGMI(args []string) {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, gmiUsageText)
		os.Exit(1)
	}

	var m gmiMode
	var outputPath string
	var files []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--help", "-h":
			fmt.Print(gmiUsageText)
			os.Exit(0)
		case "--parse":
			m = gmiModeParse
		case "--server-c":
			m = gmiModeServerC
		case "--server-meta":
			m = gmiModeServerMeta
		case "--client-c":
			m = gmiModeClientC
		case "--server-go":
			m = gmiModeServerGo
		case "--client-go":
			m = gmiModeClientGo
		case "--client-python":
			m = gmiModeClientPython
		case "--client-python-ws":
			m = gmiModeClientPythonWS
		case "--client-ts":
			m = gmiModeClientTS
		case "--client-ts-ws":
			m = gmiModeClientTSWS
		case "--client-cgo":
			m = gmiModeClientCgo
		case "--stream-server-c":
			m = gmiModeStreamServerC
		case "--stream-server-go":
			m = gmiModeStreamServerGo
		case "-o":
			if i+1 < len(args) {
				i++
				outputPath = args[i]
			}
		default:
			if len(arg) > 0 && arg[0] != '-' {
				files = append(files, arg)
			}
		}
	}

	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "modcompile gmi: no input files")
		os.Exit(1)
	}

	for _, file := range files {
		if err := processGMIFile(file, m, outputPath); err != nil {
			fmt.Fprintf(os.Stderr, "modcompile gmi: %v\n", err)
			os.Exit(1)
		}
	}
}

func processGMIFile(file string, m gmiMode, outputPath string) error {
	src, err := os.ReadFile(file)
	if err != nil {
		return err
	}

	api, errors := gmiparser.Parse(file, string(src))
	if len(errors) > 0 {
		for _, e := range errors {
			fmt.Fprintln(os.Stderr, e)
		}
		return fmt.Errorf("parse failed")
	}

	if api.License == "" {
		return fmt.Errorf("%s: missing required @license annotation", file)
	}

	if errs := gmicheck.Validate(api); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, e)
		}
		return fmt.Errorf("%s: constraint validation failed", file)
	}

	switch m {
	case gmiModeParse:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(api)
	case gmiModeServerC:
		return gmiGenerateServerC(api, outputPath)
	case gmiModeServerMeta:
		return gmiGenerateServerMeta(api, outputPath)
	case gmiModeClientC:
		if !api.RestExport {
			return fmt.Errorf("%s: --client-c requires @rest_export true", file)
		}
		return gmiGenerateClientC(api, outputPath)
	case gmiModeServerGo:
		return gmiGenerateServerGo(api, outputPath)
	case gmiModeClientGo:
		if !api.RestExport {
			return fmt.Errorf("%s: --client-go requires @rest_export true", file)
		}
		return gmiGenerateClientGo(api, outputPath)
	case gmiModeClientCgo:
		return gmiGenerateClientCgo(api, outputPath)
	case gmiModeClientPython:
		if !api.RestExport {
			return fmt.Errorf("%s: --client-python requires @rest_export true", file)
		}
		return gmiGenerateClientPython(api, outputPath)
	case gmiModeClientPythonWS:
		if !gmicgen.HasWatchFuncs(api) {
			return fmt.Errorf("%s: --client-python-ws requires at least one @watch function", file)
		}
		return gmiGenerateClientPythonWS(api, outputPath)
	case gmiModeClientTS:
		if !api.RestExport {
			return fmt.Errorf("%s: --client-ts requires @rest_export true", file)
		}
		return gmiGenerateClientTS(api, outputPath)
	case gmiModeClientTSWS:
		if !gmicgen.HasWatchFuncs(api) {
			return fmt.Errorf("%s: --client-ts-ws requires at least one @watch function", file)
		}
		return gmiGenerateClientTSWS(api, outputPath)
	case gmiModeStreamServerC:
		if !gmicgen.HasStreamServers(api) {
			return fmt.Errorf("%s: --stream-server-c requires at least one stream_server block", file)
		}
		return gmiGenerateStreamServerC(api, outputPath)
	case gmiModeStreamServerGo:
		if !gmicgen.HasStreamServers(api) {
			return fmt.Errorf("%s: --stream-server-go requires at least one stream_server block", file)
		}
		return gmiGenerateStreamServerGo(api, outputPath)
	}
	return nil
}

func gmiGenerateServerC(api *gmiast.API, outputPath string) error {
	if outputPath == "" {
		outputPath = api.Name + "_api.h"
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	if err := gmicgen.GenerateServerHeader(f, api); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "generated %s\n", outputPath)
	return nil
}

func gmiGenerateServerMeta(api *gmiast.API, outputPath string) error {
	if outputPath == "" {
		outputPath = api.Name + "_cgo.go"
	}

	dir := filepath.Dir(outputPath)
	pkgName := api.Name
	if dir != "." && dir != "" {
		pkgName = filepath.Base(dir)
	}

	headerFile := api.Name + "_api.h"

	// Generate Go cgo dispatch file (types, converters, dispatch, META, init).
	gf, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	if err := gmicgen.GenerateDispatchC(gf, api, pkgName, headerFile); err != nil {
		_ = gf.Close()
		return err
	}
	if err := gf.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "generated %s\n", outputPath)

	// Generate publish ring header + Go drain if the API has @publish functions.
	pubPath := filepath.Join(dir, api.Name+"_pub.h")
	pf, err := os.Create(pubPath)
	if err != nil {
		return err
	}
	hasPub, err := gmicgen.GeneratePublishHeader(pf, api)
	if err != nil {
		_ = pf.Close()
		return err
	}
	if !hasPub {
		// No publish functions — remove empty file.
		_ = pf.Close()
		_ = os.Remove(pubPath)
	} else {
		if err := pf.Close(); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "generated %s\n", pubPath)

		pubGoPath := filepath.Join(dir, api.Name+"_pub.go")
		pgf, err := os.Create(pubGoPath)
		if err != nil {
			return err
		}
		if _, err := gmicgen.GeneratePublishGo(pgf, api, pkgName); err != nil {
			_ = pgf.Close()
			return err
		}
		if err := pgf.Close(); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "generated %s\n", pubGoPath)

		// Generate drain hook (auto-starts drain on ring registration).
		drainHookPath := filepath.Join(dir, api.Name+"_drain_hook.go")
		dhf, err := os.Create(drainHookPath)
		if err != nil {
			return err
		}
		if _, err := gmicgen.GeneratePublishDrainHook(dhf, api, pkgName); err != nil {
			_ = dhf.Close()
			return err
		}
		if err := dhf.Close(); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "generated %s\n", drainHookPath)
	}

	// Generate push converter if the API has @watch functions returning structs.
	pushPath := filepath.Join(dir, api.Name+"_push.go")
	pushF, err := os.Create(pushPath)
	if err != nil {
		return err
	}
	hasPush, err := gmicgen.GeneratePushConvert(pushF, api, pkgName)
	if err != nil {
		_ = pushF.Close()
		return err
	}
	if !hasPush {
		_ = pushF.Close()
		_ = os.Remove(pushPath)
	} else {
		if err := pushF.Close(); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "generated %s\n", pushPath)
	}

	return nil
}

func gmiGenerateClientC(api *gmiast.API, outputPath string) error {
	var baseName string
	if outputPath == "" {
		baseName = api.Name + "_client"
	} else {
		baseName = strings.TrimSuffix(outputPath, filepath.Ext(outputPath))
	}

	headerPath := baseName + ".h"
	sourcePath := baseName + ".c"

	hf, err := os.Create(headerPath)
	if err != nil {
		return err
	}
	if err := gmicgen.GenerateClientHeader(hf, api); err != nil {
		_ = hf.Close()
		return err
	}
	if err := hf.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "generated %s\n", headerPath)

	sf, err := os.Create(sourcePath)
	if err != nil {
		return err
	}
	if err := gmicgen.GenerateClientSource(sf, api); err != nil {
		_ = sf.Close()
		return err
	}
	if err := sf.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "generated %s\n", sourcePath)

	return nil
}

func gmiGenerateServerGo(api *gmiast.API, outputPath string) error {
	if outputPath == "" {
		outputPath = api.Name + "_bridge.go"
	}

	pkgName := api.Name
	if dir := filepath.Dir(outputPath); dir != "." && dir != "" {
		pkgName = filepath.Base(dir)
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	if err := gmicgen.GenerateBridgeGo(f, api, pkgName); err != nil {
		_ = f.Close()
		return err
	}

	// Append Commands and WatchRegister functions (they reference the Callbacks interface above)
	if err := gmicgen.GenerateServerGoExtra(f, api, pkgName); err != nil {
		_ = f.Close()
		return err
	}

	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "generated %s\n", outputPath)
	return nil
}

func gmiGenerateClientGo(api *gmiast.API, outputPath string) error {
	if outputPath == "" {
		outputPath = api.Name + "_client.go"
	}

	pkgName := api.Name + "client"
	if dir := filepath.Dir(outputPath); dir != "." && dir != "" {
		pkgName = filepath.Base(dir)
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	if err := gmicgen.GenerateClientGo(f, api, pkgName); err != nil {
		_ = f.Close()
		return err
	}

	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "generated %s\n", outputPath)
	return nil
}

func gmiGenerateClientPython(api *gmiast.API, outputPath string) error {
	if outputPath == "" {
		outputPath = api.Name + "_client.py"
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	if err := gmicgen.GenerateClientPython(f, api); err != nil {
		_ = f.Close()
		return err
	}

	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "generated %s\n", outputPath)
	return nil
}

func gmiGenerateClientPythonWS(api *gmiast.API, outputPath string) error {
	if outputPath == "" {
		outputPath = api.Name + "_watch_client.py"
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	if err := gmicgen.GenerateClientPythonWS(f, api); err != nil {
		_ = f.Close()
		return err
	}

	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "generated %s\n", outputPath)
	return nil
}

func gmiGenerateClientTS(api *gmiast.API, outputPath string) error {
	if outputPath == "" {
		outputPath = api.Name + "_client.ts"
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	if err := gmicgen.GenerateClientTS(f, api); err != nil {
		_ = f.Close()
		return err
	}

	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "generated %s\n", outputPath)
	return nil
}

func gmiGenerateClientTSWS(api *gmiast.API, outputPath string) error {
	if outputPath == "" {
		outputPath = api.Name + "_watch_client.ts"
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	if err := gmicgen.GenerateClientTSWS(f, api); err != nil {
		_ = f.Close()
		return err
	}

	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "generated %s\n", outputPath)
	return nil
}

func gmiGenerateClientCgo(api *gmiast.API, outputPath string) error {
	if outputPath == "" {
		outputPath = api.Name + "_client_cgo.go"
	}

	pkgName := api.Name + "client"
	if dir := filepath.Dir(outputPath); dir != "." && dir != "" {
		pkgName = filepath.Base(dir)
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	if err := gmicgen.GenerateClientCgo(f, api, pkgName); err != nil {
		_ = f.Close()
		return err
	}

	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "generated %s\n", outputPath)
	return nil
}

func gmiGenerateStreamServerC(api *gmiast.API, outputPath string) error {
	if outputPath == "" {
		outputPath = api.Name + "_stream_api.h"
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	if _, err := gmicgen.GenerateStreamServerHeader(f, api); err != nil {
		_ = f.Close()
		return err
	}

	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "generated %s\n", outputPath)
	return nil
}

func gmiGenerateStreamServerGo(api *gmiast.API, outputPath string) error {
	if outputPath == "" {
		outputPath = api.Name + "_stream.go"
	}

	pkgName := api.Name
	if dir := filepath.Dir(outputPath); dir != "." && dir != "" {
		pkgName = filepath.Base(dir)
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	if _, err := gmicgen.GenerateStreamServerGo(f, api, pkgName); err != nil {
		_ = f.Close()
		return err
	}

	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "generated %s\n", outputPath)
	return nil
}

// hintGomcDir explains where modcompile looked for the gomc source tree and
// which knob moves it. The baked-in default is the INSTALLED location, so a
// build tree or a run-in-place tree that forgot to set $GOMC_DIR otherwise
// fails with a bare "no such file" naming a path the user never chose.
func hintGomcDir(gomcDir string) {
	fmt.Fprintf(os.Stderr, "modcompile: looked for the gomc source tree in %s\n", gomcDir)
	if os.Getenv(config.GomcDirEnv) == "" {
		fmt.Fprintf(os.Stderr,
			"modcompile: that is the installed location compiled into this binary; "+
				"set %s to the gomc source directory to override it "+
				"(run-in-place trees get it from scripts/rip-environment)\n",
			config.GomcDirEnv)
	} else {
		fmt.Fprintf(os.Stderr, "modcompile: taken from %s in the environment\n", config.GomcDirEnv)
	}
}
