// Package order is the ordering domain.
package order

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"example.com/samples/orders/api"
	"example.com/samples/orders/internal/customer"
	"example.com/samples/orders/internal/notify"
)

var (
	// ErrInvalid marks a request the caller must fix.
	ErrInvalid = errors.New("invalid order")
	// ErrUnavailable marks items inventory cannot reserve: out of stock,
	// unknown SKU or over the customer's tier limit.
	ErrUnavailable = errors.New("items unavailable")
)

// Reserver holds stock for an order. main wires in the gRPC adapter to the
// Java inventory service, so calls through it are sure interface hops.
type Reserver interface {
	Reserve(ctx context.Context, customerID string, items []api.Item) (reservationID string, err error)
	Release(ctx context.Context, reservationID string) error
}

// Pricer prices a basket. main wires in the gRPC adapter, so calls through it
// are sure interface hops.
type Pricer interface {
	Price(ctx context.Context, c api.Customer, items []api.Item) (int64, error)
}

// EventPublisher announces placed orders. main wires in the Pub/Sub adapter,
// so calls through it are sure interface hops.
type EventPublisher interface {
	OrderPlaced(ctx context.Context, o Order) error
}

// Scheduler plans the order's shipment. main wires in the gRPC adapter to
// fulfillment, so calls through it are sure interface hops.
type Scheduler interface {
	Schedule(ctx context.Context, o Order) (shipmentID string, err error)
}

// Order is a placed order.
type Order struct {
	ID            string
	CustomerID    string
	ReservationID string
	ShipmentID    string // empty when scheduling failed; fulfillment then schedules from OrderPlaced
	TotalCents    int64
	Units         int64
	PlacedAt      time.Time
}

// Service places orders.
type Service struct {
	customers *customer.Store
	reserver  Reserver
	pricer    Pricer
	events    EventPublisher
	scheduler Scheduler
	now       func() time.Time
	seq       atomic.Int64
}

// NewService returns a Service backed by the given store, reserver, pricer,
// event publisher and shipment scheduler.
func NewService(customers *customer.Store, reserver Reserver, pricer Pricer, events EventPublisher, scheduler Scheduler) *Service {
	return &Service{customers: customers, reserver: reserver, pricer: pricer, events: events, scheduler: scheduler, now: time.Now}
}

// Place validates req, reserves stock, prices it, publishes OrderPlaced,
// schedules the shipment and notifies the customer. If pricing fails the
// reservation is released. A failed publish or schedule is logged and does not
// fail the order: fulfillment also schedules from OrderPlaced.
func (s *Service) Place(ctx context.Context, req api.PlaceOrderRequest) (Order, error) {
	if err := Validate(req); err != nil {
		return Order{}, err
	}
	c, err := s.customers.Lookup(req.CustomerID)
	if err != nil {
		return Order{}, fmt.Errorf("lookup customer: %w", err)
	}
	resID, err := s.reserver.Reserve(ctx, c.ID, req.Items)
	if err != nil {
		return Order{}, fmt.Errorf("reserve stock: %w", err)
	}
	total, err := s.pricer.Price(ctx, c, req.Items)
	if err != nil {
		if rerr := s.reserver.Release(context.WithoutCancel(ctx), resID); rerr != nil {
			slog.ErrorContext(ctx, "release after failed pricing", "reservation", resID, "err", rerr)
		}
		return Order{}, fmt.Errorf("price order: %w", err)
	}

	o := Order{
		ID:            fmt.Sprintf("o-%d", s.seq.Add(1)),
		CustomerID:    c.ID,
		ReservationID: resID,
		TotalCents:    total,
		Units:         units(req.Items),
		PlacedAt:      s.now(),
	}
	// ponytail: publish after commit can lose an event on crash; a transactional outbox fixes that.
	if err := s.events.OrderPlaced(ctx, o); err != nil {
		slog.ErrorContext(ctx, "publish OrderPlaced", "order", o.ID, "err", err)
	}
	if o.ShipmentID, err = s.scheduler.Schedule(ctx, o); err != nil {
		slog.ErrorContext(ctx, "schedule shipment", "order", o.ID, "err", err)
	}
	if customer.IsVIP(c) {
		notify.Send(ctx, c, "VIP order "+o.ID+" confirmed")
	} else if total > 0 {
		notify.Send(ctx, c, "order "+o.ID+" confirmed")
	}
	return o, nil
}

func units(items []api.Item) int64 {
	var n int64
	for _, it := range items {
		n += int64(it.Qty)
	}
	return n
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
