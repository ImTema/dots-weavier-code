package events_test

import (
	"context"
	"testing"
	"time"

	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/pstest"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"

	eventsv1 "example.com/samples/orders/api/events/v1"
	"example.com/samples/orders/internal/events"
	"example.com/samples/orders/internal/order"
)

func TestOrderPlaced(t *testing.T) {
	ctx := context.Background()
	srv := pstest.NewServer()
	defer srv.Close()
	client, err := pubsub.NewClient(ctx, "test",
		option.WithEndpoint(srv.Addr),
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if err := events.EnsureTopic(ctx, client); err != nil {
		t.Fatal(err)
	}
	if err := events.EnsureTopic(ctx, client); err != nil {
		t.Fatalf("second EnsureTopic: %v", err)
	}

	pub := events.NewPublisher(client)
	defer pub.Stop()
	placed := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	if err := pub.OrderPlaced(ctx, order.Order{ID: "o-1", CustomerID: "c1", TotalCents: 100, Units: 2, PlacedAt: placed}); err != nil {
		t.Fatal(err)
	}
	if err := pub.OrderPlaced(ctx, order.Order{}); err == nil {
		t.Fatal("want error for order without ID")
	}

	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	var got eventsv1.OrderPlaced
	if err := proto.Unmarshal(msgs[0].Data, &got); err != nil {
		t.Fatal(err)
	}
	if got.GetOrderId() != "o-1" || got.GetUnits() != 2 || !got.GetPlacedAt().AsTime().Equal(placed) {
		t.Fatalf("got %v", &got)
	}
	if msgs[0].Attributes[eventsv1.AttrEventType] != "orders.events.v1.OrderPlaced" {
		t.Fatalf("attributes = %v", msgs[0].Attributes)
	}
}
