// `dotweavier trace field`: the full lifecycle of one value, as two trees.
//
//   - origin: where the value comes from, back to literals, parsing, other projects'
//     gRPC clients, ...; each line flows into the one above it.
//   - lifecycle: every local it is copied into, every conversion and transformation,
//     every struct field it is stored in and every place that field is read back;
//     each line flows into the ones below it.
//
// The analysis is field-based taint propagation over SSA, run live (no index). A
// struct field is one location for all objects of the type, keyed by package path,
// type and field name, so a protobuf field written by a gRPC client in one project is
// the same location its server reads in another. Projects that import a reached
// field's package are loaded until nothing new is reached. That over-approximates by
// design: the trees show where the value may come from and go, not where it must.
package domain

import (
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

type flow uint8

const (
	fSeed      flow = iota
	fAssign         // copied: local variable, phi, load, store to a local or global
	fConvert        // type conversion, same value
	fTransform      // derived: result of a call outside the project, arithmetic, concatenation
	fArg            // passed as an argument into a parameter
	fReturn         // returned to a caller
	fStore          // stored into a struct field
	fRead           // read from a struct field (or its protobuf getter)
	fContainer      // put into or taken out of a slice, array, map or channel
	fCapture        // captured by a closure
	fCallback       // guessed: handed by a dependency (lo.Map, ...) to a project callback
)

func (f flow) String() string {
	return [...]string{"seed", "assign", "convert", "transform", "arg", "return", "store", "read", "container", "capture", "callback?"}[f]
}

// A taint key is an ssa.Value or a fieldKey (one location for every object).
type tkey any

type fieldKey string // pkgpath.Type.Field

func (k fieldKey) pkg() string {
	s := string(k)
	s = s[:strings.LastIndex(s, ".")] // drop the field
	return s[:max(0, strings.LastIndex(s, "."))]
}

// seeds returns the values first computed on file:line: value-producing instructions
// whose operands come from elsewhere (so `x := a.Get(); x != ""` seeds the call only),
// and parameters declared there. Addresses and constants are skipped.
func (t *tracker) seeds(prog *ssa.Program, file string, line int) []ssa.Value {
	on := func(p token.Pos) bool {
		if !p.IsValid() {
			return false
		}
		ps := t.fset.Position(p)
		return ps.Line == line && ps.Filename == file
	}
	cand := map[ssa.Value]bool{}
	var order []ssa.Value
	for f := range ssautil.AllFunctions(prog) {
		if !t.keep(f) || t.fset.Position(f.Pos()).Filename != file {
			continue
		}
		for _, p := range f.Params {
			if on(p.Pos()) {
				cand[p] = true
				order = append(order, p)
			}
		}
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				v, ok := in.(ssa.Value)
				if !ok || !on(in.Pos()) || !useful(v.Type()) {
					continue
				}
				switch v.(type) {
				case *ssa.Alloc, *ssa.FieldAddr, *ssa.IndexAddr:
					continue
				}
				cand[v] = true
				order = append(order, v)
			}
		}
	}
	slices.SortStableFunc(order, func(a, b ssa.Value) int { return t.byPos(a.Pos(), b.Pos()) })
	var out []ssa.Value
	for _, v := range order {
		derived := false
		if in, ok := v.(ssa.Instruction); ok {
			for _, op := range in.Operands(nil) {
				derived = derived || cand[*op]
			}
		}
		if !derived && (t.seedOK == nil || t.seedOK(v)) {
			out = append(out, v)
		}
	}
	return out
}

// importers maps each import path to the projects whose non-test code imports it.
// Nested projects are scanned on their own, not as part of the outer one.
func importers(projects []project) map[string][]project {
	roots := map[string]bool{}
	for _, p := range projects {
		roots[p.dir] = true
	}
	out := map[string][]project{}
	fset := token.NewFileSet()
	for _, p := range projects {
		seen := map[string]bool{}
		_ = filepath.WalkDir(p.dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if path != p.dir && (roots[path] || strings.HasPrefix(d.Name(), ".") || d.Name() == "vendor" || d.Name() == "testdata") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				return nil
			}
			for _, im := range f.Imports {
				if ip, err := strconv.Unquote(im.Path.Value); err == nil && !seen[ip] {
					seen[ip] = true
					out[ip] = append(out[ip], p)
				}
			}
			return nil
		})
	}
	return out
}

