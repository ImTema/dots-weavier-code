package domain

import (
	"context"
	"errors"
	"path/filepath"

	"golang.org/x/tools/go/ssa"
)

// maxFollow caps the hops one call of TraceField or FollowHops follows.
const maxFollow = 10

// fieldRun is a field trace run while it traces and follows hops: its options,
// with Dir the folder it traces, and the joined graph of the indexes in Dir,
// loaded on first use and kept for every hop.
type fieldRun struct {
	q      FieldQuery
	r      *FieldTrace // the run's result, once traced; hops graft into it
	g      *graph
	gErr   error
	loaded bool
}

// continueRun picks up run r, as TraceField returned it, to follow more hops.
func continueRun(r *FieldTrace, progress func(string)) (*fieldRun, error) {
	if r.Dir == "" {
		return nil, errors.New("the run has no folder to follow hops in")
	}
	return &fieldRun{r: r, q: FieldQuery{Dir: r.Dir, IncludeTests: r.IncludeTests, MaxProjects: r.MaxProjects, Progress: progress}}, nil
}

// trace traces the value at addr ([project:]rel/path:LINE in the run's folder)
// and attaches call paths and hops. keep, if set, picks a Go trace's seeds.
func (run *fieldRun) trace(ctx context.Context, addr string, keep func(ssa.Value) bool) (*FieldTrace, error) {
	q := run.q
	loc, lang, line, err := parseFieldAddr(addr)
	if err != nil {
		return nil, err
	}
	projects, err := langProjects(q.Dir, lang)
	if err != nil {
		return nil, err
	}
	seedProject, file, err := resolveFile(projects, loc)
	if err != nil {
		return nil, err
	}
	var r *FieldTrace
	if lang == "jvm" {
		report(q.Progress, "tracing "+seedProject.Name)
		r, _, err = traceJVMField(ctx, seedProject, file, line, q.IncludeTests)
	} else {
		var t *tracker
		if t, err = traceField(ctx, projects, seedProject, file, line, q.IncludeTests, q.maxProjects(), keep, q.Progress); err == nil {
			r = fieldTrace(t, seedProject, projects)
		}
	}
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(seedProject.dir, file)
	if err != nil {
		return nil, err
	}
	run.attach(r, address{Project: seedProject.Name, File: filepath.ToSlash(rel), Line: line})
	return r, nil
}

// graph is the joined graph of the indexes in the run's folder, loaded once;
// warnings of the load go to r.
func (run *fieldRun) graph(r *FieldTrace) (*graph, error) {
	if !run.loaded {
		run.loaded = true
		abs, err := filepath.Abs(run.q.Dir)
		if err == nil {
			var warns []string
			run.g, warns, err = mergedGraph([]string{abs})
			r.Warnings = append(r.Warnings, warns...)
		}
		run.gErr = err
	}
	return run.g, run.gErr
}
