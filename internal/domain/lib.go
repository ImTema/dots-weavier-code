package domain

import (
	"go/types"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// Library hops join a static call into a dependency to the same function in the
// indexed project that owns it, on the function's full name, which carries the
// package path but no module version: "lib (*example.com/libs/client.Client).List".
// A call into a version other than the indexed checkout still joins.
// ponytail: static calls only; a call through a dependency's interface stays an
// external node, add it when a target needs it.
const libKey = "lib "

// libFunc is the key under which function f is called from, or serves, another
// project: a top-level exported function, or an exported method of an exported
// type, outside the standard library and internal packages. "" for anything else.
func libFunc(f *ssa.Function) string {
	if o := f.Origin(); o != nil {
		f = o
	}
	obj := f.Object()
	if obj == nil || f.Parent() != nil || !obj.Exported() || obj.Pkg() == nil {
		return ""
	}
	path := obj.Pkg().Path()
	if !strings.Contains(strings.Split(path, "/")[0], ".") || // standard library: no workspace indexes it
		strings.HasSuffix(path, "/internal") || strings.Contains(path, "/internal/") { // no other module can import it
		return ""
	}
	if recv := f.Signature.Recv(); recv != nil {
		t := recv.Type()
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem()
		}
		if n, ok := types.Unalias(t).(*types.Named); !ok || !n.Obj().Exported() {
			return ""
		}
	}
	return libKey + f.String()
}
