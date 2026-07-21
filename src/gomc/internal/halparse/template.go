// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package halparse

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/template"
)

// maxSeqLen bounds the element count of the seq/seq1/count template helpers.
// The count is template/INI-driven, so an absurd value must not be allowed to
// allocate an unbounded slice (OOM at bring-up). 1e6 is far above any real HAL
// instance count while capping the worst case at a few MB.
const maxSeqLen = 1_000_000

// checkSeqLen validates a template iteration count: non-negative and within
// maxSeqLen. It returns a template-friendly error (surfaced as a render failure)
// rather than letting an out-of-range value panic make() or OOM the process.
func checkSeqLen(n int) error {
	if n < 0 {
		return fmt.Errorf("iteration count %d is negative", n)
	}
	if n > maxSeqLen {
		return fmt.Errorf("iteration count %d exceeds maximum %d", n, maxSeqLen)
	}
	return nil
}

// HalTemplateData holds the data context available to HAL file templates.
type HalTemplateData struct {
	INI    map[string]map[string]string
	Axes   []string
	Joints int
	Env    map[string]string
}

// NewHalTemplateData creates a HalTemplateData from an INI data map.
func NewHalTemplateData(ini map[string]map[string]string) *HalTemplateData {
	data := &HalTemplateData{
		INI: ini,
		Env: make(map[string]string),
	}

	// Extract axes from [TRAJ]COORDINATES
	// Coordinates may be space-separated ("X Y Z") or concatenated ("XYZ").
	if traj, ok := ini["TRAJ"]; ok {
		if coords, ok := traj["COORDINATES"]; ok {
			for _, c := range strings.TrimSpace(coords) {
				if c != ' ' && c != '\t' {
					data.Axes = append(data.Axes, string(c))
				}
			}
		}
	}

	// Extract joints from [KINS]JOINTS
	if kins, ok := ini["KINS"]; ok {
		if joints, ok := kins["JOINTS"]; ok {
			if n, err := strconv.Atoi(strings.TrimSpace(joints)); err == nil {
				data.Joints = n
			}
		}
	}

	// Populate environment
	for _, env := range os.Environ() {
		if k, v, ok := strings.Cut(env, "="); ok {
			data.Env[k] = v
		}
	}

	return data
}

// toFloat64 coerces a numeric value to float64. Accepts int, int64, float64,
// and string (parsed via strconv.ParseFloat).
func toFloat64(v any) (float64, error) {
	switch n := v.(type) {
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case float64:
		return n, nil
	case string:
		return strconv.ParseFloat(n, 64)
	default:
		return 0, fmt.Errorf("cannot convert %T to float64", v)
	}
}

// convertTwoArgs coerces two values to float64 for use in math template functions.
func convertTwoArgs(a, b any) (float64, float64, error) {
	fa, err := toFloat64(a)
	if err != nil {
		return 0, 0, err
	}
	fb, err := toFloat64(b)
	if err != nil {
		return 0, 0, err
	}
	return fa, fb, nil
}

// toInt coerces a value to int. Accepts int, int64, float64 (must be integral),
// and string (parsed via strconv.Atoi). It is used by the integer math helpers
// so template arithmetic that feeds instance indices stays exact and prints
// without a decimal point.
func toInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		if n != float64(int(n)) {
			return 0, fmt.Errorf("value %v is not an integer", n)
		}
		return int(n), nil
	case string:
		return strconv.Atoi(strings.TrimSpace(n))
	default:
		return 0, fmt.Errorf("cannot convert %T to int", v)
	}
}

// convertTwoInts coerces two values to int for use in integer math functions.
func convertTwoInts(a, b any) (int, int, error) {
	ia, err := toInt(a)
	if err != nil {
		return 0, 0, err
	}
	ib, err := toInt(b)
	if err != nil {
		return 0, 0, err
	}
	return ia, ib, nil
}

