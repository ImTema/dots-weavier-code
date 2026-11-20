// JVM extractor: Java and Kotlin projects (Maven or Gradle) read from source
// with tree-sitter, no build and no classpath (ADR 0007).
package domain

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/kotlin"
)

type jvmFile struct {
	rel, pkg string
	kotlin   bool
	test     bool
	src      []byte
	imports  map[string]string // simple name -> fully qualified name
}

// jvmType is a class, interface, enum, record or Kotlin object.
type jvmType struct {
	pkg, name string // name is nested: Outer.Inner
	simple    string
	iface     bool
	supers    []string          // simple names as written; may be types outside the project
	fields    map[string]string // field or property -> simple type name
	methods   map[string][]*jvmFunc
	outer     *jvmType
	file      *jvmFile
}

type jvmFunc struct {
	id     int32
	arity  int
	owner  *jvmType // nil: Kotlin top-level function
	file   *jvmFile
	params *sitter.Node
	body   *sitter.Node
	vars   map[string]string // parameter or local -> simple type name
}

type jvmProject struct {
	g     *graph
	p     project
	types map[string][]*jvmType // by simple name
	subs  map[string][]*jvmType // by the simple name of a declared supertype
	top   map[string][]*jvmFunc // Kotlin top-level functions by name
	funcs []*jvmFunc
}

// jvmSkip names folders that hold build output or tool state, not sources.
func jvmSkip(name string) bool {
	return strings.HasPrefix(name, ".") || name == "target" || name == "build" || name == "out" || name == "node_modules"
}

