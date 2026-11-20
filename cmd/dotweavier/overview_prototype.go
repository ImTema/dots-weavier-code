// PROTOTYPE, throwaway (branch prototype/overview): `dotweavier overview` serves a
// 3D galaxy of every indexed function on localhost. three.js 0.187.0 is vendored
// under overview_prototype_assets (MIT, see LICENSE-three).
package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"dotweavier/internal/domain"
)

//go:embed overview-prototype.html
var overviewPage []byte

//go:embed overview_prototype_assets
var overviewAssets embed.FS

func runOverview(args []string) {
	fl := flag.NewFlagSet("overview", flag.ExitOnError)
	addr := fl.String("addr", "127.0.0.1:7357", "listen address")
	ws := fl.String("project", defaultProjectDir(), "project folder whose projects the page lists and can sync")
	maxNodes := fl.Int("max-nodes", 20000, "draw at most N functions: cross-project ones first, then the most connected (0: no cap)")
	_ = fl.Parse(args)

	src, err := domain.LoadOverview(*ws)
	must(err)
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
		shown := show(r)
		o := src.Build(shown, *maxNodes)
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
	// /route?a=ID&b=ID[&k=N][&show=project...]: ranked paths between two graph node ids.
	mux.HandleFunc("GET /route", func(w http.ResponseWriter, r *http.Request) {
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
		t0 := time.Now()
		routes, exhausted := src.Routes(int32(a), int32(b), k, show(r), *maxNodes)
		ms := time.Since(t0).Milliseconds()
		fmt.Fprintf(os.Stderr, "route %d -> %d: %d routes in %dms (exhausted=%v)\n", a, b, len(routes), ms, exhausted)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"routes": routes, "exhausted": exhausted, "ms": ms})
	})
	// Projects detected under the project folder, with whether and when each was indexed.
	mux.HandleFunc("GET /projects", func(w http.ResponseWriter, _ *http.Request) {
		ps, err := src.Detect()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"root": *ws, "projects": ps})
	})
	// A custom header forces a CORS preflight, which this server never answers,
	// so other sites open in the browser cannot trigger indexing or delete indexes.
	action := func(name string, run func(project string) (map[string]any, error)) {
		mux.HandleFunc("POST /"+name, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Dotweavier") != "1" {
				http.Error(w, "missing X-Dotweavier header", http.StatusForbidden)
				return
			}
			p := r.URL.Query().Get("project")
			t0 := time.Now()
			res, err := run(p)
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
	action("sync", func(p string) (map[string]any, error) {
		res, err := src.Sync(p)
		return map[string]any{"results": res}, err
	})
	// POST /remove?project=NAME deletes the indexes of the project and the projects nested in it.
	action("remove", func(p string) (map[string]any, error) {
		removed, err := src.Remove(p)
		return map[string]any{"removed": removed}, err
	})
	mux.Handle("GET /overview_prototype_assets/", http.FileServerFS(overviewAssets))
	// Field trace page, served so it can continue a trace: GET /ui/trace-field.html?t=RUN
	// (the same static page the CLI writes), GET /trace-field-history/RUN.js, and
	// POST /trace-field?addr=ADDR, which traces like the CLI, saves the run and returns its name.
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
		f, err := domain.Source(*ws, r.URL.Query().Get("loc"))
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(f)
	})
	mux.HandleFunc("POST /trace-field", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Dotweavier") != "1" { // same CSRF guard as sync and remove
			http.Error(w, "missing X-Dotweavier header", http.StatusForbidden)
			return
		}
		a := r.URL.Query().Get("addr")
		t0 := time.Now()
		reply := func(code int, v map[string]any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(v)
		}
		tr, err := domain.TraceField(*ws, a, false, 10)
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

	ln, err := net.Listen("tcp", *addr) // loopback by default: the page exposes source names
	must(err)
	fmt.Printf("overview -> http://%s/\n", ln.Addr())
	fmt.Printf("field traces -> http://%s/ui/trace-field.html\n", ln.Addr())
	must(http.Serve(ln, mux))
}
