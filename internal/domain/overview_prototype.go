// PROTOTYPE, throwaway (branch prototype/overview): data and layout for a
// galaxy overview of every indexed project. Answers "is a galaxy of every indexed function
// readable and fast?" before the real `dotweavier serve` Overview tab is designed.
package domain

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"maps"
	"math"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// OverviewGraph is packed into parallel arrays: ~50k nodes stay a few MB of JSON.
type OverviewGraph struct {
	Projects []string  `json:"projects"` // every indexed project, biggest first
	Sizes    []int     `json:"sizes"`    // nodes per project
	Hidden   []bool    `json:"hidden"`   // excluded from this layout: no nodes, no edges
	Centers  []float32 `json:"centers"`  // x,y,z per project; 0 for hidden ones
	Files    []string  `json:"files"`
	Proj     []int32   `json:"proj"` // per node: index into Projects
	File     []int32   `json:"file"` // per node: index into Files
	Name     []string  `json:"name"`
	ID       []int32   `json:"id"` // per node: stable graph node id, what /route takes and returns
	Line     []int32   `json:"line"`
	Deg      []int32   `json:"deg"`       // in+out edges
	Pos      []float32 `json:"pos"`       // x,y,z per node
	Edges    []int32   `json:"edges"`     // s,t pairs, calls inside one project
	Links    []int32   `json:"links"`     // s,t pairs, grpc/http/pubsub/lib hops (between projects)
	Kinds    []string  `json:"linkKinds"` // per link pair: edge kind, "grpc", "http", "pubsub", "pubsub:guess", "lib"
	Methods  []string  `json:"methods"`   // per link pair: gRPC full method, "GET /path", pubsub key, or library function
	LayoutMs int64     `json:"layoutMs"`
	Total    int       `json:"total"` // functions in the shown projects before the node cap
	Limit    int       `json:"limit"` // the node cap; 0 means none
}

// OverviewSource is every index merged once; Build lays it out per request and
// Sync re-indexes one project, then merges again.
type OverviewSource struct {
	ws      string       // the workspace name
	folders []string     // its registered folders, absolute
	mu      sync.RWMutex // guards g: Sync swaps it while requests read it
	g       *graph
	warns   []string   // of the last merge, e.g. unreadable indexes
	sync    sync.Mutex // one Sync at a time
}

// LoadOverview merges the indexes of workspace ws, the only workspace it shows.
func LoadOverview(ws string) (*OverviewSource, error) {
	ws, fs, err := workspaceFolders(ws)
	if err != nil {
		return nil, err
	}
	g, warns, err := mergedGraph(fs)
	if err != nil {
		return nil, err
	}
	return &OverviewSource{ws: ws, folders: fs, g: g, warns: warns}, nil
}

// Warnings are the warnings of the last merge of the indexes, e.g. an unreadable index.
func (src *OverviewSource) Warnings() []string {
	src.mu.RLock()
	defer src.mu.RUnlock()
	return src.warns
}

// Workspace is the name of the workspace this overview shows.
func (src *OverviewSource) Workspace() string { return src.ws }

// FolderOf is the folder that names the project of address addr ([project:]rest)
// in this workspace: field traces and Source run over that folder, where the
// project has the same name.
// ponytail: a field trace sees only the projects under that one folder; trace over
// every folder of the workspace if hops into other folders are needed.
func (src *OverviewSource) FolderOf(addr string) (string, error) {
	name, _, _ := strings.Cut(addr, ":")
	ps, err := projectsIn(src.folders)
	if err != nil {
		return "", err
	}
	for _, p := range ps {
		if p.Name == name {
			return deepest(src.folders, p.dir), nil
		}
	}
	return "", fmt.Errorf("project %q not found in workspace %s", name, src.ws)
}

// mergedGraph merges the indexes of every project inside the absolute folders, joined and without tests.
func mergedGraph(folders []string) (*graph, []string, error) {
	g, _, warns, err := loadIndexes(folders)
	if err != nil {
		return nil, nil, err
	}
	g.dropTests()
	g.join()
	return g, warns, nil
}

