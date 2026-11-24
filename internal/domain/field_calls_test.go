package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStepAddr(t *testing.T) {
	for loc, want := range map[string]address{
		"internal/order/order.go:74":                 {Project: "golang/orders", File: "internal/order/order.go", Line: 74},
		"golang/pricing:internal/server/server.go:9": {Project: "golang/pricing", File: "internal/server/server.go", Line: 9},
	} {
		got, ok := stepAddr(loc, "golang/orders")
		assert.True(t, ok, loc)
		assert.Equal(t, want, got, loc)
	}
	_, ok := stepAddr("pkg.Type.Field", "golang/orders")
	assert.False(t, ok, "a struct field has no address")
}
