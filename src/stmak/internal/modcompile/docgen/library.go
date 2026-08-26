// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package docgen

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/st"
)

// A library's documentation is written per block, in man section 3, because
// that is the unit somebody uses: you reach for a TON, not for "the standard
// library".  `man 3 TON` then answers the question that was actually asked,
// and apropos finds it.
//
// The pages are rendered from the syntax tree rather than from a checked
// package.  A library has no PROGRAM and so is not a component, and running
// sema over one would mean inventing a mode for it -- but more to the point,
// what a reader wants to see is the type as the library writer spelled it.
// `PT : TIME` is the answer to "what do I pass"; the resolved kind is not.
//
// See docs/dev/MODCOMPILE_ST_STDLIB.md §9.

// Library is one parsed library, ready to document.
type Library struct {
	// Name is the value of the file's {attribute 'library'}.
	Name string
	// Path is the file it came from, named in each page's header comment.
	Path string
	// File is the parsed source.
	File *st.File
}

// BlockPage is one block and the file its page belongs in.
type BlockPage struct {
	// Block is the man page's subject.
	Block string
	// FileName is what to call the file: TON.3.
	FileName string
}

// Pages lists the man pages this library produces, in declaration order.
func (l *Library) Pages() []BlockPage {
	out := make([]BlockPage, 0, len(l.File.Blocks))
	for _, b := range l.File.Blocks {
		out = append(out, BlockPage{Block: b.Name, FileName: b.Name + ".3"})
	}
	return out
}

// GenerateBlock writes the man page for one of the library's blocks.
func (l *Library) GenerateBlock(w io.Writer, name string) error {
	var b *st.FunctionBlockDecl
	for _, cand := range l.File.Blocks {
		if strings.EqualFold(cand.Name, name) {
			b = cand
			break
		}
	}
	if b == nil {
		return fmt.Errorf("library %s declares no function block %s", l.Name, name)
	}

	fmt.Fprintf(w, `'\" t
.\" -*- mode: troff; coding: utf-8 -*-
.\"*******************************************************************
.\"
.\" This file was generated from %s using modcompile.
.\" Modify the source file.
.\"
.\"*******************************************************************

`, l.Path)

	fmt.Fprintf(w, ".TH %s \"3\" \"%s\" \"stratuMAK Documentation\" \"ST Library\"\n",
		strings.ToUpper(b.Name), time.Now().Format("2006-01-02"))

	fmt.Fprintln(w, ".SH NAME")
	fmt.Fprintln(w)
	if doc := firstLine(attrValue(b.Attrs, "doc")); doc != "" {
		fmt.Fprintf(w, "%s \\- %s\n", b.Name, doc)
	} else {
		fmt.Fprintf(w, "%s \\- a function block of the %s library\n", b.Name, l.Name)
	}

	// SYNOPSIS: how to reach it and how to declare one, which between them are
	// everything needed before the interface below makes sense.
	fmt.Fprintln(w, ".SH SYNOPSIS")
	fmt.Fprintln(w, ".nf")
	fmt.Fprintf(w, "USING %s;\n", l.Name)
	fmt.Fprintln(w, ".sp")
	fmt.Fprintln(w, "VAR")
	fmt.Fprintf(w, "  name : %s;\n", b.Name)
	fmt.Fprintln(w, "END_VAR")
	fmt.Fprintln(w, ".sp")
	fmt.Fprintf(w, "name(%s);\n", callHint(b))
	fmt.Fprintln(w, ".fi")

	writeVarSection(w, b, st.VarInput, "INPUTS")
	writeVarSection(w, b, st.VarInOut, "IN_OUT")
	writeVarSection(w, b, st.VarOutput, "OUTPUTS")

	fmt.Fprintln(w, ".SH LIBRARY")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s, from %s\n", l.Name, l.Path)
	if s := attrValue(l.File.Attrs, "summary"); s != "" {
		fmt.Fprintf(w, ".PP\n%s\n", s)
	}

	if a := attrValue(l.File.Attrs, "author"); a != "" {
		fmt.Fprintln(w, ".SH AUTHOR")
		fmt.Fprintln(w)
		fmt.Fprintln(w, a)
	}
	if lic := attrValue(l.File.Attrs, "license"); lic != "" {
		fmt.Fprintln(w, ".SH LICENSE")
		fmt.Fprintln(w)
		fmt.Fprintln(w, lic)
	}

	// SEE ALSO names the library's other blocks, which is how somebody who
	// opened the wrong page finds the right one.
	var others []string
	for _, cand := range l.File.Blocks {
		if !strings.EqualFold(cand.Name, b.Name) {
			others = append(others, "\\fB"+cand.Name+"\\fR(3)")
		}
	}
	if len(others) > 0 {
		fmt.Fprintln(w, ".SH SEE ALSO")
		fmt.Fprintln(w)
		fmt.Fprintln(w, strings.Join(others, ",\n"))
	}
	return nil
}

