// Field traces for Java and Kotlin: the same two trees as the Go trace (field.go),
// read from tree-sitter syntax instead of SSA (ADR 0007: no build, no classpath).
//
// Keys are expressions, locals and parameters (by name, one flat scope per
// function as the extractor has), function results and fields. A field is one
// location for every object of its type, keyed pkg.Type.field, the same as Go.
// Calls follow what the JVM extractor resolved; a call it did not resolve
// derives its result from its receiver or its one data argument. With no types,
// values that only describe the datum are dropped by shape: comparisons and
// predicate calls (isBlank, equals, ...).
//
// ponytail: one project only, and returns flow to every caller (no call-site
// matching); follow shared fields into other projects and match calls as the Go
// walk does once traces need it.
package domain

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

type jvar struct {
	fn   *jvmFunc
	name string // "" is the function's result
}

type jexpr struct {
	fn *jvmFunc
	k  nkey
}

type jfield string // pkg.Type.field; a type outside the project is its simple name

// jat is where a flow happened, the at of a JVM hit.
type jat struct {
	fn  *jvmFunc
	n   *sitter.Node
	via jref // call site, for parameters
}

type jref struct {
	fn *jvmFunc
	n  *sitter.Node
}

// jdef is a way a local gets its value.
type jdef struct {
	name, value *sitter.Node // value is nil for a declaration with no source (catch e)
	how         flow
	real        bool // a declaration or assignment, not a callback or a mutation
}

type jtrace struct {
	jp     *jvmProject
	tests  bool
	uses   map[*jvmFunc]map[string][]*sitter.Node
	defs   map[*jvmFunc]map[string][]jdef
	rets   map[*jvmFunc][]*sitter.Node
	reads  map[jfield][]jref
	stores map[jfield][]jref
	ftype  map[jfield]string
	ftrace
}

// jwalk is one of the engine's walks, read as Java or Kotlin.
type jwalk struct {
	*fwalk
	t *jtrace
}

var (
	jvmPredicates = []string{"equals", "contains", "containsKey", "containsValue", "startsWith", "endsWith", "matches",
		"compareTo", "hashCode", "size", "length", "count", "require", "check", "assert"}
	arith       = []string{"+", "-", "*", "/", "%", "<<", ">>", ">>>", "&", "|", "^"} // binary operators deriving a value
	jvmMutators = []string{"add", "addAll", "put", "putAll", "putIfAbsent", "merge", "set", "offer", "push", "append", "addFirst", "addLast", "compute"}
)

// jseeds finds the values computed on line in fn: locals defined there,
// parameters declared there, fields stored there and results returned there.
func (t *jtrace) jseeds(fn *jvmFunc, ln int) {
	type seed struct {
		fwd, back    any
		fnode, bnode *sitter.Node
		read         jfield // the value is read from this field: its other reads are uses too
	}
	var ss []seed
	for i, name := range fn.names {
		if c := paramNode(fn, i); name != "" && line(c) == ln {
			ss = append(ss, seed{fwd: jvar{fn, name}, back: jvar{fn, name}, fnode: c, bnode: c})
		}
	}
	for name, ds := range t.defs[fn] {
		for _, d := range ds {
			if d.real && d.value != nil && line(d.name) == ln {
				s := seed{fwd: jvar{fn, name}, back: jexpr{fn, keyOf(fn.file, d.value)}, fnode: d.name, bnode: d.value}
				s.read, _ = t.fieldRef(fn, d.value)
				ss = append(ss, s)
			}
		}
	}
	for k, refs := range t.stores {
		for _, r := range refs {
			if r.fn == fn && line(r.n) == ln {
				ss = append(ss, seed{fwd: k, back: jexpr{fn, keyOf(fn.file, r.n)}, fnode: r.n, bnode: r.n})
			}
		}
	}
	for _, r := range t.rets[fn] {
		if line(r) == ln {
			ss = append(ss, seed{fwd: jvar{fn, ""}, back: jexpr{fn, keyOf(fn.file, r)}, fnode: r, bnode: r})
		}
	}
	// defs and stores are maps: order by position so runs are reproducible.
	slices.SortStableFunc(ss, func(a, b seed) int { return cmp.Compare(a.fnode.StartByte(), b.fnode.StartByte()) })
	for _, s := range ss {
		var read tkey
		if s.read != "" {
			read = s.read
		}
		t.seed(s.fwd, s.back, read, jat{fn: fn, n: s.fnode}, jat{fn: fn, n: s.bnode})
	}
}

