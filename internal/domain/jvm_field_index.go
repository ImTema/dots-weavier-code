package domain

import (
	"slices"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

func (t *jtrace) callees(fn *jvmFunc, n *sitter.Node) []*jvmFunc {
	return slices.DeleteFunc(slices.Clone(t.jp.callees[keyOf(fn.file, n)]), func(m *jvmFunc) bool { return !t.keep(m) })
}

func (t *jtrace) keep(fn *jvmFunc) bool { return t.tests || !fn.file.test }

func (t *jtrace) local(fn *jvmFunc, name string) bool {
	_, ok := t.defs[fn][name]
	return ok || slices.Contains(fn.names, name)
}

// record is the Java record or Kotlin primary-constructor class a call constructs, if any.
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

// member resolves name on this, a typed value, or a type outside the project whose getters are fields too.
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

// index scans every kept function once for variable uses and definitions, results, field reads and stores.
func (t *jtrace) index() {
	fns := slices.DeleteFunc(slices.Clone(t.jp.funcs), func(fn *jvmFunc) bool { return !t.keep(fn) })
	// Field initializers run in a function of their own.
	for _, tys := range t.jp.types {
		for _, ty := range tys {
			if ty.decl == nil || !t.tests && ty.file.test {
				continue
			}
			body := ty.decl.ChildByFieldName("body")
			if body == nil {
				body = childOf(ty.decl, "class_body")
			}
			if body == nil {
				continue
			}
			for _, m := range children(body) {
				init := &jvmFunc{id: -1, owner: ty, file: ty.file, vars: map[string]string{}, body: m}
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
				fns = append(fns, init)
			}
		}
	}
	for _, fn := range fns {
		t.scan(fn)
	}
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
		case "return_statement", "jump_expression":
			if e := lastExpr(n); e != nil && !lambda && (n.Type() == "return_statement" || strings.HasPrefix(f.text(n), "return")) {
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