// writeVarSection renders one VAR flavour as a labelled list.
func writeVarSection(w io.Writer, b *st.FunctionBlockDecl, kind st.VarKind, heading string) {
	type entry struct{ name, typ, doc, def string }
	var entries []entry

	for _, blk := range b.Vars {
		if blk.Kind != kind {
			continue
		}
		for _, d := range blk.Decls {
			for _, n := range d.Names {
				entries = append(entries, entry{
					name: n,
					typ:  typeName(d.Type),
					doc:  attrValue(d.Attrs, "doc"),
					def:  initText(d.Init),
				})
			}
		}
	}
	if len(entries) == 0 {
		return
	}

	fmt.Fprintf(w, ".SH %s\n", heading)
	for _, e := range entries {
		fmt.Fprintln(w, ".TP")
		def := ""
		if e.def != "" {
			def = fmt.Sprintf(" \\fR(default: \\fI%s\\fR)", e.def)
		}
		fmt.Fprintf(w, ".B %s\\fR %s%s\n", e.name, e.typ, def)
		if e.doc != "" {
			fmt.Fprintln(w, e.doc)
		} else {
			fmt.Fprintln(w, ".br")
			fmt.Fprintln(w, ".ns")
		}
	}
}

// callHint is the argument list for the SYNOPSIS, naming the inputs so the
// reader sees the shape of a call rather than an empty pair of brackets.
func callHint(b *st.FunctionBlockDecl) string {
	var args []string
	for _, blk := range b.Vars {
		if blk.Kind != st.VarInput && blk.Kind != st.VarInOut {
			continue
		}
		for _, d := range blk.Decls {
			for _, n := range d.Names {
				args = append(args, n+" := ...")
			}
		}
	}
	return strings.Join(args, ", ")
}

// typeName is the type as the library writer spelled it.
func typeName(t *st.TypeRef) string {
	if t == nil {
		return "?"
	}
	if t.Name != "" {
		return t.Name
	}
	if t.Elem != nil {
		return "ARRAY OF " + typeName(t.Elem)
	}
	return "?"
}

// initText renders a declaration's initialiser for the default column.  Only
// the literal forms, because anything else is not a value a reader can act on.
func initText(e st.Expr) string {
	switch v := e.(type) {
	case nil:
		return ""
	case *st.IntLit:
		return v.Text
	case *st.RealLit:
		return v.Text
	case *st.BoolLit:
		if v.Value {
			return "TRUE"
		}
		return "FALSE"
	case *st.TypedLit:
		return v.Prefix + "#" + v.Body
	case *st.Ident:
		return v.Name
	}
	return ""
}

// attrValue reads one pragma's value, case-insensitively.
func attrValue(attrs []st.Attribute, name string) string {
	for _, a := range attrs {
		if strings.EqualFold(a.Name, name) {
			return a.Value
		}
	}
	return ""
}

func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}
