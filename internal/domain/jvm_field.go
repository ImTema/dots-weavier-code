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
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
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

type jhit struct {
	from any
	how  flow
	fn   *jvmFunc
	at   *sitter.Node // where the flow happened
	via  string       // call site, for parameters
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
	jp        *jvmProject
	tests     bool
	uses      map[*jvmFunc]map[string][]*sitter.Node
	defs      map[*jvmFunc]map[string][]jdef
	rets      map[*jvmFunc][]*sitter.Node
	reads     map[jfield][]jref
	stores    map[jfield][]jref
	ftype     map[jfield]string
	fwd, back *jwalk
}

type jwalk struct {
	t     *jtrace
	back  bool
	hits  map[any]jhit
	order []any
	queue []any
}

var (
	jvmPredicates = []string{"equals", "contains", "containsKey", "containsValue", "startsWith", "endsWith", "matches",
		"compareTo", "hashCode", "size", "length", "count", "require", "check", "assert"}
	jvmMutators = []string{"add", "addAll", "put", "putAll", "putIfAbsent", "merge", "set", "offer", "push", "append", "addFirst", "addLast", "compute"}
)

func predicate(name string) bool {
	return slices.Contains(jvmPredicates, name) ||
		(strings.HasPrefix(name, "is") || strings.HasPrefix(name, "has")) && len(name) > 2 && isUpper(name[2:])
}

func same(a, b *sitter.Node) bool {
	return a != nil && b != nil && a.StartByte() == b.StartByte() && a.EndByte() == b.EndByte() && a.Type() == b.Type()
}

func line(n *sitter.Node) int { return int(n.StartPoint().Row) + 1 }

func isComment(n *sitter.Node) bool { return strings.HasSuffix(n.Type(), "comment") }

func exprs(n *sitter.Node) []*sitter.Node {
	if n == nil {
		return nil
	}
	return slices.DeleteFunc(children(n), isComment)
}

func lastExpr(n *sitter.Node) *sitter.Node {
	if es := exprs(n); len(es) > 0 {
		return es[len(es)-1]
	}
	return nil
}

func fieldName(p, n *sitter.Node) string {
	for i := 0; i < int(p.ChildCount()); i++ {
		if same(p.Child(i), n) {
			return p.FieldNameForChild(i)
		}
	}
	return ""
}

func isIdent(n *sitter.Node) bool {
	switch n.Type() {
	case "identifier", "simple_identifier", "interpolated_identifier":
		return true
	}
	return false
}

func isThis(n *sitter.Node) bool { return n.Type() == "this" || n.Type() == "this_expression" }

// exprIdent reports an identifier that reads a variable, not one that names
// something (a declaration, a member after a dot, a called function).
func exprIdent(n *sitter.Node) bool {
	if !isIdent(n) {
		return false
	}
	if n.Type() == "interpolated_identifier" {
		return true
	}
	p := n.Parent()
	if p == nil {
		return false
	}
	switch p.Type() {
	case "inferred_parameters", "scoped_identifier", "method_reference", "marker_annotation", "annotation", "enum_constant",
		"package_declaration", "import_declaration", "labeled_statement", "break_statement", "continue_statement",
		"catch_formal_parameter", "formal_parameter", "spread_parameter",
		"navigation_suffix", "variable_declaration", "parameter", "class_parameter", "function_declaration", "identifier",
		"label", "user_type", "callable_reference", "directly_assignable_expression", "lambda_parameters", "catch_block":
		return false
	case "call_expression": // foo(...): a function name
		return !same(p.NamedChild(0), n)
	case "value_argument": // name = value
		es := exprs(p)
		return !(len(es) > 1 && same(es[0], n))
	case "assignment_expression":
		return fieldName(p, n) != "left"
	}
	switch fieldName(p, n) {
	case "name", "field", "parameters", "label", "key", "type":
		return false
	}
	return true
}

// callParts splits a call: receiver (nil for none), method name or constructed
// type, and arguments with their Kotlin names; a trailing lambda is the last argument.
type callParts struct {
	recv       *sitter.Node
	name, ctor string
	args       []*sitter.Node
	names      []string
}

func (t *jtrace) parts(f *jvmFile, n *sitter.Node) (cp callParts, ok bool) {
	switch n.Type() {
	case "method_invocation":
		cp.recv, cp.name = n.ChildByFieldName("object"), f.text(n.ChildByFieldName("name"))
		cp.args = exprs(n.ChildByFieldName("arguments"))
	case "object_creation_expression":
		cp.ctor = simpleType(f.text(n.ChildByFieldName("type")))
		cp.args = exprs(n.ChildByFieldName("arguments"))
	case "call_expression":
		callee := n.NamedChild(0)
		switch {
		case callee == nil:
			return cp, false
		case callee.Type() == "simple_identifier":
			if name := f.text(callee); isUpper(name) && len(t.jp.types[name]) > 0 {
				cp.ctor = name
			} else {
				cp.name = name
			}
		case callee.Type() == "navigation_expression":
			cp.recv = callee.NamedChild(0)
			if sfx := childOf(callee, "navigation_suffix"); sfx != nil {
				cp.name = f.text(childOf(sfx, "simple_identifier"))
			}
		}
		if sfx := childOf(n, "call_suffix"); sfx != nil {
			for _, a := range exprs(childOf(sfx, "value_arguments")) {
				es := exprs(a)
				if len(es) == 0 {
					continue
				}
				name := ""
				if len(es) > 1 && es[0].Type() == "simple_identifier" {
					name = f.text(es[0])
				}
				cp.args, cp.names = append(cp.args, es[len(es)-1]), append(cp.names, name)
			}
			if l := childOf(sfx, "annotated_lambda"); l != nil {
				if ll := childOf(l, "lambda_literal"); ll != nil {
					cp.args, cp.names = append(cp.args, ll), append(cp.names, "")
				}
			}
		}
	default:
		return cp, false
	}
	for len(cp.names) < len(cp.args) {
		cp.names = append(cp.names, "")
	}
	return cp, true
}

