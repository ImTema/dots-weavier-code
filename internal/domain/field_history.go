// Field trace history: every run is kept in ~/.dotweavier/trace-field-history, so
// any interface can list, reopen and continue it.

package domain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// A run file is a script that sets window.TRACE, so a static page can load it
// with <script src> from file://, where fetch() of a local file fails.
const tracePrefix = "window.TRACE = "

// savedRun is a run file: the run, whole so it can be continued, and the levels
// a page opens at first.
type savedRun struct {
	Addr         string       `json:"addr"`
	Open         int          `json:"open,omitempty"`
	Origin       []*FieldStep `json:"origin"`
	Uses         []*FieldStep `json:"uses"`
	Warnings     []string     `json:"warnings,omitempty"`
	Dir          string       `json:"dir,omitempty"` // empty in runs saved before runs kept their folder
	IncludeTests bool         `json:"includeTests,omitempty"`
	MaxProjects  int          `json:"maxProjects,omitempty"`
}

// FieldHistoryDir is the folder that holds the saved runs: <name>.js for each,
// latest.js, and index.js (window.HISTORY, newest first).
func FieldHistoryDir() (string, error) {
	d, err := dotweavierDir()
	return filepath.Join(d, "trace-field-history"), err
}

// FieldRunScript is run r as a run file has it, with open the levels a page opens
// at first (0: the page's default).
func FieldRunScript(r *FieldTrace, open int) ([]byte, error) {
	js, err := json.Marshal(savedRun{r.Addr, open, r.Origin, r.Uses, r.Warnings, r.Dir, r.IncludeTests, r.MaxProjects}) // escapes <, > and &, so it is safe inside a script
	if err != nil {
		return nil, err
	}
	return append(append([]byte(tracePrefix), js...), ";\n"...), nil
}

// SaveFieldRun keeps run r in the history as <time>_<address>.js and as latest.js,
// and rewrites index.js. It returns the run's name.
// ponytail: history is never pruned; add a cap when the folder grows large.
func SaveFieldRun(r *FieldTrace, open int) (string, error) {
	data, err := FieldRunScript(r, open)
	if err != nil {
		return "", err
	}
	dir, err := FieldHistoryDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// Milliseconds keep runs apart: a follow often saves within the second of its trace.
	name := time.Now().Format("20060102-150405.000") + "_" + AddrFileName(r.Addr)
	for _, f := range []string{name + ".js", "latest.js"} {
		if err := os.WriteFile(filepath.Join(dir, f), data, 0o644); err != nil {
			return "", err
		}
	}

	type run struct {
		Name string `json:"name"`
		Addr string `json:"addr"`
		At   string `json:"at"`
	}
	var runs []run
	files, _ := filepath.Glob(filepath.Join(dir, "*_*.js"))
	for i := len(files) - 1; i >= 0; i-- { // names start with the time, so sorted means oldest first
		n := strings.TrimSuffix(filepath.Base(files[i]), ".js")
		at, _, _ := strings.Cut(n, "_")
		t, err := time.ParseInLocation("20060102-150405.000", at, time.Local)
		if err != nil { // runs saved before milliseconds were added
			t, err = time.ParseInLocation("20060102-150405", at, time.Local)
		}
		if err != nil {
			continue
		}
		var head struct{ Addr string } // the real address; the file name is a mangled copy
		if b, err := os.ReadFile(files[i]); err == nil {
			_ = json.Unmarshal(scriptJSON(b), &head)
		}
		runs = append(runs, run{n, head.Addr, t.Format("2006-01-02 15:04:05")})
	}
	js, err := json.Marshal(runs)
	if err != nil {
		return "", err
	}
	return name, os.WriteFile(filepath.Join(dir, "index.js"), append(append([]byte("window.HISTORY = "), js...), ";\n"...), 0o644)
}