func extractJVM(g *graph, p project) (st loadStats, err error) {
	t0 := time.Now()
	jp := &jvmProject{g: g, p: p, types: map[string][]*jvmType{}, subs: map[string][]*jvmType{}, top: map[string][]*jvmFunc{}}
	jparser, kparser := sitter.NewParser(), sitter.NewParser()
	jparser.SetLanguage(java.GetLanguage())
	kparser.SetLanguage(kotlin.GetLanguage())
	var protos []string
	pkgs := map[string]bool{}
	err = filepath.WalkDir(p.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != p.dir && (jvmSkip(d.Name()) || langOf(path) != "") { // nested projects are modules
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(path)
		if ext == ".proto" {
			protos = append(protos, path)
		}
		if ext != ".java" && ext != ".kt" {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(p.dir, path)
		rel = filepath.ToSlash(rel)
		f := &jvmFile{rel: rel, kotlin: ext == ".kt", src: src, imports: map[string]string{},
			test: strings.Contains("/"+rel, "/src/test")} // Maven and Gradle test source sets: src/test, src/testFixtures, ...
		ps := jparser
		if f.kotlin {
			ps = kparser
		}
		tree, err := ps.ParseCtx(context.Background(), nil, src)
		if err != nil {
			st.pkgErrs++
			return nil
		}
		root := tree.RootNode()
		if root.HasError() {
			st.illTyped++ // still read: tree-sitter recovers around the broken part
		}
		if f.kotlin {
			jp.kotlinFile(f, root)
		} else {
			jp.javaFile(f, root)
		}
		pkgs[f.pkg] = true
		return nil
	})
	if err != nil {
		return st, err
	}
	st.pkgs, st.funcs, st.load = len(pkgs), len(jp.funcs), time.Since(t0)
	t1 := time.Now()
	for _, fn := range jp.funcs {
		jp.calls(fn)
	}
	jp.grpcHandlers(protos)
	st.analyze = time.Since(t1)
	return st, nil
}

func (jp *jvmProject) text(f *jvmFile, n *sitter.Node) string { return n.Content(f.src) }

// simpleType reduces a written type (a.b.C<D>?, C[]) to its simple name C.
func simpleType(s string) string {
	s, _, _ = strings.Cut(s, "<")
	s = strings.Trim(s, "?[] \t\n")
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	return s
}

func children(n *sitter.Node) []*sitter.Node {
	out := make([]*sitter.Node, 0, n.NamedChildCount())
	for i := 0; i < int(n.NamedChildCount()); i++ {
		out = append(out, n.NamedChild(i))
	}
	return out
}

func childOf(n *sitter.Node, typ string) *sitter.Node {
	for _, c := range children(n) {
		if c.Type() == typ {
			return c
		}
	}
	return nil
}

func (jp *jvmProject) addType(f *jvmFile, name string, outer *jvmType) *jvmType {
	t := &jvmType{pkg: f.pkg, name: name, simple: name, fields: map[string]string{}, methods: map[string][]*jvmFunc{}, outer: outer, file: f}
	if outer != nil {
		t.name = outer.name + "." + name
	}
	jp.types[name] = append(jp.types[name], t)
	return t
}

func (jp *jvmProject) addSuper(t *jvmType, s string) {
	if s = simpleType(s); s != "" {
		t.supers = append(t.supers, s)
		jp.subs[s] = append(jp.subs[s], t)
	}
}

// addFunc makes a node for a function with a body; abstract methods have none,
// the same as Go interface methods.
func (jp *jvmProject) addFunc(f *jvmFile, owner *jvmType, name string, decl, params, body *sitter.Node) {
	if body == nil {
		return
	}
	display, qual := name, f.pkg+"."+name
	if owner != nil {
		display = owner.name + "." + name
		qual = owner.pkg + "." + display
	}
	fn := &jvmFunc{owner: owner, file: f, params: params, body: body, vars: map[string]string{}}
	if params != nil {
		for _, c := range children(params) {
			if c.Type() == "formal_parameter" || c.Type() == "spread_parameter" || c.Type() == "parameter" || c.Type() == "class_parameter" {
				fn.arity++
			}
		}
	}
	// Identity is the qualified name (ADR 0003); the line only splits overloads of one arity.
	id := jp.p.Name + "|" + qual + "/" + strconv.Itoa(fn.arity)
	if _, dup := jp.g.ids[id]; dup {
		id += "@" + strconv.Itoa(int(decl.StartPoint().Row)+1)
	}
	fn.id = jp.g.add(id, node{Project: jp.p.Name, File: f.rel, Name: display,
		Line: int(decl.StartPoint().Row) + 1, End: int(decl.EndPoint().Row) + 1, Test: f.test})
	jp.funcs = append(jp.funcs, fn)
	if owner != nil {
		owner.methods[name] = append(owner.methods[name], fn)
	} else {
		bare := name[strings.LastIndex(name, ".")+1:] // an extension function is called by its bare name
		jp.top[bare] = append(jp.top[bare], fn)
	}
}

// ---- Java ----

func (jp *jvmProject) javaFile(f *jvmFile, root *sitter.Node) {
	for _, c := range children(root) {
		switch c.Type() {
		case "package_declaration":
			if n := c.NamedChild(0); n != nil {
				f.pkg = jp.text(f, n)
			}
		case "import_declaration":
			s := strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(jp.text(f, c), "import"), " static")), ";")
			s = strings.TrimSpace(s)
			if !strings.HasSuffix(s, "*") {
				f.imports[s[strings.LastIndex(s, ".")+1:]] = s
			}
		default:
			jp.javaDecl(f, c, nil)
		}
	}
}

