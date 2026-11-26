package main

import (
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"dotweavier/internal/domain"
)

//go:embed overview.html
var overviewPage []byte

// three.js 0.187.0, MIT (LICENSE-three).
//
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

// serveOverview serves the overview and trace pages of ws ("": the page asks unless only one is registered) on addr, opening open if set.
func serveOverview(addr, ws string, maxNodes int, open string) {
	// Listen before loading the workspace: a running overview is reused, not loaded twice.
	ln, err := net.Listen("tcp", addr) // loopback by default: the page exposes source names
	if err != nil && open != "" && reuseOverview(addr, ws, open) {
		return
	}
	must(err)
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
	// Projects of the active workspace, with whether and when each was indexed.
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
	action := func(name string, run func(src *domain.OverviewSource, project string) (map[string]any, error)) {
		mux.HandleFunc("POST /"+name, func(w http.ResponseWriter, r *http.Request) {
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
	// POST /sync?project=NAME re-indexes the changed ones of the project and the projects nested in it.
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
	// GET /ui/trace-path.html?a=ADDR&b=ADDR: paths over the active workspace, opened from a weaved path.
	mux.HandleFunc("GET /ui/trace-path.html", func(w http.ResponseWriter, r *http.Request) {
		src := active(w)
		if src == nil {
			return
		}
		a, b := r.URL.Query().Get("a"), r.URL.Query().Get("b")
		phase, done := status("trace path "+a+" -> "+b, true)
		res, err := domain.TracePath(domain.PathQuery{Workspace: src.Workspace(), A: a, B: b, MaxPaths: 10, Progress: phase})
		if err != nil {
			done("err=%v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		done("%d paths (exhausted=%v), %d indexes, nodes=%d edges=%d", len(res.Paths), res.Exhausted, res.Indexes, res.Nodes, res.Edges)
		warn(res.Warnings)
		html, err := pathUI(res.Paths, a, b)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(html)
	})
	// GET /ui/trace-field.html?t=RUN: what `trace field --format ui` opens; served so the page can continue a trace.
	mux.HandleFunc("GET /ui/trace-field.html", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(fieldUITemplate))
	})
	// GET /source?loc=[project:]rel/path[:LINE]: a file for the field page's viewer, header-guarded like the actions.
	mux.HandleFunc("GET /source", func(w http.ResponseWriter, r *http.Request) {
		src := active(w)
		if src == nil {
			return
		}
		loc := r.URL.Query().Get("loc")
		f, err := domain.Source(src.Workspace(), loc)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(f)
	})
	fieldRoutes(mux, active)

	fmt.Printf("overview -> http://%s/\n", ln.Addr())
	fmt.Printf("field traces -> http://%s/ui/trace-field.html\n", ln.Addr())
	if open != "" {
		fmt.Printf("opened -> http://%s%s (Ctrl-C stops the server)\n", ln.Addr(), open)
		openPage(fmt.Sprintf("http://%s%s", ln.Addr(), open))
	}
	must(http.Serve(ln, guard(mux)))
}

// guard rejects every POST, and GET /source, that lacks the X-Dotweavier header.
// A custom header forces a CORS preflight this server never answers, so other sites cannot index, delete or read sources.
func guard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method == http.MethodPost || r.URL.Path == "/source") && r.Header.Get("X-Dotweavier") != "1" {
			http.Error(w, "missing X-Dotweavier header", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// fieldRoutes serves the field page's runs: list, read, export, trace and follow, each a single call into the domain.
func fieldRoutes(mux *http.ServeMux, active func(http.ResponseWriter) *domain.OverviewSource) {
	reply := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	// GET /trace-field/runs: the kept runs, newest first.
	mux.HandleFunc("GET /trace-field/runs", func(w http.ResponseWriter, _ *http.Request) {
		runs, err := domain.FieldRuns()
		if err != nil {
			reply(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		reply(w, http.StatusOK, runs)
	})
	// GET /trace-field/run?run=RUN: one kept run; without run, the newest.
	mux.HandleFunc("GET /trace-field/run", func(w http.ResponseWriter, r *http.Request) {
		run, err := domain.LoadFieldRun(r.URL.Query().Get("run"))
		if err != nil {
			reply(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		reply(w, http.StatusOK, run)
	})
	// GET /trace-field/export?run=RUN&open=N: the run as one self-contained HTML file, for download.
	mux.HandleFunc("GET /trace-field/export", func(w http.ResponseWriter, r *http.Request) {
		run, err := domain.LoadFieldRun(r.URL.Query().Get("run"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		open, _ := strconv.Atoi(r.URL.Query().Get("open"))
		page, err := exportField(run, open)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="trace-field_`+domain.AddrFileName(run.Addr)+`.html"`)
		w.Write(page)
	})
	// POST /trace-field/follow?run=RUN&path=I.J.K&hop=N follows hop N of that step, or without path every hop depth=D deep, into a new run.
	mux.HandleFunc("POST /trace-field/follow", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		phase, done := status("follow "+q.Get("run"), true)
		hop, _ := strconv.Atoi(q.Get("hop"))
		depth, _ := strconv.Atoi(q.Get("depth"))
		run, warns, err := domain.FollowFieldRun(r.Context(), domain.FollowQuery{Run: q.Get("run"), Step: q.Get("path"), Hop: hop, Depth: depth, Progress: phase})
		warn(warns)
		done("path=%s hop=%s: err=%v", q.Get("path"), q.Get("hop"), err)
		if err != nil {
			reply(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error()})
			return
		}
		reply(w, http.StatusOK, map[string]any{"run": run.Name})
	})
	// POST /trace-field?addr=ADDR[&include-tests=1][&max-projects=N][&only=origin|uses][&follow=N] traces like the CLI and returns the kept run's name.
	mux.HandleFunc("POST /trace-field", func(w http.ResponseWriter, r *http.Request) {
		src := active(w)
		if src == nil {
			return
		}
		q := r.URL.Query()
		a := q.Get("addr")
		phase, done := status("trace field "+a, true)
		maxProjects, _ := strconv.Atoi(q.Get("max-projects"))
		follow, _ := strconv.Atoi(q.Get("follow")) // a hop that fails stays unfollowed
		run, err := domain.TraceField(r.Context(), domain.FieldQuery{Workspace: src.Workspace(), IncludeTests: q.Get("include-tests") == "1",
			MaxProjects: maxProjects, Only: q.Get("only"), Follow: follow, Keep: true, Progress: phase}, a)
		if err != nil {
			done("err=%v", err)
			reply(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error()})
			return
		}
		warn(run.Warnings)
		done("origin=%d uses=%d run=%s", len(run.Origin), len(run.Uses), run.Name)
		reply(w, http.StatusOK, map[string]any{"run": run.Name})
	})
}

// reuseOverview opens open on the overview already serving addr, switching it to ws if set; false if that fails.
func reuseOverview(addr, ws, open string) bool {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + addr + "/workspaces")
	if err != nil {
		return false
	}
	var cur struct{ Active string }
	err = json.NewDecoder(resp.Body).Decode(&cur)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		return false
	}
	if ws != "" && ws != cur.Active {
		req, _ := http.NewRequest("POST", "http://"+addr+"/workspace?"+url.Values{"dir": {ws}}.Encode(), nil)
		req.Header.Set("X-Dotweavier", "1")
		c.Timeout = 0 // loading a workspace can take a while
		resp, err := c.Do(req)
		if err != nil {
			return false
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			return false
		}
		fmt.Fprintf(os.Stderr, "switched the running overview to workspace %s\n", ws)
	}
	fmt.Printf("opened -> http://%s%s (on the running overview)\n", addr, open)
	openPage("http://" + addr + open)
	return true
}
