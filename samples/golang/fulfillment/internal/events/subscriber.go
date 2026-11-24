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

	fulfillmentv1 "example.com/samples/fulfillment/api/fulfillment/v1"
	"example.com/samples/fulfillment/internal/server"
	eventsv1 "example.com/samples/orders/api/events/v1"
)

// SubscriptionOrderPlaced is fulfillment's subscription to eventsv1.TopicOrderPlaced.
const SubscriptionOrderPlaced = "fulfillment-order-placed"

// EnsureSubscription creates fulfillment's subscription to the OrderPlaced topic
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

// Subscriber schedules a shipment for every OrderPlaced event. It is the
// fallback for a lost Schedule call from orders, so it goes through the same
// gRPC handler, which dedupes on the order ID.
type Subscriber struct {
	sub    *pubsub.Subscriber
	server *server.Server
}

// NewSubscriber returns a Subscriber on SubscriptionOrderPlaced.
func NewSubscriber(client *pubsub.Client, srv *server.Server) *Subscriber {
	return &Subscriber{sub: client.Subscriber(SubscriptionOrderPlaced), server: srv}
}

// Run receives messages until ctx is done. It returns nil on cancellation.
// A message that fails to schedule is nacked and redelivered, except one that
// does not decode, which never will.
func (s *Subscriber) Run(ctx context.Context) error {
	err := s.sub.Receive(ctx, func(ctx context.Context, m *pubsub.Message) {
		retry, err := s.Handle(ctx, m.Data)
		if err != nil {
			slog.ErrorContext(ctx, "schedule from OrderPlaced", "message", m.ID, "retry", retry, "err", err)
		}
		if retry {
			m.Nack()
		} else {
			m.Ack()
		}
	})
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("receive %s: %w", SubscriptionOrderPlaced, err)
	}
	return nil
}

// Handle is the subscriber end of the pubsub hop from topic "order-placed".
// retry reports whether redelivery may succeed.
func (s *Subscriber) Handle(ctx context.Context, data []byte) (retry bool, err error) {
	var e eventsv1.OrderPlaced
	if err := proto.Unmarshal(data, &e); err != nil {
		return false, fmt.Errorf("decode: %w", err)
	}
	resp, err := s.server.Schedule(ctx, &fulfillmentv1.ScheduleRequest{
		OrderId:    e.GetOrderId(),
		CustomerId: e.GetCustomerId(),
		Units:      e.GetUnits(),
		TotalCents: e.GetTotalCents(),
	})
	if err != nil {
		return status.Code(err) == codes.Unavailable, err
	}
	if !resp.GetDuplicate() {
		slog.InfoContext(ctx, "shipment scheduled from event", "order", e.GetOrderId(), "shipment", resp.GetShipmentId())
	}
	return false, nil
}
