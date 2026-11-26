package domain

import (
	"go/constant"
	"go/token"
	"go/types"
	"regexp"
	"slices"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"golang.org/x/tools/go/ssa"
)

// HTTP hops join on method and path, each path parameter reduced to {}: "http GET /customers/{}".
// ponytail: exact path match only; prefix routes ("/static/") and {x...} tails
// are not matched, add them when a target uses them.
const httpKey = "http "

var pathParam = regexp.MustCompile(`\{[^}/]*\}`)

// httpRoute is the key of one route, method "*" serving every method, or "" if path is not a request path.
func httpRoute(method, path string) string {
	if !strings.HasPrefix(path, "/") {
		return ""
	}
	return httpKey + strings.ToUpper(method) + " " + pathParam.ReplaceAllString(path, "{}")
}

// httpServes registers net/http handlers given to Handle or HandleFunc with a constant pattern.
func httpServes(g *graph, prog *ssa.Program, c *ssa.CallCommon, nodeOf func(*ssa.Function) int32, keep func(*ssa.Function) bool) {
	sc := c.StaticCallee()
	if sc == nil || sc.Object() == nil || sc.Object().Pkg() == nil || sc.Object().Pkg().Path() != "net/http" ||
		(sc.Name() != "HandleFunc" && sc.Name() != "Handle") || len(c.Args) < 2 {
		return
	}
	k, ok := c.Args[len(c.Args)-2].(*ssa.Const)
	if !ok || k.Value == nil || k.Value.Kind() != constant.String {
		return
	}
	pattern := constant.StringVal(k.Value)
	method, path, ok := strings.Cut(pattern, " ")
	if !ok || strings.HasPrefix(pattern, "/") {
		method, path = "*", pattern
	}
	if i := strings.Index(path, "/"); i > 0 { // host-qualified pattern: example.com/x
		path = path[i:]
	}
	key := httpRoute(method, strings.TrimSpace(path))
	if key == "" {
		return
	}
	if h := handlerFunc(prog, c.Args[len(c.Args)-1]); h != nil && keep(h) {
		g.Impls[key] = append(g.Impls[key], nodeOf(h))
	}
}

// handlerFunc finds the function behind a handler argument: a function, closure, bound method value, or a conversion of one.
func handlerFunc(prog *ssa.Program, v ssa.Value) *ssa.Function {
	for {
		switch x := v.(type) {
		case *ssa.Function:
			return x
		case *ssa.MakeClosure:
			fn := x.Fn.(*ssa.Function)
			if fn.Synthetic != "" { // bound method wrapper
				if m, ok := fn.Object().(*types.Func); ok {
					return prog.FuncValue(m)
				}
				return nil
			}
			return fn
		case *ssa.ChangeType:
			v = x.X
		case *ssa.MakeInterface:
			v = x.X
		default:
			return nil
		}
	}
}

var ktorVerbs = map[string]string{"get": "GET", "post": "POST", "put": "PUT", "delete": "DELETE", "patch": "PATCH", "head": "HEAD", "options": "OPTIONS"}

// ktorRequest records a Ktor client request (client.get("…/customers/$id")) as an HTTP call on its path alone.
func (jp *jvmProject) ktorRequest(fn *jvmFunc, recv, name string, suffix *sitter.Node) {
	verb, ok := ktorVerbs[name]
	if !ok || recv != "HttpClient" {
		return
	}
	if key := httpRoute(verb, ktorPath(fn, suffix)); key != "" {
		jp.g.Calls = append(jp.g.Calls, grpcCall{fn.id, key, "HttpClient." + name})
	}
}

// ktorRoute registers a Ktor server route (get("/rates") { … }) as served by fn, the function its lambda is folded into.
// ponytail: route("/prefix") { get(...) } nesting is not read; add it when a target nests routes.
func (jp *jvmProject) ktorRoute(fn *jvmFunc, call *sitter.Node, name string, suffix *sitter.Node) {
	verb, ok := ktorVerbs[name]
	if !ok || suffix == nil {
		return
	}
	// The trailing lambda sits on an outer call: (call_expression (call_expression get (args)) (call_suffix lambda)).
	lambda := childOf(suffix, "annotated_lambda")
	if p := call.Parent(); lambda == nil && p != nil && p.Type() == "call_expression" {
		if ps := childOf(p, "call_suffix"); ps != nil {
			lambda = childOf(ps, "annotated_lambda")
		}
	}
	if lambda == nil {
		return
	}
	if key := httpRoute(verb, ktorPath(fn, suffix)); key != "" && !slices.Contains(jp.g.Impls[key], fn.id) {
		jp.g.Impls[key] = append(jp.g.Impls[key], fn.id)
	}
}

