// The dotweavier CLI: parses flags, calls internal/domain and renders its
// results as text or as a self-contained HTML page.
//
//	dotweavier init [DIR] [--workspace NAME]
//	dotweavier index [DIR] [--workspace NAME]
//	dotweavier trace path -A <addr> -B <addr> [--workspace NAME] [--max-paths 10] [--include-tests]
//	dotweavier trace field [project:]rel/path.{go,java,kt}:LINE [--only origin|uses] [--depth N] [--include-tests] [--format text|ui|html] [--verbose]
//
// Addresses: project::Type.Method, project:rel/path.go:Type.Method, project:rel/path.go:LINE.
package main

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"dotweavier/internal/domain"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  dotweavier init [DIR] [--workspace NAME]    add DIR (default: current folder) to workspace NAME (default: default); its projects are detected, not indexed
  dotweavier index [DIR] [--workspace NAME]   index the project in DIR (default: current folder) and its modules into workspace NAME (default: default)
  dotweavier trace path -A <addr> -B <addr> [--workspace NAME] [--max-paths N] [--include-tests] [--format text|ui|html] [--verbose]
  dotweavier trace field [project:]rel/path.{go,java,kt}:LINE [--project DIR] [--only origin|uses] [--depth N] [--include-tests] [--max-projects N] [--follow N] [--format text|ui|html] [--verbose]`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	start := time.Now()
	// Ctrl-C cancels only index and field trace, which write indexes whole or not at all.
	cancellable := func() context.Context {
		ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt)
		return ctx
	}
	switch os.Args[1] {
	case "init":
		runInit(os.Args[2:])
	case "index":
		runIndex(cancellable(), os.Args[2:])
	case "overview":
		runOverview(os.Args[2:])
	case "trace":
		if len(os.Args) < 3 {
			usage()
		}
		switch os.Args[2] {
		case "path":
			runTrace(os.Args[3:])
		case "field":
			runTraceField(cancellable(), os.Args[3:])
		default:
			usage()
		}
	default:
		usage()
	}
	if os.Getenv("DOTWEAVIER_DEBUG") == "1" {
		fmt.Fprintf(os.Stderr, "wall=%s%s\n", time.Since(start).Round(time.Millisecond), maxRSS())
	}
}

// defaultProjectDir is the current directory, made absolute so stored file paths stay relative to projects.
func defaultProjectDir() string {
	wd, _ := os.Getwd()
	return wd
}

// warn prints the domain's warnings to stderr, one per line.
func warn(ws []string) {
	for _, w := range ws {
		fmt.Fprintln(os.Stderr, w)
	}
}

// dirAndWorkspace parses init's and index's optional folder and --workspace, in either order.
func dirAndWorkspace(cmd string, args []string) (dir, ws string) {
	fl := flag.NewFlagSet(cmd, flag.ExitOnError)
	wsFlag := fl.String("workspace", domain.DefaultWorkspace, "workspace name; created if it does not exist")
	var pos []string
	for {
		_ = fl.Parse(args)
		if fl.NArg() == 0 {
			break
		}
		pos = append(pos, fl.Arg(0))
		args = fl.Args()[1:]
	}
	switch len(pos) {
	case 0:
		return defaultProjectDir(), *wsFlag
	case 1:
		return pos[0], *wsFlag
	}
	usage()
	return "", ""
}

func runInit(args []string) {
	dir, ws := dirAndWorkspace("init", args)
	names, err := domain.InitWorkspace(dir, ws)
	must(err)
	fmt.Printf("workspace %s: %d projects detected; index each with: dotweavier index DIR --workspace %s\n", ws, len(names), ws)
	for _, n := range names {
		fmt.Println("  " + n)
	}
}

func runIndex(ctx context.Context, args []string) {
	dir, ws := dirAndWorkspace("index", args)
	must(domain.IndexProject(ctx, dir, ws, func(r domain.IndexResult) {
		warn(r.Warnings)
		if r.Err != nil {
			fmt.Fprintf(os.Stderr, "index %s: %v\n", r.Project, r.Err)
			return
		}
		fmt.Fprintf(os.Stderr, "indexed %-28s %s\n", r.Project, r.Stats)
	}))
	fmt.Fprintf(os.Stderr, "workspace %s\n", ws)
}

func runTrace(args []string) {
	fl := flag.NewFlagSet("trace", flag.ExitOnError)
	wsFlag := fl.String("workspace", domain.DefaultWorkspace, "workspace to trace in")
	aFlag := fl.String("A", "", "start symbol address")
	bFlag := fl.String("B", "", "end symbol address")
	maxPaths := fl.Int("max-paths", 10, "number of paths to print")
	tests := fl.Bool("include-tests", false, "include test nodes (_test.go, mock/fake packages)")
	format := fl.String("format", "text", "text (one line per path), ui (serve the overview and open the paths in it) or html (one self-contained file in ~/.dotweavier/ui, opened)")
	verbose := fl.Bool("verbose", false, "text: also print progress and stats to stderr (always on for ui and html)")
	_ = fl.Parse(args)
	v := *verbose || *format != "text"

	phase, done := status("trace path", v)
	r, err := domain.TracePath(domain.PathQuery{Workspace: *wsFlag, A: *aFlag, B: *bFlag, MaxPaths: *maxPaths, IncludeTests: *tests, Progress: phase})
	if err != nil {
		done("err=%v", err)
	}
	must(err)
	done("%d paths", len(r.Paths))
	warn(r.Warnings)
	if v {
		fmt.Fprintf(os.Stderr, "loaded %d indexes: nodes=%d edges=%d grpcHops=%d pubsubHops=%d grpcClientCalls=%d servedMethods=%d\n",
			r.Indexes, r.Nodes, r.Edges, r.GRPCHops, r.PubsubHops, r.GRPCCalls, r.ServedMethods)
		fmt.Fprintf(os.Stderr, "A = %s\nB = %s\n", r.A, r.B)
	}
	switch *format {
	case "ui": // the page traces again on the server, with its defaults (10 paths, no tests)
		ws := cmp.Or(*wsFlag, domain.DefaultWorkspace)
		serveOverview(overviewAddr, ws, overviewMaxNodes, "/ui/trace-path.html?"+url.Values{"a": {*aFlag}, "b": {*bFlag}}.Encode())
	case "html":
		html, err := pathUI(r.Paths, *aFlag, *bFlag)
		must(err)
		page := writeUI("trace-path.html", html)
		fmt.Printf("%d paths -> file://%s\n", len(r.Paths), page)
		openPage(page)
	default:
		if len(r.Paths) == 0 {
			fmt.Println("no path")
		}
		for i, p := range r.Paths {
			fmt.Printf("%d. %s\n", i+1, p)
		}
	}
	if !r.Exhausted && len(r.Paths) < *maxPaths {
		fmt.Fprintln(os.Stderr, "search stopped at its expansion limit; more paths may exist")
	}
}

func runTraceField(ctx context.Context, args []string) {
	fl := flag.NewFlagSet("trace field", flag.ExitOnError)
	ws := fl.String("project", defaultProjectDir(), "folder the address is looked up in, before the registered workspace folders; the trace runs over the registered folder holding its project")
	only := fl.String("only", "", "origin or uses: show that tree alone (both walks still run, each feeds the other)")
	depth := fl.Int("depth", 0, "text: print at most N levels of each tree (0: 100); ui: levels open at first (0: 6)")
	tests := fl.Bool("include-tests", false, "include test code (_test.go, mock/fake packages)")
	maxProjects := fl.Int("max-projects", 10, "load at most N projects when following fields shared across projects")
	format := fl.String("format", "text", "text (two trees), ui (serve the overview and open the run in it) or html (one self-contained file in ~/.dotweavier/ui, opened)")
	follow := fl.Int("follow", 0, "follow gRPC/HTTP/Pub/Sub hops into other projects, N services deep (at most 10 hops)")
	verbose := fl.Bool("verbose", false, "text: also print progress and stats to stderr (always on for ui and html)")
	// Accept the address before or after the flags.
	var pos []string
	for len(args) > 0 {
		_ = fl.Parse(args)
		if fl.NArg() == 0 {
			break
		}
		pos = append(pos, fl.Arg(0))
		args = fl.Args()[1:]
	}
	if len(pos) != 1 {
		usage()
	}
	if *only != "" && *only != "origin" && *only != "uses" {
		must(fmt.Errorf("--only %q, want origin or uses", *only))
	}
	v := *verbose || *format != "text"
	phase, done := status("trace field "+pos[0], v)
	r, err := domain.TraceField(ctx, domain.FieldQuery{Dir: *ws, IncludeTests: *tests, MaxProjects: *maxProjects, Follow: *follow, Progress: phase}, pos[0])
	if err != nil {
		done("err=%v", err)
	}
	must(err)
	done("origin=%d uses=%d", len(r.Origin), len(r.Uses))
	warn(r.Warnings)
	// Both walks always run: each feeds the other (the uses walk loads projects the
	// origin walk continues in, overwrites need the origin), so --only drops a tree
	// afterwards rather than skipping its walk. A saved run without one tree shows
	// as --only on the field page.
	switch *only {
	case "origin":
		r.Uses = nil
	case "uses":
		r.Origin = nil
	}
	switch *format {
	case "ui":
		name, err := domain.SaveFieldRun(r, *depth)
		must(err)
		dir, err := domain.FieldHistoryDir()
		must(err)
		fmt.Fprintf(os.Stderr, "saved %s\n", filepath.Join(dir, name+".js"))
		wsName, err := domain.WorkspaceOf(r.Dir)
		must(err)
		serveOverview(overviewAddr, wsName, overviewMaxNodes, "/ui/trace-field.html?t="+url.QueryEscape(name))
	case "html":
		html, err := exportField(r, *depth)
		must(err)
		page := writeUI("trace-field_"+domain.AddrFileName(r.Addr)+".html", html)
		fmt.Printf("lifecycle of %s -> file://%s\n", pos[0], page)
		openPage(page)
	default:
		d := *depth
		if d == 0 {
			d = 100
		}
		if n := lifecycle(os.Stdout, r, d, *only); v {
			fmt.Fprintf(os.Stderr, "%d steps\n", n)
		}
	}
}

// lifecycle prints the trees of a field trace, both or only's one, and returns
// the number of steps printed.
func lifecycle(w io.Writer, r *domain.FieldTrace, depth int, only string) int {
	p := &treePrinter{w: w, depth: depth}
	if only != "uses" {
		fmt.Fprintln(w, "== origin: each line flows into the one above it ==")
		p.dir = "up"
		for _, s := range r.Origin {
			p.print(s, nil, "", true, 0)
		}
	}
	if only == "" {
		fmt.Fprintln(w)
	}
	if only != "origin" {
		fmt.Fprintln(w, "== lifecycle: each line flows into the ones below it ==")
		p.dir = "down"
		for _, s := range r.Uses {
			p.print(s, nil, "", true, 0)
		}
	}
	return p.steps
}

type treePrinter struct {
	w     io.Writer
	depth int
	dir   string // "up" in the origin tree, "down" in the lifecycle
	steps int
}

func label(s, parent *domain.FieldStep) string {
	switch s.Kind {
	case "field":
		if s.Type == "" { // a JVM field of a type outside the project
			return "[field] " + s.Name
		}
		return fmt.Sprintf("[field] %s : %s", s.Name, s.Type)
	case "overwritten":
		return fmt.Sprintf("[overwritten] %s  %s", s.Loc, s.Code)
	}
	l := fmt.Sprintf("[%s] %s  %s", s.Kind, s.Loc, s.Code)
	if s.Type != "" {
		l += "  : " + s.Type
	}
	if s.Via != "" {
		l += "  <- called at " + s.Via
	}
	if s.Fn != "" && (parent == nil || parent.Fn != s.Fn) {
		l += "  in " + s.Fn
	}
	return l
}

func (p *treePrinter) print(s, parent *domain.FieldStep, prefix string, last bool, depth int) {
	if s.Kind != "overwritten" {
		p.steps++
	}
	branch, next := "├─ ", "│  "
	if last {
		branch, next = "└─ ", "   "
	}
	if parent == nil {
		branch, next = "", ""
	}
	fmt.Fprintln(p.w, prefix+branch+label(s, parent))
	if p.depth > 0 && depth >= p.depth {
		if n := count(s) - 1; n > 0 {
			fmt.Fprintf(p.w, "%s%s… +%d more steps %s (raise --depth)\n", prefix, next, n, p.dir)
		}
		return
	}
	for i, k := range s.Kids {
		p.print(k, s, prefix+next, i == len(s.Kids)-1, depth+1)
	}
}

// count counts a step and the steps below it, not overwrites.
func count(s *domain.FieldStep) int {
	if s.Kind == "overwritten" {
		return 0
	}
	c := 1
	for _, k := range s.Kids {
		c += count(k)
	}
	return c
}

//go:embed trace-path.html
var pathUITemplate string

//go:embed trace-field.html
var fieldUITemplate string

type uiNode struct {
	Name    string `json:"name"`
	Project string `json:"project"`
	File    string `json:"file"`
	Line    int    `json:"line"`
}

type uiEdge struct {
	S      int    `json:"s"`
	T      int    `json:"t"`
	Kind   string `json:"kind"`
	Method string `json:"method,omitempty"`
}

type uiPath struct {
	Nodes []int    `json:"nodes"`
	Edges []int    `json:"edges"`
	Sure  int      `json:"sure"`
	Iface int      `json:"iface"`
	GRPC  []string `json:"grpc"`
}

// pathUI renders the union of all paths as one self-contained HTML page, each shared node or edge once.
func pathUI(paths []domain.Path, a, b string) ([]byte, error) {
	data := struct {
		A        string   `json:"a"`
		B        string   `json:"b"`
		Projects []string `json:"projects"`
		Nodes    []uiNode `json:"nodes"`
		Edges    []uiEdge `json:"edges"`
		Paths    []uiPath `json:"paths"`
	}{A: a, B: b, Nodes: []uiNode{}, Edges: []uiEdge{}, Paths: []uiPath{}}
	nodeIdx := map[domain.Node]int{}
	projects := map[string]bool{}
	edgeIdx := map[[2]int]int{}
	nodeOf := func(n domain.Node) int {
		if i, ok := nodeIdx[n]; ok {
			return i
		}
		if n.Project != "" && !projects[n.Project] {
			projects[n.Project] = true
			data.Projects = append(data.Projects, n.Project)
		}
		nodeIdx[n] = len(data.Nodes)
		data.Nodes = append(data.Nodes, uiNode{n.Name, n.Project, n.File, n.Line})
		return nodeIdx[n]
	}
	for _, p := range paths {
		up := uiPath{GRPC: []string{}, Sure: p.Sure, Iface: p.Iface}
		for i, st := range p.Steps {
			to := nodeOf(st.Node)
			up.Nodes = append(up.Nodes, to)
			if i == 0 {
				continue
			}
			key := [2]int{up.Nodes[i-1], to}
			if _, ok := edgeIdx[key]; !ok {
				edgeIdx[key] = len(data.Edges)
				data.Edges = append(data.Edges, uiEdge{key[0], key[1], st.Via, st.Method})
			}
			up.Edges = append(up.Edges, edgeIdx[key])
			if st.Via == "grpc" {
				up.GRPC = append(up.GRPC, st.Method)
			}
		}
		data.Paths = append(data.Paths, up)
	}
	return fill(pathUITemplate, data)
}

// dotDir is a folder under ~/.dotweavier; without a home folder it exits rather than write into a target project.
func dotDir(name string) string {
	home, err := os.UserHomeDir()
	must(err)
	return filepath.Join(home, ".dotweavier", name)
}

// exportField is the field page with one run inlined: a single file that opens
// anywhere, without the history folder or a server. It cannot be continued, so
// it leaves out the run's local folder and warnings.
func exportField(r *domain.FieldTrace, open int) ([]byte, error) {
	view := *r
	view.Dir, view.Warnings = "", nil
	data, err := domain.FieldRunScript(&view, open)
	if err != nil {
		return nil, err
	}
	page := fieldUITemplate
	i := strings.Index(page, "<script>")
	return []byte(page[:i] + "<script>" + string(data) + "window.EXPORTED = true;</script>\n" + page[i:]), nil
}

func fill(template string, data any) ([]byte, error) {
	js, err := json.Marshal(data) // escapes <, > and &, so it is safe inside <script>
	if err != nil {
		return nil, err
	}
	return []byte(strings.Replace(template, "/*DATA*/", string(js), 1)), nil
}

// writeUI writes a page under ~/.dotweavier/ui and returns its path.
func writeUI(name string, html []byte) string {
	out := filepath.Join(dotDir("ui"), name)
	must(os.MkdirAll(filepath.Dir(out), 0o755))
	must(os.WriteFile(out, html, 0o644))
	return out
}

// openPage opens the static page at path in the default browser, only warning on failure: the link is already printed.
func openPage(path string) {
	cmd := map[string][]string{"darwin": {"open"}, "windows": {"rundll32", "url.dll,FileProtocolHandler"}}[runtime.GOOS]
	if cmd == nil {
		cmd = []string{"xdg-open"}
	}
	if err := exec.Command(cmd[0], append(cmd[1:], path)...).Start(); err != nil {
		fmt.Fprintln(os.Stderr, "could not open the browser:", err)
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "dotweavier:", err)
		os.Exit(1)
	}
}
