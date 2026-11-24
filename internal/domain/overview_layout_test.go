package domain

import (
	"math"
	"reflect"
	"testing"
)

// A module joins the outermost shown project containing it, even when it is listed
// before that project; a module of a hidden project heads its own group.
func TestModuleGroups(t *testing.T) {
	names := []string{"apid/payment/sdk", "apid", "scoring", "apid/checkout/sdk", "apid/checkout", "other/sdk", "scoringx"}
	shown := []int{0, 1, 2, 3, 4, 5, 6} // "other" is not shown
	radius := []float64{2, 10, 8, 3, 4, 1, 5}
	groups, ring := moduleGroups(names, shown, radius, 6)
	want := [][]int{{1, 0, 3, 4}, {2}, {5}, {6}}
	if !reflect.DeepEqual(groups, want) {
		t.Fatalf("groups %v, want %v", groups, want)
	}
	if r := ring[1]; r.dist != 10+4+6 || r.outer != r.dist+4 {
		t.Errorf("apid ring %+v, want modules clear of the project", r)
	}
	if r := ring[2]; r.dist != 0 || r.outer != 8 {
		t.Errorf("scoring ring %+v, want just the project", r)
	}
	// Many modules: the ring grows so neighbours do not overlap.
	many := []string{"p"}
	sh, rad := []int{0}, []float64{1}
	for i := range 40 {
		many = append(many, "p/m"+string(rune('a'+i%26))+string(rune('a'+i/26)))
		sh, rad = append(sh, i+1), append(rad, 3)
	}
	_, ring = moduleGroups(many, sh, rad, 6)
	if d := ring[0].dist; 2*math.Pi*d < 40*(2*3+6)-1e-9 {
		t.Errorf("ring of 40 modules has circumference %.1f, too short", 2*math.Pi*d)
	}
}
