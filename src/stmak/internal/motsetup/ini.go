// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package motsetup

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
)

// Lenient INI accessors: a malformed value falls back to the default rather
// than failing the load. That is deliberate for the machine push — it mirrors
// the C config readers, and every value here has a usable default. Callers that
// want strict parsing (pnptask does, for its own [PNPTASK*] sections) validate
// before calling.

func getFloatOr(ini *inifile.IniFile, section, key string, def float64) float64 {
	s := ini.Get(section, key)
	if s == "" {
		return def
	}
	return parseFloat(s, def)
}

func getIntOr(ini *inifile.IniFile, section, key string, def int) int {
	s := ini.Get(section, key)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

func getBoolOr(ini *inifile.IniFile, section, key string, def bool) bool {
	switch strings.TrimSpace(strings.ToLower(ini.Get(section, key))) {
	case "":
		return def
	case "1", "yes", "true":
		return true
	case "0", "no", "false":
		return false
	}
	return def
}

// parseFloat uses Sscanf rather than ParseFloat so a value with a trailing
// comment or unit ("25.4 mm") still yields its number, matching the C readers.
func parseFloat(s string, def float64) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	var v float64
	if _, err := fmt.Sscanf(s, "%f", &v); err != nil {
		return def
	}
	return v
}
