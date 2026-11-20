package domain

import (
	"path/filepath"
	"slices"
	"testing"
)

// TestExtractJVM indexes the Java and Kotlin samples and checks the hops that
// samples/README.md lists for them.
func TestExtractJVM(t *testing.T) {
	g := &graph{ids: map[string]int32{}, Impls: map[string][]int32{}}
	for _, p := range []project{
		{"java/inventory", filepath.Join("..", "..", "samples", "java", "inventory"), "jvm"},
		{"kotlin/shipping", filepath.Join("..", "..", "samples", "kotlin", "shipping"), "jvm"},
	} {
		if _, err := extract(g, p, true); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for from, es := range g.Out {
		for _, e := range es {
			got = append(got, g.Nodes[from].Name+" -"+e.Kind.String()+"-> "+g.Nodes[e.To].Name)
		}
	}
	for _, want := range []string{
		"InventoryGrpcService.reserve -call-> ReservationService.reserve",
		"ReservationService.reserve -iface:guess-> OrdersCustomerDirectory.tier",
		"ReservationService.reserve -call-> StockLedger.reserve",
		"main -call-> Application.shippingModule",
		"Application.shippingModule -call-> ShippingRates.quote",
		"ShippingRates.quote -iface:guess-> OrdersCustomerDirectory.find", // CustomerDirectory itself does not parse
		"ShippingRates.quote -call-> RateCalculator.cents",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %s", want)
		}
	}
	hs := g.Impls["/inventory.v1.InventoryService/Reserve"]
	if len(hs) != 1 || g.Nodes[hs[0]].Name != "InventoryGrpcService.reserve" {
		t.Errorf("Reserve handlers = %v", hs)
	}
}
