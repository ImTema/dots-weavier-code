// Package events consumes order events from Google Cloud Pub/Sub.
package events

import (
	"context"
	"fmt"
	"log/slog"

	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	eventsv1 "example.com/samples/orders/api/events/v1"
	"example.com/samples/pricing/internal/loyalty"
)

// SubscriptionOrderPlaced is pricing's subscription to eventsv1.TopicOrderPlaced.
const SubscriptionOrderPlaced = "pricing-order-placed"

// EnsureSubscription creates pricing's subscription to the OrderPlaced topic
// if it does not exist yet. The topic must exist; orders creates it.
func EnsureSubscription(ctx context.Context, client *pubsub.Client) error {
	project := client.Project()
	_, err := client.SubscriptionAdminClient.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  fmt.Sprintf("projects/%s/subscriptions/%s", project, SubscriptionOrderPlaced),
		Topic: fmt.Sprintf("projects/%s/topics/%s", project, eventsv1.TopicOrderPlaced),
	})
	if status.Code(err) == codes.AlreadyExists {
		return nil
	} else if err != nil {
		return fmt.Errorf("create subscription %s: %w", SubscriptionOrderPlaced, err)
	}
	return nil
}

// Subscriber feeds OrderPlaced events into the loyalty ledger.
type Subscriber struct {
	sub    *pubsub.Subscriber
	ledger *loyalty.Ledger
}

// NewSubscriber returns a Subscriber on SubscriptionOrderPlaced.
func NewSubscriber(client *pubsub.Client, ledger *loyalty.Ledger) *Subscriber {
	return &Subscriber{sub: client.Subscriber(SubscriptionOrderPlaced), ledger: ledger}
}

// Run receives messages until ctx is done. It returns nil on cancellation.
func (s *Subscriber) Run(ctx context.Context) error {
	err := s.sub.Receive(ctx, func(ctx context.Context, m *pubsub.Message) {
		if err := s.Handle(ctx, m.Data); err != nil {
			// A payload that does not decode will never decode: ack it so it is not redelivered forever.
			slog.ErrorContext(ctx, "drop OrderPlaced", "message", m.ID, "err", err)
		}
		m.Ack()
	})
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("receive %s: %w", SubscriptionOrderPlaced, err)
	}
	return nil
}

// Handle is the subscriber end of the pubsub hop from topic "order-placed".
func (s *Subscriber) Handle(ctx context.Context, data []byte) error {
	var e eventsv1.OrderPlaced
	if err := proto.Unmarshal(data, &e); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if s.ledger.Record(e.GetOrderId(), e.GetCustomerId()) {
		slog.InfoContext(ctx, "order recorded", "order", e.GetOrderId(), "customer", e.GetCustomerId(),
			"orders", s.ledger.Orders(e.GetCustomerId()))
	}
	return nil
}
