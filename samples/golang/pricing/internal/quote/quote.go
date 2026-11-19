// Package quote is the pricing domain: subtotal, then discount, then tax.
package quote

import (
	"errors"
	"fmt"

	"example.com/samples/pricing/internal/discount"
	"example.com/samples/pricing/internal/tax"
)

var (
	// ErrInvalidLine marks a basket line that cannot be priced.
	ErrInvalidLine = errors.New("invalid line")
	// ErrUnknownRegion is re-exported so callers depend on quote only.
	ErrUnknownRegion = tax.ErrUnknownRegion
)

// Line is one basket line.
type Line struct {
	SKU       string
	UnitCents int64
	Qty       int64
}

// Quote is a priced basket, all amounts in cents.
type Quote struct {
	Subtotal, Discount, Tax, Total int64
}

// Calculate prices lines for a customer tier in region.
func Calculate(lines []Line, tier, region string) (Quote, error) {
	sub, units, err := subtotal(lines)
	if err != nil {
		return Quote{}, err
	}
	d := discount.Best(discount.Rules(tier), sub, units)
	t, err := tax.Amount(region, sub-d)
	if err != nil {
		return Quote{}, err
	}
	return Quote{Subtotal: sub, Discount: d, Tax: t, Total: sub - d + t}, nil
}

func subtotal(lines []Line) (cents, units int64, err error) {
	for _, l := range lines {
		if l.Qty <= 0 {
			return 0, 0, fmt.Errorf("%w: sku %q qty %d", ErrInvalidLine, l.SKU, l.Qty)
		} else if l.UnitCents < 0 {
			return 0, 0, fmt.Errorf("%w: sku %q negative price", ErrInvalidLine, l.SKU)
		}
		cents += l.UnitCents * l.Qty
		units += l.Qty
	}
	return cents, units, nil
}
