// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package comp

import "testing"

// The skippers shared by the tokenizer, CaptureC and the header scanner.
func TestSkipCommentsAndLiterals(t *testing.T) {
	src := "// a \\\nb\nc"
	if end, spliced := skipLineComment(src, 0, true); end != 8 || spliced {
		t.Errorf("spliced skip: end %d spliced %v", end, spliced)
	}
	if end, spliced := skipLineComment(src, 0, false); end != 6 || !spliced {
		t.Errorf("unspliced skip: end %d spliced %v", end, spliced)
	}
	if end, spliced := skipLineComment("// x", 0, true); end != 4 || spliced {
		t.Errorf("end of text: end %d spliced %v", end, spliced)
	}
	if end, ok := skipBlockComment("/* x */y", 0); end != 7 || !ok {
		t.Errorf("block: end %d ok %v", end, ok)
	}
	if end, ok := skipBlockComment("/* x", 0); end != 4 || ok {
		t.Errorf("unclosed block: end %d ok %v", end, ok)
	}
	if end, ok := skipCLiteral(`"a\"b" c`, 0); end != 6 || !ok {
		t.Errorf("string: end %d ok %v", end, ok)
	}
	if end, ok := skipCLiteral("'x\nb", 0); end != 2 || ok {
		t.Errorf("unclosed char: end %d ok %v", end, ok)
	}
	if end, ok := skipCLiteral(`"ab\`, 0); end != 4 || ok {
		t.Errorf("backslash at the end: end %d ok %v", end, ok)
	}
}

// In the header a '//' comment ends at its line even after a backslash.
func TestScannerLineCommentNotSpliced(t *testing.T) {
	sc := NewScanner("f", "// note \\\npin in bit x;")
	if tok := sc.Next(); tok.Val != "pin" || tok.Pos.Line != 2 {
		t.Errorf("got %s at %s", tok, tok.Pos)
	}
}
