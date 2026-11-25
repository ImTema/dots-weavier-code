package domain

import (
	"context"
	"fmt"
	"go/types"
	"strconv"
	"strings"

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

// hopsOf lists the cross-project hops out of node n whose handler has a file to trace.
func hopsOf(g *graph, n int32) []Hop {
	var hs []Hop
	from := g.Nodes[n]
	for _, e := range g.Out[n] {
		if e.Kind < grpcHop {
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

// TraceHop traces the request the handler declared at addr (Hop.Addr) receives, every location qualified by project for grafting.
func TraceHop(ctx context.Context, q FieldQuery, addr string) (*FieldTrace, error) {
	i := strings.LastIndex(addr, ":")
	line, err := strconv.Atoi(addr[i+1:])
	if i < 0 || err != nil {
		return nil, fmt.Errorf("bad handler address %q", addr)
	}
	proj, _, _ := strings.Cut(addr, ":")
	var r *FieldTrace
	if strings.HasSuffix(addr[:i], ".go") {
		r, err = traceGoHandler(ctx, q, addr[:i], line)
	} else {
		for l := line; l < line+12; l++ { // annotations, then the signature
			if r, err = TraceField(ctx, q, addr[:i]+":"+strconv.Itoa(l)); err == nil {
				break
			}
		}
	}
	if err != nil {
		return nil, err
	}
	qualifyLocs(r.Origin, proj)
	qualifyLocs(r.Uses, proj)
	return r, nil
}

func traceGoHandler(ctx context.Context, q FieldQuery, loc string, line int) (*FieldTrace, error) {
	gos, err := langProjects(q.Dir, "go")
	if err != nil {
		return nil, err
	}
	seedProject, file, err := resolveFile(gos, loc)
	if err != nil {
		return nil, err
	}
	t, err := traceField(ctx, gos, seedProject, file, line, q.IncludeTests, q.maxProjects(), requestParam, q.Progress)
	if err != nil {
		return nil, err
	}
	return fieldTrace(q.Dir, t, seedProject, gos, file, line), nil
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

// qualifyLocs prefixes project to every location that has none ("rel.go:LINE").
func qualifyLocs(ss []*FieldStep, project string) {
	for _, s := range ss {
		if s.Loc != "" && strings.Count(s.Loc, ":") == 1 {
			s.Loc = project + ":" + s.Loc
		}
		if s.Via != "" && strings.Count(s.Via, ":") == 1 {
			s.Via = project + ":" + s.Via
		}
		qualifyLocs(s.Kids, project)
	}
}

// FollowHop grafts the trace of hop k of the lifecycle step at path (indexes from r.Uses down through Kids) under that step.
func FollowHop(ctx context.Context, q FieldQuery, r *FieldTrace, path []int, k int) error {
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
	sub, err := TraceHop(ctx, q, h.Addr)
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

// FollowHops follows the lifecycle's hops and those in what they graft, depth services deep, at most budget hops in all.
// ponytail: breadth-first with a flat budget; per-service limits if traces fan out.
func FollowHops(ctx context.Context, fq FieldQuery, r *FieldTrace, depth, budget int) (followed int, errs []error) {
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
	for len(queue) > 0 && followed < budget {
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
			if h.Followed || followed >= budget {
				continue
			}
			if seen[h.Addr] { // already traced at another step of this trace: do not repeat it
				h.Followed = true
				s.Kids = append(s.Kids, hopStep(h, nil))
				s.Kids[len(s.Kids)-1].Code = h.Method + "  (traced at another step)"
				continue
			}
			seen[h.Addr] = true
			report(fq.Progress, fmt.Sprintf("hop %d: %s %s", followed+1, h.Kind, h.Method))
			if err := FollowHop(ctx, fq, r, q.path, k); err != nil {
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
