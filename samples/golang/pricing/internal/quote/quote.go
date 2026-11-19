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
	Subtotal, Discount, Tax, Shipping, Total int64
}

// Units returns the total quantity across lines.
func Units(lines []Line) int64 {
	var n int64
	for _, l := range lines {
		n += l.Qty
	}
	return n
}

// Customer is what pricing knows about the buyer.
type Customer struct {
	Tier, Region string
	PriorOrders  int
}

// Calculate prices lines for c. Tax applies to goods only; shippingCents is
// added untaxed.
func Calculate(lines []Line, c Customer, shippingCents int64) (Quote, error) {
	if shippingCents < 0 {
		return Quote{}, fmt.Errorf("%w: negative shipping", ErrInvalidLine)
	}
	sub, units, err := subtotal(lines)
	if err != nil {
		return Quote{}, err
	}
	d := discount.Best(discount.Rules(c.Tier, c.PriorOrders), sub, units)
	t, err := tax.Amount(c.Region, sub-d)
	if err != nil {
		return Quote{}, err
	}
	return Quote{
		Subtotal: sub,
		Discount: d,
		Tax:      t,
		Shipping: shippingCents,
		Total:    sub - d + t + shippingCents,
	}, nil
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
