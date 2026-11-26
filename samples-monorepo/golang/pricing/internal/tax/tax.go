// Package tax computes sales tax by region.
package tax

import (
	"errors"
	"fmt"
)

// ErrUnknownRegion is returned for regions without a tax rate.
var ErrUnknownRegion = errors.New("unknown region")

// rates in basis points.
var rates = map[string]int64{"EU": 2000, "UK": 2000, "US": 700}

// Amount returns the tax on cents in region. An empty region is untaxed.
func Amount(region string, cents int64) (int64, error) {
	bp, err := rate(region)
	if err != nil {
		return 0, err
	}
	if cents <= 0 {
		return 0, nil
	}
	return cents * bp / 10000, nil
}

func rate(region string) (int64, error) {
	if region == "" {
		return 0, nil
	} else if bp, ok := rates[region]; ok {
		return bp, nil
	}
	return 0, fmt.Errorf("%w %q", ErrUnknownRegion, region)
}