func (jp *jvmProject) javaDecl(f *jvmFile, n *sitter.Node, outer *jvmType) {
	switch n.Type() {
	case "class_declaration", "interface_declaration", "enum_declaration", "record_declaration", "annotation_type_declaration":
	default:
		return
	}
	name := n.ChildByFieldName("name")
	if name == nil {
		return
	}
	t := jp.addType(f, jp.text(f, name), outer)
	t.iface = n.Type() == "interface_declaration" || n.Type() == "annotation_type_declaration"
	for _, c := range children(n) {
		switch c.Type() {
		case "superclass", "super_interfaces", "extends_interfaces":
			for _, s := range children(c) {
				if s.Type() == "type_list" {
					for _, ty := range children(s) {
						jp.addSuper(t, jp.text(f, ty))
					}
				} else {
					jp.addSuper(t, jp.text(f, s))
				}
			}
		}
	}
	if ps := n.ChildByFieldName("parameters"); ps != nil && n.Type() == "record_declaration" {
		for _, p := range children(ps) {
			if ty, nm := p.ChildByFieldName("type"), p.ChildByFieldName("name"); ty != nil && nm != nil {
				t.fields[jp.text(f, nm)] = simpleType(jp.text(f, ty))
			}
		}
	}
	body := n.ChildByFieldName("body")
	if body == nil {
		return
	}
	members := children(body)
	if d := childOf(body, "enum_body_declarations"); d != nil {
		members = append(members, children(d)...)
	}
	for _, m := range members {
		switch m.Type() {
		case "field_declaration", "constant_declaration":
			ty := m.ChildByFieldName("type")
			for _, d := range children(m) {
				if d.Type() == "variable_declarator" && ty != nil {
					if nm := d.ChildByFieldName("name"); nm != nil {
						t.fields[jp.text(f, nm)] = simpleType(jp.text(f, ty))
					}
				}
			}
		case "method_declaration":
			if nm := m.ChildByFieldName("name"); nm != nil {
				jp.addFunc(f, t, jp.text(f, nm), m, m.ChildByFieldName("parameters"), m.ChildByFieldName("body"))
			}
		case "constructor_declaration", "compact_constructor_declaration":
			jp.addFunc(f, t, "<init>", m, m.ChildByFieldName("parameters"), m.ChildByFieldName("body"))
		default:
			jp.javaDecl(f, m, t)
		}
	}
}

// ---- Kotlin ----
// The Kotlin grammar has no field names, so children are found by type.

func (jp *jvmProject) kotlinFile(f *jvmFile, root *sitter.Node) {
	for _, c := range children(root) {
		switch c.Type() {
		case "package_header":
			if n := childOf(c, "identifier"); n != nil {
				f.pkg = jp.text(f, n)
			}
		case "import_list":
			for _, h := range children(c) {
				if id := childOf(h, "identifier"); id != nil && childOf(h, "wildcard_import") == nil {
					s := jp.text(f, id)
					local := s[strings.LastIndex(s, ".")+1:]
					if a := childOf(h, "import_alias"); a != nil {
						local = strings.TrimSpace(strings.TrimPrefix(jp.text(f, a), "as"))
					}
					f.imports[local] = s
				}
			}
		default:
			jp.kotlinMember(f, c, nil)
		}
	}
}

func (jp *jvmProject) kotlinMember(f *jvmFile, n *sitter.Node, owner *jvmType) {
	switch n.Type() {
	case "class_declaration", "object_declaration":
		name := childOf(n, "type_identifier")
		if name == nil {
			return
		}
		t := jp.addType(f, jp.text(f, name), owner)
		for i := 0; i < int(n.ChildCount()); i++ {
			if n.Child(i).Type() == "interface" {
				t.iface = true
			}
		}
		for _, c := range children(n) {
			switch c.Type() {
			case "delegation_specifier":
				if u := firstOfType(c, "user_type"); u != nil {
					jp.addSuper(t, jp.text(f, u))
				}
			case "primary_constructor":
				for _, p := range children(c) {
					if p.Type() == "class_parameter" && childOf(p, "binding_pattern_kind") != nil {
						if nm, ty := childOf(p, "simple_identifier"), typeChild(p); nm != nil && ty != nil {
							t.fields[jp.text(f, nm)] = simpleType(jp.text(f, ty))
						}
					}
				}
			case "class_body", "enum_class_body":
				for _, m := range children(c) {
					jp.kotlinMember(f, m, t)
				}
			}
		}
	case "companion_object":
		// Companion members are called through the class name, like statics.
		if body := childOf(n, "class_body"); body != nil && owner != nil {
			for _, m := range children(body) {
				jp.kotlinMember(f, m, owner)
			}
		}
	case "function_declaration":
		nm := childOf(n, "simple_identifier")
		if nm == nil {
			return
		}
		name := jp.text(f, nm)
		// An extension function's receiver type comes before its name.
		if c := n.NamedChild(0); c != nil && owner == nil && (c.Type() == "user_type" || c.Type() == "nullable_type") && c.StartByte() < nm.StartByte() {
			name = simpleType(jp.text(f, c)) + "." + name
		}
		jp.addFunc(f, owner, name, n, childOf(n, "function_value_parameters"), childOf(n, "function_body"))
	case "secondary_constructor":
		jp.addFunc(f, owner, "<init>", n, childOf(n, "function_value_parameters"), childOf(n, "statements"))
	case "property_declaration":
		if owner != nil {
			if v := childOf(n, "variable_declaration"); v != nil {
				if nm := childOf(v, "simple_identifier"); nm != nil {
					if ty := kotlinVarType(f, v, n); ty != "" {
						owner.fields[jp.text(f, nm)] = ty
					}
				}
			}
		}
	}
}

