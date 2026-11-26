// Package order is the ordering domain.
package order

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
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
	// ErrDeclined marks an order payments refused to charge.
	ErrDeclined = errors.New("payment declined")
	// ErrNotFound is returned for unknown order IDs.
	ErrNotFound = errors.New("order not found")
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

// Payments charges orders. main wires in the HTTP adapter to the Java
// payments service, so calls through it are sure interface hops.
type Payments interface {
	Charge(ctx context.Context, orderID, customerID string, cents int64) (chargeID string, err error)
	Charged(ctx context.Context, chargeID string) (cents int64, err error)
}

// Order is a placed order.
type Order struct {
	ID            string
	CustomerID    string
	ReservationID string
	ShipmentID    string // empty when scheduling failed; fulfillment then schedules from OrderPlaced
	ChargeID      string // empty for a free order
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
	payments  Payments
	now       func() time.Time
	seq       atomic.Int64

	mu     sync.Mutex
	placed map[string]Order
}

// NewService returns a Service backed by the given store, reserver, pricer,
// event publisher, shipment scheduler and payments.
func NewService(customers *customer.Store, reserver Reserver, pricer Pricer, events EventPublisher, scheduler Scheduler, payments Payments) *Service {
	return &Service{customers: customers, reserver: reserver, pricer: pricer, events: events, scheduler: scheduler, payments: payments,
		now: time.Now, placed: map[string]Order{}}
}

// Place validates req, reserves stock, prices it, charges it, publishes
// OrderPlaced, schedules the shipment and notifies the customer. If pricing or
// the charge fails the reservation is released; a free order is not charged.
// A failed publish or schedule is logged and does not fail the order:
// fulfillment also schedules from OrderPlaced.
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
		s.release(ctx, resID, "pricing")
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
	if total > 0 {
		if o.ChargeID, err = s.payments.Charge(ctx, o.ID, c.ID, total); err != nil {
			s.release(ctx, resID, "charge")
			return Order{}, fmt.Errorf("charge order: %w", err)
		}
	}
	s.mu.Lock()
	s.placed[o.ID] = o
	s.mu.Unlock()
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

// Status is a placed order and what payments charged for it.
type Status struct {
	Order
	ChargedCents int64
}

// Get returns a placed order with the amount payments charged for it.
func (s *Service) Get(ctx context.Context, id string) (Status, error) {
	s.mu.Lock()
	o, ok := s.placed[id]
	s.mu.Unlock()
	if !ok {
		return Status{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	st := Status{Order: o}
	if o.ChargeID == "" {
		return st, nil
	}
	var err error
	if st.ChargedCents, err = s.payments.Charged(ctx, o.ChargeID); err != nil {
		return Status{}, fmt.Errorf("get charge: %w", err)
	}
	return st, nil
}

// release returns a reservation after a later step failed.
func (s *Service) release(ctx context.Context, resID, after string) {
	if err := s.reserver.Release(context.WithoutCancel(ctx), resID); err != nil {
		slog.ErrorContext(ctx, "release after failed "+after, "reservation", resID, "err", err)
	}
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