func (f *jvmFile) text(n *sitter.Node) string {
	if n == nil {
		return ""
	}
	return n.Content(f.src)
}

func (t *jtrace) callees(fn *jvmFunc, n *sitter.Node) []*jvmFunc {
	return slices.DeleteFunc(slices.Clone(t.jp.callees[keyOf(fn.file, n)]), func(m *jvmFunc) bool { return !t.keep(m) })
}

func (t *jtrace) keep(fn *jvmFunc) bool { return t.tests || !fn.file.test }

func (t *jtrace) local(fn *jvmFunc, name string) bool {
	_, ok := t.defs[fn][name]
	return ok || slices.Contains(fn.names, name)
}

// record is the project type a call constructs, when it has components to fill:
// a Java record or a Kotlin class with a primary constructor.
func (t *jtrace) record(fn *jvmFunc, cp callParts) *jvmType {
	if cp.ctor == "" {
		return nil
	}
	for _, ty := range t.jp.pick(fn.file, cp.ctor) {
		if len(ty.comps) > 0 {
			return ty
		}
	}
	return nil
}

// compAt names the component argument i of a record construction fills.
func compAt(ty *jvmType, cp callParts, i int) string {
	if cp.names[i] != "" {
		return cp.names[i]
	}
	if i < len(ty.comps) {
		return ty.comps[i]
	}
	return ""
}

func (t *jtrace) key(ty *jvmType, name string) (jfield, bool) {
	o := t.jp.fieldOwner(ty, name)
	if o == nil {
		return "", false
	}
	k := jfield(o.pkg + "." + o.name + "." + name)
	t.ftype[k] = o.fields[name]
	return k, true
}

// member resolves name on a receiver: this, a typed value, or a type outside the
// project, whose getters (protobuf messages) are fields too.
func (t *jtrace) member(fn *jvmFunc, recv *sitter.Node, name string, getter bool) (jfield, bool) {
	if recv != nil && isThis(recv) {
		return t.key(fn.owner, name)
	}
	simple := ""
	if recv != nil {
		simple = t.jp.typeOf(fn, recv)
	} else if fn.owner != nil {
		simple = fn.owner.simple
	}
	if simple == "" {
		return "", false
	}
	tys := t.jp.pick(fn.file, simple)
	for _, ty := range tys {
		if k, ok := t.key(ty, name); ok {
			return k, true
		}
	}
	if getter && len(tys) == 0 && recv != nil && !isUpper(fn.file.text(recv)) {
		return jfield(simple + "." + name), true
	}
	return "", false
}

// getterField is the field a getter or record accessor reads: getX -> x.
func getterField(name string) (string, bool) {
	for _, p := range []string{"get", "is"} {
		if rest, ok := strings.CutPrefix(name, p); ok && rest != "" && isUpper(rest) {
			return strings.ToLower(rest[:1]) + rest[1:], true
		}
	}
	return "", false
}

// fieldRef reports the field an expression reads.
func (t *jtrace) fieldRef(fn *jvmFunc, n *sitter.Node) (jfield, bool) {
	f := fn.file
	switch n.Type() {
	case "identifier", "simple_identifier", "interpolated_identifier":
		if !exprIdent(n) || t.local(fn, f.text(n)) || fn.owner == nil {
			return "", false
		}
		return t.key(fn.owner, f.text(n))
	case "field_access":
		return t.member(fn, n.ChildByFieldName("object"), f.text(n.ChildByFieldName("field")), false)
	case "navigation_expression":
		if p := n.Parent(); p != nil && p.Type() == "call_expression" && same(p.NamedChild(0), n) {
			return "", false // a method call's callee
		}
		if sfx := childOf(n, "navigation_suffix"); sfx != nil {
			if nm := childOf(sfx, "simple_identifier"); nm != nil {
				return t.member(fn, n.NamedChild(0), f.text(nm), false)
			}
		}
	case "method_invocation", "call_expression":
		cp, ok := t.parts(f, n)
		if !ok || cp.ctor != "" || len(cp.args) > 0 || len(t.callees(fn, n)) > 0 {
			return "", false
		}
		if fld, ok := getterField(cp.name); ok {
			return t.member(fn, cp.recv, fld, true)
		}
		if cp.recv != nil && !f.kotlin { // record accessor: l.sku()
			return t.member(fn, cp.recv, cp.name, false)
		}
	}
	return "", false
}

// lambdaParams names a lambda's parameters; a Kotlin lambda without any has it.
func lambdaParams(f *jvmFile, n *sitter.Node) []*sitter.Node {
	switch n.Type() {
	case "lambda_expression":
		ps := n.ChildByFieldName("parameters")
		if ps == nil {
			return nil
		}
		if ps.Type() == "identifier" {
			return []*sitter.Node{ps}
		}
		var out []*sitter.Node
		for _, c := range children(ps) {
			if c.Type() == "identifier" {
				out = append(out, c)
			} else if nm := c.ChildByFieldName("name"); nm != nil {
				out = append(out, nm)
			}
		}
		return out
	case "lambda_literal":
		ps := childOf(n, "lambda_parameters")
		if ps == nil {
			return []*sitter.Node{n} // it
		}
		var out []*sitter.Node
		for _, v := range children(ps) {
			if nm := firstOfType(v, "simple_identifier"); nm != nil {
				out = append(out, nm)
			}
		}
		return out
	}
	return nil
}

