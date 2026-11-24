package domain

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A module joins the outermost shown project containing it, even when it is listed
// before that project; a module of a hidden project heads its own group.
func TestModuleGroups(t *testing.T) {
	names := []string{"apid/payment/sdk", "apid", "scoring", "apid/checkout/sdk", "apid/checkout", "other/sdk", "scoringx"}
	shown := []int{0, 1, 2, 3, 4, 5, 6} // "other" is not shown
	radius := []float64{2, 10, 8, 3, 4, 1, 5}
	groups, ring := moduleGroups(names, shown, radius, 6)
	require.Equal(t, [][]int{{1, 0, 3, 4}, {2}, {5}, {6}}, groups)
	assert.Equal(t, 10.0+4+6, ring[1].dist, "apid ring: modules clear of the project")
	assert.Equal(t, ring[1].dist+4, ring[1].outer, "apid ring: modules clear of the project")
	assert.Equal(t, 0.0, ring[2].dist, "scoring ring: just the project")
	assert.Equal(t, 8.0, ring[2].outer, "scoring ring: just the project")
	// Many modules: the ring grows so neighbours do not overlap.
	many := []string{"p"}
	sh, rad := []int{0}, []float64{1}
	for i := range 40 {
		many = append(many, "p/m"+string(rune('a'+i%26))+string(rune('a'+i/26)))
		sh, rad = append(sh, i+1), append(rad, 3)
	}
	_, ring = moduleGroups(many, sh, rad, 6)
	assert.GreaterOrEqual(t, 2*math.Pi*ring[0].dist, 40*(2*3+6)-1e-9, "ring of 40 modules too short")
}
