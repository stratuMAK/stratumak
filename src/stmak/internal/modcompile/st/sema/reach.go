// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package sema

import (
	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
)

// Every function block and pure function becomes a `static` C function, and
// cmods build with -Werror, so one that nothing calls is
// -Werror=unused-function and the component fails to compile.  Declaring a
// block and not using it is therefore a build failure today, with the error
// against generated source rather than the .st.
//
// That is a wart while a file declares only what it uses.  It is fatal once a
// library is in the picture, because a library declares everything a consumer
// might want and no consumer wants all of it.  So the package is pruned to
// what is reachable before it reaches the backend.
//
// Pruning runs after checking, not instead of it: a type error inside a block
// nobody calls is still a type error, and finding it only once someone calls
// the block would be a poor trade.
//
// See docs/dev/MODCOMPILE_ST_STDLIB.md §6.

// pruneUnreachable drops the blocks and functions nothing can reach from an
// exported HAL function.
func (c *checker) pruneUnreachable() {
	blocks := make(map[string]*ast.FunctionBlock, len(c.pkg.Blocks))
	for i := range c.pkg.Blocks {
		blocks[key(c.pkg.Blocks[i].Name)] = &c.pkg.Blocks[i]
	}
	funcs := make(map[string]*ast.PureFunction, len(c.pkg.Functions))
	for i := range c.pkg.Functions {
		funcs[key(c.pkg.Functions[i].Name)] = &c.pkg.Functions[i]
	}

	live := make(map[string]bool, len(blocks)+len(funcs))

	var reach func(name string)
	reach = func(name string) {
		k := key(name)
		if live[k] {
			return
		}
		if b, ok := blocks[k]; ok {
			live[k] = true
			// A block's own members may hold instances of other blocks, which
			// are reachable whether or not the body ever invokes them: the
			// instance is laid out in the struct either way, and its type has
			// to exist.
			for _, ref := range containedBlocks(b) {
				reach(ref.name)
			}
			for _, ref := range calleesOf(b.Body) {
				reach(ref.name)
			}
			for j := range b.Actions {
				for _, ref := range calleesOf(b.Actions[j].Body) {
					reach(ref.name)
				}
			}
			return
		}
		if f, ok := funcs[k]; ok {
			live[k] = true
			for _, ref := range calleesOf(f.Body) {
				reach(ref.name)
			}
		}
	}

	// The roots are the exported HAL functions -- what a thread can call --
	// together with anything the component's own storage holds.  A declared
	// instance counts even if no statement invokes it, because inst_t has a
	// member of its type.
	for i := range c.pkg.Component.Functions {
		fn := &c.pkg.Component.Functions[i]
		for _, ref := range calleesOf(fn.Body) {
			reach(ref.name)
		}
		for j := range fn.Actions {
			for _, ref := range calleesOf(fn.Actions[j].Body) {
				reach(ref.name)
			}
		}
	}
	for _, name := range c.instanceTypes() {
		reach(name)
	}

	c.pkg.Blocks = filterBlocks(c.pkg.Blocks, live)
	c.pkg.Functions = filterFuncs(c.pkg.Functions, live)
}

// instanceTypes names the function blocks the component's own variables hold,
// through an array or directly.
func (c *checker) instanceTypes() []string {
	// Component.Variables carries C types rather than ST ones, so the instance
	// list sema kept while declaring them is the record that still knows which
	// were function blocks.
	var out []string
	for _, in := range c.allInstances {
		t := in.typ
		for t.Kind == ast.TypeArray && t.Elem != nil {
			t = *t.Elem
		}
		if t.Kind == ast.TypeFB {
			out = append(out, t.Ref)
		}
	}
	return out
}

func filterBlocks(in []ast.FunctionBlock, live map[string]bool) []ast.FunctionBlock {
	out := in[:0]
	for _, b := range in {
		if live[key(b.Name)] {
			out = append(out, b)
		}
	}
	return out
}

func filterFuncs(in []ast.PureFunction, live map[string]bool) []ast.PureFunction {
	out := in[:0]
	for _, f := range in {
		if live[key(f.Name)] {
			out = append(out, f)
		}
	}
	return out
}

// orderBlocks puts a function block after every block it holds an instance of.
//
// C needs the contained type complete before the containing struct names it,
// and ST declaration order carries no such obligation: bodies are
// forward-declared so blocks may call each other in any order, and a reader
// reasonably expects the same of containment.  Without this,
//
//	FUNCTION_BLOCK Outer  VAR inner : Inner; END_VAR  ... END_FUNCTION_BLOCK
//	FUNCTION_BLOCK Inner  ...                          END_FUNCTION_BLOCK
//
// generates `Inner_t inner;` before Inner_t exists, and the error lands in
// generated C rather than against anything the author wrote.
//
// A post-order walk gives the order directly.  It terminates because
// checkContainmentCycles has already refused a block that contains itself, so
// the graph is acyclic by the time this runs.
func (c *checker) orderBlocks() {
	index := make(map[string]int, len(c.pkg.Blocks))
	for i := range c.pkg.Blocks {
		index[key(c.pkg.Blocks[i].Name)] = i
	}

	done := make(map[string]bool, len(c.pkg.Blocks))
	out := make([]ast.FunctionBlock, 0, len(c.pkg.Blocks))

	var visit func(k string)
	visit = func(k string) {
		if done[k] {
			return
		}
		i, ok := index[k]
		if !ok {
			return // pruned away, or not a block at all
		}
		done[k] = true
		for _, ref := range containedBlocks(&c.pkg.Blocks[i]) {
			visit(key(ref.name))
		}
		out = append(out, c.pkg.Blocks[i])
	}

	// Declaration order among independent blocks, so the generated C keeps
	// following the source wherever containment does not force a change.
	for i := range c.pkg.Blocks {
		visit(key(c.pkg.Blocks[i].Name))
	}
	c.pkg.Blocks = out
}
