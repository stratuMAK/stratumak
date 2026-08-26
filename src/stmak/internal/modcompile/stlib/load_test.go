// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package stlib

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/st"
)

// write puts a file in dir and returns its path.
func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const timersLib = `{attribute 'library' := 'timers'}
{attribute 'summary' := 'Delay-on timers'}

FUNCTION_BLOCK TON
  VAR_INPUT
    IN : BOOL;
    PT : TIME;
  END_VAR
  VAR_OUTPUT
    Q : BOOL;
  END_VAR
  Q := IN;
END_FUNCTION_BLOCK
`

// TestLoadMergesLibrary covers the ordinary case: a component names a library
// and the library's declarations become visible to it.
func TestLoadMergesLibrary(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "timers.st", timersLib)
	root := write(t, dir, "c.st", `{attribute 'summary' := 'x'}
USING timers;
PROGRAM c
  VAR_OUTPUT
    q : BOOL;
  END_VAR
  VAR
    d : TON;
  END_VAR
  d(IN := TRUE, PT := T#1s);
  q := d.Q;
END_PROGRAM
`)

	l := &Loader{Dirs: []string{dir}}
	f, err := l.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Blocks) != 1 || f.Blocks[0].Name != "TON" {
		t.Fatalf("got blocks %v, want just TON", blockNames(f.Blocks))
	}
	// The declaration keeps the library's position, which is what makes a
	// diagnostic against it name the library rather than the component.
	if got := filepath.Base(f.Blocks[0].Pos.File); got != "timers.st" {
		t.Errorf("TON's position names %q, want timers.st", got)
	}
	// The library's own metadata is not the component's.
	for _, a := range f.Attrs {
		if strings.EqualFold(a.Value, "Delay-on timers") {
			t.Error("the library's summary became the component's")
		}
	}
	if len(l.Loaded) != 1 || l.Loaded[0].Name != "timers" {
		t.Errorf("Loaded = %v, want one entry for timers", l.Loaded)
	}
}

// TestLoadTransitiveAndDiamond covers a library using a library, and the same
// library reached twice.  Merging it twice would make every declaration
// collide with itself.
func TestLoadTransitiveAndDiamond(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "base.st", `{attribute 'library' := 'base'}
FUNCTION_BLOCK Base
  VAR_OUTPUT
    o : DINT;
  END_VAR
  o := 1;
END_FUNCTION_BLOCK
`)
	write(t, dir, "left.st", "{attribute 'library' := 'left'}\nUSING base;\n")
	write(t, dir, "right.st", "{attribute 'library' := 'right'}\nUSING base;\n")
	root := write(t, dir, "c.st", `{attribute 'summary' := 'x'}
USING left, right;
PROGRAM c
  VAR_OUTPUT
    q : BOOL;
  END_VAR
  q := TRUE;
END_PROGRAM
`)

	l := &Loader{Dirs: []string{dir}}
	f, err := l.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Blocks) != 1 {
		t.Errorf("got %d copies of Base, want 1: %v", len(f.Blocks), blockNames(f.Blocks))
	}
	if len(l.Loaded) != 3 {
		t.Errorf("loaded %d libraries, want 3 (base once, through both)", len(l.Loaded))
	}
}

