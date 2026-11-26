// Package domain indexes projects and traces call paths and value lifecycles across them.
package domain

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// IndexOptions says what Index indexes.
type IndexOptions struct {
	Workspace string            // workspace name; DefaultWorkspace for ""
	Names     []string          // project names in the workspace; empty: every project
	Progress  func(IndexResult) // called after each project; may be nil
}

// IndexResult is what indexing one project did.
type IndexResult struct {
	Project  string
	Stats    string   // load stats, when indexed
	Warnings []string // one line each, e.g. an SSA build panic
	Err      error
}

// Index indexes a workspace's projects, or the named ones; ctx stops it between package loads, and an index is written whole or not at all.
func Index(ctx context.Context, o IndexOptions) error {
	done := o.Progress
	if done == nil {
		done = func(IndexResult) {}
	}
	names := o.Names
	ws, fs, err := workspaceFolders(o.Workspace)
	if err != nil {
		return err
	}
	all, err := projectsIn(fs)
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
		if err := ctx.Err(); err != nil {
			return err
		}
		g := &graph{ids: map[string]int32{}, Impls: map[string][]int32{}}
		// Tests are always indexed and flagged; traces drop them unless asked.
		st, err := extract(ctx, g, p, true)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			done(IndexResult{Project: p.Name, Warnings: st.warnings, Err: err})
			continue
		}
		if err := save(&stored{Project: p.Name, Dir: p.dir, Built: time.Now(), Graph: *g}); err != nil {
			return err
		}
		done(IndexResult{Project: p.Name, Stats: st.String(), Warnings: st.warnings})
	}
	for n := range want {
		return fmt.Errorf("project %q not found in workspace %s", n, ws)
	}
	return nil
}

// InitWorkspace registers folder dir in workspace ws, creating ws, and returns the projects under dir without indexing them.
func InitWorkspace(dir, ws string) ([]string, error) {
	if ws == "" {
		ws = DefaultWorkspace
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", dir)
	}
	if err := register(ws, dir); err != nil {
		return nil, err
	}
	return projectsUnder(ws, dir)
}

// projectsUnder lists the projects of workspace ws whose folder is dir or inside it.
func projectsUnder(ws, dir string) ([]string, error) {
	_, fs, err := workspaceFolders(ws)
	if err != nil {
		return nil, err
	}
	ps, err := projectsIn(fs)
	var names []string
	for _, p := range ps {
		if within(p.dir, dir) {
			names = append(names, p.Name)
		}
	}
	return names, err
}

