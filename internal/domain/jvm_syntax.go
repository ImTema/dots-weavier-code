package domain

import (
	"slices"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
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

// exprIdent reports an identifier that reads a variable rather than naming a declaration, member or called function.
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

// callParts splits a call into receiver, method or constructed type, and arguments with their Kotlin names, a trailing lambda last.
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
