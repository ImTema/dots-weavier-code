package domain

import (
	"slices"
	"strconv"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

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
	store := func(kf jfield, ok bool) {
		if ok {
			w.taint(kf, k, fStore, fn, p, "")
		}
	}
	// declare is a declaration: a field initializer stores, a local assigns.
	declare := func(name string) {
		if fn.id < 0 {
			store(t.key(fn.owner, name))
		} else {
			local(name, p, fAssign)
		}
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
		if slices.Contains(arith, f.text(p.ChildByFieldName("operator"))) {
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
		declare(f.text(p.ChildByFieldName("name")))
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
		declare(f.text(childOf(v, "simple_identifier")))
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
			store(t.storeKey(fn, left))
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
			store(t.member(fn, nil, f.text(es[0]), false))
		case len(es) == 2 && es[1].Type() == "navigation_suffix":
			store(t.member(fn, es[0], f.text(childOf(es[1], "simple_identifier")), false))
		case len(es) == 2 && es[1].Type() == "indexing_suffix":
			if isIdent(es[0]) && t.local(fn, f.text(es[0])) {
				local(f.text(es[0]), p, fContainer)
			}
		}
	case "return_statement", "function_body", "jump_expression":
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
		if slices.Contains(arith, f.text(e.ChildByFieldName("operator"))) {
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
