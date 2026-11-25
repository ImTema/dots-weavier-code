// Package domain is dotweavier's logic: indexing Go projects into per-project
// stores, tracing call paths between symbols and tracing a value's lifecycle.
// It returns plain results; each interface (the CLI now, a UI later) renders
// them its own way.
package domain

import (
	"cmp"
	"context"
	"errors"
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

// Index indexes the projects of a workspace, or only the named ones, each into
// the one index of its folder. Cancelling ctx stops it between projects and
// package loads; a project's index is written whole or not at all.
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

// InitWorkspace registers folder dir in workspace ws (DefaultWorkspace for ""),
// creating ws if it does not exist, and returns the projects detected under dir,
// named as ws names them. Nothing is indexed.
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

// IndexProject indexes the project in folder dir, with its modules, into
// workspace ws (DefaultWorkspace for ""), creating ws if it does not exist. If no
// folder of ws contains dir, dir's parent folder is registered in ws first.
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

// TracePath finds up to maxPaths call paths from address a to address b over
// every project indexed in workspace ws (DefaultWorkspace for ""). Addresses:
// project::Type.Method, project:rel/path.go:Type.Method, project:rel/path.go:LINE.
func TracePath(q PathQuery) (*PathTrace, error) {
	a, b, maxPaths, tests := q.A, q.B, q.MaxPaths, q.IncludeTests
	ws, fs, err := workspaceFolders(q.Workspace)
	if err != nil {
		return nil, err
	}
	addrA, err := symbolAddr(a)
	if err != nil {
		return nil, err
	}
	addrB, err := symbolAddr(b)
	if err != nil {
		return nil, err
	}
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

// FieldTrace is a field trace run: the two trees, and what it ran with, so
// FollowFieldRun can continue it later from a saved copy.
type FieldTrace struct {
	Origin       []*FieldStep // where the value comes from; each step flows into its parent
	Uses         []*FieldStep // where it goes; each step flows into its kids
	Warnings     []string     `json:",omitempty"` // one line each: projects loaded, skipped or not loaded, call paths skipped, hops not followed
	Addr         string       `json:",omitempty"` // the seed, as project:rel/path:LINE in Dir
	Dir          string       `json:",omitempty"` // absolute folder the run traces; hops are followed in it too
	IncludeTests bool         `json:",omitempty"`
	MaxProjects  int          `json:",omitempty"`
}

// FieldQuery is what TraceField traces.
type FieldQuery struct {
	// Folder the address resolves in, else the registered workspace folder whose
	// projects have it. The run traces the registered folder that holds the
	// seed's project, where projects have the names their indexes have.
	Dir          string
	IncludeTests bool
	MaxProjects  int          // load at most this many projects when following shared fields; 0: 10
	Follow       int          // follow hops this many services deep, at most maxFollow hops; 0: none
	Progress     func(string) // optional: told each phase as it starts
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

// symbolAddr parses the address of a trace's end: it names a project and a
// symbol or line.
func symbolAddr(s string) (address, error) {
	a, err := parseAddr(s)
	if err != nil || a.Project == "" || a.Name == "" && a.Line == 0 {
		return address{}, fmt.Errorf("bad address %q, want project::Type.Method, project:rel/path:Type.Method or project:rel/path:LINE", s)
	}
	return a, nil
}

// langOfExt is the language of a source file by its extension, "" if unknown.
func langOfExt(file string) string {
	return map[string]string{".go": "go", ".java": "jvm", ".kt": "jvm"}[filepath.Ext(file)]
}

// langProjects is the projects under dir written in lang; every project for "".
func langProjects(dir, lang string) ([]project, error) {
	ps, err := discover(dir)
	return slices.DeleteFunc(ps, func(p project) bool { return lang != "" && p.lang != lang }), err
}

// runFolder resolves addr from folder dir: it returns the folder a run traces
// and addr as named there. addr resolves in dir when one of its projects has the
// file, else in the one registered folder whose projects have it, so an address
// from an indexed project traces from any current folder. The run traces the
// deepest registered folder holding the seed's project, whose projects are named
// as their indexes name them; outside every registered folder, the folder addr
// resolved in.
func runFolder(dir, addr string) (string, string, error) {
	a, lang, err := fieldAddr(addr)
	if err != nil {
		return "", "", err
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return "", "", err
	}
	fs, err := registryList(`SELECT DISTINCT dir FROM workspace_folders`)
	if err != nil {
		return "", "", err
	}
	ps, err := langProjects(dir, lang)
	if err != nil {
		return "", "", err
	}
	p, file, miss := resolveFile(ps, a)
	if miss != nil {
		f, err := folderWithFile(fs, a, lang)
		if errors.Is(err, errNoProject) {
			return "", "", miss
		}
		if err != nil {
			return "", "", err
		}
		if ps, err = langProjects(f, lang); err != nil {
			return "", "", err
		}
		if p, file, err = resolveFile(ps, a); err != nil {
			return "", "", err
		}
		dir = f
	}
	if top := deepest(fs, p.dir); top != "" {
		dir = top
	}
	name, err := filepath.Rel(dir, p.dir)
	if err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(p.dir, file)
	if err != nil {
		return "", "", err
	}
	return dir, address{Project: filepath.ToSlash(name), File: filepath.ToSlash(rel), Line: a.Line}.String(), nil
}

// folderWithFile is the one folder of folders whose projects in lang ("": any)
// have the file a names. The innermost folder wins over a folder containing it.
// A missing folder is skipped.
func folderWithFile(folders []string, a address, lang string) (string, error) {
	folders = slices.Clone(folders)
	slices.SortFunc(folders, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	found := map[string]bool{} // absolute files already found
	var hits []string
	for _, f := range folders {
		ps, err := langProjects(f, lang)
		if err != nil {
			continue // a registered folder that was moved or deleted
		}
		_, abs, err := resolveFile(ps, a)
		if err != nil {
			if !errors.Is(err, errNoProject) {
				return "", err // in several projects of one folder: the user must pick
			}
			continue
		}
		if !found[abs] {
			found[abs] = true
			hits = append(hits, f)
		}
	}
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("%w %s", errNoProject, a.loc())
	case 1:
		return hits[0], nil
	}
	return "", fmt.Errorf("%s is in several workspace folders (%s); pass --project DIR", a.loc(), strings.Join(hits, ", "))
}

// WorkspaceOf is a registered workspace with a folder containing dir, or "" if none.
func WorkspaceOf(dir string) (string, error) {
	all, err := Workspaces()
	if err != nil {
		return "", err
	}
	for _, ws := range all {
		fs, err := folders(ws)
		if err != nil {
			return "", err
		}
		if deepest(fs, dir) != "" {
			return ws, nil
		}
	}
	return "", nil
}

// TraceField runs a field trace: it traces the value computed at
// [project:]rel/path.go:LINE in both directions, live over SSA of the projects
// in the run's folder (see FieldQuery.Dir), gives every step its call path and
// hops from the indexes, and follows q.Follow services deep. A .java or .kt
// file is traced from its project's syntax instead (jvm_field.go). Hops that
// cannot be followed come back as warnings. Cancelling ctx stops it between
// package loads.
func TraceField(ctx context.Context, q FieldQuery, addr string) (*FieldTrace, error) {
	dir, addr, err := runFolder(q.Dir, addr)
	if err != nil {
		return nil, err
	}
	q.Dir = dir
	run := &fieldRun{q: q}
	r, err := run.trace(ctx, addr, nil, 1)
	if err != nil {
		return nil, err
	}
	r.Addr, r.Dir, r.IncludeTests, r.MaxProjects = addr, dir, q.IncludeTests, q.MaxProjects
	run.r = r
	if q.Follow > 0 {
		_, errs := run.followHops(ctx, q.Follow)
		for _, e := range errs {
			r.Warnings = append(r.Warnings, "hop not followed: "+e.Error())
		}
	}
	return r, nil
}

// fieldTrace turns a Go tracker's walks into the two trees.
func fieldTrace(t *tracker, seedProject project, projects []project) *FieldTrace {
	return t.result(newPrinter(seedProject, projects))
}