// ktorPath is the path of a call's first argument when it is a string literal, interpolations as {} and a leading base URL dropped; "" otherwise.
func ktorPath(fn *jvmFunc, suffix *sitter.Node) string {
	if suffix == nil {
		return ""
	}
	va := childOf(suffix, "value_arguments")
	if va == nil || va.NamedChildCount() == 0 {
		return ""
	}
	lit := va.NamedChild(0).NamedChild(0)
	if lit == nil || lit.Type() != "string_literal" {
		return ""
	}
	var b strings.Builder
	for _, part := range children(lit) {
		if part.Type() == "string_content" {
			b.WriteString(part.Content(fn.file.src))
		} else {
			b.WriteString("{}")
		}
	}
	url := b.String()
	if i := strings.Index(url, "://"); i >= 0 {
		url = url[i+3:]
		url = url[strings.Index(url+"/", "/"):]
	}
	url = strings.TrimPrefix(url, "{}")
	if q := strings.IndexAny(url, "?#"); q >= 0 {
		url = url[:q]
	}
	return url
}

// goRequest is the route key of a net/http client request (http.NewRequest, http.Get, Client.Post, ...) with a constant method, or "".
func goRequest(c *ssa.CallCommon) string {
	sc := c.StaticCallee()
	if sc == nil || sc.Object() == nil || sc.Object().Pkg() == nil || sc.Object().Pkg().Path() != "net/http" {
		return ""
	}
	args := c.Args
	if sc.Signature.Recv() != nil {
		if types.TypeString(sc.Signature.Recv().Type(), nil) != "*net/http.Client" {
			return ""
		}
		args = args[1:]
	}
	var method string
	var u ssa.Value
	switch sc.Name() {
	case "NewRequest", "NewRequestWithContext":
		k, ok := args[len(args)-3].(*ssa.Const)
		if !ok || k.Value == nil || k.Value.Kind() != constant.String {
			return ""
		}
		method, u = constant.StringVal(k.Value), args[len(args)-2]
	case "Get", "Head", "Post", "PostForm":
		method, u = strings.ToUpper(strings.TrimSuffix(sc.Name(), "Form")), args[0]
	default:
		return ""
	}
	p := urlText(u, 0)
	if i := strings.Index(p, "://"); i >= 0 { // scheme and host
		p = p[i+3:]
		p = p[strings.Index(p+"/", "/"):]
	}
	p = strings.TrimPrefix(p, "{}") // the base URL
	if q := strings.IndexAny(p, "?#"); q >= 0 {
		p = p[:q]
	}
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	return httpRoute(method, p)
}

var printfVerb = regexp.MustCompile(`%[-+# 0-9.*]*[a-zA-Z]`)

// urlText rebuilds the URL a value holds from constants, +, fmt.Sprintf, url.JoinPath, url.Parse
// and URL.JoinPath/String; any other part becomes {}.
// ponytail: follows values within one function only, no phis or struct fields; widen when a target builds URLs elsewhere.
func urlText(v ssa.Value, depth int) string {
	if depth > 16 {
		return "{}"
	}
	switch x := v.(type) {
	case *ssa.Const:
		if x.Value != nil && x.Value.Kind() == constant.String {
			return constant.StringVal(x.Value)
		}
	case *ssa.BinOp:
		if x.Op == token.ADD {
			return urlText(x.X, depth+1) + urlText(x.Y, depth+1)
		}
	case *ssa.Extract: // u, err := url.Parse(s)
		return urlText(x.Tuple, depth+1)
	case *ssa.Call:
		sc := x.Call.StaticCallee()
		if sc == nil || sc.Object() == nil || sc.Object().Pkg() == nil {
			break
		}
		a := x.Call.Args
		switch sc.Object().Pkg().Path() + "." + sc.Name() {
		case "fmt.Sprintf":
			if k, ok := a[0].(*ssa.Const); ok && k.Value != nil && k.Value.Kind() == constant.String {
				return strings.ReplaceAll(printfVerb.ReplaceAllString(constant.StringVal(k.Value), "{}"), "%%", "%")
			}
		case "net/url.Parse", "net/url.String":
			return urlText(a[0], depth+1)
		case "net/url.JoinPath": // url.JoinPath(base, elem...) and (*URL).JoinPath(elem...)
			s := urlText(a[0], depth+1)
			for _, e := range varargs(a[len(a)-1]) {
				s += "/" + urlText(e, depth+1)
			}
			return s
		}
	}
	return "{}"
}

// varargs are the values stored into the slice passed as a ...T argument.
func varargs(v ssa.Value) []ssa.Value {
	sl, ok := v.(*ssa.Slice)
	if !ok {
		return nil
	}
	arr, ok := sl.X.(*ssa.Alloc)
	if !ok {
		return nil
	}
	var out []ssa.Value
	for _, r := range *arr.Referrers() {
		if ia, ok := r.(*ssa.IndexAddr); ok {
			for _, rr := range *ia.Referrers() {
				if st, ok := rr.(*ssa.Store); ok && st.Addr == ia {
					out = append(out, st.Val)
				}
			}
		}
	}
	return out
}
