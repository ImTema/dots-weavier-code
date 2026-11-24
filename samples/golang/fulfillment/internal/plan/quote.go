package plan

import (
	"context"
	"fmt"
	"time"
)

// taxTimeout bounds one call to pricing; the gRPC client has its own, longer one.
const taxTimeout = 2 * time.Second

// costs is what a shipment costs, before it is stored.
type costs struct {
	warehouse string
	shipping  int64
	insurance int64
}

// quote prices o's delivery and insurance, and picks the warehouse for its region.
func (p *Planner) quote(ctx context.Context, o Order) (costs, error) {
	region, ship, err := p.rates.Rate(ctx, o.CustomerID, o.Units)
	if err != nil {
		return costs{}, fmt.Errorf("shipping rate: %w", err)
	}
	wh, err := Warehouse(region)
	if err != nil {
		return costs{}, err
	}
	ins, err := p.insure(ctx, o, region)
	if err != nil {
		return costs{}, err
	}
	return costs{warehouse: wh, shipping: ship, insurance: ins}, nil
}

// insure returns o's insurance: the premium plus the tax on it, or 0 when o is
// too cheap to insure.
func (p *Planner) insure(ctx context.Context, o Order, region string) (int64, error) {
	ins := Premium(o.TotalCents)
	if ins <= 0 {
		return ins, nil
	}
	tax, err := p.taxOn(ctx, o.CustomerID, region, ins)
	if err != nil {
		return 0, fmt.Errorf("insurance tax: %w", err)
	}
	return ins + tax, nil
}

// taxOn asks pricing for the tax on cents, once more if the first call fails:
// a pricing restart drops the odd call. A cancelled ctx is not retried.
func (p *Planner) taxOn(ctx context.Context, customerID, region string, cents int64) (int64, error) {
	tax, err := p.askTax(ctx, customerID, region, cents)
	if err != nil && ctx.Err() == nil {
		tax, err = p.askTax(ctx, customerID, region, cents)
	}
	return tax, err
}

// askTax is one call to pricing, bounded by taxTimeout.
func (p *Planner) askTax(ctx context.Context, customerID, region string, cents int64) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, taxTimeout)
	defer cancel()
	return p.taxes.Tax(ctx, customerID, region, cents)
}
