package domain

import (
	"math"
	"testing"
)

// A tight clump, including two stars on the same spot, ends up with no pair much closer than the gap.
func TestSeparateBreaksUpCrowdedSpots(t *testing.T) {
	pos := []vec{{0, 0, 0}, {0, 0, 0}, {0.1, 0, 0}, {0, 0.2, 0}, {0.1, 0.1, 0.1}, {10, 10, 10}}
	separate(pos, 2, 50)
	for i := range pos {
		for j := i + 1; j < len(pos); j++ {
			d := pos[j].add(pos[i], -1)
			if l := math.Sqrt(d[0]*d[0] + d[1]*d[1] + d[2]*d[2]); l < 1.9 {
				t.Errorf("stars %d and %d are %.2f apart, want >= 1.9", i, j, l)
			}
		}
	}
	if pos[5] != (vec{10, 10, 10}) {
		t.Errorf("a lone star moved to %v", pos[5])
	}
}
