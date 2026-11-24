package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStepAtAndQualifyLocs(t *testing.T) {
	leaf := &FieldStep{Kind: "arg", Loc: "internal/x.go:9", Via: "internal/y.go:3"}
	other := &FieldStep{Kind: "read", Loc: "golang/pricing:internal/z.go:4"}
	uses := []*FieldStep{{Kind: "seed", Loc: "a.go:1", Kids: []*FieldStep{other, leaf}}}

	s, err := stepAt(uses, []int{0, 1})
	require.NoError(t, err)
	assert.Same(t, leaf, s)
	for _, bad := range [][]int{nil, {1}, {0, 2}, {0, -1}} {
		_, err := stepAt(uses, bad)
		assert.Error(t, err, "stepAt(%v) found a step", bad)
	}

	qualifyLocs(uses, "java/inventory")
	assert.Equal(t, "java/inventory:a.go:1", uses[0].Loc)
	assert.Equal(t, "java/inventory:internal/x.go:9", leaf.Loc)
	assert.Equal(t, "java/inventory:internal/y.go:3", leaf.Via)
	assert.Equal(t, "golang/pricing:internal/z.go:4", other.Loc) // already qualified: kept
}
