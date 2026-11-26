// Field trace history: every kept run is a JSON file in ~/.dotweavier/trace-field-history,
// so any interface can list, reopen and continue it.

package domain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Runs saved before runs were plain JSON are scripts that set window.TRACE; they still load.
const legacyPrefix = "window.TRACE = "

// runTime is the layout of the time a run's name starts with.
const runTime = "20060102-150405.000"

// historyDir is the folder that holds the kept runs, <name>.json each.
func historyDir() (string, error) {
	d, err := dotweavierDir()
	return filepath.Join(d, "trace-field-history"), err
}

// saveRun keeps r in the history under a new name, <time>_<address>, which it sets in r.
// ponytail: history is never pruned; add a cap when the folder grows large.
func saveRun(r *FieldRun) error {
	dir, err := historyDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Milliseconds keep runs apart: a follow often saves within the second of its trace.
	r.Name = time.Now().Format(runTime) + "_" + AddrFileName(r.Addr)
	js, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, r.Name+".json"), js, 0o644)
}

// FieldRunInfo is one kept run, as FieldRuns lists it.
type FieldRunInfo struct {
	Name string    `json:"name"`
	Addr string    `json:"addr"`
	At   time.Time `json:"at"`
}

// FieldRuns lists the kept runs, newest first.
// ponytail: reads every run file for its address; keep an index file if listing gets slow.
func FieldRuns() ([]FieldRunInfo, error) {
	dir, err := historyDir()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, ext := range []string{".json", ".js"} {
		fs, _ := filepath.Glob(filepath.Join(dir, "*_*"+ext))
		files = append(files, fs...)
	}
	var runs []FieldRunInfo
	for _, f := range files {
		n := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(f), ".json"), ".js")
		at, _, _ := strings.Cut(n, "_")
		t, err := time.ParseInLocation(runTime, at, time.Local)
		if err != nil { // runs saved before milliseconds were added
			t, err = time.ParseInLocation("20060102-150405", at, time.Local)
		}
		if err != nil {
			continue
		}
		var head struct{ Addr string } // the real address; the name has a mangled copy
		if b, err := os.ReadFile(f); err == nil {
			_ = json.Unmarshal(runJSON(b), &head)
		}
		runs = append(runs, FieldRunInfo{n, head.Addr, t})
	}
	slices.SortFunc(runs, func(a, b FieldRunInfo) int { return b.At.Compare(a.At) })
	return runs, nil
}

// LoadFieldRun reads run name back from the history; "" is the newest run. A
// run saved before runs kept their folder gets the registered folder that has
// its seed's file, and one saved before runs kept Only gets it from the tree
// it left out.
func LoadFieldRun(name string) (*FieldRun, error) {
	if name == "" {
		runs, err := FieldRuns()
		if err != nil {
			return nil, err
		}
		if len(runs) == 0 {
			return nil, errors.New("no field trace run kept yet")
		}
		name = runs[0].Name
	}
	if filepath.Base(name) != name || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return nil, fmt.Errorf("bad run %q", name)
	}
	dir, err := historyDir()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(dir, name+".json"))
	if errors.Is(err, os.ErrNotExist) {
		b, err = os.ReadFile(filepath.Join(dir, name+".js"))
	}
	if err != nil {
		return nil, err
	}
	r := &FieldRun{}
	if err := json.Unmarshal(runJSON(b), r); err != nil {
		return nil, err
	}
	r.Name = name
	if r.Only == "" && len(r.Uses) == 0 { // old runs dropped the tree --only left out
		r.Only = "origin"
	} else if r.Only == "" && len(r.Origin) == 0 {
		r.Only = "uses"
	}
	if r.Dir == "" {
		if r.Dir, err = legacyRunFolder(r.Addr); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// runJSON is the JSON in a run file, of either format.
func runJSON(b []byte) []byte {
	return bytes.TrimSuffix(bytes.TrimSpace(bytes.TrimPrefix(b, []byte(legacyPrefix))), []byte(";"))
}

// FollowQuery is what FollowFieldRun follows in a kept run.
type FollowQuery struct {
	Run      string       // the kept run's name
	Step     string       // "I.J.K": the lifecycle step at r.Uses[I].Kids[J].Kids[K]; "" follows every hop
	Hop      int          // with Step: the step's hop to follow
	Depth    int          // without Step: services deep; 0: 1
	Progress func(string) // optional: told each phase as it starts
}

// FollowFieldRun continues a kept run with the options it ran with: it follows
// one hop of a step, or every hop Depth services deep (at most maxFollow hops),
// and keeps the result as a new run, which it returns with the warnings the
// follow added. A handler already traced in the run is not traced again.
// Following every hop fails when none could be followed; the hops that failed
// are warnings.
func FollowFieldRun(ctx context.Context, q FollowQuery) (*FieldRun, []string, error) {
	r, err := LoadFieldRun(q.Run)
	if err != nil {
		return nil, nil, err
	}
	seen := len(r.Warnings)
	opts := r.FieldQuery
	opts.Progress = q.Progress
	run := &fieldRun{r: &r.FieldTrace, q: opts}
	if q.Step == "" {
		n, errs := run.followHops(ctx, max(q.Depth, 1))
		if n == 0 && len(errs) > 0 {
			return nil, r.Warnings[seen:], errs[0]
		}
		if n == 0 {
			return nil, nil, errors.New("no hop left to follow")
		}
		for _, e := range errs {
			r.Warnings = append(r.Warnings, "hop not followed: "+e.Error())
		}
	} else {
		var path []int
		for _, p := range strings.Split(q.Step, ".") {
			i, err := strconv.Atoi(p)
			if err != nil {
				return nil, nil, fmt.Errorf("bad step %q", q.Step)
			}
			path = append(path, i)
		}
		if err := run.followHop(ctx, path, q.Hop); err != nil {
			return nil, r.Warnings[seen:], err
		}
	}
	return r, r.Warnings[seen:], saveRun(r)
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
