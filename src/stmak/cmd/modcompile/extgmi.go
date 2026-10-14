// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// External GMI APIs: IDL files that live outside the stratuMAK tree, typically
// in a machine configuration next to the component that provides the API.
//
// The REST routing of an API is Go code generated from its IDL and compiled
// into stmakd, so an IDL from outside has to go the same way an external Go
// module does: `modcompile add-gmi` records it in the module store (the trust
// decision, as for add-gomod), and the rebuild generates its code and links
// it in. Each IDL is one store entry, gmi-<api>/gmi/<api>.gmi; regenerate puts
// the generated package next to it in gmi-<api>/gmi/<api>/, where
// pkgreg.DiscoverGMI already looks for the GMI packages of external modules.
//
// The generated C header is the one a cmod providing or consuming the API
// includes; gmiIncludeDir finds it there for --compile/--install.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/config"
)

// extGMIPrefix names the store entries add-gmi creates, keeping them apart
// from the external Go modules in the same store.
const extGMIPrefix = "gmi-"

// cmdAddGmi records external IDL files in the module store and rebuilds.
func cmdAddGmi(files []string, force bool) {
	if config.DerivedBuild() {
		requirePrivilege("add-gmi")
		acquireBuildLock("add-gmi")
	}

	for _, file := range files {
		if err := addGmi(file, force); err != nil {
			fmt.Fprintf(os.Stderr, "modcompile add-gmi: %v\n", err)
			os.Exit(1)
		}
	}

	cmdRebuild()
}

// addGmi validates one IDL and records it as the store entry gmi-<api>.
func addGmi(file string, force bool) error {
	absFile, err := filepath.Abs(file)
	if err != nil {
		return fmt.Errorf("resolving path: %v", err)
	}
	if !strings.HasSuffix(absFile, ".gmi") {
		return fmt.Errorf("%s is not a .gmi file", absFile)
	}

	// Validated before anything is recorded: a broken IDL found by the
	// rebuild would leave the store holding something no rebuild can get
	// past.
	api, err := loadGMI(absFile)
	if err != nil {
		return err
	}

	if builtin := builtinGMIDir(api.Name); builtin != "" {
		return fmt.Errorf("API %q is built into stratuMAK (%s); an external IDL cannot replace it",
			api.Name, builtin)
	}

	store := moduleStoreDir()
	name := extGMIPrefix + api.Name
	entryDir := filepath.Join(store, name)
	originFile := filepath.Join(entryDir, ".origin")

	if other := externalGMIProvider(store, api.Name, name); other != "" {
		return fmt.Errorf("API %q is already provided by the external module %s", api.Name, other)
	}

	removeStaleExternals(name)

	if info, err := os.Stat(entryDir); err == nil && info.IsDir() {
		originData, _ := os.ReadFile(originFile)
		existingOrigin := strings.TrimSpace(string(originData))
		switch {
		case existingOrigin == absFile:
			fmt.Fprintf(os.Stderr, "Reinstalling %s from %s\n", name, absFile)
		case !force:
			msg := fmt.Sprintf("API %q is already registered", api.Name)
			if existingOrigin != "" {
				msg += " from " + existingOrigin
			}
			return fmt.Errorf("%s\nUse --force to overwrite", msg)
		default:
			fmt.Fprintf(os.Stderr, "Overwriting %s (--force)\n", name)
		}
	}

	// The entry holds the IDL and nothing else; the generated package is
	// the rebuild's to write. Start from empty so a previous generation, or
	// an IDL recorded under another file name, cannot linger.
	if err := os.RemoveAll(entryDir); err != nil {
		return fmt.Errorf("clearing %s: %v", entryDir, err)
	}
	gmiDir := filepath.Join(entryDir, "gmi")
	if err := os.MkdirAll(gmiDir, 0755); err != nil {
		return fmt.Errorf("creating %s: %v", gmiDir, err)
	}
	if err := copyFile(absFile, filepath.Join(gmiDir, api.Name+".gmi"), 0644); err != nil {
		return fmt.Errorf("copying %s: %v", absFile, err)
	}
	if err := os.WriteFile(originFile, []byte(absFile+"\n"), 0644); err != nil {
		return fmt.Errorf("writing .origin: %v", err)
	}
	if config.DerivedBuild() {
		if err := normalizeModes(entryDir); err != nil {
			return fmt.Errorf("setting permissions on the recorded copy: %v", err)
		}
	}

	fmt.Fprintf(os.Stderr, "Recorded %s → %s\n", absFile, entryDir)
	return nil
}

