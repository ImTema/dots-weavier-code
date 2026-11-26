package domain

import (
	"context"
	"fmt"
	"go/types"
	"strconv"

	"golang.org/x/tools/go/ssa"
)

// A Hop is an indexed gRPC, HTTP or Pub/Sub call from a step's function into another project's handler.
type Hop struct {
	Kind     string `json:"kind"`               // grpc, http, pubsub, pubsub:guess
	Method   string `json:"method"`             // full gRPC method, "GET /customers/{}", topic
	Project  string `json:"project"`            // the handler's project
	Fn       string `json:"fn"`                 // the handler
	Addr     string `json:"addr"`               // project:rel/path:LINE of the handler's declaration
	Followed bool   `json:"followed,omitempty"` // its trace is grafted under the step
}

// hopsOf lists the cross-project hops out of node n. A library call is no hop:
// it carries no request into a handler. Handlers without a file (stale or
// external nodes) cannot be traced and are left out.
func hopsOf(g *graph, n int32) []Hop {
	var hs []Hop
	from := g.Nodes[n]
	for _, e := range g.Out[n] {
		if !e.Kind.link() || e.Kind == libHop {
			continue
		}
		to := g.Nodes[e.To]
		if to.File == "" || to.Project == from.Project {
			continue
		}
		hs = append(hs, Hop{Kind: e.Kind.String(), Method: e.Method, Project: to.Project, Fn: to.Name,
			Addr: to.Project + ":" + to.File + ":" + strconv.Itoa(to.Line)})
	}
	return hs
}

// traceHop traces the request a handler receives: addr is the handler's
// declaration, as in Hop.Addr. In Go the seeds are the handler's parameters
// except the receiver, a context.Context and an http.ResponseWriter. A JVM
// declaration may start with annotations, so the first line from addr on that
// yields a trace is the signature. Every location in the result carries its
// project, so the trees can be grafted into another project's trace.
func (run *fieldRun) traceHop(ctx context.Context, addr string) (*FieldTrace, error) {
	a, lang, err := fieldAddr(addr)
	if err != nil || a.Project == "" {
		return nil, fmt.Errorf("bad handler address %q", addr)
	}
	proj := a.Project
	var r *FieldTrace
	if lang == "go" {
		r, err = run.trace(ctx, addr, requestParam, 1)
	} else {
		r, err = run.trace(ctx, addr, nil, 12) // annotations, then the signature
	}
	if err != nil {
		return nil, err
	}
	qualifyLocs(r.Origin, proj)
	qualifyLocs(r.Uses, proj)
	return r, nil
}

// requestParam accepts the parameters that carry a request into a handler.
func requestParam(v ssa.Value) bool {
	p, ok := v.(*ssa.Parameter)
	if !ok {
		return false
	}
	if f := p.Parent(); f != nil && f.Signature.Recv() != nil && len(f.Params) > 0 && f.Params[0] == p {
		return false
	}
	switch types.TypeString(p.Type(), nil) {
	case "context.Context", "net/http.ResponseWriter":
		return false
	}
	return true
}

// qualify prefixes project to location loc if it has none ("rel.go:LINE").
func qualify(loc, project string) string {
	if a, err := parseAddr(loc); err == nil && a.Project == "" && a.Line > 0 {
		a.Project = project
		return a.String()
	}
	return loc
}

// qualifyLocs prefixes project to every location that has none ("rel.go:LINE").
func qualifyLocs(ss []*FieldStep, project string) {
	for _, s := range ss {
		s.Loc, s.Via = qualify(s.Loc, project), qualify(s.Via, project)
		qualifyLocs(s.Kids, project)
	}
}

// followHop grafts the trace of hop k of the lifecycle step at path (indexes
// from r.Uses down through Kids) under that step, behind a step of the hop's kind.
func (run *fieldRun) followHop(ctx context.Context, path []int, k int) error {
	r := run.r
	s, err := stepAt(r.Uses, path)
	if err != nil {
		return err
	}
	if k < 0 || k >= len(s.Hops) {
		return fmt.Errorf("step has no hop %d", k)
	}
	h := &s.Hops[k]
	if h.Followed {
		return nil
	}
	sub, err := run.traceHop(ctx, h.Addr)
	if err != nil {
		return err
	}
	r.Warnings = append(r.Warnings, sub.Warnings...)
	s.Kids = append(s.Kids, hopStep(h, sub.Uses))
	h.Followed = true
	return nil
}

// hopStep is the handler step a followed hop adds under its caller, the request's trace below it.
func hopStep(h *Hop, kids []*FieldStep) *FieldStep {
	return &FieldStep{Kind: h.Kind, Name: h.Method, Loc: h.Addr, Code: h.Method, Fn: h.Fn, Kids: kids}
}

// followHops follows every hop on the lifecycle side, then the hops in what it
// grafted, depth services deep, breadth-first, with maxFollow hops for budget. It
// returns how many it followed and why the others failed.
// ponytail: breadth-first with a flat budget; per-service limits if traces fan out.
func (run *fieldRun) followHops(ctx context.Context, depth int) (followed int, errs []error) {
	r := run.r
	type at struct {
		path  []int
		level int // services from the seed: 1 for hops in the seed's own trace
	}
	var queue []at
	seen := map[string]bool{} // handlers already followed in this trace
	var scan func(ss []*FieldStep, path []int, level int)
	scan = func(ss []*FieldStep, path []int, level int) {
		for i, s := range ss {
			p := append(append([]int(nil), path...), i)
			if len(s.Hops) > 0 {
				queue = append(queue, at{p, level})
			}
			scan(s.Kids, p, level)
		}
	}
	scan(r.Uses, nil, 1)
	for len(queue) > 0 && followed < maxFollow {
		if err := ctx.Err(); err != nil {
			return followed, append(errs, err)
		}
		q := queue[0]
		queue = queue[1:]
		if q.level > depth {
			continue
		}
		s, err := stepAt(r.Uses, q.path)
		if err != nil {
			continue
		}
		for k := range s.Hops {
			h := &s.Hops[k]
			if h.Followed || followed >= maxFollow {
				continue
			}
			if seen[h.Addr] { // already traced at another step of this trace: do not repeat it
				h.Followed = true
				s.Kids = append(s.Kids, hopStep(h, nil))
				s.Kids[len(s.Kids)-1].Code = h.Method + "  (traced at another step)"
				continue
			}
			seen[h.Addr] = true
			report(run.q.Progress, fmt.Sprintf("hop %d: %s %s", followed+1, h.Kind, h.Method))
			if err := run.followHop(ctx, q.path, k); err != nil {
				errs = append(errs, fmt.Errorf("%s %s: %w", s.Hops[k].Kind, s.Hops[k].Method, err))
				continue
			}
			followed++
			g := len(s.Kids) - 1 // the grafted hop step; its trees are one service further
			scan(s.Kids[g].Kids, append(append([]int(nil), q.path...), g), q.level+1)
		}
	}
	return followed, errs
}

func stepAt(ss []*FieldStep, path []int) (*FieldStep, error) {
	var s *FieldStep
	for _, i := range path {
		if i < 0 || i >= len(ss) {
			return nil, fmt.Errorf("no step at %v", path)
		}
		s = ss[i]
		ss = s.Kids
	}
	if s == nil {
		return nil, fmt.Errorf("empty path")
	}
	return s, nil
}
