// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// Package inirest exposes the parsed INI file via the generated ini GMI API.
package inirest

import (
	"fmt"
	"os"

	"github.com/sittner/linuxcnc/src/gomc/generated/gmi/ini"
	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
	"github.com/sittner/linuxcnc/src/gomc/internal/pathres"
	"github.com/sittner/linuxcnc/src/gomc/pkg/inifile"
)

type iniImpl struct {
	ini *inifile.IniFile
}

func (im *iniImpl) Query(items []ini.IniQueryItem) ([]ini.IniQueryResult, error) {
	if im.ini == nil {
		// Permanent for this process (an INI-less launcher, halrun mode), so
		// this is a state conflict rather than a 503 inviting a retry.
		return nil, apiserver.Faultf(apiserver.FaultState, "INI file not loaded")
	}

	results := make([]ini.IniQueryResult, len(items))
	for i, q := range items {
		iniFile := im.ini
		if q.Namespace != nil && *q.Namespace != "" {
			iniFile = iniFile.WithNamespace(*q.Namespace)
		}
		if q.All != nil && *q.All {
			vals := iniFile.GetAll(q.Section, q.Key)
			if vals == nil {
				vals = []string{}
			}
			results[i] = ini.IniQueryResult{Values: vals}
		} else {
			// An absent key reports a null value; a key that is present with an
			// empty value reports "". Those are different answers — an INI may
			// legitimately carry `LATHE =` — and until `string?` became a real
			// pointer this branch could not express the difference: both arms
			// produced the same zero struct.
			v := iniFile.Get(q.Section, q.Key)
			if v == "" && !im.keyExists(q.Section, q.Key) {
				results[i] = ini.IniQueryResult{}
			} else {
				results[i] = ini.IniQueryResult{Value: &v}
			}
		}
	}
	return results, nil
}

func (im *iniImpl) GetParameterFile(namespace *string) (string, error) {
	if im.ini == nil {
		return "", apiserver.Faultf(apiserver.FaultState, "INI file not loaded")
	}
	ini := im.ini
	if namespace != nil && *namespace != "" {
		ini = ini.WithNamespace(*namespace)
	}
	rel := ini.Get("RS274NGC", "PARAMETER_FILE")
	if rel == "" {
		return "", apiserver.Faultf(apiserver.FaultNotFound, "[RS274NGC]PARAMETER_FILE not set")
	}
	// The file's contents are served over REST, so the path goes through the
	// shared resolver and its containment check (internal/pathres) — an INI
	// value must not be able to name an arbitrary file.
	// Unset, unresolvable and unreadable are one answer to a client — there is
	// no parameter file — and none of them is a controller failure. The reason
	// (including a containment refusal) travels in the message, which is what
	// an operator debugging their INI needs.
	path, err := pathres.Resolve(rel, pathres.Read)
	if err != nil {
		return "", apiserver.NewFault(apiserver.FaultNotFound, fmt.Errorf("paramfile: %w", err))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", apiserver.NewFault(apiserver.FaultNotFound, fmt.Errorf("paramfile: %w", err))
	}
	return string(data), nil
}

func (im *iniImpl) keyExists(section, key string) bool {
	entries := im.ini.GetSection(section)
	for _, e := range entries {
		if e.Key == key {
			return true
		}
	}
	return false
}

// Register registers the INI REST API with the given registry.
func Register(reg *apiserver.Registry, parsed *inifile.IniFile) error {
	apiserver.RegisterMeta(ini.IniMeta)
	impl := &iniImpl{ini: parsed}
	return ini.RegisterIniAPI(reg, "ini", impl)
}
