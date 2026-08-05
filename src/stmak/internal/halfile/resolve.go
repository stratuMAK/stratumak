// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package halfile

import (
	"github.com/stratuMAK/stratumak/src/stmak/internal/pathres"
)

// resolvePath finds a HAL file.
//
// The rule lives in internal/pathres and is shared with every other
// configuration-supplied path in the system (module arguments, config files
// referenced from them, INI-derived paths): tilde expansion, "LIB:" from the
// library directories only, otherwise the config directory first and then each
// HALLIB_PATH directory, regular-file check, and containment within those
// directories.
//
// Containment is why this is not just a search loop: HAL files are named by
// INI values and by `source` directives inside other HAL files, and the load
// path is reachable over REST.
func (e *Executor) resolvePath(filename string) (string, error) {
	r, err := e.resolver()
	if err != nil {
		return "", err
	}
	return r.Resolve(filename, pathres.Read)
}

// resolver returns the Executor's path resolver, building it on first use.
//
// An Executor is constructed per HAL-file run and is not shared across
// goroutines, so a plain memoised field is enough.
func (e *Executor) resolver() (*pathres.Resolver, error) {
	if e.pathResolver != nil {
		return e.pathResolver, nil
	}
	r, err := pathres.New(e.configDir, e.halibPath)
	if err != nil {
		return nil, err
	}
	e.pathResolver = r
	return r, nil
}
