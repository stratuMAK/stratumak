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
// An IDL an external Go module ships itself is that module's business: it
// comes with the package generated from it, and is left alone.
//
// The generated C header is the one a cmod providing or consuming the API
// includes; gmiIncludeDir finds it there for --compile/--install.

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/config"
	gmiast "github.com/stratuMAK/stratumak/src/stmak/internal/gmicompile/ast"
)

// extGMIPrefix names the store entries add-gmi creates, keeping them apart
// from the external Go modules in the same store.
const extGMIPrefix = "gmi-"

// gmiCandidate is one IDL add-gmi has checked and not yet recorded.
type gmiCandidate struct {
	file string // absolute path of the IDL
	api  *gmiast.API
	name string // its store entry, gmi-<api>
}

// cmdAddGmi records external IDL files in the module store and rebuilds.
func cmdAddGmi(files []string, force bool) {
	if config.DerivedBuild() {
		requirePrivilege("add-gmi")
		acquireBuildLock("add-gmi")
	}

	// Stale entries go first, so the checks below see the store as it will
	// be. Removing one is a change stmakd has yet to see.
	changed := removeStaleExternals("")

	// Every file is checked before any is recorded: a bad second file must
	// not leave the first in the store without the rebuild that links it.
	var cands []*gmiCandidate
	seen := map[string]string{}
	for _, file := range files {
		c, err := checkGmi(file, force)
		if err != nil {
			fmt.Fprintf(os.Stderr, "modcompile add-gmi: %v\n", err)
			os.Exit(1)
		}
		if prev, dup := seen[c.api.Name]; dup {
			fmt.Fprintf(os.Stderr, "modcompile add-gmi: API %q is declared by both %s and %s\n",
				c.api.Name, prev, c.file)
			os.Exit(1)
		}
		seen[c.api.Name] = c.file
		cands = append(cands, c)
	}
	for _, c := range cands {
		ch, err := recordGmi(c)
		if err != nil {
			fmt.Fprintf(os.Stderr, "modcompile add-gmi: %v\n", err)
			os.Exit(1)
		}
		changed = changed || ch
	}

	// An install script registers its IDL on every run; rebuilding stmakd
	// each time for nothing would make that a minutes-long no-op.
	if !changed {
		fmt.Fprintln(os.Stderr, "Nothing changed, stmakd not rebuilt (modcompile rebuild forces one)")
		return
	}
	cmdRebuild()
}

// addGmi checks and records one IDL. changed is false when the store already
// holds the same IDL from the same file and stmakd was built from it: then
// there is nothing to rebuild.
func addGmi(file string, force bool) (changed bool, err error) {
	c, err := checkGmi(file, force)
	if err != nil {
		return false, err
	}
	return recordGmi(c)
}

// checkGmi validates one IDL against itself and the store without touching
// either. A refusal here costs nothing; one found by the rebuild would leave
// the store holding something no rebuild can get past.
func checkGmi(file string, force bool) (*gmiCandidate, error) {
	absFile, err := filepath.Abs(file)
	if err != nil {
		return nil, fmt.Errorf("resolving path: %v", err)
	}
	if !strings.HasSuffix(absFile, ".gmi") {
		return nil, fmt.Errorf("%s is not a .gmi file", absFile)
	}

	api, err := loadGMI(absFile)
	if err != nil {
		return nil, err
	}
	if len(api.StreamServers) > 0 {
		// The rebuild generates --server-c and --server-meta only; a stream
		// bridge it did not generate would register nothing and nobody would
		// be told.
		return nil, fmt.Errorf("%s: API %q has stream_server blocks; external stream APIs are not supported yet",
			absFile, api.Name)
	}
	if builtin := builtinGMIDir(api.Name); builtin != "" {
		return nil, fmt.Errorf("API %q is built into stratuMAK (%s); an external IDL cannot replace it",
			api.Name, builtin)
	}

	store := moduleStoreDir()
	name := extGMIPrefix + api.Name
	if other := externalGMIProvider(store, api.Name, name); other != "" {
		return nil, fmt.Errorf("API %q is already provided by the external module %s", api.Name, other)
	}

	// Registered from another file: refused unless forced, or unless that
	// file is gone (then the entry is stale and recording replaces it).
	entryDir := filepath.Join(store, name)
	if info, err := os.Stat(entryDir); err == nil && info.IsDir() && !force {
		existing := readOrigin(entryDir)
		if existing != absFile && (existing == "" || fileExists(existing)) {
			msg := fmt.Sprintf("API %q is already registered", api.Name)
			if existing != "" {
				msg += " from " + existing
			}
			return nil, fmt.Errorf("%s\nUse --force to overwrite", msg)
		}
	}

	return &gmiCandidate{file: absFile, api: api, name: name}, nil
}