func paramName(f *jvmFile, n *sitter.Node) string {
	if n.Type() == "lambda_literal" {
		return "it"
	}
	return f.text(n)
}

// lambdaCall is the call a lambda is passed to, and its parts.
func (t *jtrace) lambdaCall(f *jvmFile, l *sitter.Node) (*sitter.Node, callParts, bool) {
	p := l.Parent()
	for p != nil && (p.Type() == "argument_list" || p.Type() == "annotated_lambda" || p.Type() == "call_suffix" ||
		p.Type() == "value_argument" || p.Type() == "value_arguments") {
		p = p.Parent()
	}
	if p == nil {
		return nil, callParts{}, false
	}
	cp, ok := t.parts(f, p)
	return p, cp, ok
}

// index scans every kept function once: variable uses and definitions, results,
// field reads and stores.
func (t *jtrace) index() {
	fns := slices.DeleteFunc(slices.Clone(t.jp.funcs), func(fn *jvmFunc) bool { return !t.keep(fn) })
	// Field initializers run in a function of their own.
	for _, tys := range t.jp.types {
		for _, ty := range tys {
			if ty.decl == nil || !t.tests && ty.file.test {
				continue
			}
			init := &jvmFunc{id: -1, owner: ty, file: ty.file, vars: map[string]string{}}
			body := ty.decl.ChildByFieldName("body")
			if body == nil {
				body = childOf(ty.decl, "class_body")
			}
			if body == nil {
				continue
			}
			for _, m := range children(body) {
				switch m.Type() {
				case "field_declaration", "constant_declaration":
					for _, d := range children(m) {
						if v := d.ChildByFieldName("value"); d.Type() == "variable_declarator" && v != nil {
							if k, ok := t.key(ty, ty.file.text(d.ChildByFieldName("name"))); ok {
								t.stores[k] = append(t.stores[k], jref{init, v})
							}
						}
					}
				case "property_declaration":
					if v, val := kotlinProperty(m); v != nil && val != nil {
						if k, ok := t.key(ty, ty.file.text(childOf(v, "simple_identifier"))); ok {
							t.stores[k] = append(t.stores[k], jref{init, val})
						}
					}
				default:
					continue
				}
				init.body = m
				fns = append(fns, init)
				init = &jvmFunc{id: -1, owner: ty, file: ty.file, vars: map[string]string{}}
			}
		}
	}
	for _, fn := range fns {
		t.scan(fn)
	}
}

// kotlinProperty splits a property declaration into its variable and its value.
func kotlinProperty(n *sitter.Node) (v, val *sitter.Node) {
	for _, c := range exprs(n) {
		switch c.Type() {
		case "variable_declaration", "multi_variable_declaration":
			v = c
		case "binding_pattern_kind", "modifiers", "type_constraints", "property_delegate", "getter", "setter", "user_type", "nullable_type":
		default:
			if v != nil && val == nil {
				val = c
			}
		}
	}
	return v, val
}