// IndexProject indexes the project in dir with its modules into ws, first registering dir's parent folder if no folder of ws contains dir.
func IndexProject(ctx context.Context, dir, ws string, progress func(IndexResult)) error {
	if ws == "" {
		ws = DefaultWorkspace
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if langOf(dir) == "" {
		return fmt.Errorf("%s is not a project: it has no go.mod, pom.xml, build.gradle(.kts) or settings.gradle(.kts)", dir)
	}
	fs, err := folders(ws)
	if err != nil {
		return err
	}
	if deepest(fs, dir) == "" {
		if err := register(ws, filepath.Dir(dir)); err != nil {
			return err
		}
	}
	names, err := projectsUnder(ws, dir)
	if err != nil {
		return err
	}
	return Index(ctx, IndexOptions{Workspace: ws, Names: names, Progress: progress})
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
	Warnings                                                              []string // e.g. an index that could not be read
}

// PathQuery is what TracePath traces.
type PathQuery struct {
	Workspace    string // workspace name; DefaultWorkspace for ""
	A, B         string // symbol addresses
	MaxPaths     int
	IncludeTests bool
	Progress     func(string) // optional: told each phase as it starts
}

// TracePath finds up to maxPaths call paths a->b, each address project::Type.Method, [project:]rel/path.go:Type.Method or [project:]rel/path.go:LINE, located in q.Workspace.
func TracePath(q PathQuery) (*PathTrace, error) {
	a, b, maxPaths, tests := q.A, q.B, q.MaxPaths, q.IncludeTests
	ws, fs, err := workspaceFolders(q.Workspace)
	if err != nil {
		return nil, err
	}
	l := locator{ws: ws}
	var addrs [2]address
	for i, s := range []string{a, b} {
		sp, err := l.locate("", s)
		if err != nil {
			return nil, err
		}
		if addrs[i] = sp.Addr; sp.Addr.Name == "" && sp.Addr.Line == 0 {
			return nil, fmt.Errorf("bad address %q, want project::Type.Method, [project:]rel/path:Type.Method or [project:]rel/path:LINE", s)
		}
	}
	addrA, addrB := addrs[0], addrs[1]
	g, loaded, warnings, err := loadIndexes(fs, q.Progress)
	if err != nil {
		return nil, err
	}
	for _, p := range []string{addrA.Project, addrB.Project} {
		if !loaded[p] {
			return nil, fmt.Errorf("project %q is not indexed in workspace %s; run: dotweavier index DIR --workspace %s", p, ws, ws)
		}
	}
	if !tests {
		g.dropTests()
	}
	report(q.Progress, fmt.Sprintf("joining %d nodes", len(g.Nodes)))
	g.join()
	r := &PathTrace{Warnings: warnings, Indexes: len(loaded), Nodes: len(g.Nodes), GRPCCalls: len(g.Calls), ServedMethods: len(g.Impls)}
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
	report(q.Progress, "searching paths")
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
	Hops   []Hop        `json:"hops,omitempty"` // calls from this step's function into other projects
}

// FieldTrace is the two trees of a field trace.
type FieldTrace struct {
	Origin   []*FieldStep // where the value comes from; each step flows into its parent
	Uses     []*FieldStep // where it goes; each step flows into its kids
	Warnings []string     `json:",omitempty"` // one line each: projects loaded, skipped or not loaded, call paths skipped, hops not followed
}

// FieldQuery is what TraceField traces, and what a run keeps of how it ran.
type FieldQuery struct {
	Workspace    string       `json:",omitempty"` // workspace the address resolves in when Dir does not have it; DefaultWorkspace for ""
	Dir          string       `json:",omitempty"` // query: folder the address resolves in first; run: the deepest Workspace folder holding the seed's project, else Dir
	IncludeTests bool         `json:",omitempty"`
	MaxProjects  int          `json:",omitempty"` // load at most this many projects when following shared fields; 0: 10
	Only         string       `json:",omitempty"` // "origin" or "uses": show that tree alone; both walks still run, as each feeds the other
	Follow       int          `json:"-"`          // follow hops this many services deep, at most maxFollow hops; 0: none
	Keep         bool         `json:"-"`          // keep the run in the history, under FieldRun.Name
	Progress     func(string) `json:"-"`          // optional: told each phase as it starts
}

// FieldRun is a field trace run: its trees whole, whatever Only says, and what
// it ran with, so FollowFieldRun can continue it.
type FieldRun struct {
	Name string `json:",omitempty"` // its ID in the history; "" if not kept
	Addr string `json:",omitempty"` // the seed, as project:rel/path:LINE in Dir
	FieldQuery
	FieldTrace
}

// report tells progress, if any, that phase starts.
func report(progress func(string), phase string) {
	if progress != nil {
		progress(phase)
	}
}

func (q FieldQuery) maxProjects() int {
	if q.MaxProjects <= 0 {
		return 10
	}
	return q.MaxProjects
}

// langOfExt is the language of a source file by its extension, "" if unknown.
func langOfExt(file string) string {
	return map[string]string{".go": "go", ".java": "jvm", ".kt": "jvm"}[filepath.Ext(file)]
}

// TraceField runs a field trace: it traces the value computed at
// [project:]rel/path.go:LINE in both directions, live over SSA of the projects
// in the run's folder (see FieldQuery.Dir), gives every step its call path and
// hops from the indexes, follows q.Follow services deep and, with q.Keep, keeps
// the run in the history. A .java or .kt file is traced from its project's
// syntax instead (jvm_field.go). Hops that cannot be followed come back as
// warnings. Cancelling ctx stops it between package loads.
func TraceField(ctx context.Context, q FieldQuery, addr string) (*FieldRun, error) {
	if q.Only != "" && q.Only != "origin" && q.Only != "uses" {
		return nil, fmt.Errorf("only %q, want origin or uses", q.Only)
	}
	if _, _, err := fieldAddr(addr); err != nil {
		return nil, err
	}
	run := &fieldRun{loc: locator{ws: q.Workspace}}
	sp, err := run.loc.locate(q.Dir, addr)
	if err != nil {
		return nil, err
	}
	q.Dir = sp.Folder
	run.q = q
	t, err := run.traceAt(ctx, sp, nil, 1)
	if err != nil {
		return nil, err
	}
	q.Keep, q.Progress = false, nil // run.q keeps both
	r := &FieldRun{Addr: sp.Addr.String(), FieldQuery: q, FieldTrace: *t}
	run.r = &r.FieldTrace
	if q.Follow > 0 {
		_, errs := run.followHops(ctx, q.Follow)
		for _, e := range errs {
			r.Warnings = append(r.Warnings, "hop not followed: "+e.Error())
		}
	}
	if run.q.Keep {
		err = saveRun(r)
	}
	return r, err
}
