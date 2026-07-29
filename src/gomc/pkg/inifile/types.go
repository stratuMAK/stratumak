// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// Package inifile provides a parser for the LinuxCNC INI file format.
//
// The LinuxCNC INI format supports:
//   - Section headers: [SECTION_NAME]
//   - Key-value pairs: KEY = VALUE
//   - Comments: lines starting with # (excluding #INCLUDE) or ;
//   - #INCLUDE directives for recursive file inclusion
//   - Repeated keys (all values are preserved)
package inifile

import "strings"

// Entry represents a single key-value pair within an INI section.
type Entry struct {
	// Key is the entry key (left side of =).
	Key string
	// Value is the entry value (right side of =, trimmed, inline comments stripped).
	Value string
	// SourceFile is the absolute path of the file this entry was parsed from.
	// Set during parsing (including through #INCLUDE chains).
	SourceFile string
	// SourceLine is the 1-based line number in SourceFile where this entry appears.
	SourceLine int
}

// Section represents a named section in an INI file.
type Section struct {
	// Name is the section name (without brackets).
	Name string
	// Entries contains all key-value pairs in this section, in order.
	// The same key may appear multiple times.
	Entries []Entry
}

// IniFile holds the fully parsed content of one or more INI files
// (including any files pulled in via #INCLUDE).
type IniFile struct {
	// Sections contains all parsed sections, in the order they were encountered.
	Sections []Section

	// sourceFile is the path of the root file being parsed (used in error messages).
	sourceFile string

	// namespace is an optional prefix for section lookups. When set,
	// Get("JOINT_0", "KEY") first tries [namespace:JOINT_0], then [JOINT_0].
	// This enables multiple instances to share one INI file with per-instance
	// overrides in namespaced sections like [mill:JOINT_0].
	namespace string
}

// SourceFile returns the absolute path of the root INI file that was parsed.
func (ini *IniFile) SourceFile() string {
	return ini.sourceFile
}

// WithNamespace returns a shallow copy of the IniFile with a namespace prefix
// set for section lookups. Get("JOINT_0", "KEY") on the returned copy first
// checks [prefix:JOINT_0], then falls back to [JOINT_0].
// The underlying Sections slice is shared (not copied).
func (ini *IniFile) WithNamespace(prefix string) *IniFile {
	return &IniFile{
		Sections:   ini.Sections,
		sourceFile: ini.sourceFile,
		namespace:  prefix,
	}
}

// Namespace returns the current namespace prefix, or "" if none is set.
func (ini *IniFile) Namespace() string {
	return ini.namespace
}

// AllSections returns the full INI content as a nested section→key→value map.
// When a section or key appears more than once (e.g. via #INCLUDE or repeated
// keys), the last value encountered wins. The namespace prefix is ignored;
// sections are keyed by their literal names. This form is used for Go-template
// rendering, which accesses the raw section map.
func (ini *IniFile) AllSections() map[string]map[string]string {
	m := make(map[string]map[string]string)
	for _, sec := range ini.Sections {
		if _, ok := m[sec.Name]; !ok {
			m[sec.Name] = make(map[string]string)
		}
		for _, entry := range sec.Entries {
			m[sec.Name][entry.Key] = entry.Value
		}
	}
	return m
}

// AllSectionsResolved is AllSections with the namespace applied: entries of
// [ns:SECTION] overlay those of [SECTION] under the base section name,
// exactly as Get resolves an individual key. Template rendering must see the
// same view that the [SECTION]KEY substitutions it emits will read — a guard
// like `{{if ini "EMCIO" "TOOL_TABLE"}}` judged on the raw map would disagree
// with the namespaced value it guards, silently dropping the parameter for
// exactly the instance the namespace exists to configure. The literal
// (prefixed) section names remain present alongside the merged ones.
func (ini *IniFile) AllSectionsResolved() map[string]map[string]string {
	m := ini.AllSections()
	if ini.namespace == "" {
		return m
	}
	prefix := ini.namespace + ":"
	for name, entries := range m {
		base, ok := strings.CutPrefix(name, prefix)
		if !ok || base == "" {
			continue
		}
		dst := m[base]
		if dst == nil {
			dst = make(map[string]string, len(entries))
			m[base] = dst
		}
		for k, v := range entries {
			dst[k] = v
		}
	}
	return m
}

// GetSection returns all entries in the named section, in the order they
// appear in the file.  If the section appears more than once (e.g. via
// #INCLUDE), the entries are concatenated in file order.  Returns nil if the
// section does not exist.
//
// When a namespace is set, entries from [namespace:section] are returned first,
// followed by entries from [section] (global fallback).
func (ini *IniFile) GetSection(section string) []Entry {
	var result []Entry
	if ini.namespace != "" {
		nsSection := ini.namespace + ":" + section
		for i := range ini.Sections {
			if ini.Sections[i].Name == nsSection {
				result = append(result, ini.Sections[i].Entries...)
			}
		}
	}
	for i := range ini.Sections {
		if ini.Sections[i].Name == section {
			result = append(result, ini.Sections[i].Entries...)
		}
	}
	return result
}