func (t *jtrace) scan(fn *jvmFunc) {
	f := fn.file
	uses, defs := map[string][]*sitter.Node{}, map[string][]jdef{}
	t.uses[fn], t.defs[fn] = uses, defs
	def := func(name string, d jdef) { defs[name] = append(defs[name], d) }
	var walk func(n *sitter.Node, lambda bool)
	walk = func(n *sitter.Node, lambda bool) {
		switch n.Type() {
		case "lambda_expression", "lambda_literal":
			for _, p := range lambdaParams(f, n) {
				d := jdef{name: p, how: fCallback}
				if _, cp, ok := t.lambdaCall(f, n); ok {
					d.value = cp.recv // the receiver hands its elements to the lambda
				}
				def(paramName(f, p), d)
			}
			lambda = true
		case "class_body", "anonymous_function": // an anonymous class: its returns are its own
			lambda = true
		case "variable_declarator":
			if nm := n.ChildByFieldName("name"); nm != nil && n.Parent().Type() != "field_declaration" {
				def(f.text(nm), jdef{name: nm, value: n.ChildByFieldName("value"), how: fAssign, real: true})
			}
		case "property_declaration":
			if v, val := kotlinProperty(n); v != nil && !same(n, fn.body) {
				if v.Type() == "variable_declaration" {
					def(f.text(childOf(v, "simple_identifier")), jdef{name: v, value: val, how: fAssign, real: true})
				} else {
					for _, c := range children(v) {
						def(f.text(childOf(c, "simple_identifier")), jdef{name: c, value: val, how: fContainer, real: true})
					}
				}
			}
		case "enhanced_for_statement":
			if nm := n.ChildByFieldName("name"); nm != nil {
				def(f.text(nm), jdef{name: nm, value: n.ChildByFieldName("value"), how: fContainer, real: true})
			}
		case "for_statement":
			var vs []*sitter.Node
			var iter *sitter.Node
			for _, c := range exprs(n) {
				switch c.Type() {
				case "variable_declaration":
					vs = append(vs, c)
				case "multi_variable_declaration":
					vs = append(vs, children(c)...)
				case "control_structure_body":
				default:
					if iter == nil && len(vs) > 0 {
						iter = c
					}
				}
			}
			for _, v := range vs {
				def(f.text(childOf(v, "simple_identifier")), jdef{name: v, value: iter, how: fContainer, real: true})
			}
		case "catch_formal_parameter":
			if nm := n.ChildByFieldName("name"); nm != nil {
				def(f.text(nm), jdef{name: nm})
			}
		case "catch_block":
			if nm := childOf(n, "simple_identifier"); nm != nil {
				def(f.text(nm), jdef{name: nm})
			}
		case "assignment_expression":
			left, right := n.ChildByFieldName("left"), n.ChildByFieldName("right")
			if left.Type() == "identifier" && t.local(fn, f.text(left)) {
				def(f.text(left), jdef{name: left, value: right, how: fAssign, real: true})
			} else if k, ok := t.storeKey(fn, left); ok {
				t.stores[k] = append(t.stores[k], jref{fn, right})
			}
		case "assignment":
			if da := childOf(n, "directly_assignable_expression"); da != nil {
				t.assignK(fn, da, lastExpr(n), def)
			}
		case "return_statement":
			if e := lastExpr(n); e != nil && !lambda {
				t.rets[fn] = append(t.rets[fn], e)
			}
		case "jump_expression":
			if e := lastExpr(n); e != nil && !lambda && strings.HasPrefix(f.text(n), "return") {
				t.rets[fn] = append(t.rets[fn], e)
			}
		case "function_body":
			if e := lastExpr(n); e != nil && e.Type() != "statements" && same(n, fn.body) {
				t.rets[fn] = append(t.rets[fn], e) // fun f() = expr
			}
		case "method_invocation", "call_expression", "object_creation_expression":
			if cp, ok := t.parts(f, n); ok {
				if len(t.callees(fn, n)) == 0 {
					if ty := t.record(fn, cp); ty != nil {
						for i, a := range cp.args {
							if c := compAt(ty, cp, i); c != "" {
								if k, ok := t.key(ty, c); ok {
									t.stores[k] = append(t.stores[k], jref{fn, a})
								}
							}
						}
					}
					if cp.recv != nil && isIdent(cp.recv) && slices.Contains(jvmMutators, cp.name) {
						for _, a := range cp.args {
							def(f.text(cp.recv), jdef{name: cp.recv, value: a, how: fContainer})
						}
					}
				}
			}
		}
		if exprIdent(n) {
			uses[f.text(n)] = append(uses[f.text(n)], n)
		}
		if k, ok := t.fieldRef(fn, n); ok && !t.isStoreTarget(n) {
			t.reads[k] = append(t.reads[k], jref{fn, n})
		}
		for _, c := range children(n) {
			walk(c, lambda)
		}
	}
	walk(fn.body, false)
	// Defs were found in source order but a use may come first; drop uses of fields.
	for name := range uses {
		if !t.local(fn, name) {
			delete(uses, name)
		}
	}
}

// storeKey is the field a Java assignment's left side writes: this.x, a.x or a bare field.
func (t *jtrace) storeKey(fn *jvmFunc, left *sitter.Node) (jfield, bool) {
	switch left.Type() {
	case "field_access":
		return t.fieldRef(fn, left)
	case "identifier":
		return t.member(fn, nil, fn.file.text(left), false)
	}
	return "", false
}

func (t *jtrace) isStoreTarget(n *sitter.Node) bool {
	p := n.Parent()
	return p != nil && (p.Type() == "assignment_expression" && fieldName(p, n) == "left" || p.Type() == "directly_assignable_expression")
}

// assignK handles a Kotlin assignment: a local, this.x, x.y or a bare property.
func (t *jtrace) assignK(fn *jvmFunc, da, val *sitter.Node, def func(string, jdef)) {
	f := fn.file
	es := exprs(da)
	if len(es) == 1 && es[0].Type() == "simple_identifier" {
		name := f.text(es[0])
		if t.local(fn, name) {
			def(name, jdef{name: es[0], value: val, how: fAssign, real: true})
		} else if k, ok := t.member(fn, nil, name, false); ok {
			t.stores[k] = append(t.stores[k], jref{fn, val})
		}
		return
	}
	if len(es) == 2 && es[1].Type() == "navigation_suffix" {
		if k, ok := t.member(fn, es[0], f.text(childOf(es[1], "simple_identifier")), false); ok {
			t.stores[k] = append(t.stores[k], jref{fn, val})
		}
	}
}

func (t *jtrace) newWalk(back bool) *jwalk {
	return &jwalk{t: t, back: back, hits: map[any]jhit{}}
}

func (w *jwalk) taint(k, from any, how flow, fn *jvmFunc, at *sitter.Node, via string) {
	if _, ok := w.hits[k]; ok {
		return
	}
	w.hits[k] = jhit{from, how, fn, at, via}
	w.order = append(w.order, k)
	w.queue = append(w.queue, k)
}

func (w *jwalk) expr(fn *jvmFunc, n *sitter.Node, from any, how flow) {
	if n != nil {
		w.taint(jexpr{fn, keyOf(fn.file, n)}, from, how, fn, n, "")
	}
}

func (w *jwalk) run() {
	for len(w.queue) > 0 {
		k := w.queue[0]
		w.queue = w.queue[1:]
		h := w.hits[k]
		switch k := k.(type) {
		case jexpr:
			if w.back {
				w.exprBack(k, h.fn, h.at)
			} else {
				w.exprFwd(k, h.fn, h.at)
			}
		case jvar:
			if w.back {
				w.varBack(k)
			} else {
				w.varFwd(k)
			}
		case jfield:
			refs := w.t.reads[k]
			how := fRead
			if w.back {
				refs, how = w.t.stores[k], fStore
			}
			for _, r := range refs {
				w.expr(r.fn, r.n, k, how)
			}
		}
	}
}