// cmdRmGmi removes external IDLs by API name and rebuilds.
func cmdRmGmi(apis []string) {
	if config.DerivedBuild() {
		requirePrivilege("rm-gmi")
		acquireBuildLock("rm-gmi")
	}

	store := moduleStoreDir()
	for _, api := range apis {
		// Same containment rule as rm-gomod: the name must resolve to one
		// directory directly inside the store.
		target, err := rmGomodTarget(extGMIPrefix+strings.TrimPrefix(api, extGMIPrefix), store)
		if err != nil {
			fmt.Fprintf(os.Stderr, "modcompile rm-gmi: %v\n", err)
			os.Exit(1)
		}
		if _, err := os.Stat(target); err != nil {
			fmt.Fprintf(os.Stderr, "modcompile rm-gmi: API %s is not registered (looked in %s)\n", api, store)
			os.Exit(1)
		}
		if err := os.RemoveAll(target); err != nil {
			fmt.Fprintf(os.Stderr, "modcompile rm-gmi: removing %s: %v\n", target, err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Deleted %s\n", target)
	}

	cmdRebuild()
}

// builtinGMIDir returns the generated package directory of a built-in API, or
// "" if stratuMAK has no API of that name. Looked up in the pristine sources:
// the build tree also holds the external packages.
func builtinGMIDir(api string) string {
	dir := filepath.Join(config.StmakDir(), "generated", "gmi", api)
	if _, err := os.Stat(filepath.Join(dir, api+"_api.h")); err == nil {
		return dir
	}
	return ""
}

// externalGMIProvider returns the name of a store entry other than skip that
// already carries the API, as an IDL or as a generated package, or "".
func externalGMIProvider(store, api, skip string) string {
	subs, err := os.ReadDir(store)
	if err != nil {
		return ""
	}
	for _, sub := range subs {
		if !sub.IsDir() || sub.Name() == skip {
			continue
		}
		gmiDir := filepath.Join(store, sub.Name(), "gmi")
		if _, err := os.Stat(filepath.Join(gmiDir, api+".gmi")); err == nil {
			return sub.Name()
		}
		if _, err := os.Stat(filepath.Join(gmiDir, api)); err == nil {
			return sub.Name()
		}
	}
	return ""
}

// generateExternalGMI generates the server code for every IDL found at
// <tree>/external/*/gmi/*.gmi into the package directory next to it,
// <tree>/external/<module>/gmi/<api>/: the C header (--server-c) and the Go
// dispatch with the REST metadata (--server-meta). That is what the in-tree
// C-only APIs get from src/gmi/codegen/Submakefile.
//
// Runs as part of regenerate, before the GMI packages are discovered, so a
// rebuild always compiles code generated by this modcompile from the IDLs
// the store holds now.
func generateExternalGMI(tree string) error {
	extDir := filepath.Join(tree, "external")
	mods, err := os.ReadDir(extDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	seen := map[string]string{}
	for _, mod := range mods {
		if !mod.IsDir() {
			continue
		}
		gmiDir := filepath.Join(extDir, mod.Name(), "gmi")
		idls, _ := filepath.Glob(filepath.Join(gmiDir, "*.gmi"))
		sort.Strings(idls)
		for _, idl := range idls {
			api, err := loadGMI(idl)
			if err != nil {
				return fmt.Errorf("external module %s: %w", mod.Name(), err)
			}
			if prev, dup := seen[api.Name]; dup {
				return fmt.Errorf("API %q is provided by both external/%s and external/%s",
					api.Name, prev, mod.Name())
			}
			seen[api.Name] = mod.Name()
			if builtin := builtinGMIDir(api.Name); builtin != "" {
				return fmt.Errorf("external module %s: API %q is built into stratuMAK (%s)",
					mod.Name(), api.Name, builtin)
			}

			out := filepath.Join(gmiDir, api.Name)
			if err := os.MkdirAll(out, 0755); err != nil {
				return err
			}
			if err := gmiGenerateServerC(api, filepath.Join(out, api.Name+"_api.h")); err != nil {
				return fmt.Errorf("external module %s: %w", mod.Name(), err)
			}
			if err := gmiGenerateServerMeta(api, filepath.Join(out, api.Name+"_cgo.go")); err != nil {
				return fmt.Errorf("external module %s: %w", mod.Name(), err)
			}
		}
	}
	return nil
}

// gmiIncludeDir returns the directory holding <api>_api.h: the built-in
// package if there is one, else the package an external IDL was generated
// into by the last rebuild. "" if neither exists.
func gmiIncludeDir(api string) string {
	if dir := builtinGMIDir(api); dir != "" {
		return dir
	}
	matches, _ := filepath.Glob(filepath.Join(config.BuildTreeDir(), "external", "*", "gmi", api, api+"_api.h"))
	if len(matches) > 0 {
		return filepath.Dir(matches[0])
	}
	return ""
}

// allGMIIncludeDirs returns the directory of every API header there is,
// built-in and external. A hand-written .c declares nothing about the APIs it
// uses, so it is compiled with all of them on the include path.
func allGMIIncludeDirs() []string {
	var dirs []string
	builtin, _ := filepath.Glob(filepath.Join(config.StmakDir(), "generated", "gmi", "*", "*_api.h"))
	external, _ := filepath.Glob(filepath.Join(config.BuildTreeDir(), "external", "*", "gmi", "*", "*_api.h"))
	seen := map[string]bool{}
	for _, h := range append(builtin, external...) {
		d := filepath.Dir(h)
		if !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	return dirs
}
