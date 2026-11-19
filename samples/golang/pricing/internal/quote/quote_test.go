package quote_test

import (
	"errors"
	"testing"

	"example.com/samples/pricing/internal/quote"
)

func TestCalculate(t *testing.T) {
	twelve := []quote.Line{{SKU: "a", UnitCents: 1000, Qty: 12}}
	tests := []struct {
		name    string
		lines   []quote.Line
		tier    string
		region  string
		want    quote.Quote
		wantErr error
	}{
		// 15% gold beats 500 volume: 12000-1800=10200, +20% tax.
		{"gold EU", twelve, "gold", "EU", quote.Quote{Subtotal: 12000, Discount: 1800, Tax: 2040, Total: 12240}, nil},
		// 5% silver (600) beats 500 volume: 11400, +7% tax.
		{"silver US", twelve, "silver", "US", quote.Quote{Subtotal: 12000, Discount: 600, Tax: 798, Total: 12198}, nil},
		{"standard untaxed", twelve, "standard", "", quote.Quote{Subtotal: 12000, Discount: 500, Tax: 0, Total: 11500}, nil},
		{"bad qty", []quote.Line{{SKU: "a", UnitCents: 1}}, "", "", quote.Quote{}, quote.ErrInvalidLine},
		{"bad region", twelve, "", "MARS", quote.Quote{}, quote.ErrUnknownRegion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := quote.Calculate(tt.lines, tt.tier, tt.region)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
