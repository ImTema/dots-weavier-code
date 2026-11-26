package domain

import (
	"context"
	"path/filepath"

	"golang.org/x/tools/go/ssa"
)

// maxFollow caps the hops one call of TraceField or FollowFieldRun follows.
const maxFollow = 10

// fieldRun is a field trace run while it traces and follows hops: its options,
// with Dir the folder it traces, and the joined graph of the indexes in Dir,
// loaded on first use and kept for every hop.
type fieldRun struct {
	q      FieldQuery
	loc    locator     // resolves the run's addresses, discovering each folder once
	r      *FieldTrace // the run's result, once traced; hops graft into it
	g      *graph
	gErr   error
	loaded bool
}

// trace traces the value at addr ([project:]rel/path:LINE in the run's folder)
// and attaches call paths and hops. keep, if set, picks a Go trace's seeds. A
// JVM trace seeds at the first of lines lines from LINE on that computes a value.
func (run *fieldRun) trace(ctx context.Context, addr string, keep func(ssa.Value) bool, lines int) (*FieldTrace, error) {
	if _, _, err := fieldAddr(addr); err != nil {
		return nil, err
	}
	sp, err := run.loc.in(run.q.Dir, addr)
	if err != nil {
		return nil, err
	}
	return run.traceAt(ctx, sp, keep, lines)
}

// traceAt is trace of the seed at sp, resolved in the run's folder.
func (run *fieldRun) traceAt(ctx context.Context, sp spot, keep func(ssa.Value) bool, lines int) (*FieldTrace, error) {
	q, lang, line := run.q, langOfExt(sp.File), sp.Addr.Line
	projects, seedProject, file := sp.Projects, sp.Project, sp.File
	var r *FieldTrace
	var err error
	if lang == "jvm" {
		report(q.Progress, "tracing "+seedProject.Name)
		r, line, err = traceJVMField(ctx, seedProject, file, line, lines, q.IncludeTests)
	} else {
		var t *tracker
		if t, err = traceField(ctx, projects, seedProject, file, line, q.IncludeTests, q.maxProjects(), keep, q.Progress); err == nil {
			r = t.result(newPrinter(seedProject, projects))
		}
	}
	if err != nil {
		return nil, err
	}
	run.attach(r, address{Project: seedProject.Name, File: sp.Addr.File, Line: line})
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
