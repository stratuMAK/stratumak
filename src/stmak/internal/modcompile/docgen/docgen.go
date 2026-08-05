// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// Package docgen generates man pages from parsed .comp AST.
package docgen

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// Generate writes a troff-formatted man page to w.
func Generate(w io.Writer, pkg *ast.Package) error {
	c := &pkg.Component
	section := "9"
	if c.Options["userspace"] == "yes" {
		section = "1"
	}

	// Header comment. The '\" t first line is man(1)'s preprocessor hint:
	// pages with pin/param tables go through tbl, and without the hint the
	// tables render as raw rows (groff: "TE macro called with TW register
	// undefined"). Emitted unconditionally -- it is inert without tables --
	// and it must stay the first line to be honoured.
	_, _ = fmt.Fprintf(w, `'\" t
.\" -*- mode: troff; coding: utf-8 -*-
.\"*******************************************************************
.\"
.\" This file was generated from %s using modcompile.
.\" Modify the source file.
.\"
.\"*******************************************************************

`, c.Pos.File)

	// Title
	_, _ = fmt.Fprintf(w, ".TH %s \"%s\" \"%s\" \"LinuxCNC Documentation\" \"HAL Component\"\n",
		strings.ToUpper(c.Name), section, time.Now().Format("2006-01-02"))

	// NAME section - use Summary (from component line), first line only
	_, _ = fmt.Fprintln(w, ".SH NAME")
	_, _ = fmt.Fprintln(w)
	summary := c.Summary
	// Only take first line of Summary
	if idx := strings.Index(summary, "\n"); idx >= 0 {
		summary = summary[:idx]
	}
	// If no Summary, fall back to first line of Description
	if summary == "" && c.Description != "" {
		summary = c.Description
		if idx := strings.Index(summary, "\n"); idx >= 0 {
			summary = summary[:idx]
		}
	}
	if summary != "" {
		_, _ = fmt.Fprintf(w, "%s \\- %s\n", c.Name, summary)
	} else {
		_, _ = fmt.Fprintf(w, "%s\n", c.Name)
	}

	// SYNOPSIS section
	_, _ = fmt.Fprintln(w, ".SH SYNOPSIS")
	if c.Options["userspace"] == "yes" {
		_, _ = fmt.Fprintf(w, ".B %s\n", c.Name)
	} else {
		_, _ = fmt.Fprintln(w, ".HP")
		// Build the load command
		_, _ = fmt.Fprintf(w, ".B load %s [%s.\\fIN\\fB]", c.Name, c.Name)
		for _, mp := range c.Modparams {
			if mp.Type == "string" {
				_, _ = fmt.Fprintf(w, " [%s=\\fISTR\\fB]", mp.Name)
			} else {
				_, _ = fmt.Fprintf(w, " [%s=\\fIN\\fB]", mp.Name)
			}
		}
		_, _ = fmt.Fprintln(w)

		// Document modparams if they have descriptions
		hasModparamDoc := false
		for _, mp := range c.Modparams {
			if mp.Doc != "" {
				hasModparamDoc = true
				break
			}
		}
		if hasModparamDoc {
			_, _ = fmt.Fprintln(w, ".RS 4")
			for _, mp := range c.Modparams {
				_, _ = fmt.Fprintln(w, ".TP")
				_, _ = fmt.Fprintf(w, "\\fB%s\\fR", mp.Name)
				if mp.Default != "" {
					_, _ = fmt.Fprintf(w, " [default: %s]", mp.Default)
				}
				_, _ = fmt.Fprintln(w)
				if mp.Doc != "" {
					_, _ = fmt.Fprintln(w, mp.Doc)
				}
			}
			_, _ = fmt.Fprintln(w, ".RE")
		}
	}

	// DESCRIPTION section
	if c.Description != "" {
		_, _ = fmt.Fprintln(w, ".SH DESCRIPTION")
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, c.Description)
	}

	// FUNCTIONS section
	if len(c.Functions) > 0 {
		_, _ = fmt.Fprintln(w, ".SH FUNCTIONS")
		for _, fn := range c.Functions {
			_, _ = fmt.Fprintln(w, ".TP")
			name := fn.Name
			if name == "_" {
				name = c.Name
			} else if !strings.Contains(name, ".") {
				name = c.Name + "." + name
			}
			_, _ = fmt.Fprintf(w, "\\fB%s.\\fIN\\fB\\fR", name)
			if fn.FP {
				_, _ = fmt.Fprintln(w, " (requires a floating-point thread)")
			} else {
				_, _ = fmt.Fprintln(w)
			}
			if fn.Doc != "" {
				_, _ = fmt.Fprintln(w, fn.Doc)
			}
		}
	}

	// PINS section
	if len(c.Pins) > 0 {
		_, _ = fmt.Fprintln(w, ".SH PINS")
		lead := ".TP"
		for _, pin := range c.Pins {
			_, _ = fmt.Fprintln(w, lead)
			name := toHALMan(c.Name, pin.Name)
			_, _ = fmt.Fprintf(w, ".B %s\\fR %s %s", name, pin.Type, pin.Dir)
			if pin.ArraySize > 0 {
				nHash := strings.Count(pin.Name, "#")
				if nHash == 0 {
					nHash = 1
				}
				if pin.ArrayPersonality != "" {
					_, _ = fmt.Fprintf(w, " (M=%0*d..%s)", nHash, 0, pin.ArrayPersonality)
				} else {
					_, _ = fmt.Fprintf(w, " (M=%0*d..%0*d)", nHash, 0, nHash, pin.ArraySize-1)
				}
			}
			if pin.Personality != "" {
				_, _ = fmt.Fprintf(w, " [if %s]", pin.Personality)
			}
			if pin.Default != "" {
				_, _ = fmt.Fprintf(w, " \\fR(default: \\fI%s\\fR)", pin.Default)
			}
			_, _ = fmt.Fprintln(w, " \\fR")
			if pin.Doc != "" {
				_, _ = fmt.Fprintln(w, pin.Doc)
				lead = ".TP"
			} else {
				lead = ".br\n.ns\n.TP"
			}
		}
	}

	// PARAMETERS section
	if len(c.Params) > 0 {
		_, _ = fmt.Fprintln(w, ".SH PARAMETERS")
		lead := ".TP"
		for _, param := range c.Params {
			_, _ = fmt.Fprintln(w, lead)
			name := toHALMan(c.Name, param.Name)
			_, _ = fmt.Fprintf(w, ".B %s\\fR %s %s", name, param.Type, param.Dir)
			if param.ArraySize > 0 {
				nHash := strings.Count(param.Name, "#")
				if nHash == 0 {
					nHash = 1
				}
				if param.ArrayPersonality != "" {
					_, _ = fmt.Fprintf(w, " (M=%0*d..%s)", nHash, 0, param.ArrayPersonality)
				} else {
					_, _ = fmt.Fprintf(w, " (M=%0*d..%0*d)", nHash, 0, nHash, param.ArraySize-1)
				}
			}
			if param.Personality != "" {
				_, _ = fmt.Fprintf(w, " [if %s]", param.Personality)
			}
			if param.Default != "" {
				_, _ = fmt.Fprintf(w, " \\fR(default: \\fI%s\\fR)", param.Default)
			}
			_, _ = fmt.Fprintln(w, " \\fR")
			if param.Doc != "" {
				_, _ = fmt.Fprintln(w, param.Doc)
				lead = ".TP"
			} else {
				lead = ".br\n.ns\n.TP"
			}
		}
	}

	// EXAMPLES section
	if c.Examples != "" {
		_, _ = fmt.Fprintln(w, ".SH EXAMPLES")
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, c.Examples)
	}

	// SEE ALSO section
	if c.SeeAlso != "" {
		_, _ = fmt.Fprintln(w, ".SH SEE ALSO")
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, c.SeeAlso)
	}

	// NOTES section
	if c.Notes != "" {
		_, _ = fmt.Fprintln(w, ".SH NOTES")
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, c.Notes)
	}

	// ARCHITECTURE section — only for arch-restricted modules.  On other
	// targets the module builds but refuses to load, so document the limit.
	if len(c.Archs) > 0 {
		if _, err := fmt.Fprintf(w, ".SH ARCHITECTURE\n\n"+
			"Supported only on: %s.\n"+
			"On other architectures the module loads to an error and does not run.\n",
			strings.Join(c.Archs, ", ")); err != nil {
			return err
		}
	}

	// AUTHOR section
	if c.Author != "" {
		_, _ = fmt.Fprintln(w, ".SH AUTHOR")
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, c.Author)
	}

	// LICENSE section
	if c.License != "" {
		_, _ = fmt.Fprintln(w, ".SH LICENSE")
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, c.License)
	}

	return nil
}

// toHALMan converts a pin/param name to man page format.
// e.g., "in-#" becomes "comp.\fIN\fB.in-\fIM\fB"
func toHALMan(compName, name string) string {
	// Replace # with \fIM\fB for array indices
	name = strings.ReplaceAll(name, "#", "\\fIM\\fB")
	// Replace _ with - for HAL naming convention
	name = strings.ReplaceAll(name, "_", "-")
	return fmt.Sprintf("%s.\\fIN\\fB.%s", compName, name)
}