func (w *jwalk) where(fn *jvmFunc, n *sitter.Node) string {
	return fn.file.rel + ":" + strconv.Itoa(line(n))
}

// paramNode is the declaration of m's parameter i.
func paramNode(m *jvmFunc, i int) *sitter.Node {
	for _, c := range children(m.params) {
		nm := c.ChildByFieldName("name")
		if m.file.kotlin {
			nm = childOf(c, "simple_identifier")
		}
		if nm == nil {
			nm = childOf(c, "identifier")
		}
		if nm != nil && m.file.text(nm) == m.names[i] {
			return c
		}
	}
	return m.params
}

// param taints parameter i of m; via is the call site it was entered from.
func (w *jwalk) param(m *jvmFunc, i int, from any, how flow, via string) {
	if i < 0 || i >= len(m.names) || m.names[i] == "" {
		return
	}
	w.taint(jvar{m, m.names[i]}, from, how, m, paramNode(m, i), via)
}

func (w *jwalk) varFwd(v jvar) {
	if v.name == "" {
		for _, c := range w.t.jp.callers[v.fn] {
			if w.t.keep(c.fn) {
				w.expr(c.fn, c.node, v, fReturn)
			}
		}
		return
	}
	for _, u := range w.t.uses[v.fn][v.name] {
		w.expr(v.fn, u, v, fAssign)
	}
}

func (w *jwalk) varBack(v jvar) {
	t := w.t
	if v.name == "" {
		for _, r := range t.rets[v.fn] {
			w.expr(v.fn, r, v, fReturn)
		}
		return
	}
	if i := slices.Index(v.fn.names, v.name); i >= 0 {
		for _, c := range t.jp.callers[v.fn] {
			cp, ok := t.parts(c.fn.file, c.node)
			if !ok || !t.keep(c.fn) {
				continue
			}
			for j, a := range cp.args {
				if cp.names[j] == v.name || cp.names[j] == "" && j == i {
					w.expr(c.fn, a, v, fArg)
				}
			}
		}
	}
	for _, d := range t.defs[v.fn][v.name] {
		w.expr(v.fn, d.value, v, d.how)
	}
}

