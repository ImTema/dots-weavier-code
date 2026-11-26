// Package notify tells customers about their orders.
package notify

import (
	"context"
	"log/slog"

	"example.com/samples/orders/api"
)

// Notifier delivers a message to a customer. It travels in a context.Context,
// so dotweavier cannot see which concrete type reaches Send: the call is a
// guess hop to Email and SMS.
type Notifier interface {
	Notify(ctx context.Context, c api.Customer, msg string) error
}

type ctxKey struct{}

// WithNotifier returns a copy of ctx carrying n.
func WithNotifier(ctx context.Context, n Notifier) context.Context {
	return context.WithValue(ctx, ctxKey{}, n)
}

// Send notifies c through the Notifier in ctx. Without one it does nothing;
// delivery failures are logged, never returned, so they cannot fail an order.
func Send(ctx context.Context, c api.Customer, msg string) {
	n, ok := ctx.Value(ctxKey{}).(Notifier)
	if !ok {
		return
	}
	if err := n.Notify(ctx, c, msg); err != nil {
		slog.WarnContext(ctx, "notify failed", "customer", c.ID, "err", err)
	}
}

// Email notifies by email.
type Email struct{ from string }

// NewEmail returns an Email notifier sending from the given address.
func NewEmail(from string) *Email { return &Email{from: from} }

func (e *Email) Notify(ctx context.Context, c api.Customer, msg string) error {
	if e.from == "" {
		slog.InfoContext(ctx, "email", "to", c.ID, "msg", msg)
	} else {
		slog.InfoContext(ctx, "email", "from", e.from, "to", c.ID, "msg", msg)
	}
	return nil
}

// SMS notifies by text message.
type SMS struct{}

// NewSMS returns an SMS notifier.
func NewSMS() *SMS { return &SMS{} }

func (*SMS) Notify(ctx context.Context, c api.Customer, msg string) error {
	if c.Phone == "" {
		slog.InfoContext(ctx, "sms skipped: no phone", "customer", c.ID)
		return nil
	}
	slog.InfoContext(ctx, "sms", "to", c.Phone, "msg", msg)
	return nil
}
