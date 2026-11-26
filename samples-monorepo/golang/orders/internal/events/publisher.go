// Package events publishes order events to Google Cloud Pub/Sub.
package events

import (
	"context"
	"errors"
	"fmt"

	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "example.com/samples/orders/api/events/v1"
	"example.com/samples/orders/internal/order"
)

// Publisher publishes OrderPlaced events to eventsv1.TopicOrderPlaced.
type Publisher struct {
	topic *pubsub.Publisher
}

// NewPublisher returns a Publisher on client's project. Call Stop when done.
func NewPublisher(client *pubsub.Client) *Publisher {
	return &Publisher{topic: client.Publisher(eventsv1.TopicOrderPlaced)}
}

// EnsureTopic creates the OrderPlaced topic if it does not exist yet.
func EnsureTopic(ctx context.Context, client *pubsub.Client) error {
	name := fmt.Sprintf("projects/%s/topics/%s", client.Project(), eventsv1.TopicOrderPlaced)
	_, err := client.TopicAdminClient.CreateTopic(ctx, &pubsubpb.Topic{Name: name})
	if status.Code(err) == codes.AlreadyExists {
		return nil
	} else if err != nil {
		return fmt.Errorf("create topic %s: %w", name, err)
	}
	return nil
}

// OrderPlaced publishes the pubsub hop to topic "order-placed" and waits for
// the server to accept the message.
func (p *Publisher) OrderPlaced(ctx context.Context, o order.Order) error {
	if o.ID == "" {
		return errors.New("order without ID")
	}
	data, err := proto.Marshal(&eventsv1.OrderPlaced{
		OrderId:    o.ID,
		CustomerId: o.CustomerID,
		TotalCents: o.TotalCents,
		Units:      o.Units,
		PlacedAt:   timestamppb.New(o.PlacedAt),
	})
	if err != nil {
		return fmt.Errorf("marshal OrderPlaced: %w", err)
	}
	res := p.topic.Publish(ctx, &pubsub.Message{
		Data:       data,
		Attributes: map[string]string{eventsv1.AttrEventType: "orders.events.v1.OrderPlaced"},
	})
	if _, err := res.Get(ctx); err != nil {
		return fmt.Errorf("publish OrderPlaced %s: %w", o.ID, err)
	}
	return nil
}

// Stop flushes pending messages and releases the publisher's goroutines.
func (p *Publisher) Stop() { p.topic.Stop() }
