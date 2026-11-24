package plan

import (
	"context"
	"errors"
	"testing"
)

func TestRules(t *testing.T) {
	for _, tt := range []struct {
		units, ship, want int64
		carrier           string
	}{
		{51, 100, 0, "freight"},
		{50, 1500, 0, "courier"},
		{50, 1499, 0, "post"},
	} {
		if got := Carrier(tt.units, tt.ship); got != tt.carrier {
			t.Errorf("Carrier(%d, %d) = %q; want %q", tt.units, tt.ship, got, tt.carrier)
		}
	}
	for total, want := range map[int64]int64{19999: 0, 20000: 200, 31234: 312} {
		if got := Premium(total); got != want {
			t.Errorf("Premium(%d) = %d; want %d", total, got, want)
		}
	}
	if w, err := Warehouse("UK"); w != "LHR" || err != nil {
		t.Errorf("Warehouse(UK) = %q, %v", w, err)
	}
	if _, err := Warehouse("MARS"); !errors.Is(err, ErrUnsupportedRegion) {
		t.Errorf("Warehouse(MARS) err = %v", err)
	}
}

type fakeRates struct {
	region string
	calls  int
}

func (f *fakeRates) Rate(context.Context, string, int64) (string, int64, error) {
	f.calls++
	return f.region, 1250, nil
}

type fakeTaxes struct{ err error }

func (f fakeTaxes) Tax(_ context.Context, _, _ string, cents int64) (int64, error) {
	return cents / 5, f.err
}

func TestPlan(t *testing.T) {
	ctx := context.Background()
	rates := &fakeRates{region: "UK"}
	p := NewPlanner(rates, fakeTaxes{})

	s, dup, err := p.Plan(ctx, Order{ID: "o-1", CustomerID: "c3", Units: 12, TotalCents: 30000})
	want := Shipment{ID: "s-1", OrderID: "o-1", Warehouse: "LHR", Carrier: "post", ShippingCents: 1250, InsuranceCents: 360}
	if err != nil || dup || s != want {
		t.Fatalf("Plan = %+v, %v, %v; want %+v", s, dup, err, want)
	}
	again, dup, err := p.Plan(ctx, Order{ID: "o-1", CustomerID: "c3", Units: 12, TotalCents: 30000})
	if err != nil || !dup || again != want || rates.calls != 1 {
		t.Fatalf("second Plan = %+v, %v, %v (rate calls %d); want the first shipment", again, dup, err, rates.calls)
	}
	if s, _, _ := p.Plan(ctx, Order{ID: "o-2", CustomerID: "c3", Units: 1, TotalCents: 100}); s.InsuranceCents != 0 {
		t.Errorf("cheap order insured: %+v", s)
	}

	for name, tt := range map[string]struct {
		p    *Planner
		o    Order
		want error
	}{
		"no units":    {p, Order{ID: "o-3", CustomerID: "c3"}, ErrInvalid},
		"no customer": {p, Order{ID: "o-3", Units: 1}, ErrInvalid},
		"bad region":  {NewPlanner(&fakeRates{region: "MARS"}, fakeTaxes{}), Order{ID: "o-3", CustomerID: "c9", Units: 1}, ErrUnsupportedRegion},
		"tax fails":   {NewPlanner(rates, fakeTaxes{err: errBoom}), Order{ID: "o-4", CustomerID: "c3", Units: 1, TotalCents: 50000}, errBoom},
	} {
		if _, _, err := tt.p.Plan(ctx, tt.o); !errors.Is(err, tt.want) {
			t.Errorf("%s: err = %v; want %v", name, err, tt.want)
		}
	}
}

var errBoom = errors.New("boom")

// flakyTaxes fails its first call, as pricing does while it restarts.
type flakyTaxes struct{ calls int }

func (f *flakyTaxes) Tax(_ context.Context, _, _ string, cents int64) (int64, error) {
	if f.calls++; f.calls == 1 {
		return 0, errBoom
	}
	return cents / 5, nil
}

func TestPlanRetriesTaxOnce(t *testing.T) {
	taxes := &flakyTaxes{}
	s, _, err := NewPlanner(&fakeRates{region: "UK"}, taxes).Plan(context.Background(), Order{ID: "o-1", CustomerID: "c3", Units: 12, TotalCents: 30000})
	if err != nil || s.InsuranceCents != 360 || taxes.calls != 2 {
		t.Fatalf("Plan = %+v, %v after %d tax calls; want 360 cents insurance after 2", s, err, taxes.calls)
	}
}
