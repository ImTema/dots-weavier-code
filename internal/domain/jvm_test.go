package domain

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
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
		if _, err := extract(context.Background(), g, p, true); err != nil {
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

	// The Ktor request joins a Go route that serves every method.
	h := g.add("orders|getCustomer", node{Project: "golang/orders", File: "h.go", Name: "Handler.getCustomer"})
	g.Impls[httpKey+"* /customers/{}"] = []int32{h}
	g.join()
	found := false
	for from, es := range g.Out {
		for _, e := range es {
			found = found || g.Nodes[from].Name == "OrdersCustomerDirectory.find" && e.To == h && e.Kind == httpHop && e.Method == "GET /customers/{}"
		}
	}
	if !found {
		t.Errorf("no http hop from OrdersCustomerDirectory.find to Handler.getCustomer; calls = %v", g.Calls)
	}
}

// TestTraceJVMField traces values through the Java and Kotlin samples: into a
// resolved call's parameter, into a record or data class field, and back from
// a field to where it is stored.
func TestTraceJVMField(t *testing.T) {
	flat := func(r *FieldTrace) []string {
		var out []string
		var walk func(ss []*FieldStep, tree string)
		walk = func(ss []*FieldStep, tree string) {
			for _, s := range ss {
				out = append(out, tree+" "+s.Kind+" "+s.Name+s.Loc)
				walk(s.Kids, tree)
			}
		}
		walk(r.Origin, "origin")
		walk(r.Uses, "uses")
		return out
	}
	for _, c := range []struct {
		dir, file string
		line      int
		want      []string
	}{
		{"java/inventory", "src/main/java/com/example/inventory/reservation/ReservationService.java", 24, []string{ // reserve(customerId, lines)
			"origin arg src/main/java/com/example/inventory/grpc/InventoryGrpcService.java:38",
			"origin field ReserveRequest.customerId",
			"uses arg src/main/java/com/example/inventory/customers/OrdersCustomerDirectory.java:20",
		}},
		{"java/inventory", "src/main/java/com/example/inventory/reservation/ReservationService.java", 21, []string{ // record Line(sku, qty)
			"origin store src/main/java/com/example/inventory/grpc/InventoryGrpcService.java:35",
			"uses read src/main/java/com/example/inventory/reservation/ReservationService.java:35",
		}},
		{"kotlin/shipping", "src/main/kotlin/com/example/shipping/http/Routes.kt", 47, []string{ // val customerId = ...
			"uses arg src/main/kotlin/com/example/shipping/rates/ShippingRates.kt:9",
			"uses arg src/main/kotlin/com/example/shipping/customers/CustomerDirectory.kt:26",
			"uses field RateResponse.customerId",
		}},
		{"kotlin/shipping", "src/main/kotlin/com/example/shipping/rates/RateCalculator.kt", 13, []string{ // val base = ...
			"origin store src/main/kotlin/com/example/shipping/rates/RateCalculator.kt:5",
			"uses return src/main/kotlin/com/example/shipping/rates/ShippingRates.kt:14",
		}},
	} {
		dir, _ := filepath.Abs(filepath.Join("..", "..", "samples", c.dir))
		r, _, err := traceJVMField(project{c.dir, dir, "jvm"}, filepath.Join(dir, c.file), c.line, false)
		if err != nil {
			t.Fatal(err)
		}
		got := flat(r)
		for _, w := range c.want {
			if !slices.Contains(got, w) {
				t.Errorf("%s:%d: missing %q in\n%s", c.file, c.line, w, strings.Join(got, "\n"))
			}
		}
	}
}