// fieldSeeds seeds the fields declared on line of file: Java fields and record
// components, Kotlin properties and primary constructor properties.
func (t *jtrace) fieldSeeds(rel string, ln int) {
	for _, tys := range t.jp.types {
		for _, ty := range tys {
			if ty.file.rel != rel || ty.decl == nil {
				continue
			}
			var walk func(n *sitter.Node, depth int)
			walk = func(n *sitter.Node, depth int) {
				if depth > 4 || line(n) > ln || int(n.EndPoint().Row)+1 < ln {
					return
				}
				var name *sitter.Node
				switch n.Type() {
				case "variable_declarator":
					if p := n.Parent(); p != nil && (p.Type() == "field_declaration" || p.Type() == "constant_declaration") {
						name = n.ChildByFieldName("name")
					}
				case "formal_parameter":
					if p := n.Parent(); p != nil && p.Parent() != nil && p.Parent().Type() == "record_declaration" {
						name = n.ChildByFieldName("name")
					}
				case "class_parameter":
					name = childOf(n, "simple_identifier")
				case "property_declaration":
					if v, _ := kotlinProperty(n); v != nil {
						name = childOf(v, "simple_identifier")
					}
				}
				if name != nil && line(name) == ln {
					if k, ok := t.key(ty, ty.file.text(name)); ok {
						t.seed(k, k, nil, jat{n: n}, jat{n: n})
					}
					return
				}
				for _, c := range children(n) {
					if !strings.HasSuffix(c.Type(), "declaration") || c.Type() == "property_declaration" || c.Type() == "field_declaration" {
						walk(c, depth+1)
					}
				}
			}
			walk(ty.decl, 0)
		}
	}
}

// traceJVMField traces the value computed at rel:line in a Java or Kotlin project.
// Cancelling ctx stops it after the project is parsed.
func traceJVMField(ctx context.Context, seedProject project, file string, ln int, tests bool) (*FieldTrace, string, error) {
	rel, err := filepath.Rel(seedProject.dir, file)
	if err != nil {
		return nil, "", err
	}
	rel = filepath.ToSlash(rel)
	g := &graph{ids: map[string]int32{}, Impls: map[string][]int32{}}
	jp, st, err := loadJVM(g, seedProject)
	if err != nil {
		return nil, "", err
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	t := &jtrace{jp: jp, tests: tests, uses: map[*jvmFunc]map[string][]*sitter.Node{}, defs: map[*jvmFunc]map[string][]jdef{},
		rets: map[*jvmFunc][]*sitter.Node{}, reads: map[jfield][]jref{}, stores: map[jfield][]jref{}, ftype: map[jfield]string{}}
	t.ftrace = newFtrace(t)
	t.warnings = st.warnings
	t.index()
	var in *jvmFunc
	for _, fn := range jp.funcs {
		n := g.Nodes[fn.id]
		if fn.file.rel == rel && n.Line <= ln && ln <= n.End && (in == nil || n.End-n.Line < g.Nodes[in.id].End-g.Nodes[in.id].Line) {
			in = fn
		}
	}
	if in != nil {
		t.jseeds(in, ln)
	}
	if len(t.fwd.order) == 0 {
		t.fieldSeeds(rel, ln)
	}
	if len(t.fwd.order) == 0 {
		return nil, "", fmt.Errorf("no value computed at %s:%d in project %s", rel, ln, seedProject.Name)
	}
	if err := t.run(ctx); err != nil {
		return nil, "", err
	}
	return t.result(newPrinter(seedProject, nil)), rel, nil
}
