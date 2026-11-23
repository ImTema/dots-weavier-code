package domain

import (
	"fmt"
	"go/constant"
	"go/types"
	"path"
	"slices"

	"golang.org/x/tools/go/ssa"
)

// Pubsub hops (ticket .scratch/gofacon-mvp/issues/06). Ends reuse graph.Calls and
// graph.Impls with these key prefixes, so they are stored and joined like gRPC.
//
//   - topic key, sure hop: the topic is bound where the *pubsub.Publisher or the
//     subscription is made, never at Publish/Receive, so it is followed through
//     the struct field that holds the client.
//   - type key, guess hop: the proto type marshalled next to Publish and the one
//     unmarshalled by a subscriber project. The same type can travel on several
//     topics, so a type match alone is only a guess.
//
// shortcut: only cloud.google.com/go/pubsub/v2 with constant names; upgrade with
// recognisers for pkg/pubsub (Topic/Subscription wiring, typed SDK handlers)
// and config-sourced topics.
const (
	topicKey = "topic "
	typeKey  = "type "

	psPkg          = "cloud.google.com/go/pubsub/v2"
	fnPublisher    = "(*" + psPkg + ".Client).Publisher"
	fnSubscriber   = "(*" + psPkg + ".Client).Subscriber"
	fnPublish      = "(*" + psPkg + ".Publisher).Publish"
	fnReceive      = "(*" + psPkg + ".Subscriber).Receive"
	fnCreateSub    = "(*" + psPkg + "/apiv1.SubscriptionAdminClient).CreateSubscription"
	fnProtoMarshal = "google.golang.org/protobuf/proto.Marshal"
	fnProtoUnmarsh = "google.golang.org/protobuf/proto.Unmarshal"
)

func calleeName(c *ssa.CallCommon) string {
	if sc := c.StaticCallee(); sc != nil {
		if fn, ok := sc.Object().(*types.Func); ok {
			return fn.FullName()
		}
	}
	return ""
}

// strVal resolves a string constant, or fmt.Sprintf over constants with "*" for
// unknown arguments (e.g. "projects/%s/topics/%s" with a runtime project ID).
func strVal(v ssa.Value) (string, bool) {
	if k, ok := v.(*ssa.Const); ok && k.Value != nil && k.Value.Kind() == constant.String {
		return constant.StringVal(k.Value), true
	}
	call, ok := v.(*ssa.Call)
	if !ok || calleeName(&call.Call) != "fmt.Sprintf" || len(call.Call.Args) != 2 {
		return "", false
	}
	format, ok := strVal(call.Call.Args[0])
	if !ok {
		return "", false
	}
	var args []any
	if sl, ok := call.Call.Args[1].(*ssa.Slice); ok {
		if arr, ok := sl.X.(*ssa.Alloc); ok {
			for _, r := range *arr.Referrers() {
				ia, ok := r.(*ssa.IndexAddr)
				if !ok {
					continue
				}
				arg := "*"
				for _, rr := range *ia.Referrers() {
					if st, ok := rr.(*ssa.Store); ok {
						if mi, ok := st.Val.(*ssa.MakeInterface); ok {
							if s, ok := strVal(mi.X); ok {
								arg = s
							}
						}
					}
				}
				args = append(args, arg)
			}
		}
	}
	return fmt.Sprintf(format, args...), true
}

func fieldOf(fa *ssa.FieldAddr) *types.Var {
	return fa.X.Type().Underlying().(*types.Pointer).Elem().Underlying().(*types.Struct).Field(fa.Field)
}

// protoType names the Go type behind a proto.Message argument; the Go import
// path identifies it across projects because both ends import the same package.
func protoType(v ssa.Value) string {
	mi, ok := v.(*ssa.MakeInterface)
	if !ok {
		return ""
	}
	t := mi.X.Type()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if n, ok := types.Unalias(t).(*types.Named); ok {
		return types.TypeString(n, nil)
	}
	return ""
}