func firstOfType(n *sitter.Node, typ string) *sitter.Node {
	if n.Type() == typ {
		return n
	}
	for _, c := range children(n) {
		if r := firstOfType(c, typ); r != nil {
			return r
		}
	}
	return nil
}

func typeChild(n *sitter.Node) *sitter.Node {
	for _, c := range children(n) {
		if c.Type() == "user_type" || c.Type() == "nullable_type" {
			return c
		}
	}
	return nil
}

// kotlinVarType is a property's declared type, or the class it is constructed
// from (val x = Foo(...)), or "".
func kotlinVarType(f *jvmFile, v, decl *sitter.Node) string {
	if ty := typeChild(v); ty != nil {
		return simpleType(ty.Content(f.src))
	}
	for _, c := range children(decl) {
		if c.Type() != "call_expression" {
			continue
		}
		for c.Type() == "call_expression" { // Foo(a) { ... } nests one call in another
			c = c.NamedChild(0)
		}
		if c.Type() == "simple_identifier" && isUpper(c.Content(f.src)) {
			return c.Content(f.src)
		}
	}
	return ""
}

func isUpper(s string) bool {
	for _, r := range s {
		return unicode.IsUpper(r)
	}
	return false
}

// ---- calls ----

func (jp *jvmProject) calls(fn *jvmFunc) {
	f := fn.file
	if fn.params != nil {
		for _, p := range children(fn.params) {
			if f.kotlin {
				if nm, ty := childOf(p, "simple_identifier"), typeChild(p); nm != nil && ty != nil {
					fn.vars[jp.text(f, nm)] = simpleType(jp.text(f, ty))
				}
			} else if nm, ty := p.ChildByFieldName("name"), p.ChildByFieldName("type"); nm != nil && ty != nil {
				fn.vars[jp.text(f, nm)] = simpleType(jp.text(f, ty))
			}
		}
	}
	// ponytail: one flat scope per function and lambdas folded into their
	// enclosing function; split them into nodes if traces need that precision.
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		switch n.Type() {
		case "local_variable_declaration":
			ty := simpleType(jp.text(f, n.ChildByFieldName("type")))
			for _, d := range children(n) {
				if d.Type() != "variable_declarator" {
					continue
				}
				t := ty
				if v := d.ChildByFieldName("value"); t == "var" && v != nil && v.Type() == "object_creation_expression" {
					t = simpleType(jp.text(f, v.ChildByFieldName("type")))
				}
				if nm := d.ChildByFieldName("name"); nm != nil && t != "var" {
					fn.vars[jp.text(f, nm)] = t
				}
			}
		case "enhanced_for_statement":
			if nm, ty := n.ChildByFieldName("name"), n.ChildByFieldName("type"); nm != nil && ty != nil {
				fn.vars[jp.text(f, nm)] = simpleType(jp.text(f, ty))
			}
		case "property_declaration":
			if v := childOf(n, "variable_declaration"); v != nil {
				if nm := childOf(v, "simple_identifier"); nm != nil {
					if ty := kotlinVarType(f, v, n); ty != "" {
						fn.vars[jp.text(f, nm)] = ty
					}
				}
			}
		case "method_invocation":
			name := jp.text(f, n.ChildByFieldName("name"))
			arity := len(children(n.ChildByFieldName("arguments")))
			if obj := n.ChildByFieldName("object"); obj != nil {
				jp.linkMember(fn, jp.typeOf(fn, obj), name, arity)
			} else {
				jp.linkLocal(fn, name, arity)
			}
		case "object_creation_expression":
			if ty := n.ChildByFieldName("type"); ty != nil {
				jp.linkMember(fn, simpleType(jp.text(f, ty)), "<init>", len(children(n.ChildByFieldName("arguments"))))
			}
		case "call_expression":
			callee, suffix := n.NamedChild(0), childOf(n, "call_suffix")
			arity := 0
			if suffix != nil {
				if va := childOf(suffix, "value_arguments"); va != nil {
					arity = len(children(va))
				}
				if childOf(suffix, "annotated_lambda") != nil {
					arity++
				}
			}
			switch {
			case callee == nil:
			case callee.Type() == "simple_identifier":
				name := jp.text(f, callee)
				if isUpper(name) && len(jp.types[name]) > 0 {
					jp.linkMember(fn, name, "<init>", arity)
				} else {
					jp.linkLocal(fn, name, arity)
				}
			case callee.Type() == "navigation_expression":
				if sfx := childOf(callee, "navigation_suffix"); sfx != nil {
					if nm := childOf(sfx, "simple_identifier"); nm != nil {
						recv := jp.typeOf(fn, callee.NamedChild(0))
						jp.linkMember(fn, recv, jp.text(f, nm), arity)
						jp.ktorRequest(fn, recv, jp.text(f, nm), suffix)
					}
				}
			}
		}
		for _, c := range children(n) {
			walk(c)
		}
	}
	walk(fn.body)
}

