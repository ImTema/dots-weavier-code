package domain

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A tight clump, including two stars on the same spot, ends up with no pair much closer than the gap.
func TestSeparateBreaksUpCrowdedSpots(t *testing.T) {
	pos := []vec{{0, 0, 0}, {0, 0, 0}, {0.1, 0, 0}, {0, 0.2, 0}, {0.1, 0.1, 0.1}, {10, 10, 10}}
	separate(pos, 2, 50)
	for i := range pos {
		for j := i + 1; j < len(pos); j++ {
			d := pos[j].add(pos[i], -1)
			l := math.Sqrt(d[0]*d[0] + d[1]*d[1] + d[2]*d[2])
			assert.GreaterOrEqual(t, l, 1.9, "stars %d and %d too close", i, j)
		}
	}
	assert.Equal(t, vec{10, 10, 10}, pos[5], "a lone star moved")
}