// TestLoadRefuses covers every way a library reference can be wrong.  Each is
// refused with a reason, because "cannot find TON" is a poor way to learn that
// a file is missing an attribute.
func TestLoadRefuses(t *testing.T) {
	for _, tt := range []struct {
		name  string
		files map[string]string
		want  string
	}{{
		name: "no such library",
		files: map[string]string{
			"c.st": "{attribute 'summary' := 'x'}\nUSING nope;\n",
		},
		want: "no library named nope",
	}, {
		name: "not marked as a library",
		files: map[string]string{
			"plain.st": "{attribute 'summary' := 'x'}\n",
			"c.st":     "{attribute 'summary' := 'x'}\nUSING plain;\n",
		},
		want: "is not a library",
	}, {
		name: "the attribute names a different library",
		files: map[string]string{
			"timers.st": "{attribute 'library' := 'clocks'}\n",
			"c.st":      "{attribute 'summary' := 'x'}\nUSING timers;\n",
		},
		want: "declares itself as library",
	}, {
		name: "a library declaring a PROGRAM",
		files: map[string]string{
			"lib.st": "{attribute 'library' := 'lib'}\nPROGRAM p\nVAR_OUTPUT\n q : BOOL;\nEND_VAR\nq := TRUE;\nEND_PROGRAM\n",
			"c.st":   "{attribute 'summary' := 'x'}\nUSING lib;\n",
		},
		want: "may not declare a PROGRAM",
	}, {
		name: "a library declaring VAR_GLOBAL",
		files: map[string]string{
			"lib.st": "{attribute 'library' := 'lib'}\nVAR_GLOBAL\n n : DINT;\nEND_VAR\n",
			"c.st":   "{attribute 'summary' := 'x'}\nUSING lib;\n",
		},
		want: "may not declare VAR_GLOBAL",
	}, {
		name: "a library that uses itself",
		files: map[string]string{
			"lib.st": "{attribute 'library' := 'lib'}\nUSING lib;\n",
			"c.st":   "{attribute 'summary' := 'x'}\nUSING lib;\n",
		},
		want: "uses itself",
	}, {
		name: "a ring of libraries",
		files: map[string]string{
			"la.st": "{attribute 'library' := 'la'}\nUSING lb;\n",
			"lb.st": "{attribute 'library' := 'lb'}\nUSING la;\n",
			"c.st":  "{attribute 'summary' := 'x'}\nUSING la;\n",
		},
		want: "la -> lb -> la",
	}} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tt.files {
				write(t, dir, name, body)
			}
			l := &Loader{Dirs: []string{dir}}
			_, err := l.Load(filepath.Join(dir, "c.st"))
			if err == nil {
				t.Fatalf("expected a diagnostic, got none")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error is %q, want one containing %q", err, tt.want)
			}
		})
	}
}

// TestLoadRefusesCompilingALibrary covers pointing modcompile at a library.
// Making a component of it would export nothing and is never what was meant.
func TestLoadRefusesCompilingALibrary(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "timers.st", timersLib)
	l := &Loader{Dirs: []string{dir}}
	if _, err := l.Load(p); err == nil || !strings.Contains(err.Error(), "is a library") {
		t.Errorf("Load(a library) = %v, want a refusal saying so", err)
	}
	if !IsLibrary(p) {
		t.Error("IsLibrary should recognise it")
	}
}

// TestFirstDirWins covers shadowing: a project's own copy of a library beats
// the shipped one, which is what lets it override without moving anything.
func TestFirstDirWins(t *testing.T) {
	shipped, local := t.TempDir(), t.TempDir()
	write(t, shipped, "timers.st", timersLib)
	write(t, local, "timers.st", strings.Replace(timersLib, "FUNCTION_BLOCK TON", "FUNCTION_BLOCK TOF", 1))
	root := write(t, local, "c.st", "{attribute 'summary' := 'x'}\nUSING timers;\n")

	l := &Loader{Dirs: []string{local, shipped}}
	f, err := l.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Blocks) != 1 || f.Blocks[0].Name != "TOF" {
		t.Errorf("got %v, want the local copy's TOF", blockNames(f.Blocks))
	}
}

// TestDefaultDirsOrder pins the search order: the environment first, then the
// source's own directory, then whatever ships.
func TestDefaultDirsOrder(t *testing.T) {
	t.Setenv(PathEnv, "/first"+string(os.PathListSeparator)+"/second")
	got := DefaultDirs("/proj/comp.st", "/shipped")
	want := []string{"/first", "/second", "/proj", "/shipped"}
	if len(got) != len(want) {
		t.Fatalf("DefaultDirs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("DefaultDirs = %v, want %v", got, want)
		}
	}
}

// blockNames is for a failure message that says which blocks were merged.
func blockNames(bs []*st.FunctionBlockDecl) []string {
	out := make([]string, 0, len(bs))
	for _, b := range bs {
		out = append(out, b.Name)
	}
	return out
}