// typeOf is the simple type name of a receiver expression, or "" when it
// cannot be told from declarations alone (a chained call, a library value).
func (jp *jvmProject) typeOf(fn *jvmFunc, n *sitter.Node) string {
	f := fn.file
	switch n.Type() {
	case "this", "this_expression":
		if fn.owner != nil {
			return fn.owner.simple
		}
	case "field_access":
		if o := n.ChildByFieldName("object"); o != nil && o.Type() == "this" {
			return jp.field(fn.owner, jp.text(f, n.ChildByFieldName("field")))
		}
	case "identifier", "simple_identifier":
		s := jp.text(f, n)
		if t, ok := fn.vars[s]; ok {
			return t
		}
		if t := jp.field(fn.owner, s); t != "" {
			return t
		}
		if isUpper(s) {
			return s // a type: static method, Kotlin object or companion
		}
	}
	return ""
}

// field finds a field's type on t, its project supertypes or its outer classes.
func (jp *jvmProject) field(t *jvmType, name string) string {
	seen := map[*jvmType]bool{}
	var find func(t *jvmType) string
	find = func(t *jvmType) string {
		if t == nil || seen[t] {
			return ""
		}
		seen[t] = true
		if ty, ok := t.fields[name]; ok {
			return ty
		}
		for _, s := range t.supers {
			for _, st := range jp.pick(t.file, s) {
				if ty := find(st); ty != "" {
					return ty
				}
			}
		}
		return find(t.outer)
	}
	return find(t)
}

// pick narrows the project types of one simple name to those the file can mean:
// the imported one, else those in its package, else all of them.
func (jp *jvmProject) pick(f *jvmFile, simple string) []*jvmType {
	ts := jp.types[simple]
	if len(ts) < 2 {
		return ts
	}
	if fq, ok := f.imports[simple]; ok {
		for _, t := range ts {
			if t.pkg+"."+t.name == fq {
				return []*jvmType{t}
			}
		}
	}
	var same []*jvmType
	for _, t := range ts {
		if t.pkg == f.pkg {
			same = append(same, t)
		}
	}
	if len(same) > 0 {
		return same
	}
	return ts
}

func byArity(fs []*jvmFunc, n int) []*jvmFunc {
	var out []*jvmFunc
	for _, f := range fs {
		if f.arity == n {
			out = append(out, f)
		}
	}
	if len(out) == 0 { // varargs, Kotlin default arguments
		return fs
	}
	return out
}

// inherited finds a method on t or the nearest project supertype that has it.
func (jp *jvmProject) inherited(t *jvmType, name string, seen map[*jvmType]bool) []*jvmFunc {
	if t == nil || seen[t] {
		return nil
	}
	seen[t] = true
	if ms := t.methods[name]; len(ms) > 0 {
		return ms
	}
	for _, s := range t.supers {
		for _, st := range jp.pick(t.file, s) {
			if ms := jp.inherited(st, name, seen); len(ms) > 0 {
				return ms
			}
		}
	}
	return nil
}