// DetectedProject is one project under the project folder and the state of its index.
type DetectedProject struct {
	Name    string         `json:"name"`
	Indexed bool           `json:"indexed"`
	Built   time.Time      `json:"built,omitzero"`
	Parent  string         `json:"parent,omitempty"` // nearest detected project folder containing this one
	Nodes   int            `json:"nodes"`            // non-test functions in the index
	Edges   int            `json:"edges"`            // calls between them, plus outgoing link hops
	Links   map[string]int `json:"links,omitempty"`  // outgoing link hops by kind
}

// Detect lists the projects under the project folder; it reads only each index's meta row.
func (src *OverviewSource) Detect() ([]DetectedProject, error) {
	ps, err := projectsIn(src.folders)
	if err != nil {
		return nil, err
	}
	nodes, edges, links := src.counts()
	names := map[string]bool{}
	for _, p := range ps {
		names[p.Name] = true
	}
	out := make([]DetectedProject, 0, len(ps))
	for _, p := range ps {
		d := DetectedProject{Name: p.Name, Parent: parentOf(p.Name, names), Nodes: nodes[p.Name], Edges: edges[p.Name], Links: links[p.Name]}
		if built, ok := indexBuilt(p.dir); ok {
			d.Indexed, d.Built = true, built
		}
		out = append(out, d)
	}
	return out, nil
}

// counts tallies the merged graph per project, the same nodes and edges an
// overview would draw for it before the node cap.
func (src *OverviewSource) counts() (nodes, edges map[string]int, links map[string]map[string]int) {
	src.mu.RLock()
	defer src.mu.RUnlock()
	g := src.g
	nodes, edges, links = map[string]int{}, map[string]int{}, map[string]map[string]int{}
	drawn := func(i int32) bool { n := g.Nodes[i]; return n.Project != "" && !n.Test }
	for i, n := range g.Nodes {
		if !drawn(int32(i)) {
			continue
		}
		nodes[n.Project]++
		for _, e := range g.Out[i] {
			if !drawn(e.To) || int(e.To) == i {
				continue
			}
			edges[n.Project]++
			if e.Kind.link() {
				if links[n.Project] == nil {
					links[n.Project] = map[string]int{}
				}
				links[n.Project][e.Kind.String()]++
			}
		}
	}
	return nodes, edges, links
}

// indexBuilt is when the index of the project in folder dir was built, if it has one.
func indexBuilt(dir string) (time.Time, bool) {
	path, err := storePath(dir)
	if err != nil {
		return time.Time{}, false
	}
	_, _, built, err := readMeta(path)
	return built, err == nil
}

// detected returns the detected project with this name. Request input is only
// ever turned into a store path through a name discover produced.
func (src *OverviewSource) family(name string) ([]project, error) {
	ps, err := projectsIn(src.folders)
	if err != nil {
		return nil, err
	}
	var fam []project
	for _, p := range ps {
		if p.Name == name {
			fam = append([]project{p}, fam...)
		} else if strings.HasPrefix(p.Name, name+"/") {
			fam = append(fam, p)
		}
	}
	if len(fam) == 0 || fam[0].Name != name {
		return nil, fmt.Errorf("project %q not found in workspace %s", name, src.ws)
	}
	return fam, nil
}

// parentOf is the nearest detected project folder that contains name, or "".
func parentOf(name string, names map[string]bool) string {
	for i := strings.LastIndex(name, "/"); i > 0; i = strings.LastIndex(name[:i], "/") {
		if names[name[:i]] {
			return name[:i]
		}
	}
	return ""
}

var errChanged = errors.New("changed")

var sourceExt = map[string]bool{".go": true, ".java": true, ".kt": true, ".kts": true, ".proto": true}

