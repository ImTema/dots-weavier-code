// `dotweavier overview` serves a 3D galaxy of every indexed function on localhost. three.js 0.187.0 is vendored
// under overview_assets (MIT, see LICENSE-three).
package main

import (
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"dotweavier/internal/domain"
)

//go:embed overview.html
var overviewPage []byte

//go:embed overview_assets
var overviewAssets embed.FS

// The overview's defaults, also used when a trace opens its run in the overview.
const (
	overviewAddr     = "127.0.0.1:7357"
	overviewMaxNodes = 20000
)

func runOverview(args []string) {
	fl := flag.NewFlagSet("overview", flag.ExitOnError)
	addr := fl.String("addr", overviewAddr, "listen address")
	ws := fl.String("workspace", "", "registered workspace to show (default: the only one registered; with several, the page asks)")
	maxNodes := fl.Int("max-nodes", overviewMaxNodes, "draw at most N functions: cross-project ones first, then the most connected (0: no cap)")
	_ = fl.Parse(args)
	serveOverview(*addr, *ws, *maxNodes, "")
}

// serveOverview serves the overview and the trace pages on addr until killed, over
// workspace ws ("": the only registered one, else the page asks). With open set
// (a path like /ui/trace-field.html?t=RUN), it opens that page in the browser.
func serveOverview(addr, ws string, maxNodes int, open string) {
	// Only one workspace is active at a time; the page can switch to another registered one.
	var cur atomic.Pointer[domain.OverviewSource]
	activate := func(name string) error {
		all, err := domain.Workspaces()
		if err != nil {
			return err
		}
		if !slices.Contains(all, name) { // request input never reaches the loader unless registered
			return fmt.Errorf("workspace %q does not exist; create it with: dotweavier index DIR --workspace %s", name, name)
		}
		src, err := domain.LoadOverview(name)
		if err != nil {
			return err
		}
		warn(src.Warnings())
		cur.Store(src)
		return nil
	}
	all, err := domain.Workspaces()
	must(err)
	switch {
	case ws != "":
		must(activate(ws))
	case len(all) == 0:
		must(errors.New("no workspace registered; run: dotweavier index DIR"))
	case len(all) == 1:
		must(activate(all[0]))
	}
	// active is the active workspace's overview, or nil after answering 409 when none is chosen yet.
	active := func(w http.ResponseWriter) *domain.OverviewSource {
		src := cur.Load()
		if src == nil {
			http.Error(w, "no active workspace; choose one", http.StatusConflict)
		}
		return src
	}
	show := func(r *http.Request) map[string]bool {
		m := map[string]bool{}
		for _, p := range r.URL.Query()["show"] {
			m[p] = true
		}
		return m
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(overviewPage)
	})
	// ?show=project (repeatable): lay out only those projects; none shown is a valid, empty overview.
	mux.HandleFunc("GET /data.json", func(w http.ResponseWriter, r *http.Request) {
		src := active(w)
		if src == nil {
			return
		}
		shown := show(r)
		o := src.Build(shown, maxNodes)
		data, err := json.Marshal(o)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(os.Stderr, "overview: %d shown, %d nodes, %d call edges, %d link hops, layout %dms, %d KB json\n",
			len(shown), len(o.Name), len(o.Edges)/2, len(o.Links)/2, o.LayoutMs, len(data)>>10)
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	})
	// /paths?a=ID&b=ID[&k=N][&show=project...]: ranked paths between two graph node ids.
	mux.HandleFunc("GET /paths", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		a, errA := strconv.Atoi(q.Get("a"))
		b, errB := strconv.Atoi(q.Get("b"))
		k, errK := strconv.Atoi(q.Get("k"))
		if errA != nil || errB != nil {
			http.Error(w, "a and b must be node ids", http.StatusBadRequest)
			return
		}
		if errK != nil || k < 1 || k > 20 {
			k = 5
		}
		src := active(w)
		if src == nil {
			return
		}
		t0 := time.Now()
		paths, exhausted := src.Paths(int32(a), int32(b), k, show(r), maxNodes)
		ms := time.Since(t0).Milliseconds()
		fmt.Fprintf(os.Stderr, "paths %d -> %d: %d paths in %dms (exhausted=%v)\n", a, b, len(paths), ms, exhausted)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"paths": paths, "exhausted": exhausted, "ms": ms})
	})
	// Projects detected under the project folder, with whether and when each was indexed.
	mux.HandleFunc("GET /projects", func(w http.ResponseWriter, _ *http.Request) {
		src := active(w)
		if src == nil {
			return
		}
		ps, err := src.Detect()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		invalid, err := src.Invalid()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"root": src.Workspace(), "projects": ps, "invalid": invalid})
	})
	// The registered workspaces and the active one ("" until one is chosen).
	mux.HandleFunc("GET /workspaces", func(w http.ResponseWriter, _ *http.Request) {
		all, err := domain.Workspaces()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		act := ""
		if src := cur.Load(); src != nil {
			act = src.Workspace()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"active": act, "workspaces": all})
	})
	// A custom header forces a CORS preflight, which this server never answers,
	// so other sites open in the browser cannot trigger indexing or delete indexes.
	action := func(name string, run func(src *domain.OverviewSource, project string) (map[string]any, error)) {
		mux.HandleFunc("POST /"+name, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Dotweavier") != "1" {
				http.Error(w, "missing X-Dotweavier header", http.StatusForbidden)
				return
			}
			src := active(w)
			if src == nil {
				return
			}
			p := r.URL.Query().Get("project")
			t0 := time.Now()
			res, err := run(src, p)
			fmt.Fprintf(os.Stderr, "%s %s: %v err=%v (%s)\n", name, p, res, err, time.Since(t0).Round(time.Millisecond))
			w.Header().Set("Content-Type", "application/json")
			if err != nil {
				w.WriteHeader(http.StatusUnprocessableEntity)
				res = map[string]any{"error": err.Error()}
			}
			json.NewEncoder(w).Encode(res)
		})
	}
	// POST /sync?project=NAME syncs the project and the projects nested in it;
	// each is re-indexed only if it changed.
	action("sync", func(src *domain.OverviewSource, p string) (map[string]any, error) {
		res, err := src.Sync(p)
		return map[string]any{"results": res}, err
	})
	// POST /remove?project=NAME deletes the indexes of the project and the projects nested in it.
	action("remove", func(src *domain.OverviewSource, p string) (map[string]any, error) {
		removed, err := src.Remove(p)
		return map[string]any{"removed": removed}, err
	})
	// POST /remove-invalid deletes every invalid index of the active workspace.
	action("remove-invalid", func(src *domain.OverviewSource, _ string) (map[string]any, error) {
		removed, err := src.RemoveInvalid()
		return map[string]any{"removed": removed}, err
	})
	// POST /workspace?dir=DIR makes the registered workspace DIR the active one.
	mux.HandleFunc("POST /workspace", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Dotweavier") != "1" {
			http.Error(w, "missing X-Dotweavier header", http.StatusForbidden)
			return
		}
		dir := r.URL.Query().Get("dir")
		t0 := time.Now()
		err := activate(dir)
		fmt.Fprintf(os.Stderr, "workspace %s: err=%v (%s)\n", dir, err, time.Since(t0).Round(time.Millisecond))
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("GET /overview_assets/", http.FileServerFS(overviewAssets))
	// Field trace page, served so it can continue a trace: GET /ui/trace-field.html?t=RUN
	// (what `trace field --format ui` opens), GET /trace-field-history/RUN.js, and
	// POST /trace-field?addr=ADDR, which traces like the CLI, saves the run and returns its name.
	// Trace path page for two symbol addresses, traced like `dotweavier trace path --format ui`
	// over the active workspace: GET /ui/trace-path.html?a=ADDR&b=ADDR. Opened from a weaved path.
	mux.HandleFunc("GET /ui/trace-path.html", func(w http.ResponseWriter, r *http.Request) {
		src := active(w)
		if src == nil {
			return
		}
		a, b := r.URL.Query().Get("a"), r.URL.Query().Get("b")
		res, err := domain.TracePath(domain.PathQuery{Workspace: src.Workspace(), A: a, B: b, MaxPaths: 10})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		warn(res.Warnings)
		html, err := pathUI(res.Paths, a, b)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(html)
	})
	mux.HandleFunc("GET /ui/trace-field.html", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(fieldUITemplate))
	})
	mux.Handle("GET /trace-field-history/", http.StripPrefix("/trace-field-history/", http.FileServer(http.Dir(historyDir()))))
	// GET /source?loc=[project:]rel/path[:LINE]: a whole file of an indexed project, for the
	// field page's viewer. The header guard keeps other sites from reading it (no CORS reply).
	mux.HandleFunc("GET /source", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Dotweavier") != "1" {
			http.Error(w, "missing X-Dotweavier header", http.StatusForbidden)
			return
		}
		src := active(w)
		if src == nil {
			return
		}
		loc := r.URL.Query().Get("loc")
		dir, err := src.FolderOf(loc)
		var f *domain.SourceFile
		if err == nil {
			f, err = domain.Source(dir, loc)
		}
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(f)
	})
	// POST /trace-field/follow?run=RUN&path=I.J.K&hop=N follows hop N of the lifecycle step at
	// path (indexes from the run's uses down through kids) into its handler's project.
	// Without path it follows every hop, depth=D services deep. Either way the result is
	// saved as a new run, whose name it returns.
	// GET /trace-field/export?run=RUN: the run as one self-contained HTML file, for download.
	mux.HandleFunc("GET /trace-field/export", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("run")
		run, err := loadFieldRun(name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		data, err := fieldData(&domain.FieldTrace{Origin: run.Origin, Uses: run.Uses}, run.Addr, run.Open)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="trace-field_`+traceName(run.Addr)+`.html"`)
		w.Write(exportField(data))
	})
	mux.HandleFunc("POST /trace-field/follow", func(w http.ResponseWriter, r *http.Request) {
		reply := func(code int, v map[string]any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(v)
		}
		if r.Header.Get("X-Dotweavier") != "1" {
			reply(http.StatusForbidden, map[string]any{"error": "missing X-Dotweavier header"})
			return
		}
		src := active(w)
		if src == nil {
			return
		}
		q := r.URL.Query()
		run, err := loadFieldRun(q.Get("run"))
		if err != nil {
			reply(http.StatusUnprocessableEntity, map[string]any{"error": err.Error()})
			return
		}
		tr := &domain.FieldTrace{Origin: run.Origin, Uses: run.Uses}
		t0 := time.Now()
		dir, err := src.FolderOf(run.Addr)
		if err != nil {
			reply(http.StatusUnprocessableEntity, map[string]any{"error": err.Error()})
			return
		}
		if q.Get("path") == "" {
			depth, _ := strconv.Atoi(q.Get("depth"))
			n, errs := domain.FollowHops(r.Context(), domain.FieldQuery{Dir: dir}, tr, max(depth, 1), maxFollow)
			if n == 0 && len(errs) > 0 {
				err = errs[0]
			}
		} else {
			var path []int
			for _, p := range strings.Split(q.Get("path"), ".") {
				i, perr := strconv.Atoi(p)
				if perr != nil {
					err = fmt.Errorf("bad path %q", q.Get("path"))
					break
				}
				path = append(path, i)
			}
			if err == nil {
				k, _ := strconv.Atoi(q.Get("hop"))
				err = domain.FollowHop(r.Context(), domain.FieldQuery{Dir: dir}, tr, path, k)
			}
		}
		warn(tr.Warnings)
		var name string
		if err == nil {
			var data []byte
			if data, err = fieldData(tr, run.Addr, run.Open); err == nil {
				name, err = saveFieldRun(data, run.Addr)
			}
		}
		fmt.Fprintf(os.Stderr, "follow %s path=%s hop=%s: run=%s err=%v (%s)\n", q.Get("run"), q.Get("path"), q.Get("hop"), name, err, time.Since(t0).Round(time.Millisecond))
		if err != nil {
			reply(http.StatusUnprocessableEntity, map[string]any{"error": err.Error()})
			return
		}
		reply(http.StatusOK, map[string]any{"run": name})
	})
	mux.HandleFunc("POST /trace-field", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Dotweavier") != "1" { // same CSRF guard as sync and remove
			http.Error(w, "missing X-Dotweavier header", http.StatusForbidden)
			return
		}
		src := active(w)
		if src == nil {
			return
		}
		a := r.URL.Query().Get("addr")
		t0 := time.Now()
		reply := func(code int, v map[string]any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(v)
		}
		var tr *domain.FieldTrace
		dir, err := src.FolderOf(a)
		if err == nil {
			tr, err = domain.TraceField(r.Context(), domain.FieldQuery{Dir: dir}, a)
		}
		if n, _ := strconv.Atoi(r.URL.Query().Get("follow")); err == nil && n > 0 {
			domain.FollowHops(r.Context(), domain.FieldQuery{Dir: dir}, tr, n, maxFollow) // a hop that fails stays unfollowed
		}
		if tr != nil {
			warn(tr.Warnings)
		}
		var name string
		if err == nil {
			var data []byte
			if data, err = fieldData(tr, a, 0); err == nil {
				name, err = saveFieldRun(data, a)
			}
		}
		fmt.Fprintf(os.Stderr, "trace field %s: run=%s err=%v (%s)\n", a, name, err, time.Since(t0).Round(time.Millisecond))
		if err != nil {
			reply(http.StatusUnprocessableEntity, map[string]any{"error": err.Error()})
			return
		}
		reply(http.StatusOK, map[string]any{"run": name})
	})

	ln, err := net.Listen("tcp", addr) // loopback by default: the page exposes source names
	if err != nil && open != "" {
		// Most likely a running overview; it shares the run history, so open the page there.
		fmt.Fprintln(os.Stderr, "dotweavier: not serving:", err)
		openPage("http://" + addr + open)
		return
	}
	must(err)
	fmt.Printf("overview -> http://%s/\n", ln.Addr())
	fmt.Printf("field traces -> http://%s/ui/trace-field.html\n", ln.Addr())
	if open != "" {
		fmt.Printf("opened -> http://%s%s (Ctrl-C stops the server)\n", ln.Addr(), open)
		openPage(fmt.Sprintf("http://%s%s", ln.Addr(), open))
	}
	must(http.Serve(ln, mux))
}
