package domain

import (
	"go/constant"
	"go/types"
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"golang.org/x/tools/go/ssa"
)

// HTTP hops join a client request to the handler of its route on the method
// and the path, with every path parameter reduced to {}: "http GET /customers/{}".
// ponytail: exact path match only; prefix routes ("/static/") and {x...} tails
// are not matched, add them when a target uses them.
const httpKey = "http "

var pathParam = regexp.MustCompile(`\{[^}/]*\}`)

// httpRoute is the key of one route, or "" if s is not a request path.
// method "*" stands for a route that serves every method.
func httpRoute(method, path string) string {
	if !strings.HasPrefix(path, "/") {
		return ""
	}
	return httpKey + strings.ToUpper(method) + " " + pathParam.ReplaceAllString(path, "{}")
}

// httpServes registers Go net/http handlers: ServeMux.Handle/HandleFunc and
// http.Handle/HandleFunc with a constant pattern ("GET /customers/{id}").
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

// handlerFunc finds the function behind a handler argument: a function, a
// closure, a bound method value (h.getCustomer), or a conversion of one.
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

// ktorRequest records a Ktor client request (client.get("…/customers/$id")) as
// an HTTP call. Interpolations become {}; a leading base URL, an interpolation
// or a scheme and host, is dropped, so only the path is matched.
func (jp *jvmProject) ktorRequest(fn *jvmFunc, recv, name string, suffix *sitter.Node) {
	verb, ok := ktorVerbs[name]
	if !ok || recv != "HttpClient" || suffix == nil {
		return
	}
	va := childOf(suffix, "value_arguments")
	if va == nil || va.NamedChildCount() == 0 {
		return
	}
	lit := va.NamedChild(0).NamedChild(0)
	if lit == nil || lit.Type() != "string_literal" {
		return
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
	if key := httpRoute(verb, url); key != "" {
		jp.g.Calls = append(jp.g.Calls, grpcCall{fn.id, key, "HttpClient." + name})
	}
}
