// The call graph: nodes, edges and the joins between projects.
package domain

import (
	"slices"
	"strings"
)

type kind uint8

const (
	direct      kind = iota // static call, always sure
	sure                    // interface hop confirmed by VTA
	guess                   // interface hop linked by CHA only (or a closure assumed to run)
	grpcHop                 // client call joined to a server handler on the full method name
	pubsubHop               // publish joined to a subscriber handler on the topic
	pubsubGuess             // publish joined to a subscriber on the proto message type only
	httpHop                 // HTTP request joined to the handler of its method and path (http.go)
	libHop                  // static call into a dependency joined to its function in another indexed project (lib.go)
)

// link reports whether k joins two ends by a contract (method, route, topic)
// rather than by a call, which is what makes a hop between projects.
func (k kind) link() bool { return k >= grpcHop }

func (k kind) String() string {
	return [...]string{"call", "iface:sure", "iface:guess", "grpc", "pubsub", "pubsub:guess", "http", "lib"}[k]
}

type edge struct {
	To     int32
	Kind   kind
	Method string // gRPC full method name, or pubsub topic/type key (pubsub.go)
}

type node struct {
	Project, File, Name string
	Line, End           int  // declaration span of top-level functions, for path:LINE addresses
	Test                bool // _test.go or mock/fake package
}

func (n node) String() string {
	if n.File == "" {
		return n.Name
	}
	return n.Project + ":" + n.File + ":" + n.Name
}

type grpcCall struct {
	From   int32
	Method string
	Ext    string // external node name used when no indexed project serves the method
}

type graph struct {
	Nodes []node
	ids   map[string]int32
	Out   [][]edge
	Calls []grpcCall
	Impls map[string][]int32 // full method name -> handler nodes
}

func (g *graph) add(id string, n node) int32 {
	if i, ok := g.ids[id]; ok {
		return i
	}
	i := int32(len(g.Nodes))
	g.ids[id] = i
	g.Nodes = append(g.Nodes, n)
	g.Out = append(g.Out, nil)
	return i
}

// link keeps one edge per (from, to): the most certain kind wins.
func (g *graph) link(from, to int32, k kind, method string) {
	for i, e := range g.Out[from] {
		if e.To == to {
			if k < e.Kind {
				g.Out[from][i] = edge{to, k, method}
			}
			return
		}
	}
	g.Out[from] = append(g.Out[from], edge{to, k, method})
}

func (g *graph) external(name string) int32 {
	return g.add("external|"+name, node{Name: "external: " + name})
}

// join turns gRPC client calls and pubsub publishes into hops to every indexed
// handler of the method, topic or message type.
func (g *graph) join() {
	for _, c := range g.Calls {
		k := grpcHop
		switch {
		case strings.HasPrefix(c.Method, topicKey):
			k = pubsubHop
		case strings.HasPrefix(c.Method, typeKey):
			k = pubsubGuess
		case strings.HasPrefix(c.Method, httpKey):
			k = httpHop
		case strings.HasPrefix(c.Method, libKey):
			k = libHop
		}
		hs := g.Impls[c.Method]
		if k == httpHop { // a route without a method serves them all
			_, path, _ := strings.Cut(strings.TrimPrefix(c.Method, httpKey), " ")
			hs = append(slices.Clip(hs), g.Impls[httpKey+"* "+path]...)
		}
		// An unmatched type says nothing, and an unmatched library call is a third-party dependency: no external node.
		if len(hs) == 0 && k != pubsubGuess && k != libHop {
			g.link(c.From, g.external(c.Ext+" ["+c.Method+"]"), guess, "")
		}
		method := c.Method
		if k == httpHop || k == libHop {
			method = strings.TrimPrefix(strings.TrimPrefix(method, httpKey), libKey) // the kind already says it
		}
		for _, h := range hs {
			if h != c.From {
				g.link(c.From, h, k, method)
			}
		}
	}
}

// merge appends another project's graph, shifting its node numbers.
func (g *graph) merge(o *graph) {
	base := int32(len(g.Nodes))
	g.Nodes = append(g.Nodes, o.Nodes...)
	for _, es := range o.Out {
		shifted := make([]edge, len(es))
		for i, e := range es {
			e.To += base
			shifted[i] = e
		}
		g.Out = append(g.Out, shifted)
	}
	for _, c := range o.Calls {
		c.From += base
		g.Calls = append(g.Calls, c)
	}
	for m, hs := range o.Impls {
		for _, h := range hs {
			g.Impls[m] = append(g.Impls[m], h+base)
		}
	}
}

// dropTests removes every edge into a test node, so traces never pass through one.
func (g *graph) dropTests() {
	for from, es := range g.Out {
		g.Out[from] = slices.DeleteFunc(es, func(e edge) bool { return g.Nodes[e.To].Test })
	}
}