// changedSince reports whether any source of the project (.go, .java, .kt, .proto, a manifest, go.sum,
// or a directory, whose mtime moves when a file is added, removed or renamed)
// is newer than t. Nested modules, vendor, testdata and dot-dirs are other
// projects or not analyzed, the same skips as discover.
// shortcut: mtimes only, so a checkout that restores old mtimes looks unchanged; hash contents if that bites.
func changedSince(dir string, t time.Time) bool {
	jvm := langOf(dir) == "jvm"
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p != dir {
			n := d.Name()
			if strings.HasPrefix(n, ".") || n == "vendor" || n == "testdata" || n == "node_modules" {
				return filepath.SkipDir
			}
			if langOf(p) != "" || jvm && jvmSkip(n) {
				return filepath.SkipDir
			}
		}
		if !d.IsDir() && !sourceExt[filepath.Ext(p)] && !slices.ContainsFunc(manifests, func(m struct{ file, lang string }) bool { return m.file == d.Name() }) && d.Name() != "go.sum" {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(t) {
			return errChanged
		}
		return nil
	})
	return errors.Is(err, errChanged)
}

// SyncResult is what Sync did for one project of the family.
type SyncResult struct {
	Project  string   `json:"project"`
	UpToDate bool     `json:"upToDate"`
	Stats    string   `json:"stats,omitempty"`
	Error    string   `json:"error,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// Sync syncs a project and every project nested in its folder. Each keeps its
// own index and is re-indexed only if a source file is newer than that index.
// The graph is re-merged once if anything was re-indexed; node ids then change,
// so clients must reload their layout.
func (src *OverviewSource) Sync(project string) ([]SyncResult, error) {
	src.sync.Lock()
	defer src.sync.Unlock()
	fam, err := src.family(project)
	if err != nil {
		return nil, err
	}
	res := make([]SyncResult, len(fam))
	at := map[string]int{}
	var stale []string
	for i, p := range fam {
		res[i].Project = p.Name
		at[p.Name] = i
		if built, ok := indexBuilt(p.dir); ok && !changedSince(p.dir, built) {
			res[i].UpToDate = true
		} else {
			stale = append(stale, p.Name)
		}
	}
	if len(stale) == 0 {
		return res, nil
	}
	// ponytail: not cancellable; pass the request's context if a sync must stop with its page.
	if err := Index(context.Background(), IndexOptions{Workspace: src.ws, Names: stale, Progress: func(ir IndexResult) {
		r := &res[at[ir.Project]]
		r.Stats, r.Warnings = ir.Stats, ir.Warnings
		if ir.Err != nil {
			r.Error = ir.Err.Error()
		}
	}}); err != nil {
		return nil, err
	}
	return res, src.remerge()
}

// Remove deletes the index of a project and of every project nested in it.
func (src *OverviewSource) Remove(project string) ([]string, error) {
	src.sync.Lock()
	defer src.sync.Unlock()
	fam, err := src.family(project)
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, p := range fam {
		path, err := storePath(p.dir)
		if err != nil {
			return removed, err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return removed, err
		}
		removed = append(removed, p.Name)
	}
	return removed, src.remerge()
}

// Invalid lists the invalid indexes of this workspace: indexes whose project is
// no longer detected where it was found.
func (src *OverviewSource) Invalid() ([]string, error) {
	files, err := src.invalid()
	out := slices.Sorted(maps.Values(files))
	return out, err
}

// invalid maps each invalid index file of this workspace to its project's name.
func (src *OverviewSource) invalid() (map[string]string, error) {
	ps, err := projectsIn(src.folders)
	if err != nil {
		return nil, err
	}
	files, err := indexesIn(src.folders)
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		path, err := storePath(p.dir)
		if err != nil {
			return nil, err
		}
		delete(files, path)
	}
	return files, nil
}

// RemoveInvalid deletes every invalid index of this workspace and returns their projects.
// A nested workspace sharing such an index loses it too: its project is gone for both.
func (src *OverviewSource) RemoveInvalid() ([]string, error) {
	src.sync.Lock()
	defer src.sync.Unlock()
	files, err := src.invalid()
	if err != nil {
		return nil, err
	}
	var removed []string
	for f, name := range files {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return removed, err
		}
		removed = append(removed, name)
	}
	slices.Sort(removed)
	return removed, src.remerge()
}

func (src *OverviewSource) remerge() error {
	g, warns, err := mergedGraph(src.folders)
	if err != nil {
		return err
	}
	src.mu.Lock()
	src.g, src.warns = g, warns
	src.mu.Unlock()
	return nil
}

// Build lays out every non-test, non-external function outside the hidden
// projects: one cluster per project, one sub-cluster per package directory.
// Hidden projects free their space, so the rest is laid out as if they were never indexed.
func (src *OverviewSource) Build(show map[string]bool, limit int) *OverviewGraph {
	src.mu.RLock()
	defer src.mu.RUnlock()
	g := src.g
	hide := hiddenExcept(g, show)
	t0 := time.Now()
	keep, total := selectNodes(g, hide, limit)

	// Empty, not nil: an overview with nothing shown must still encode as [] for the page.
	o := &OverviewGraph{Projects: []string{}, Sizes: []int{}, Hidden: []bool{}, Centers: []float32{}, Files: []string{},
		Proj: []int32{}, File: []int32{}, Name: []string{}, ID: []int32{}, Line: []int32{}, Deg: []int32{}, Pos: []float32{},
		Edges: []int32{}, Links: []int32{}, Kinds: []string{}, Methods: []string{}}
	idx := make([]int32, len(g.Nodes)) // graph node -> overview node, -1 when dropped
	projOf := map[string]int32{}
	fileOf := map[string]int32{}
	count := map[string]int{}
	for _, n := range g.Nodes {
		if n.Project != "" && !n.Test {
			count[n.Project]++
		}
	}
	for p := range count {
		o.Projects = append(o.Projects, p)
	}
	slices.SortFunc(o.Projects, func(a, b string) int { return cmp.Or(cmp.Compare(count[b], count[a]), cmp.Compare(a, b)) })
	for i, p := range o.Projects {
		projOf[p] = int32(i)
		o.Sizes = append(o.Sizes, count[p])
		o.Hidden = append(o.Hidden, hide[p])
	}
	for i, n := range g.Nodes {
		if !keep[i] {
			idx[i] = -1
			continue
		}
		fk := n.Project + ":" + n.File
		f, ok := fileOf[fk]
		if !ok {
			f = int32(len(o.Files))
			fileOf[fk] = f
			o.Files = append(o.Files, n.File)
		}
		idx[i] = int32(len(o.Name))
		o.ID = append(o.ID, int32(i))
		o.Proj = append(o.Proj, projOf[n.Project])
		o.File = append(o.File, f)
		o.Name = append(o.Name, n.Name)
		o.Line = append(o.Line, int32(n.Line))
	}
	o.Deg = make([]int32, len(o.Name))
	for from, es := range g.Out {
		s := idx[from]
		for _, e := range es {
			t := idx[e.To]
			if s < 0 || t < 0 || s == t {
				continue
			}
			o.Deg[s]++
			o.Deg[t]++
			if e.Kind.link() {
				o.Links = append(o.Links, s, t)
				o.Kinds = append(o.Kinds, e.Kind.String())
				o.Methods = append(o.Methods, e.Method)
			} else {
				o.Edges = append(o.Edges, s, t)
			}
		}
	}
	o.layout()
	o.Total, o.Limit = total, limit
	o.LayoutMs = time.Since(t0).Milliseconds()
	return o
}

type vec [3]float64

// minStarGap is the closest two stars may sit, in world units; a star's core is 0.35-1.5 wide.
const minStarGap = 2.0

// separate breaks up crowded spots: each pass pushes every pair closer than gap apart,
// each star by half the overlap. A grid of gap-sized cells keeps a pass near O(n).
func separate(pos []vec, gap float64, passes int) {
	cell := func(p vec) [3]int {
		return [3]int{int(math.Floor(p[0] / gap)), int(math.Floor(p[1] / gap)), int(math.Floor(p[2] / gap))}
	}
	for range passes {
		grid := map[[3]int][]int{}
		for i, p := range pos {
			c := cell(p)
			grid[c] = append(grid[c], i)
		}
		moved := false
		for i := range pos {
			c := cell(pos[i])
			for dx := -1; dx <= 1; dx++ {
				for dy := -1; dy <= 1; dy++ {
					for dz := -1; dz <= 1; dz++ {
						for _, j := range grid[[3]int{c[0] + dx, c[1] + dy, c[2] + dz}] {
							if j <= i {
								continue
							}
							d := pos[j].add(pos[i], -1)
							l := math.Sqrt(d[0]*d[0] + d[1]*d[1] + d[2]*d[2])
							if l >= gap {
								continue
							}
							if l < 1e-9 { // same spot: pick a direction from the indices, so the layout stays deterministic
								d, l = inBall(fmt.Sprint(i, "|", j)), 0
								if n := math.Sqrt(d[0]*d[0] + d[1]*d[1] + d[2]*d[2]); n > 0 {
									d = d.add(d, 1/n-1)
								} else {
									d = vec{1, 0, 0}
								}
							} else {
								d = d.add(d, 1/l-1) // unit vector i -> j
							}
							push := (gap - l) / 2
							pos[i] = pos[i].add(d, -push)
							pos[j] = pos[j].add(d, push)
							moved = true
						}
					}
				}
			}
		}
		if !moved {
			return
		}
	}
}

func (a vec) add(b vec, k float64) vec { return vec{a[0] + b[0]*k, a[1] + b[1]*k, a[2] + b[2]*k} }
func (a vec) scale(k float64) vec      { return vec{a[0] * k, a[1] * k, a[2] * k} }
func cross(a, b vec) vec {
	return vec{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}

// groupRing is how a project's group is laid out: modules at dist from the
// project's centre, the whole group within outer of it.
type groupRing struct{ dist, outer float64 }

// moduleGroups groups the shown projects (indexes into names, biggest first) as
// [project, its modules...]: a module joins the outermost shown project whose
// folder contains it, otherwise it heads its own group. Groups keep the order of
// their projects. ring is keyed by each group's project.
func moduleGroups(names []string, shown []int, radius []float64, gap float64) ([][]int, map[int]groupRing) {
	idx := map[string]int{}
	for _, i := range shown {
		idx[names[i]] = i
	}
	head := func(i int) int { // outermost shown project containing names[i], or i
		n := names[i]
		for j := range len(n) { // shortest prefix first: the outermost project
			if n[j] == '/' {
				if h, ok := idx[n[:j]]; ok {
					return h
				}
			}
		}
		return i
	}
	at := map[int]int{} // head -> its group's index
	var groups [][]int
	for _, i := range shown {
		h := head(i)
		if _, ok := at[h]; !ok {
			at[h] = len(groups)
			groups = append(groups, []int{h})
		}
		if h != i {
			groups[at[h]] = append(groups[at[h]], i)
		}
	}
	ring := map[int]groupRing{}
	for _, g := range groups {
		r0, rm := radius[g[0]], 0.0
		for _, i := range g[1:] {
			rm = max(rm, radius[i])
		}
		if len(g) == 1 {
			ring[g[0]] = groupRing{0, r0}
			continue
		}
		// Far enough to clear the project, and a ring long enough that modules do not touch.
		d := max(r0+rm+gap, float64(len(g)-1)*(2*rm+gap)/(2*math.Pi))
		ring[g[0]] = groupRing{d, d + rm}
	}
	return groups, ring
}

// inBall is a deterministic point in the unit ball for key.
func inBall(key string) vec {
	h := fnv.New64a()
	h.Write([]byte(key))
	r := rand.New(rand.NewPCG(h.Sum64(), 0x9e3779b97f4a7c15))
	for {
		v := vec{r.Float64()*2 - 1, r.Float64()*2 - 1, r.Float64()*2 - 1}
		if v[0]*v[0]+v[1]*v[1]+v[2]*v[2] <= 1 {
			return v
		}
	}
}

// layout seeds projects on a Fibonacci sphere, packages around their project and
// functions around their package, then pulls each function toward its callees.
// shortcut: anchored neighbour smoothing, no Barnes-Hut repulsion; upgrade if clusters look mushy.
func (o *OverviewGraph) layout() {
	const spread = 7.0 // world units per cbrt(node)
	const gap = 6.0    // between a project and the ring of its modules
	radius := make([]float64, len(o.Projects))
	var shown []int
	for i, n := range o.Sizes {
		if !o.Hidden[i] {
			shown = append(shown, i)
			radius[i] = spread * math.Cbrt(float64(n))
		}
	}
	// A module is drawn beside its project: each shown project and the shown modules
	// inside it form one group, placed on the sphere as one cluster.
	groups, ring := moduleGroups(o.Projects, shown, radius, gap)
	sum := 0.0
	for _, g := range groups {
		sum += ring[g[0]].outer * ring[g[0]].outer
	}
	// Sphere surface ~ 4πR² must hold every group's disc πr²; R >= 0.5*sqrt(sum)
	// fits them exactly, 0.55 keeps groups nearly touching.
	R := 0.55 * math.Sqrt(sum)
	centers := make([]vec, len(o.Projects))
	golden := math.Pi * (3 - math.Sqrt(5))
	for k, g := range groups {
		y := 1 - 2*(float64(k)+0.5)/float64(len(groups))
		rr := math.Sqrt(1 - y*y)
		th := golden * float64(k)
		c := vec{math.Cos(th) * rr * R, y * R, math.Sin(th) * rr * R}
		centers[g[0]] = c
		// Modules on a ring around the project, in the plane tangent to the sphere.
		n := vec{math.Cos(th) * rr, y, math.Sin(th) * rr}
		u := cross(n, vec{0, 1, 0})
		if l := math.Hypot(u[0], math.Hypot(u[1], u[2])); l > 1e-6 {
			u = u.scale(1 / l)
		} else {
			u = vec{1, 0, 0}
		}
		w := cross(n, u)
		d := ring[g[0]].dist
		for m, i := range g[1:] {
			a := 2 * math.Pi * float64(m) / float64(len(g)-1)
			centers[i] = c.add(u, d*math.Cos(a)).add(w, d*math.Sin(a))
		}
	}
	for _, c := range centers {
		o.Centers = append(o.Centers, float32(c[0]), float32(c[1]), float32(c[2]))
	}

	pkgSize := map[[2]string]int{}
	pkgCenter := map[[2]string]vec{}
	pkgKey := make([][2]string, len(o.Name))
	for i := range o.Name {
		j := o.Proj[i]
		k := [2]string{o.Projects[j], path.Dir(o.Files[o.File[i]])}
		pkgKey[i] = k
		if pkgSize[k]++; pkgSize[k] == 1 {
			pkgCenter[k] = centers[j].add(inBall(k[0]+"/"+k[1]), 0.75*radius[j])
		}
	}
	anchor := make([]vec, len(o.Name))
	for i := range o.Name {
		k := pkgKey[i]
		r := 1.7 * math.Cbrt(float64(pkgSize[k])) // stars inside a package
		anchor[i] = pkgCenter[k].add(inBall(k[0]+"|"+o.Files[o.File[i]]+"|"+o.Name[i]), r)
	}

	adj := make([][]int32, len(o.Name))
	for i := 0; i < len(o.Edges); i += 2 {
		s, t := o.Edges[i], o.Edges[i+1]
		adj[s] = append(adj[s], t)
		adj[t] = append(adj[t], s)
	}
	pos := slices.Clone(anchor)
	next := make([]vec, len(pos))
	for range 20 {
		for i, ns := range adj {
			if len(ns) == 0 {
				next[i] = pos[i]
				continue
			}
			var m vec
			for _, n := range ns {
				m = m.add(pos[n], 1/float64(len(ns)))
			}
			next[i] = anchor[i].add(anchor[i], -0.35).add(m, 0.35)
		}
		pos, next = next, pos
	}
	separate(pos, minStarGap, 6)
	o.Pos = make([]float32, 0, 3*len(pos))
	for _, p := range pos {
		o.Pos = append(o.Pos, float32(p[0]), float32(p[1]), float32(p[2]))
	}
}

// Route is one path for the overview's directions panel, in graph node ids.
type Route struct {
	Nodes   []int32  `json:"nodes"`
	Via     []string `json:"via"`     // per hop (len(Nodes)-1): call, iface:sure, iface:guess, grpc
	Methods []string `json:"methods"` // per hop: the link's method, route or pubsub key, or ""
	Sure    int      `json:"sure"`
	Iface   int      `json:"iface"`
}

// Routes returns up to k ranked paths a->b (fewest guesses, then fewest hops) that
// stay out of hidden projects, so every route can be drawn on the current layout.
// exhausted is false when the search hit its limit and more routes may exist.
func (src *OverviewSource) Routes(a, b int32, k int, show map[string]bool, limit int) (routes []Route, exhausted bool) {
	src.mu.RLock()
	defer src.mu.RUnlock()
	g := src.g
	hide := hiddenExcept(g, show)
	// Same cap as Build: a route may only pass through functions the page draws.
	// shortcut: a route through a capped-out function is not found; send its nodes with the route if that bites.
	keep, _ := selectNodes(g, hide, limit)
	if a < 0 || b < 0 || int(a) >= len(g.Nodes) || int(b) >= len(g.Nodes) || a == b {
		return nil, true
	}
	f := &graph{Nodes: g.Nodes, Out: make([][]edge, len(g.Out))}
	for from, es := range g.Out {
		if !keep[from] {
			continue
		}
		for _, e := range es {
			if keep[e.To] {
				f.Out[from] = append(f.Out[from], e)
			}
		}
	}
	states, exhausted := f.trace(a, b, k)
	for _, st := range states {
		var r Route
		for s := st; s != nil; s = s.parent {
			r.Nodes = append(r.Nodes, s.n)
			if s.parent != nil {
				r.Via = append(r.Via, s.in.Kind.String())
				r.Methods = append(r.Methods, s.in.Method)
				switch s.in.Kind {
				case sure:
					r.Sure++
					r.Iface++
				case guess:
					r.Iface++
				}
			}
		}
		slices.Reverse(r.Nodes)
		slices.Reverse(r.Via)
		slices.Reverse(r.Methods)
		routes = append(routes, r)
	}
	return routes, exhausted
}

// hiddenExcept marks every indexed project not in show as hidden.
func hiddenExcept(g *graph, show map[string]bool) map[string]bool {
	hide := map[string]bool{}
	for _, n := range g.Nodes {
		if n.Project != "" && !show[n.Project] {
			hide[n.Project] = true
		}
	}
	return hide
}

// selectNodes picks the functions an overview draws: every non-test function of a
// shown project, capped at limit (0: no cap). Over the cap it keeps functions with
// an edge to another project first (the links between projects are the point of
// the overview), then the most connected. total is the count before the cap.
func selectNodes(g *graph, hide map[string]bool, limit int) (keep []bool, total int) {
	keep = make([]bool, len(g.Nodes))
	for i, n := range g.Nodes {
		if n.Project != "" && !n.Test && !hide[n.Project] {
			keep[i] = true
			total++
		}
	}
	if limit <= 0 || total <= limit {
		return keep, total
	}
	deg := make([]int32, len(g.Nodes))
	cross := make([]bool, len(g.Nodes))
	for from, es := range g.Out {
		if !keep[from] {
			continue
		}
		for _, e := range es {
			if !keep[e.To] || int(e.To) == from {
				continue
			}
			deg[from]++
			deg[e.To]++
			if g.Nodes[from].Project != g.Nodes[e.To].Project {
				cross[from], cross[e.To] = true, true
			}
		}
	}
	cand := make([]int32, 0, total)
	for i, k := range keep {
		if k {
			cand = append(cand, int32(i))
		}
	}
	slices.SortFunc(cand, func(a, b int32) int {
		if cross[a] != cross[b] {
			if cross[a] {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(deg[b], deg[a]), cmp.Compare(a, b))
	})
	for _, i := range cand[limit:] {
		keep[i] = false
	}
	return keep, total
}
