package plan

import (
	"context"
	"fmt"
	"sync"
)

// Rates prices delivery and tells the customer's region. main wires in the HTTP
// client to the Kotlin shipping service, so calls through it are sure interface hops.
type Rates interface {
	Rate(ctx context.Context, customerID string, units int64) (region string, cents int64, err error)
}

// Taxes computes the tax on an amount for a customer. main wires in the gRPC
// client to pricing, so calls through it are sure interface hops.
type Taxes interface {
	Tax(ctx context.Context, customerID, region string, cents int64) (int64, error)
}

// Order is what a shipment is planned from.
type Order struct {
	ID         string
	CustomerID string
	Units      int64
	TotalCents int64
}

// Shipment is a planned shipment.
type Shipment struct {
	ID             string
	OrderID        string
	Warehouse      string
	Carrier        string
	ShippingCents  int64
	InsuranceCents int64
}

// Planner plans one shipment per order and remembers it, so the direct RPC
// from orders and the OrderPlaced event, whichever comes second, get the same one.
type Planner struct {
	rates Rates
	taxes Taxes

	mu      sync.Mutex
	byOrder map[string]Shipment // ponytail: in memory, lost on restart; a table when it must survive one
}

// NewPlanner returns a Planner pricing delivery through rates and insurance tax through taxes.
func NewPlanner(rates Rates, taxes Taxes) *Planner {
	return &Planner{rates: rates, taxes: taxes, byOrder: map[string]Shipment{}}
}

// Plan returns o's shipment and whether it was planned before.
func (p *Planner) Plan(ctx context.Context, o Order) (Shipment, bool, error) {
	if o.ID == "" || o.CustomerID == "" {
		return Shipment{}, false, fmt.Errorf("%w: missing order or customer", ErrInvalid)
	} else if o.Units <= 0 || o.TotalCents < 0 {
		return Shipment{}, false, fmt.Errorf("%w: %d units, %d cents", ErrInvalid, o.Units, o.TotalCents)
	}
	if s, ok := p.find(o.ID); ok {
		return s, true, nil
	}

	region, ship, err := p.rates.Rate(ctx, o.CustomerID, o.Units)
	if err != nil {
		return Shipment{}, false, fmt.Errorf("shipping rate: %w", err)
	}
	wh, err := Warehouse(region)
	if err != nil {
		return Shipment{}, false, err
	}
	ins := Premium(o.TotalCents)
	if ins > 0 {
		tax, err := p.taxes.Tax(ctx, o.CustomerID, region, ins)
		if err != nil {
			return Shipment{}, false, fmt.Errorf("insurance tax: %w", err)
		}
		ins += tax
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.byOrder[o.ID]; ok { // planned concurrently: the first one wins
		return s, true, nil
	}
	s := Shipment{
		ID:             fmt.Sprintf("s-%d", len(p.byOrder)+1),
		OrderID:        o.ID,
		Warehouse:      wh,
		Carrier:        Carrier(o.Units, ship),
		ShippingCents:  ship,
		InsuranceCents: ins,
	}
	p.byOrder[o.ID] = s
	return s, false, nil
}

func (p *Planner) find(orderID string) (Shipment, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.byOrder[orderID]
	return s, ok
}