// exprFwd takes a tainted expression one step to what consumes it.
func (w *jwalk) exprFwd(k jexpr, fn *jvmFunc, e *sitter.Node) {
	t, f := w.t, fn.file
	p := e.Parent()
	if p == nil {
		return
	}
	next := func(n *sitter.Node, how flow) { w.expr(fn, n, k, how) }
	local := func(name string, at *sitter.Node, how flow) {
		w.taint(jvar{fn, name}, k, how, fn, at, "")
	}
	switch p.Type() {
	case "parenthesized_expression", "postfix_expression", "elvis_expression", "spread_element":
		next(p, fAssign)
	case "cast_expression", "as_expression":
		next(p, fConvert)
	case "ternary_expression":
		if !same(e, p.ChildByFieldName("condition")) {
			next(p, fAssign)
		}
	case "binary_expression":
		switch f.text(p.ChildByFieldName("operator")) {
		case "+", "-", "*", "/", "%", "<<", ">>", ">>>", "&", "|", "^":
			next(p, fTransform)
		}
	case "unary_expression", "prefix_expression":
		if !strings.HasPrefix(f.text(p), "!") {
			next(p, fTransform)
		}
	case "additive_expression", "multiplicative_expression", "range_expression", "string_literal", "interpolated_expression":
		next(p, fTransform)
	case "statements", "control_structure_body":
		// The last expression of an if or when branch is the branch's value.
		body := p
		if p.Type() == "statements" {
			body = p.Parent()
		}
		if body == nil || body.Type() != "control_structure_body" || !same(lastExpr(p), e) {
			return
		}
		switch b := body.Parent(); {
		case b == nil:
		case b.Type() == "if_expression":
			next(b, fAssign)
		case b.Type() == "when_entry" && b.Parent() != nil:
			next(b.Parent(), fAssign)
		}
	case "array_access":
		if same(e, p.ChildByFieldName("array")) {
			next(p, fContainer)
		}
	case "indexing_expression":
		if same(e, p.NamedChild(0)) {
			next(p, fContainer)
		}
	case "field_access":
		if _, shared := t.fieldRef(fn, p); !shared && same(e, p.ChildByFieldName("object")) {
			next(p, fContainer)
		}
	case "navigation_expression":
		if !same(e, p.NamedChild(0)) {
			return
		}
		if c := p.Parent(); c != nil && c.Type() == "call_expression" && same(c.NamedChild(0), p) {
			w.callOn(k, fn, c)
		} else if _, shared := t.fieldRef(fn, p); !shared {
			next(p, fContainer)
		}
	case "method_invocation":
		if same(e, p.ChildByFieldName("object")) {
			w.callOn(k, fn, p)
		}
	case "argument_list":
		w.callArg(k, fn, p.Parent(), e)
	case "value_argument":
		if va := p.Parent(); va != nil && same(lastExpr(p), e) && va.Parent() != nil && va.Parent().Parent() != nil {
			w.callArg(k, fn, va.Parent().Parent(), e)
		}
	case "variable_declarator":
		if !same(e, p.ChildByFieldName("value")) {
			return
		}
		name := p.ChildByFieldName("name")
		if fn.id < 0 { // a field initializer
			if kf, ok := t.key(fn.owner, f.text(name)); ok {
				w.taint(kf, k, fStore, fn, p, "")
			}
		} else {
			local(f.text(name), p, fAssign)
		}
	case "property_declaration":
		v, val := kotlinProperty(p)
		if v == nil || !same(val, e) {
			return
		}
		if v.Type() == "multi_variable_declaration" {
			for _, c := range children(v) {
				local(f.text(childOf(c, "simple_identifier")), p, fContainer)
			}
			return
		}
		name := f.text(childOf(v, "simple_identifier"))
		if fn.id < 0 {
			if kf, ok := t.key(fn.owner, name); ok {
				w.taint(kf, k, fStore, fn, p, "")
			}
		} else {
			local(name, p, fAssign)
		}
	case "assignment_expression":
		if !same(e, p.ChildByFieldName("right")) {
			return
		}
		left := p.ChildByFieldName("left")
		switch {
		case left.Type() == "identifier" && t.local(fn, f.text(left)):
			local(f.text(left), p, fAssign)
		case left.Type() == "array_access":
			if a := left.ChildByFieldName("array"); isIdent(a) && t.local(fn, f.text(a)) {
				local(f.text(a), p, fContainer)
			}
		default:
			if kf, ok := t.storeKey(fn, left); ok {
				w.taint(kf, k, fStore, fn, p, "")
			}
		}
	case "assignment":
		da := childOf(p, "directly_assignable_expression")
		if da == nil || !same(lastExpr(p), e) {
			return
		}
		es := exprs(da)
		switch {
		case len(es) == 1 && es[0].Type() == "simple_identifier" && t.local(fn, f.text(es[0])):
			local(f.text(es[0]), p, fAssign)
		case len(es) == 1 && es[0].Type() == "simple_identifier":
			if kf, ok := t.member(fn, nil, f.text(es[0]), false); ok {
				w.taint(kf, k, fStore, fn, p, "")
			}
		case len(es) == 2 && es[1].Type() == "navigation_suffix":
			if kf, ok := t.member(fn, es[0], f.text(childOf(es[1], "simple_identifier")), false); ok {
				w.taint(kf, k, fStore, fn, p, "")
			}
		case len(es) == 2 && es[1].Type() == "indexing_suffix":
			if isIdent(es[0]) && t.local(fn, f.text(es[0])) {
				local(f.text(es[0]), p, fContainer)
			}
		}
	case "return_statement", "function_body":
		if slices.ContainsFunc(t.rets[fn], func(r *sitter.Node) bool { return same(r, e) }) {
			w.taint(jvar{fn, ""}, k, fAssign, fn, p, "")
		}
	case "jump_expression":
		if slices.ContainsFunc(t.rets[fn], func(r *sitter.Node) bool { return same(r, e) }) {
			w.taint(jvar{fn, ""}, k, fAssign, fn, p, "")
		}
	case "enhanced_for_statement":
		if same(e, p.ChildByFieldName("value")) {
			local(f.text(p.ChildByFieldName("name")), p, fContainer)
		}
	case "for_statement": // for (x in e)
		if e.Type() == "variable_declaration" || e.Type() == "multi_variable_declaration" || e.Type() == "control_structure_body" {
			return
		}
		for _, c := range exprs(p) {
			if c.Type() == "variable_declaration" || c.Type() == "multi_variable_declaration" {
				for _, nm := range kotlinNames(c) {
					local(f.text(nm), p, fContainer)
				}
			}
		}
	}
}

// callArg follows a tainted argument into the call it is passed to.
func (w *jwalk) callArg(k jexpr, fn *jvmFunc, call, e *sitter.Node) {
	t := w.t
	if call == nil {
		return
	}
	cp, ok := t.parts(fn.file, call)
	if !ok {
		return
	}
	i := slices.IndexFunc(cp.args, func(a *sitter.Node) bool { return same(a, e) })
	if i < 0 {
		return
	}
	if ms := t.callees(fn, call); len(ms) > 0 {
		for _, m := range ms {
			j := i
			if cp.names[i] != "" {
				j = slices.Index(m.names, cp.names[i])
			}
			w.param(m, j, k, fArg, w.where(fn, call))
		}
		return
	}
	if ty := t.record(fn, cp); ty != nil {
		if c := compAt(ty, cp, i); c != "" {
			if kf, ok := t.key(ty, c); ok {
				w.taint(kf, k, fStore, fn, call, "")
			}
		}
		return
	}
	if predicate(cp.name) {
		return
	}
	if cp.recv != nil && isIdent(cp.recv) && t.local(fn, fn.file.text(cp.recv)) && slices.Contains(jvmMutators, cp.name) {
		w.taint(jvar{fn, fn.file.text(cp.recv)}, k, fContainer, fn, call, "")
	}
	w.expr(fn, call, k, fTransform)
	w.callbacks(k, fn, cp)
}

// callOn follows a tainted receiver into a method called on it. A project
// method's this is not tracked; its fields are reached as fields.
func (w *jwalk) callOn(k jexpr, fn *jvmFunc, call *sitter.Node) {
	t := w.t
	if len(t.callees(fn, call)) > 0 {
		return
	}
	if _, shared := t.fieldRef(fn, call); shared {
		return
	}
	cp, ok := t.parts(fn.file, call)
	if !ok || predicate(cp.name) {
		return
	}
	w.expr(fn, call, k, fTransform)
	w.callbacks(k, fn, cp)
}

