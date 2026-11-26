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

type failingPricer struct{}

func (failingPricer) Price(context.Context, api.Customer, []api.Item) (int64, error) {
	return 0, errors.New("pricing down")
}

// fakeReserver rejects SKU "gone" and records releases.
type fakeReserver struct{ released []string }

func (r *fakeReserver) Reserve(_ context.Context, _ string, items []api.Item) (string, error) {
	for _, it := range items {
		if it.SKU == "gone" {
			return "", order.ErrUnavailable
		}
	}
	return "r-1", nil
}

func (r *fakeReserver) Release(_ context.Context, id string) error {
	r.released = append(r.released, id)
	return nil
}

// recordingEvents records published orders.
type recordingEvents struct{ placed []order.Order }

func (e *recordingEvents) OrderPlaced(_ context.Context, o order.Order) error {
	e.placed = append(e.placed, o)
	return nil
}

// fakeScheduler fails for orders over 5000 cents.
type fakeScheduler struct{}

func (fakeScheduler) Schedule(_ context.Context, o order.Order) (string, error) {
	if o.TotalCents > 5000 {
		return "", errors.New("fulfillment down")
	}
	return "s-" + o.ID, nil
}

func TestPlaceReleasesWhenPricingFails(t *testing.T) {
	res := &fakeReserver{}
	svc := order.NewService(customer.NewStore(customer.Seed()), res, failingPricer{}, &recordingEvents{}, fakeScheduler{})
	_, err := svc.Place(context.Background(), api.PlaceOrderRequest{
		CustomerID: "c1",
		Items:      []api.Item{{SKU: "a", UnitCents: 100, Qty: 1}},
	})
	if err == nil {
		t.Fatal("want error")
	}
	if len(res.released) != 1 || res.released[0] != "r-1" {
		t.Fatalf("released = %v, want [r-1]", res.released)
	}
}

func TestPlace(t *testing.T) {
	store := customer.NewStore(customer.Seed())
	events := &recordingEvents{}
	svc := order.NewService(store, &fakeReserver{}, fixedPricer(1234), events, fakeScheduler{})
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
		{"out of stock", api.PlaceOrderRequest{CustomerID: "c1", Items: []api.Item{{SKU: "gone", Qty: 1}}}, order.ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, err := svc.Place(context.Background(), tt.req)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if o.ShipmentID != "s-"+o.ID {
				t.Fatalf("shipment = %q, want s-%s", o.ShipmentID, o.ID)
			}
			if o.TotalCents != 1234 {
				t.Fatalf("total = %d, want 1234", o.TotalCents)
			}
			if last := events.placed[len(events.placed)-1]; last.ID != o.ID || last.Units != 1 {
				t.Fatalf("published %+v, want order %s with 1 unit", last, o.ID)
			}
		})
	}
}

func TestPlaceSurvivesFailedSchedule(t *testing.T) {
	svc := order.NewService(customer.NewStore(customer.Seed()), &fakeReserver{}, fixedPricer(9999), &recordingEvents{}, fakeScheduler{})
	o, err := svc.Place(context.Background(), api.PlaceOrderRequest{CustomerID: "c1", Items: []api.Item{{SKU: "a", UnitCents: 100, Qty: 1}}})
	if err != nil || o.ShipmentID != "" {
		t.Fatalf("Place = %+v, %v; want the order without a shipment", o, err)
	}
}