// recordGmi writes a checked IDL into the store as the entry gmi-<api>.
func recordGmi(c *gmiCandidate) (changed bool, err error) {
	store := moduleStoreDir()
	entryDir := filepath.Join(store, c.name)

	changed = removeStaleExternals(c.name)
	// The same file registered under another API name: its @api was
	// renamed, and the old entry would otherwise stay compiled in.
	if removeRenamedGMI(store, c.name, c.file) {
		changed = true
	}

	if info, err := os.Stat(entryDir); err == nil && info.IsDir() {
		existing := readOrigin(entryDir)
		switch {
		case existing == c.file && gmiUpToDate(entryDir, c.api.Name, c.file):
			fmt.Fprintf(os.Stderr, "%s is registered and unchanged\n", c.file)
			return changed, nil
		case existing == c.file:
			fmt.Fprintf(os.Stderr, "Reinstalling %s from %s\n", c.name, c.file)
		case existing != "" && !fileExists(existing):
			fmt.Fprintf(os.Stderr, "Replacing %s (origin %s no longer exists)\n", c.name, existing)
		default:
			fmt.Fprintf(os.Stderr, "Overwriting %s (--force)\n", c.name)
		}
	}

	// The entry holds the IDL and nothing else; the generated package is
	// the rebuild's to write. Start from empty so a previous generation, or
	// an IDL recorded under another file name, cannot linger.
	if err := os.RemoveAll(entryDir); err != nil {
		return false, fmt.Errorf("clearing %s: %v", entryDir, err)
	}
	gmiDir := filepath.Join(entryDir, "gmi")
	if err := os.MkdirAll(gmiDir, 0755); err != nil {
		return false, fmt.Errorf("creating %s: %v", gmiDir, err)
	}
	if err := copyFile(c.file, filepath.Join(gmiDir, c.api.Name+".gmi"), 0644); err != nil {
		return false, fmt.Errorf("copying %s: %v", c.file, err)
	}
	if err := os.WriteFile(filepath.Join(entryDir, ".origin"), []byte(c.file+"\n"), 0644); err != nil {
		return false, fmt.Errorf("writing .origin: %v", err)
	}
	if config.DerivedBuild() {
		if err := normalizeModes(entryDir); err != nil {
			return false, fmt.Errorf("setting permissions on the recorded copy: %v", err)
		}
	}

	fmt.Fprintf(os.Stderr, "Recorded %s → %s\n", c.file, entryDir)
	return true, nil
}

// readOrigin returns the trimmed .origin of a store entry, or "".
func readOrigin(entryDir string) string {
	data, _ := os.ReadFile(filepath.Join(entryDir, ".origin"))
	return strings.TrimSpace(string(data))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// removeRenamedGMI deletes the add-gmi entries other than keep whose origin
// is file, and reports whether it deleted any.
func removeRenamedGMI(store, keep, file string) bool {
	removed := false
	subs, err := os.ReadDir(store)
	if err != nil {
		return false
	}
	for _, sub := range subs {
		if !sub.IsDir() || sub.Name() == keep || !strings.HasPrefix(sub.Name(), extGMIPrefix) {
			continue
		}
		dir := filepath.Join(store, sub.Name())
		if readOrigin(dir) != file {
			continue
		}
		fmt.Fprintf(os.Stderr, "Removing %s: %s now declares another API\n", sub.Name(), file)
		if err := os.RemoveAll(dir); err == nil {
			removed = true
		}
	}
	return removed
}

// gmiUpToDate reports whether the store entry holds the same IDL as file, and
// stmakd was built after the package was generated from it. A rebuild that
// generated the package and then failed leaves the header newer than the
// binary: the next add-gmi must rebuild, not report nothing to do.
func gmiUpToDate(entryDir, api, file string) bool {
	recorded, err := os.ReadFile(filepath.Join(entryDir, "gmi", api+".gmi"))
	if err != nil {
		return false
	}
	current, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(recorded, current) {
		return false
	}
	dir := gmiIncludeDir(api)
	if dir == "" {
		return false
	}
	header, err := os.Stat(filepath.Join(dir, api+"_api.h"))
	if err != nil {
		return false
	}
	server, err := os.Stat(serverOutputPath())
	if err != nil {
		return false
	}
	return !server.ModTime().Before(header.ModTime())
}

// cmdRmGmi removes external IDLs by API name and rebuilds.
func cmdRmGmi(apis []string) {
	if config.DerivedBuild() {
		requirePrivilege("rm-gmi")
		acquireBuildLock("rm-gmi")
	}

	store := moduleStoreDir()

	// Every name is resolved before any entry is deleted: an unknown second
	// name must not leave the first gone without the rebuild that unlinks it.
	var targets []string
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
		targets = append(targets, target)
	}
	for _, target := range targets {
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
// already carries the API, as an IDL or as a generated package, or "". An
// external Go module's own IDL counts: its package is the one compiled in.
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

// generateExternalGMI generates the server code for every IDL add-gmi
// recorded, <tree>/external/gmi-*/gmi/*.gmi, into the package directory next
// to it, <tree>/external/gmi-<api>/gmi/<api>/: the C header (--server-c) and
// the Go dispatch with the REST metadata (--server-meta). That is what the
// in-tree C-only APIs get from src/gmi/codegen/Submakefile. The IDLs of other
// external modules are theirs, shipped with the packages generated from them.
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
		if !mod.IsDir() || !strings.HasPrefix(mod.Name(), extGMIPrefix) {
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
			if len(api.StreamServers) > 0 {
				return fmt.Errorf("external module %s: API %q has stream_server blocks; external stream APIs are not supported yet",
					mod.Name(), api.Name)
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
