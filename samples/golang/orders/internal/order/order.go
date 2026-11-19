// Package order is the ordering domain.
package order

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"example.com/samples/orders/api"
	"example.com/samples/orders/internal/customer"
	"example.com/samples/orders/internal/notify"
)

// ErrInvalid marks a request the caller must fix.
var ErrInvalid = errors.New("invalid order")

// Pricer prices a basket. main wires in the gRPC adapter, so calls through it
// are sure interface hops.
type Pricer interface {
	Price(ctx context.Context, c api.Customer, items []api.Item) (int64, error)
}

// Order is a placed order.
type Order struct {
	ID         string
	CustomerID string
	TotalCents int64
}

// Service places orders.
type Service struct {
	customers *customer.Store
	pricer    Pricer
	seq       atomic.Int64
}

// NewService returns a Service backed by the given store and pricer.
func NewService(customers *customer.Store, pricer Pricer) *Service {
	return &Service{customers: customers, pricer: pricer}
}

// Place validates req, prices it and notifies the customer.
func (s *Service) Place(ctx context.Context, req api.PlaceOrderRequest) (Order, error) {
	if err := Validate(req); err != nil {
		return Order{}, err
	}
	c, err := s.customers.Lookup(req.CustomerID)
	if err != nil {
		return Order{}, fmt.Errorf("lookup customer: %w", err)
	}
	total, err := s.pricer.Price(ctx, c, req.Items)
	if err != nil {
		return Order{}, fmt.Errorf("price order: %w", err)
	}

	o := Order{ID: fmt.Sprintf("o-%d", s.seq.Add(1)), CustomerID: c.ID, TotalCents: total}
	if customer.IsVIP(c) {
		notify.Send(ctx, c, "VIP order "+o.ID+" confirmed")
	} else if total > 0 {
		notify.Send(ctx, c, "order "+o.ID+" confirmed")
	}
	return o, nil
}

// Validate reports whether req can be placed.
func Validate(req api.PlaceOrderRequest) error {
	if req.CustomerID == "" {
		return fmt.Errorf("%w: missing customer", ErrInvalid)
	} else if len(req.Items) == 0 {
		return fmt.Errorf("%w: no items", ErrInvalid)
	}
	for _, it := range req.Items {
		if it.Qty <= 0 {
			return fmt.Errorf("%w: sku %q qty %d", ErrInvalid, it.SKU, it.Qty)
		} else if it.UnitCents < 0 {
			return fmt.Errorf("%w: sku %q negative price", ErrInvalid, it.SKU)
		}
	}
	return nil
}
