// Package domain is dotweavier's logic: indexing Go projects into per-project
// stores, tracing call paths between symbols and tracing a value's lifecycle.
// It returns plain results; each interface (the CLI now, a UI later) renders
// them its own way.
package domain

import (
	"cmp"
	"context"
	"fmt"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	addrA, err := parseAddr(a)
	if err != nil {
		return nil, err
	}
	addrB, err := parseAddr(b)
	if err != nil {
		return nil, err
	}
	g, loaded, warnings, err := loadIndexes(fs)
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

type FieldTrace struct {
	Origin   []*FieldStep // where the value comes from; each step flows into its parent
	Uses     []*FieldStep // where it goes; each step flows into its kids
	Warnings []string     `json:",omitempty"` // one line each: projects loaded, skipped or not loaded, call paths skipped
}

// FieldQuery holds the options shared by TraceField, TraceHop, FollowHop and FollowHops.
type FieldQuery struct {
	Dir          string // folder whose projects (and modules) are analysed
	IncludeTests bool
	MaxProjects  int // load at most this many projects when following shared fields; 0: 10
}

func (q FieldQuery) maxProjects() int {
	if q.MaxProjects <= 0 {
		return 10
	}
	return q.MaxProjects
}

// TraceField traces the value computed at [project:]rel/path.go:LINE in both
// directions, live over SSA of the projects under ws. A .java or .kt file is
// traced from its project's syntax instead (jvm_field.go).
// Cancelling ctx stops it between package loads.
func TraceField(ctx context.Context, q FieldQuery, addr string) (*FieldTrace, error) {
	ws, tests, maxProjects := q.Dir, q.IncludeTests, q.maxProjects()
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
		attachCalls(r, ws, home.Name, rel, line)
		return r, nil
	}
	t, err := traceField(ctx, projects, home, file, line, tests, maxProjects, nil)
	if err != nil {
		return nil, err
	}
	return fieldTrace(ws, t, home, projects, file, line), nil
}

// fieldTrace turns a Go tracker's walks into the two trees, with call paths and hops.
func fieldTrace(ws string, t *tracker, home project, projects []project, file string, line int) *FieldTrace {
	projects = slices.Clone(projects)
	slices.SortFunc(projects, func(a, b project) int { return cmp.Compare(len(b.dir), len(a.dir)) })
	p := &printer{t: t, home: home, projects: projects, src: map[string][]string{}}
	r := &FieldTrace{Warnings: t.warnings}
	for _, n := range t.back.tree() {
		r.Origin = append(r.Origin, p.step(n))
	}
	for _, n := range t.fwd.tree() {
		r.Uses = append(r.Uses, p.step(n))
	}
	attachCalls(r, ws, home.Name, p.rel(file), line)
	return r
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

// printer turns a finished trace into Steps: paths relative to the projects,
// source lines read from disk.
type printer struct {
	t        *tracker
	home     project   // files here show without a project prefix
	projects []project // innermost first
	src      map[string][]string
}

func (p *printer) rel(file string) string {
	for _, pr := range append([]project{p.home}, p.projects...) {
		if r, err := filepath.Rel(pr.dir, file); err == nil && !strings.HasPrefix(r, "..") {
			if pr == p.home {
				return filepath.ToSlash(r)
			}
			return pr.Name + ":" + filepath.ToSlash(r)
		}
	}
	return file
}

func (p *printer) text(file string, line int) string {
	ls, ok := p.src[file]
	if !ok {
		b, _ := os.ReadFile(file)
		ls = strings.Split(string(b), "\n")
		p.src[file] = ls
	}
	if line < 1 || line > len(ls) {
		return ""
	}
	return strings.TrimSpace(ls[line-1])
}

func (p *printer) where(pos token.Pos) string {
	ps := p.t.fset.Position(pos)
	return p.rel(ps.Filename) + ":" + strconv.Itoa(ps.Line)
}

func qual(pkg *types.Package) string { return pkg.Name() }

// fieldName shows a field as pkg.Type.Field.
func (p *printer) fieldName(k fieldKey) string {
	s := string(k)
	name := s[strings.LastIndex(s[:strings.LastIndex(s, ".")], ".")+1:] // Type.Field
	if v := p.t.fields[k]; v.Pkg() != nil {
		name = v.Pkg().Name() + "." + name
	}
	return name
}
