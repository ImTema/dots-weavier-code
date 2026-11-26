// Package plan decides where, how and at what insured value an order ships.
package plan

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalid marks a request the caller must fix.
	ErrInvalid = errors.New("invalid order")
	// ErrUnsupportedRegion marks a customer no warehouse ships to.
	ErrUnsupportedRegion = errors.New("unsupported region")
)

// warehouses by customer region.
var warehouses = map[string]string{"EU": "AMS", "UK": "LHR", "US": "EWR"}

// Warehouse returns the warehouse shipping to region.
func Warehouse(region string) (string, error) {
	if w, ok := warehouses[region]; ok {
		return w, nil
	}
	return "", fmt.Errorf("%w %q", ErrUnsupportedRegion, region)
}

const (
	freightUnits = 50   // above this, a parcel carrier will not take it
	courierCents = 1500 // from this delivery price on, a courier is worth it
	insuredCents = 20000
)

// Carrier picks the carrier for units at a delivery price of shipCents.
func Carrier(units, shipCents int64) string {
	switch {
	case units > freightUnits:
		return "freight"
	case shipCents >= courierCents:
		return "courier"
	default:
		return "post"
	}
}

// Premium is the insurance premium before tax: 1% of an order worth at least
// 200.00, nothing below.
func Premium(totalCents int64) int64 {
	if totalCents < insuredCents {
		return 0
	}
	return totalCents / 100
}
