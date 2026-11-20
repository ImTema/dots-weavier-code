// Package domain is dotweavier's logic: indexing Go projects into per-project
// stores, tracing call paths between symbols and tracing a value's lifecycle.
// It returns plain results; each interface (the CLI now, a UI later) renders
// them its own way.
package domain

import (
	"cmp"
	"fmt"
	"go/types"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Index indexes the projects under ws, or only the named ones, each into its own
// store. done is called after each project with its load stats or its error.
func Index(ws string, names []string, done func(project, stats string, err error)) error {
	all, err := discover(ws)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	for _, p := range all {
		if len(names) > 0 && !want[p.Name] {
			continue
		}
		delete(want, p.Name)
		g := &graph{ids: map[string]int32{}, Impls: map[string][]int32{}}
		// Tests are always indexed and flagged; traces drop them unless asked.
		st, err := extract(g, p, true)
		if err != nil {
			done(p.Name, "", err)
			continue
		}
		if err := save(&stored{Project: p.Name, Dir: p.dir, Built: time.Now(), Graph: *g}); err != nil {
			return err
		}
		done(p.Name, st.String(), nil)
	}
	for n := range want {
		return fmt.Errorf("project %q not found under %s", n, ws)
	}
	return nil
}

func (st loadStats) String() string {
	return fmt.Sprintf("pkgs=%d pkgErrors=%d illTyped=%d stubbedFuncs=%d ssaPanics=%d funcs=%d load=%s analyze=%s",
		st.pkgs, st.pkgErrs, st.illTyped, st.stubbed, st.buildPanics, st.funcs, st.load.Round(time.Millisecond), st.analyze.Round(time.Millisecond))
}

// Node is a function in the index, or an external endpoint when File is empty.
type Node struct {
	Project, File, Name string
	Line                int
}

func (n Node) String() string {
	if n.File == "" {
		return n.Name
	}
	return n.Project + ":" + n.File + ":" + n.Name
}

// Step is one node of a path and the edge that led to it; Via is empty on the first.
type Step struct {
	Node   Node
	Via    string // call, iface:sure, iface:guess, grpc, pubsub or pubsub:guess
	Method string // gRPC full method name, or pubsub "topic X" / "type X"
}

type Path struct {
	Steps       []Step
	Sure, Iface int // interface hops confirmed by VTA, and all interface hops
}

func (p Path) String() string {
	var b strings.Builder
	for _, s := range p.Steps {
		if s.Via != "" {
			label := s.Via
			if s.Method != "" {
				label += " " + s.Method
			}
			fmt.Fprintf(&b, " -%s-> ", label)
		}
		b.WriteString(s.Node.String())
	}
	if p.Iface > 0 {
		fmt.Fprintf(&b, "  (%d/%d sure)", p.Sure, p.Iface)
	}
	return b.String()
}

type PathTrace struct {
	A, B      Node
	Paths     []Path // best first: fewest guesses, then fewest hops
	Exhausted bool   // false: search hit its limit, more paths may exist
	// What the search ran over.
	Indexes, Nodes, Edges, GRPCHops, PubsubHops, GRPCCalls, ServedMethods int
}

// TracePath finds up to maxPaths call paths from address a to address b over
// every indexed project. Addresses: project::Type.Method,
// project:rel/path.go:Type.Method, project:rel/path.go:LINE.
func TracePath(a, b string, maxPaths int, tests bool) (*PathTrace, error) {
	addrA, err := parseAddr(a)
	if err != nil {
		return nil, err
	}
	addrB, err := parseAddr(b)
	if err != nil {
		return nil, err
	}
	g, loaded, err := loadIndexes()
	if err != nil {
		return nil, err
	}
	for _, p := range []string{addrA.Project, addrB.Project} {
		if !loaded[p] {
			return nil, fmt.Errorf("project %q is not indexed; run: dotweavier index %s", p, p)
		}
	}
	if !tests {
		g.dropTests()
	}
	g.join()
	r := &PathTrace{Indexes: len(loaded), Nodes: len(g.Nodes), GRPCCalls: len(g.Calls), ServedMethods: len(g.Impls)}
	for _, es := range g.Out {
		r.Edges += len(es)
		for _, e := range es {
			if e.Kind == grpcHop {
				r.GRPCHops++
			}
			if e.Kind == pubsubHop || e.Kind == pubsubGuess {
				r.PubsubHops++
			}
		}
	}
	na, err := g.resolve(addrA, tests)
	if err != nil {
		return nil, err
	}
	nb, err := g.resolve(addrB, tests)
	if err != nil {
		return nil, err
	}
	r.A, r.B = g.node(na), g.node(nb)
	states, exhausted := g.trace(na, nb, maxPaths)
	r.Exhausted = exhausted
	for _, s := range states {
		r.Paths = append(r.Paths, g.path(s))
	}
	return r, nil
}

func (g *graph) node(i int32) Node {
	n := g.Nodes[i]
	return Node{n.Project, n.File, n.Name, n.Line}
}

func (g *graph) path(s *state) Path {
	var p Path
	for ; s != nil; s = s.parent {
		st := Step{Node: g.node(s.n)}
		if s.parent != nil {
			st.Via, st.Method = s.in.Kind.String(), s.in.Method
			switch s.in.Kind {
			case sure:
				p.Sure++
				p.Iface++
			case guess:
				p.Iface++
			}
		}
		p.Steps = append(p.Steps, st)
	}
	slices.Reverse(p.Steps)
	return p
}

// FieldStep is one source line (or struct field) in a value's lifecycle.
type FieldStep struct {
	Kind   string       `json:"kind"`             // flow into this step; "field" or "overwritten" are special
	Name   string       `json:"name,omitempty"`   // field, as pkg.Type.Field
	Loc    string       `json:"loc,omitempty"`    // [project:]rel/path.go:LINE
	Code   string       `json:"code,omitempty"`   // the source line
	Before string       `json:"before,omitempty"` // the line above, for context
	After  string       `json:"after,omitempty"`  // the line below
	Type   string       `json:"type,omitempty"`
	Fn     string       `json:"fn,omitempty"`
	Via    string       `json:"via,omitempty"` // call site, for parameters
	Kids   []*FieldStep `json:"kids,omitempty"`
	Call   *Path        `json:"call,omitempty"` // best indexed call path between the seed's function and this step's
}

type FieldTrace struct {
	Origin []*FieldStep // where the value comes from; each step flows into its parent
	Uses   []*FieldStep // where it goes; each step flows into its kids
}

// TraceField traces the value computed at [project:]rel/path.go:LINE in both
// directions, live over SSA of the projects under ws. A .java or .kt file is
// traced from its project's syntax instead (jvm_field.go).
func TraceField(ws, addr string, tests bool, maxProjects int) (*FieldTrace, error) {
	i := strings.LastIndex(addr, ":")
	line, err := strconv.Atoi(addr[i+1:])
	lang := map[string]string{".go": "go", ".java": "jvm", ".kt": "jvm"}[filepath.Ext(addr[:max(i, 0)])]
	if i < 0 || err != nil || lang == "" {
		return nil, fmt.Errorf("bad address %q, want [project:]rel/path.{go,java,kt}:LINE", addr)
	}
	projects, err := discover(ws)
	if err != nil {
		return nil, err
	}
	projects = slices.DeleteFunc(projects, func(p project) bool { return p.lang != lang })
	home, file, err := resolveFile(projects, addr[:i])
	if err != nil {
		return nil, err
	}
	if lang == "jvm" {
		r, rel, err := traceJVMField(home, file, line, tests)
		if err != nil {
			return nil, err
		}
		attachCalls(r, home.Name, rel, line)
		return r, nil
	}
	t, err := traceField(projects, home, file, line, tests, maxProjects)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(projects, func(a, b project) int { return cmp.Compare(len(b.dir), len(a.dir)) })
	p := &printer{t: t, home: home, projects: projects, src: map[string][]string{}}
	r := &FieldTrace{}
	for _, n := range t.back.tree() {
		r.Origin = append(r.Origin, p.step(n))
	}
	for _, n := range t.fwd.tree() {
		r.Uses = append(r.Uses, p.step(n))
	}
	attachCalls(r, home.Name, p.rel(file), line)
	return r, nil
}

// step converts a tnode; kids are ordered by location, overwrites come last.
func (p *printer) step(n *tnode) *FieldStep {
	s := &FieldStep{Kind: n.how.String()}
	if n.field != "" {
		s.Kind, s.Name = "field", p.fieldName(n.field)
	} else {
		s.Loc, s.Code = p.rel(n.file)+":"+strconv.Itoa(n.line), p.text(n.file, n.line)
		s.Before, s.After = p.text(n.file, n.line-1), p.text(n.file, n.line+1) // one line of context each side
		if n.fn != nil {
			s.Fn = display(n.fn)
		}
	}
	if n.typ != nil {
		s.Type = types.TypeString(n.typ, qual)
	}
	if n.via.IsValid() {
		s.Via = p.where(n.via)
	}
	kids := slices.Clone(n.kids)
	slices.SortStableFunc(kids, func(a, b *tnode) int {
		return cmp.Or(cmp.Compare(p.rel(a.file), p.rel(b.file)), cmp.Compare(a.line, b.line))
	})
	for _, k := range kids {
		s.Kids = append(s.Kids, p.step(k))
	}
	for _, o := range n.over {
		ps := p.t.fset.Position(o)
		s.Kids = append(s.Kids, &FieldStep{Kind: "overwritten", Loc: p.where(o), Code: p.text(ps.Filename, ps.Line)})
	}
	return s
}