// traceField runs both walks from file:line in seedProject, loading further projects that
// import a reached field's package, up to maxProjects in total. A non-nil seedOK
// keeps only the line's values it accepts as seeds.
func traceField(ctx context.Context, projects []project, seedProject project, file string, line int, tests bool, maxProjects int, seedOK func(ssa.Value) bool, progress func(string)) (*tracker, error) {
	var modules []string
	moduleOf := map[string]string{} // project name -> module path
	for _, p := range projects {
		if b, err := os.ReadFile(filepath.Join(p.dir, "go.mod")); err == nil {
			if m := modfile.ModulePath(b); m != "" {
				modules = append(modules, m)
				moduleOf[p.Name] = m
			}
		}
	}
	t := newTracker(token.NewFileSet(), tests, modules)
	t.seedOK = seedOK
	loaded := map[string]bool{seedProject.Name: true}
	// Loads run one after another: concurrent loads measured slower (12.1s vs 11.4s),
	// go list contends on the go command's locks and is mostly system time.
	loadAll := func(ps []project) []*ssa.Program {
		progs := make([]*ssa.Program, len(ps))
		for i, p := range ps {
			if ctx.Err() != nil {
				break
			}
			report(progress, fmt.Sprintf("loading %s (%d/%d)", p.Name, len(loaded)-len(ps)+i+1, len(loaded)))
			var st loadStats
			prog, own, err := loadSSA(ctx, p, tests, &st, t.fset)
			t.warnings = append(t.warnings, st.warnings...)
			if err != nil {
				if ctx.Err() == nil {
					t.warnings = append(t.warnings, fmt.Sprintf("skip project %s: %v", p.Name, err))
				}
				continue
			}
			t0 := time.Now()
			t.add(prog, own)
			progs[i] = prog
			t.warnings = append(t.warnings, fmt.Sprintf("loaded %s: pkgs=%d load=%s analyze=%s", p.Name, st.pkgs, st.load.Round(time.Millisecond), time.Since(t0).Round(time.Millisecond)))
		}
		return progs
	}
	// The import scan of every project only matters once fields are reached; run it meanwhile.
	impsC := make(chan map[string][]project, 1)
	go func() { impsC <- importers(projects) }()
	prog := loadAll([]project{seedProject})[0]
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if prog == nil {
		return nil, fmt.Errorf("project %s did not load", seedProject.Name)
	}
	seeds := t.seeds(prog, file, line)
	if len(seeds) == 0 {
		return nil, fmt.Errorf("no value computed at %s:%d in project %s", file, line, seedProject.Name)
	}
	for _, s := range seeds {
		var read tkey
		if k, ok := t.readOf(s); ok {
			read = k
		}
		t.seed(s, s, read, s.Pos(), s.Pos())
	}

	// Fields of packages shared with other projects (protobuf messages, SDKs) are
	// followed into the projects that import them, until no new project is reached.
	var imps map[string][]project
	asked := map[string]bool{}
	for {
		report(progress, fmt.Sprintf("walking %d projects", len(loaded)))
		if err := t.run(ctx); err != nil {
			return nil, err
		}
		var next []project
		for _, w := range []*fwalk{t.back, t.fwd} {
			for _, k := range w.order {
				f, ok := k.(fieldKey)
				if !ok || asked[f.pkg()] {
					continue
				}
				asked[f.pkg()] = true
				if imps == nil {
					imps = <-impsC
				}
				for _, p := range imps[f.pkg()] {
					// The module declaring the package (generated protobuf, SDK types)
					// only defines it; its getters are already read as fields.
					m := moduleOf[p.Name]
					if m != "" && (f.pkg() == m || strings.HasPrefix(f.pkg(), m+"/")) {
						continue
					}
					if !loaded[p.Name] && !slices.Contains(next, p) {
						next = append(next, p)
					}
				}
			}
		}
		var batch []project
		for _, p := range next {
			if len(loaded) >= maxProjects {
				t.warnings = append(t.warnings, fmt.Sprintf("not loaded (--max-projects %d): %s", maxProjects, p.Name))
				continue
			}
			loaded[p.Name] = true
			batch = append(batch, p)
		}
		progs := loadAll(batch)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(progs, func(p *ssa.Program) bool { return p != nil }) {
			return t, nil
		}
		requeueFields(t.back)
		requeueFields(t.fwd)
	}
}
