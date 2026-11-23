package domain

import "testing"

func TestStepAddr(t *testing.T) {
	for loc, want := range map[string]address{
		"internal/order/order.go:74":                 {Project: "golang/orders", File: "internal/order/order.go", Line: 74},
		"golang/pricing:internal/server/server.go:9": {Project: "golang/pricing", File: "internal/server/server.go", Line: 9},
	} {
		if got, ok := stepAddr(loc, "golang/orders"); !ok || got != want {
			t.Errorf("stepAddr(%q) = %+v, %v; want %+v", loc, got, ok, want)
		}
	}
	if _, ok := stepAddr("pkg.Type.Field", "golang/orders"); ok {
		t.Error("a struct field has no address")
	}
}