// callbacks guesses that a library call hands the tainted value (or its elements,
// as stream().map does) to the lambdas passed in the same call.
func (w *jwalk) callbacks(k jexpr, fn *jvmFunc, cp callParts) {
	for _, a := range cp.args {
		for _, p := range lambdaParams(fn.file, a) {
			w.taint(jvar{fn, paramName(fn.file, p)}, k, fCallback, fn, a, "")
		}
	}
}

// exprBack taints what an expression is computed from.
func (w *jwalk) exprBack(k jexpr, fn *jvmFunc, e *sitter.Node) {
	t, f := w.t, fn.file
	down := func(n *sitter.Node, how flow) { w.expr(fn, n, k, how) }
	switch e.Type() {
	case "identifier", "simple_identifier", "interpolated_identifier":
		name := f.text(e)
		if t.local(fn, name) {
			if i := slices.Index(fn.names, name); i >= 0 {
				w.param(fn, i, k, fAssign, "")
				return
			}
			w.taint(jvar{fn, name}, k, fAssign, fn, e, "")
		} else if kf, ok := t.fieldRef(fn, e); ok {
			w.taint(kf, k, fRead, fn, e, "")
		}
	case "parenthesized_expression", "postfix_expression", "elvis_expression", "interpolated_expression", "spread_element":
		for _, c := range exprs(e) {
			down(c, fAssign)
		}
	case "ternary_expression":
		down(e.ChildByFieldName("consequence"), fAssign)
		down(e.ChildByFieldName("alternative"), fAssign)
	case "if_expression":
		for _, c := range exprs(e) {
			if c.Type() == "control_structure_body" {
				w.branch(fn, k, c)
			}
		}
	case "when_expression":
		for _, c := range exprs(e) {
			if c.Type() == "when_entry" {
				if b := childOf(c, "control_structure_body"); b != nil {
					w.branch(fn, k, b)
				}
			}
		}
	case "cast_expression":
		down(e.ChildByFieldName("value"), fConvert)
	case "as_expression":
		down(e.NamedChild(0), fConvert)
	case "binary_expression":
		switch f.text(e.ChildByFieldName("operator")) {
		case "+", "-", "*", "/", "%", "<<", ">>", ">>>", "&", "|", "^":
			down(e.ChildByFieldName("left"), fTransform)
			down(e.ChildByFieldName("right"), fTransform)
		}
	case "additive_expression", "multiplicative_expression", "string_literal", "unary_expression", "prefix_expression":
		for _, c := range exprs(e) {
			down(c, fTransform)
		}
	case "array_access":
		down(e.ChildByFieldName("array"), fContainer)
	case "indexing_expression":
		down(e.NamedChild(0), fContainer)
	case "field_access", "navigation_expression":
		if kf, ok := t.fieldRef(fn, e); ok {
			w.taint(kf, k, fRead, fn, e, "")
		} else if r := e.NamedChild(0); e.Type() == "navigation_expression" {
			down(r, fContainer)
		} else {
			down(e.ChildByFieldName("object"), fContainer)
		}
	case "method_invocation", "call_expression", "object_creation_expression":
		w.callBack(k, fn, e)
	}
}

func (w *jwalk) branch(fn *jvmFunc, k jexpr, body *sitter.Node) {
	e := lastExpr(body)
	if e != nil && e.Type() == "statements" {
		e = lastExpr(e)
	}
	if e != nil {
		w.expr(fn, e, k, fAssign)
	}
}

// callBack taints what a call's result is computed from.
func (w *jwalk) callBack(k jexpr, fn *jvmFunc, call *sitter.Node) {
	t, f := w.t, fn.file
	if ms := t.callees(fn, call); len(ms) > 0 {
		for _, m := range ms {
			w.taint(jvar{m, ""}, k, fReturn, fn, call, "")
		}
		return
	}
	if kf, ok := t.fieldRef(fn, call); ok {
		w.taint(kf, k, fRead, fn, call, "")
		return
	}
	cp, ok := t.parts(f, call)
	if !ok {
		return
	}
	if t.record(fn, cp) != nil {
		for _, a := range cp.args {
			w.expr(fn, a, k, fContainer)
		}
		return
	}
	// A library call derives its result from its receiver (id.trim()) or its one
	// data argument (UUID.fromString(s)); with several inputs the call is the origin.
	args := cp.args
	if cp.recv != nil && !(isIdent(cp.recv) && isUpper(f.text(cp.recv)) && !t.local(fn, f.text(cp.recv))) {
		args = []*sitter.Node{cp.recv}
	}
	var data []*sitter.Node
	for _, a := range args {
		switch {
		case strings.HasSuffix(a.Type(), "literal") && a.Type() != "string_literal" || a.Type() == "lambda_expression" ||
			a.Type() == "method_reference" || a.Type() == "callable_reference" || a.Type() == "true" || a.Type() == "false":
		case a.Type() == "string_literal" && childOf(a, "interpolated_identifier") == nil && childOf(a, "interpolated_expression") == nil:
		default:
			data = append(data, a)
		}
	}
	if len(data) == 1 {
		w.expr(fn, data[0], k, fTransform)
	}
}

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
		t.fwd.taint(s.fwd, nil, fSeed, fn, s.fnode, "")
		t.back.taint(s.back, nil, fSeed, fn, s.bnode, "")
		if s.read != "" {
			t.fwd.taint(s.read, s.fwd, fRead, fn, s.bnode, "")
		}
	}
}