// halTemplateFuncs returns the function map for HAL file templates.
// The ini function is a closure over the provided INI data so that templates
// can call {{ini "SECTION" "KEY"}} without explicitly passing .INI.
// hasJoint and hasAxis are closures over the HalTemplateData for INI-derived
// range checks that require no HAL runtime probing.
func halTemplateFuncs(data *HalTemplateData) template.FuncMap {
	return template.FuncMap{
		// String operations
		"lower":    strings.ToLower,
		"upper":    strings.ToUpper,
		"replace":  strings.ReplaceAll,
		"contains": strings.Contains,
		"split":    strings.Split,
		"join":     strings.Join,
		"printf":   fmt.Sprintf,
		"trim":     strings.TrimSpace,

		// Math operations — accept any numeric type via toFloat64
		"add": func(a, b any) (float64, error) {
			fa, fb, err := convertTwoArgs(a, b)
			return fa + fb, err
		},
		"sub": func(a, b any) (float64, error) {
			fa, fb, err := convertTwoArgs(a, b)
			return fa - fb, err
		},
		"mul": func(a, b any) (float64, error) {
			fa, fb, err := convertTwoArgs(a, b)
			return fa * fb, err
		},
		"div": func(a, b any) (float64, error) {
			fa, fb, err := convertTwoArgs(a, b)
			if err != nil {
				return 0, err
			}
			if fb == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			return fa / fb, nil
		},
		"neg": func(a any) (float64, error) {
			fa, err := toFloat64(a)
			return -fa, err
		},

		// Integer math — keep values exact and print without a decimal point,
		// for building instance indices (e.g. FIFO station numbers).
		"addi": func(a, b any) (int, error) {
			ia, ib, err := convertTwoInts(a, b)
			return ia + ib, err
		},
		"muli": func(a, b any) (int, error) {
			ia, ib, err := convertTwoInts(a, b)
			return ia * ib, err
		},

		// Iteration helpers. The element count comes from the template (often
		// via `atoi (ini ...)`), so it is bounded to maxSeqLen: an absurd count
		// (a hostile/typo'd INI value) would otherwise `make` a multi-GB slice
		// and OOM the controller at bring-up. Negative counts are rejected
		// rather than left to panic in make().
		"seq": func(start, end int) ([]int, error) {
			n := end - start
			if err := checkSeqLen(n); err != nil {
				return nil, err
			}
			result := make([]int, 0, n)
			for i := start; i < end; i++ {
				result = append(result, i)
			}
			return result, nil
		},
		"seq1": func(n int) ([]int, error) {
			if err := checkSeqLen(n); err != nil {
				return nil, err
			}
			result := make([]int, n)
			for i := range result {
				result[i] = i + 1
			}
			return result, nil
		},
		"count": func(n int) ([]int, error) {
			if err := checkSeqLen(n); err != nil {
				return nil, err
			}
			result := make([]int, n)
			for i := range result {
				result[i] = i
			}
			return result, nil
		},

		// INI access — closure over the template's own INI data
		"ini": func(section, key string) string {
			if s, ok := data.INI[section]; ok {
				if v, ok := s[key]; ok {
					return v
				}
			}
			return ""
		},
		// iniGT0 returns true if the INI value is a positive integer (> 0).
		// Returns false for missing, empty, non-numeric, zero, or negative values.
		"iniGT0": func(section, key string) bool {
			if s, ok := data.INI[section]; ok {
				if v, ok := s[key]; ok {
					if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
						return n > 0
					}
				}
			}
			return false
		},

		// Environment access
		"env": os.Getenv,

		// Type conversions
		"atoi": strconv.Atoi,
		"atof": func(s string) (float64, error) {
			return strconv.ParseFloat(s, 64)
		},
		"itoa": strconv.Itoa,

		// INI-derived range checks — no HAL runtime probing needed.
		// hasJoint returns true if joint n is within the configured range [0, .Joints).
		"hasJoint": func(n int) bool {
			return n >= 0 && n < data.Joints
		},
		// hasAxis returns true if the given letter (case-insensitive) appears in .Axes.
		"hasAxis": func(letter string) bool {
			upper := strings.ToUpper(letter)
			for _, a := range data.Axes {
				if strings.ToUpper(a) == upper {
					return true
				}
			}
			return false
		},
	}
}

// RenderHalTemplate renders a HAL file through Go's text/template engine.
// Returns the rendered output as a string.
// If the input contains no template directives (no "{{"), it is returned as-is.
func RenderHalTemplate(name, content string, data *HalTemplateData) (string, error) {
	// Fast path: no template directives
	if !strings.Contains(content, "{{") {
		return content, nil
	}

	tmpl, err := template.New(name).Funcs(halTemplateFuncs(data)).Parse(content)
	if err != nil {
		return "", fmt.Errorf("template parse error in %s: %w", name, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("template execute error in %s: %w", name, err)
	}

	return buf.String(), nil
}
