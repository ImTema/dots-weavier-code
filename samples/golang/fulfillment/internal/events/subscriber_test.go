package events_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"cloud.google.com/go/pubsub/v2/pstest"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"

	"example.com/samples/fulfillment/internal/events"
	"example.com/samples/fulfillment/internal/plan"
	"example.com/samples/fulfillment/internal/server"
	eventsv1 "example.com/samples/orders/api/events/v1"
)

type countingRates struct{ calls atomic.Int64 }

func (r *countingRates) Rate(context.Context, string, int64) (string, int64, error) {
	r.calls.Add(1)
	return "UK", 900, nil
}

type noTax struct{}

func (noTax) Tax(context.Context, string, string, int64) (int64, error) { return 0, nil }

func TestSubscriberSchedulesOnce(t *testing.T) {
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
	for range 2 { // the second call finds the subscription
		if err := events.EnsureSubscription(ctx, client); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"o-1", "o-1", "o-2"} { // o-1 delivered twice
		data, _ := proto.Marshal(&eventsv1.OrderPlaced{OrderId: id, CustomerId: "c3", Units: 2, TotalCents: 1000})
		srv.Publish(topic, data, nil)
	}
	srv.Publish(topic, []byte("not a proto"), nil)

	rates := &countingRates{}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- events.NewSubscriber(client, server.New(plan.NewPlanner(rates, noTax{}))).Run(runCtx)
	}()
	for rates.calls.Load() < 2 && ctx.Err() == nil {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // let the duplicate arrive
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := rates.calls.Load(); got != 2 {
		t.Fatalf("planned %d shipments, want 2 (o-1 once, o-2 once)", got)
	}
}
