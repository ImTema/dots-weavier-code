package events_test

import (
	"context"
	"testing"
	"time"

	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"cloud.google.com/go/pubsub/v2/pstest"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"

	eventsv1 "example.com/samples/orders/api/events/v1"
	"example.com/samples/pricing/internal/events"
	"example.com/samples/pricing/internal/loyalty"
)

func TestSubscriberRecordsOrders(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
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

	topic := "projects/test/topics/" + eventsv1.TopicOrderPlaced
	if _, err := client.TopicAdminClient.CreateTopic(ctx, &pubsubpb.Topic{Name: topic}); err != nil {
		t.Fatal(err)
	}
	if err := events.EnsureSubscription(ctx, client); err != nil {
		t.Fatal(err)
	}
	if err := events.EnsureSubscription(ctx, client); err != nil {
		t.Fatalf("second EnsureSubscription: %v", err)
	}

	for _, id := range []string{"o-1", "o-1", "o-2"} { // o-1 delivered twice
		data, _ := proto.Marshal(&eventsv1.OrderPlaced{OrderId: id, CustomerId: "c3"})
		srv.Publish(topic, data, nil)
	}
	srv.Publish(topic, []byte("not a proto"), nil)

	ledger := loyalty.NewLedger()
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- events.NewSubscriber(client, ledger).Run(runCtx) }()

	for ledger.Orders("c3") < 2 && ctx.Err() == nil {
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := ledger.Orders("c3"); got != 2 {
		t.Fatalf("Orders(c3) = %d, want 2", got)
	}
}