// kotlinNames lists the names a Kotlin variable or destructuring declaration binds.
func kotlinNames(n *sitter.Node) []*sitter.Node {
	if n.Type() == "variable_declaration" {
		if nm := childOf(n, "simple_identifier"); nm != nil {
			return []*sitter.Node{nm}
		}
		return nil
	}
	var out []*sitter.Node
	for _, c := range children(n) {
		out = append(out, kotlinNames(c)...)
	}
	return out
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
						t.fwd.taint(k, nil, fSeed, nil, n, "")
						t.back.taint(k, nil, fSeed, nil, n, "")
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

// jnode is one step of a tree: a source line in a function, or a field.
type jnode struct {
	fn       *jvmFunc
	line     int
	field    jfield
	how      flow
	typ, via string
	kids     []*jnode
}

func (w *jwalk) tree() []*jnode {
	at := map[any]*jnode{}
	type lineKey struct {
		fn   *jvmFunc
		file *jvmFile
		line int
	}
	lines := map[lineKey]*jnode{}
	fields := map[jfield]*jnode{}
	var roots []*jnode
	// A plain read of a variable that leads nowhere (x == null, x.isBlank()) only
	// describes the value; the Go walk drops those by their bool type.
	next := map[any][]any{}
	for _, k := range w.order {
		next[w.hits[k].from] = append(next[w.hits[k].from], k)
	}
	live := map[any]bool{}
	for _, k := range slices.Backward(w.order) { // kids come after their parent
		h := w.hits[k]
		_, expr := k.(jexpr)
		live[k] = !expr || h.how != fAssign || h.from == nil || slices.ContainsFunc(next[k], func(n any) bool { return live[n] })
	}
	for _, k := range w.order {
		h := w.hits[k]
		if !live[k] {
			continue
		}
		parent := at[h.from]
		var n *jnode
		created := false
		if f, ok := k.(jfield); ok {
			if n = fields[f]; n == nil {
				n = &jnode{field: f, how: h.how, typ: w.t.ftype[f]}
				fields[f], created = n, true
			}
		} else {
			lk := lineKey{h.fn, h.fn.file, line(h.at)}
			if n = lines[lk]; n == nil {
				n = &jnode{fn: h.fn, line: lk.line, how: h.how, via: h.via}
				lines[lk], created = n, true
			}
			if v, ok := k.(jvar); ok && n.typ == "" && v.name != "" {
				n.typ = v.fn.vars[v.name]
			}
		}
		at[k] = n
		switch {
		case !created:
		case parent == nil:
			roots = append(roots, n)
		default:
			parent.kids = append(parent.kids, n)
		}
	}
	return roots
}

func (t *jtrace) step(n *jnode) *FieldStep {
	s := &FieldStep{Kind: n.how.String(), Type: n.typ, Via: n.via}
	if n.field != "" {
		name := string(n.field)
		if i := strings.LastIndex(name[:max(0, strings.LastIndex(name, "."))], "."); i >= 0 {
			name = name[i+1:] // Type.field
		}
		s.Kind, s.Name = "field", name
	} else {
		ls := strings.Split(string(n.fn.file.src), "\n")
		text := func(i int) string {
			if i < 1 || i > len(ls) {
				return ""
			}
			return strings.TrimSpace(ls[i-1])
		}
		s.Loc, s.Code, s.Before, s.After = n.fn.file.rel+":"+strconv.Itoa(n.line), text(n.line), text(n.line-1), text(n.line+1)
		if n.fn.id >= 0 {
			s.Fn = t.jp.g.Nodes[n.fn.id].Name
		} else if n.fn.owner != nil {
			s.Fn = n.fn.owner.name
		}
	}
	kids := slices.Clone(n.kids)
	slices.SortStableFunc(kids, func(a, b *jnode) int {
		fa, fb := "", ""
		if a.fn != nil {
			fa = a.fn.file.rel
		}
		if b.fn != nil {
			fb = b.fn.file.rel
		}
		return cmp.Or(cmp.Compare(fa, fb), cmp.Compare(a.line, b.line))
	})
	for _, k := range kids {
		s.Kids = append(s.Kids, t.step(k))
	}
	return s
}

// traceJVMField traces the value computed at rel:line in a Java or Kotlin project.
func traceJVMField(home project, file string, ln int, tests bool) (*FieldTrace, string, error) {
	rel, err := filepath.Rel(home.dir, file)
	if err != nil {
		return nil, "", err
	}
	rel = filepath.ToSlash(rel)
	g := &graph{ids: map[string]int32{}, Impls: map[string][]int32{}}
	jp, _, err := loadJVM(g, home)
	if err != nil {
		return nil, "", err
	}
	t := &jtrace{jp: jp, tests: tests, uses: map[*jvmFunc]map[string][]*sitter.Node{}, defs: map[*jvmFunc]map[string][]jdef{},
		rets: map[*jvmFunc][]*sitter.Node{}, reads: map[jfield][]jref{}, stores: map[jfield][]jref{}, ftype: map[jfield]string{}}
	t.fwd, t.back = t.newWalk(false), t.newWalk(true)
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
		return nil, "", fmt.Errorf("no value computed at %s:%d in project %s", rel, ln, home.Name)
	}
	t.back.run()
	t.fwd.run()
	r := &FieldTrace{}
	for _, n := range t.back.tree() {
		r.Origin = append(r.Origin, t.step(n))
	}
	for _, n := range t.fwd.tree() {
		r.Uses = append(r.Uses, t.step(n))
	}
	return r, rel, nil
}