// overrides collects name on every project subtype of simple, transitively.
// Supertypes are matched by simple name, so a type whose own declaration did
// not parse still reaches its implementers.
func (jp *jvmProject) overrides(simple, name string, seen map[*jvmType]bool, out *[]*jvmFunc) {
	for _, s := range jp.subs[simple] {
		if seen[s] {
			continue
		}
		seen[s] = true
		*out = append(*out, s.methods[name]...)
		jp.overrides(s.simple, name, seen, out)
	}
}

// linkMember links a call of name on a receiver of type simple. A method found
// on a class is a direct call; anything that may run instead of it, every
// override in a subtype and every implementation behind an interface, is a guess:
// no value tracking says which concrete type reaches the call (ADR 0005).
func (jp *jvmProject) linkMember(fn *jvmFunc, simple, name string, arity int) {
	if simple == "" {
		return
	}
	hit := map[*jvmFunc]bool{}
	for _, t := range jp.pick(fn.file, simple) {
		if t.iface {
			continue
		}
		for _, m := range byArity(jp.inherited(t, name, map[*jvmType]bool{}), arity) {
			hit[m] = true
			jp.g.link(fn.id, m.id, direct, "")
		}
	}
	if name == "<init>" {
		return
	}
	var subs []*jvmFunc
	jp.overrides(simple, name, map[*jvmType]bool{}, &subs)
	for _, m := range byArity(subs, arity) {
		if !hit[m] {
			jp.g.link(fn.id, m.id, guess, "")
		}
	}
}

// linkLocal links an unqualified call: a method of the enclosing class chain,
// else a Kotlin top-level function in the same package or imported.
func (jp *jvmProject) linkLocal(fn *jvmFunc, name string, arity int) {
	for t := fn.owner; t != nil; t = t.outer {
		if ms := jp.inherited(t, name, map[*jvmType]bool{}); len(ms) > 0 {
			jp.linkMember(fn, t.simple, name, arity)
			return
		}
	}
	var hits []*jvmFunc
	for _, top := range jp.top[name] {
		if top.file.pkg == fn.file.pkg || fn.file.imports[name] == top.file.pkg+"."+name {
			hits = append(hits, top)
		}
	}
	for _, m := range byArity(hits, arity) {
		jp.g.link(fn.id, m.id, direct, "")
	}
}

var (
	protoComment = regexp.MustCompile(`(?s)//[^\n]*|/\*.*?\*/`)
	protoPackage = regexp.MustCompile(`\bpackage\s+([\w.]+)\s*;`)
	protoService = regexp.MustCompile(`\bservice\s+(\w+)\s*\{`)
	protoRPC     = regexp.MustCompile(`\brpc\s+(\w+)\s*\(`)
)

// grpcHandlers registers the project's gRPC server methods under their full
// method names, so join links clients in any project to them. A server class
// is found by its supertype, generated from a .proto in the project: X
// (Quarkus, Mutiny), XImplBase (grpc-java) or XCoroutineImplBase (grpc-kotlin).
// ponytail: protos are read from this project only; also read dependency
// jars when a server's contract lives in a library.
func (jp *jvmProject) grpcHandlers(protos []string) {
	for _, path := range protos {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		src := protoComment.ReplaceAllString(string(b), "")
		pkg := ""
		if m := protoPackage.FindStringSubmatch(src); m != nil {
			pkg = m[1] + "."
		}
		svcs := protoService.FindAllStringSubmatchIndex(src, -1)
		for i, s := range svcs {
			end := len(src)
			if i+1 < len(svcs) {
				end = svcs[i+1][0]
			}
			svc := src[s[2]:s[3]]
			for _, r := range protoRPC.FindAllStringSubmatch(src[s[1]:end], -1) {
				full := "/" + pkg + svc + "/" + r[1]
				method := strings.ToLower(r[1][:1]) + r[1][1:]
				for _, base := range []string{svc, svc + "ImplBase", svc + "CoroutineImplBase"} {
					for _, t := range jp.subs[base] {
						for _, h := range t.methods[method] {
							jp.g.Impls[full] = append(jp.g.Impls[full], h.id)
						}
					}
				}
			}
		}
	}
}
