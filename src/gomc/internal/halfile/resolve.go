// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package halfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// isRegularFile reports whether path names an existing regular (non-directory)
// file. The legacy linuxcnc.in resolver clears its "found" marker for a
// directory (`[ -d $foundfile ] && foundmsg=""`, linuxcnc.in), forcing a clean
// "cannot find file" instead of returning a directory that then fails
// confusingly in the parser. os.Stat succeeds on directories, so this guard
// reproduces that behaviour.
func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// expandTilde reproduces the `$INIVAR -tildeexpand` step the legacy
// linuxcnc.in applies to HALFILE values before resolution: a leading "~" or
// "~/" is replaced with the current user's home directory. "~user" forms are
// left untouched (rare and not portably resolvable here). If the home
// directory cannot be determined, the path is returned unchanged.
func expandTilde(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// resolvePath finds a HAL file by searching in:
//  1. The directory containing the INI configuration file (configDir).
//  2. Each directory in halibPath (colon-separated, same as HALLIB_PATH).
//
// If filename starts with "LIB:", the prefix is stripped and the file is
// resolved exclusively from the hallib directories in halibPath (not from
// configDir), matching the legacy linuxcnc.in behaviour.
//
// If filename is already absolute and the file exists, it is returned as-is.
// An error is returned if the file cannot be found in any search location.
// Directories that name-match are rejected (a HAL file must be a regular file),
// and a leading "~"/"~/" is expanded to the user's home directory.
func (e *Executor) resolvePath(filename string) (string, error) {
	filename = expandTilde(filename)

	// LIB: prefix – resolve from hallib directories only, not configDir.
	if strings.HasPrefix(filename, "LIB:") {
		libFile := strings.TrimPrefix(filename, "LIB:")
		for _, dir := range strings.Split(e.halibPath, ":") {
			dir = strings.TrimSpace(dir)
			if dir == "" {
				continue
			}
			candidate := filepath.Join(dir, libFile)
			if isRegularFile(candidate) {
				abs, err := filepath.Abs(candidate)
				if err != nil {
					return "", fmt.Errorf("resolving path %q: %w", candidate, err)
				}
				return abs, nil
			}
		}
		return "", fmt.Errorf("HAL file %q not found in HALLIB_PATH", filename)
	}

	// Absolute paths are used directly if the file exists.
	if filepath.IsAbs(filename) {
		if isRegularFile(filename) {
			return filename, nil
		}
		return "", fmt.Errorf("HAL file not found: %s", filename)
	}

	// Build the ordered list of directories to search.
	var searchDirs []string
	if e.configDir != "" {
		searchDirs = append(searchDirs, e.configDir)
	}
	for _, dir := range strings.Split(e.halibPath, ":") {
		dir = strings.TrimSpace(dir)
		if dir != "" {
			searchDirs = append(searchDirs, dir)
		}
	}

	for _, dir := range searchDirs {
		candidate := filepath.Join(dir, filename)
		if isRegularFile(candidate) {
			abs, err := filepath.Abs(candidate)
			if err != nil {
				return "", fmt.Errorf("resolving path %q: %w", candidate, err)
			}
			return abs, nil
		}
	}

	return "", fmt.Errorf("HAL file %q not found in config dir or HALLIB_PATH", filename)
}
