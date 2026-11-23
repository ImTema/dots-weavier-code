package domain

import "testing"

func TestStepAtAndQualifyLocs(t *testing.T) {
	leaf := &FieldStep{Kind: "arg", Loc: "internal/x.go:9", Via: "internal/y.go:3"}
	other := &FieldStep{Kind: "read", Loc: "golang/pricing:internal/z.go:4"}
	uses := []*FieldStep{{Kind: "seed", Loc: "a.go:1", Kids: []*FieldStep{other, leaf}}}

	if s, err := stepAt(uses, []int{0, 1}); err != nil || s != leaf {
		t.Fatalf("stepAt(0.1) = %v, %v; want the leaf", s, err)
	}
	for _, bad := range [][]int{nil, {1}, {0, 2}, {0, -1}} {
		if _, err := stepAt(uses, bad); err == nil {
			t.Errorf("stepAt(%v) found a step", bad)
		}
	}

	qualifyLocs(uses, "java/inventory")
	for got, want := range map[string]string{
		uses[0].Loc: "java/inventory:a.go:1",
		leaf.Loc:    "java/inventory:internal/x.go:9",
		leaf.Via:    "java/inventory:internal/y.go:3",
		other.Loc:   "golang/pricing:internal/z.go:4", // already qualified: kept
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}