// LoadFieldRun reads run name back from the history, with the levels its page
// opens. A run saved before runs kept their folder gets the registered folder
// that has its seed's file.
func LoadFieldRun(name string) (*FieldTrace, int, error) {
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
		return nil, 0, fmt.Errorf("bad run %q", name)
	}
	dir, err := FieldHistoryDir()
	if err != nil {
		return nil, 0, err
	}
	b, err := os.ReadFile(filepath.Join(dir, name+".js"))
	if err != nil {
		return nil, 0, err
	}
	var f savedRun
	if err := json.Unmarshal(scriptJSON(b), &f); err != nil {
		return nil, 0, err
	}
	r := &FieldTrace{Addr: f.Addr, Origin: f.Origin, Uses: f.Uses, Warnings: f.Warnings, Dir: f.Dir,
		IncludeTests: f.IncludeTests, MaxProjects: f.MaxProjects}
	if r.Dir == "" {
		a, lang, err := fieldAddr(r.Addr)
		if err != nil {
			return nil, 0, err
		}
		fs, err := registryList(`SELECT DISTINCT dir FROM workspace_folders`)
		if err != nil {
			return nil, 0, err
		}
		if r.Dir, err = folderWithFile(fs, a, lang); err != nil {
			return nil, 0, err
		}
	}
	return r, f.Open, nil
}

// scriptJSON is the JSON in a run file.
func scriptJSON(b []byte) []byte {
	return bytes.TrimSuffix(bytes.TrimSpace(bytes.TrimPrefix(b, []byte(tracePrefix))), []byte(";"))
}

// FollowQuery is what FollowFieldRun follows in a saved run.
type FollowQuery struct {
	Run      string       // the saved run's name
	Step     string       // "I.J.K": the lifecycle step at r.Uses[I].Kids[J].Kids[K]; "" follows every hop
	Hop      int          // with Step: the step's hop to follow
	Depth    int          // without Step: services deep; 0: 1
	Progress func(string) // optional: told each phase as it starts
}

// FollowFieldRun continues a saved run: it follows one hop of a step, or every
// hop Depth services deep (at most 10 hops), and saves the result as a new run,
// whose name it returns with the warnings the follow added. Following every
// hop fails only when none could be followed; the hops that failed are warnings.
func FollowFieldRun(ctx context.Context, q FollowQuery) (name string, warnings []string, err error) {
	r, open, err := LoadFieldRun(q.Run)
	if err != nil {
		return "", nil, err
	}
	seen := len(r.Warnings)
	run, err := continueRun(r, q.Progress)
	if err != nil {
		return "", nil, err
	}
	if q.Step == "" {
		n, errs := run.followHops(ctx, max(q.Depth, 1))
		if n == 0 && len(errs) > 0 {
			return "", r.Warnings[seen:], errs[0]
		}
		for _, e := range errs {
			r.Warnings = append(r.Warnings, "hop not followed: "+e.Error())
		}
	} else {
		var path []int
		for _, p := range strings.Split(q.Step, ".") {
			i, err := strconv.Atoi(p)
			if err != nil {
				return "", nil, fmt.Errorf("bad step %q", q.Step)
			}
			path = append(path, i)
		}
		if err := run.followHop(ctx, path, q.Hop); err != nil {
			return "", r.Warnings[seen:], err
		}
	}
	name, err = SaveFieldRun(r, open)
	return name, r.Warnings[seen:], err
}

// AddrFileName turns an address into a file name: golang/orders:a/b.go:45 -> golang_orders_a_b.go_45.
func AddrFileName(addr string) string {
	return strings.Map(func(r rune) rune {
		if r == '.' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return r
		}
		return '_'
	}, addr)
}

// continueRun picks up run r, as TraceField returned it, to follow more hops.
func continueRun(r *FieldTrace, progress func(string)) (*fieldRun, error) {
	if r.Dir == "" {
		return nil, errors.New("the run has no folder to follow hops in")
	}
	return &fieldRun{r: r, q: FieldQuery{Dir: r.Dir, IncludeTests: r.IncludeTests, MaxProjects: r.MaxProjects, Progress: progress}}, nil
}
