package domain

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExtractJVM indexes the Java and Kotlin samples and checks the hops that
// samples/README.md lists for them.
func TestExtractJVM(t *testing.T) {
	g := &graph{ids: map[string]int32{}, Impls: map[string][]int32{}}
	for _, p := range []project{
		{"java/inventory", filepath.Join("..", "..", "samples", "java", "inventory"), "jvm"},
		{"kotlin/shipping", filepath.Join("..", "..", "samples", "kotlin", "shipping"), "jvm"},
	} {
		_, err := extract(t.Context(), g, p, true)
		require.NoError(t, err)
	}
	var got []string
	for from, es := range g.Out {
		for _, e := range es {
			got = append(got, g.Nodes[from].Name+" -"+e.Kind.String()+"-> "+g.Nodes[e.To].Name)
		}
	}
	assert.Subset(t, got, []string{
		"InventoryGrpcService.reserve -call-> ReservationService.reserve",
		"ReservationService.reserve -iface:guess-> OrdersCustomerDirectory.tier",
		"ReservationService.reserve -call-> StockLedger.reserve",
		"main -call-> Application.shippingModule",
		"Application.shippingModule -call-> ShippingRates.quote",
		"ShippingRates.quote -iface:guess-> OrdersCustomerDirectory.find", // CustomerDirectory itself does not parse
		"ShippingRates.quote -call-> RateCalculator.cents",
	})
	hs := g.Impls["/inventory.v1.InventoryService/Reserve"]
	if assert.Len(t, hs, 1, "Reserve handlers") {
		assert.Equal(t, "InventoryGrpcService.reserve", g.Nodes[hs[0]].Name)
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
	assert.True(t, found, "no http hop from OrdersCustomerDirectory.find to Handler.getCustomer; calls = %v", g.Calls)
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
		dir, err := filepath.Abs(filepath.Join("..", "..", "samples", c.dir))
		require.NoError(t, err)
		r, _, err := traceJVMField(t.Context(), project{c.dir, dir, "jvm"}, filepath.Join(dir, c.file), c.line, 1, false)
		require.NoError(t, err)
		assert.Subset(t, flat(r), c.want, "%s:%d", c.file, c.line)
	}
}

// A hop lands on a handler's first annotation: one parse finds the signature below it.
func TestTraceJVMFieldSkipsAnnotations(t *testing.T) {
	dir, err := filepath.Abs("../../samples/java/inventory")
	require.NoError(t, err)
	file := filepath.Join(dir, "src/main/java/com/example/inventory/grpc/InventoryGrpcService.java")
	p := project{"java/inventory", dir, "jvm"}
	_, _, err = traceJVMField(t.Context(), p, file, 31, 1, false)
	require.Error(t, err, "an annotation computes no value")
	r, line, err := traceJVMField(t.Context(), p, file, 31, 12, false)
	require.NoError(t, err)
	assert.Equal(t, 33, line)
	assert.NotEmpty(t, r.Uses)
}
