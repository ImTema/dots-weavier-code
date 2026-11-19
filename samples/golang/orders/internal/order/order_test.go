package order_test

import (
	"context"
	"errors"
	"testing"

	"example.com/samples/orders/api"
	"example.com/samples/orders/internal/customer"
	"example.com/samples/orders/internal/order"
)

type fixedPricer int64

func (p fixedPricer) Price(context.Context, api.Customer, []api.Item) (int64, error) {
	return int64(p), nil
}

func TestPlace(t *testing.T) {
	store := customer.NewStore(customer.Seed())
	svc := order.NewService(store, fixedPricer(1234))
	item := []api.Item{{SKU: "a", UnitCents: 100, Qty: 1}}

	tests := []struct {
		name    string
		req     api.PlaceOrderRequest
		wantErr error
	}{
		{"ok", api.PlaceOrderRequest{CustomerID: "c1", Items: item}, nil},
		{"no customer id", api.PlaceOrderRequest{Items: item}, order.ErrInvalid},
		{"no items", api.PlaceOrderRequest{CustomerID: "c1"}, order.ErrInvalid},
		{"bad qty", api.PlaceOrderRequest{CustomerID: "c1", Items: []api.Item{{SKU: "a"}}}, order.ErrInvalid},
		{"unknown customer", api.PlaceOrderRequest{CustomerID: "zz", Items: item}, customer.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, err := svc.Place(context.Background(), tt.req)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && o.TotalCents != 1234 {
				t.Fatalf("total = %d, want 1234", o.TotalCents)
			}
		})
	}
}