// pubsubEnds adds the publish ends (to g.Calls) and subscribe ends (to g.Impls) found in fns.
func pubsubEnds(g *graph, fns map[*ssa.Function]bool, nodeOf func(*ssa.Function) int32, keep func(*ssa.Function) bool) {
	// binding is the topic of a (*Client).Publisher call, or the subscription ID of a (*Client).Subscriber call.
	binding := func(v ssa.Value) string {
		call, ok := v.(*ssa.Call)
		if !ok {
			return ""
		}
		if n := calleeName(&call.Call); n != fnPublisher && n != fnSubscriber {
			return ""
		}
		if s, ok := strVal(call.Call.Args[1]); ok {
			return path.Base(s) // accepts both an ID and projects/p/topics/ID
		}
		return ""
	}
	bound := map[*types.Var]string{} // struct field holding a Publisher/Subscriber -> its binding
	subTopic := map[string]string{}  // subscription ID -> topic ID, from CreateSubscription
	type end struct {
		f    *ssa.Function
		recv ssa.Value
	}
	var pubs, subs []end
	marshals := map[*ssa.Function][]string{}
	type typed struct {
		f *ssa.Function
		t string
	}
	var unmarshals []typed

	for f := range fns {
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				switch in := in.(type) {
				case *ssa.Store:
					if fa, ok := in.Addr.(*ssa.FieldAddr); ok {
						if id := binding(in.Val); id != "" {
							bound[fieldOf(fa)] = id
						}
					}
				case ssa.CallInstruction:
					c := in.Common()
					switch calleeName(c) {
					case fnPublish:
						pubs = append(pubs, end{f, c.Args[0]})
					case fnReceive:
						var h *ssa.Function
						switch fn := c.Args[2].(type) {
						case *ssa.MakeClosure:
							h = fn.Fn.(*ssa.Function)
						case *ssa.Function:
							h = fn
						}
						if keep(h) {
							subs = append(subs, end{h, c.Args[0]})
						}
					case fnCreateSub:
						var name, topic string
						if a, ok := c.Args[2].(*ssa.Alloc); ok {
							for _, r := range *a.Referrers() {
								fa, ok := r.(*ssa.FieldAddr)
								if !ok {
									continue
								}
								for _, rr := range *fa.Referrers() {
									if st, ok := rr.(*ssa.Store); ok {
										if s, ok := strVal(st.Val); ok {
											switch fieldOf(fa).Name() {
											case "Name":
												name = path.Base(s)
											case "Topic":
												topic = path.Base(s)
											}
										}
									}
								}
							}
						}
						if name != "" && topic != "" && name != "*" && topic != "*" {
							subTopic[name] = topic
						}
					case fnProtoMarshal:
						if t := protoType(c.Args[0]); t != "" {
							marshals[f] = append(marshals[f], t)
						}
					case fnProtoUnmarsh:
						if t := protoType(c.Args[1]); t != "" {
							unmarshals = append(unmarshals, typed{f, t})
						}
					}
				}
			}
		}
	}

	// recvID follows a Publish/Receive receiver to its binding: a direct call, or a struct field.
	recvID := func(v ssa.Value) string {
		if id := binding(v); id != "" {
			return id
		}
		if u, ok := v.(*ssa.UnOp); ok {
			if fa, ok := u.X.(*ssa.FieldAddr); ok {
				return bound[fieldOf(fa)]
			}
		}
		return ""
	}
	serve := func(key string, n int32) {
		if !slices.Contains(g.Impls[key], n) {
			g.Impls[key] = append(g.Impls[key], n)
		}
	}
	for _, p := range pubs {
		from := nodeOf(p.f)
		if t := recvID(p.recv); t != "" {
			g.Calls = append(g.Calls, grpcCall{from, topicKey + t, "pubsub"})
		}
		for _, t := range marshals[p.f] {
			g.Calls = append(g.Calls, grpcCall{from, typeKey + t, "pubsub"})
		}
	}
	for _, s := range subs {
		if t := subTopic[recvID(s.recv)]; t != "" {
			serve(topicKey+t, nodeOf(s.f))
		}
	}
	if len(subs) > 0 { // only a project that receives counts its unmarshals as subscribe ends
		for _, u := range unmarshals {
			if keep(u.f) {
				serve(typeKey+u.t, nodeOf(u.f))
			}
		}
	}
}
