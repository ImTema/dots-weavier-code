package domain

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A saved run reads back whole, so following from a page continues it with the
// options it ran with.
func TestSavedFieldRunRoundTrips(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := &FieldTrace{Addr: "golang/orders:a.go:3", Dir: "/work/samples", IncludeTests: true, MaxProjects: 3,
		Warnings: []string{"loaded golang/orders"},
		Uses:     []*FieldStep{{Kind: "seed", Loc: "a.go:3", Hops: []Hop{{Kind: "grpc", Addr: "golang/pricing:s.go:9"}}}}}
	name, err := SaveFieldRun(r, 4)
	require.NoError(t, err)
	got, open, err := LoadFieldRun(name)
	require.NoError(t, err)
	assert.Equal(t, r, got)
	assert.Equal(t, 4, open)

	_, _, err = FollowFieldRun(t.Context(), FollowQuery{Run: name, Step: "0.x"})
	assert.ErrorContains(t, err, `bad step "0.x"`)
	_, _, err = FollowFieldRun(t.Context(), FollowQuery{Run: "../x"})
	assert.ErrorContains(t, err, "bad run")
}

// Following one hop of a saved run, then every hop, each saves a new run that
// reads back with the hop followed and the options kept.
func TestFollowFieldRun(t *testing.T) {
	isolate(t)
	_, err := InitWorkspace("../../samples", "")
	require.NoError(t, err)
	require.NoError(t, Index(t.Context(), IndexOptions{Names: []string{"golang/orders", "golang/pricing"}}))
	r, err := TraceField(t.Context(), FieldQuery{Dir: "../../samples", MaxProjects: 2}, "golang/orders:internal/pricer/pricer.go:23")
	require.NoError(t, err)
	name, err := SaveFieldRun(r, 3)
	require.NoError(t, err)

	path := hopPath(r.Uses, nil)
	require.NotNil(t, path, "no hop in %v", hopsIn(r.Uses))
	step := strings.Trim(strings.Join(strings.Fields(fmt.Sprint(path)), "."), "[]")
	one, _, err := FollowFieldRun(t.Context(), FollowQuery{Run: name, Step: step})
	require.NoError(t, err)
	got, open, err := LoadFieldRun(one)
	require.NoError(t, err)
	assert.Equal(t, 3, open)
	assert.Equal(t, 2, got.MaxProjects)
	assert.Equal(t, r.Dir, got.Dir)
	s, err := stepAt(got.Uses, path)
	require.NoError(t, err)
	assert.True(t, s.Hops[0].Followed, "hop at %s not followed", step)

	_, _, err = FollowFieldRun(t.Context(), FollowQuery{Run: one})
	assert.NoError(t, err)
}

// hopPath is the path of the first step under ss with an unfollowed first hop, or nil.
func hopPath(ss []*FieldStep, path []int) []int {
	for i, s := range ss {
		p := append(append([]int(nil), path...), i)
		if len(s.Hops) > 0 && !s.Hops[0].Followed {
			return p
		}
		if q := hopPath(s.Kids, p); q != nil {
			return q
		}
	}
	return nil
}
