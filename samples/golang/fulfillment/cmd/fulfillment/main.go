// Command fulfillment serves fulfillment.v1.FulfillmentService over gRPC and
// consumes OrderPlaced events from Pub/Sub as a fallback for lost Schedule calls.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cloud.google.com/go/pubsub/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	fulfillmentv1 "example.com/samples/fulfillment/api/fulfillment/v1"
	"example.com/samples/fulfillment/internal/events"
	"example.com/samples/fulfillment/internal/plan"
	"example.com/samples/fulfillment/internal/pricing"
	"example.com/samples/fulfillment/internal/server"
	"example.com/samples/fulfillment/internal/shipping"
	pricingv1 "example.com/samples/pricing/api/pricing/v1"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fulfillment stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", ":9092", "gRPC listen address")
	pricingAddr := flag.String("pricing", "127.0.0.1:9090", "pricing gRPC address")
	shippingURL := flag.String("shipping", "http://localhost:8081", "shipping HTTP base URL")
	project := flag.String("pubsub-project", "samples-local", "Pub/Sub project ID; set PUBSUB_EMULATOR_HOST to use an emulator")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pricingConn, err := grpc.NewClient(*pricingAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial pricing: %w", err)
	}
	defer pricingConn.Close()
	ps, err := pubsub.NewClient(ctx, *project)
	if err != nil {
		return fmt.Errorf("pubsub client: %w", err)
	}
	defer ps.Close()
	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	srv := server.New(plan.NewPlanner(
		shipping.NewClient(*shippingURL, &http.Client{Timeout: 3 * time.Second}),
		pricing.New(pricingv1.NewPricingServiceClient(pricingConn)),
	))
	s := grpc.NewServer(grpc.ConnectionTimeout(5 * time.Second))
	fulfillmentv1.RegisterFulfillmentServiceServer(s, srv)

	go func() {
		if err := consume(ctx, ps, srv); err != nil {
			slog.Error("order events", "err", err)
			stop()
		}
	}()
	go func() {
		<-ctx.Done()
		s.GracefulStop()
	}()
	slog.Info("fulfillment listening", "addr", *addr)
	return s.Serve(lis)
}

// consume subscribes to OrderPlaced until ctx is done. orders owns the topic,
// so until orders has created it, it retries.
func consume(ctx context.Context, ps *pubsub.Client, srv *server.Server) error {
	for {
		err := events.EnsureSubscription(ctx, ps)
		if err == nil {
			break
		} else if status.Code(err) != codes.NotFound {
			return err
		}
		slog.Info("waiting for orders to create the topic", "err", err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(2 * time.Second):
		}
	}
	slog.Info("consuming order events", "subscription", events.SubscriptionOrderPlaced)
	return events.NewSubscriber(ps, srv).Run(ctx)
}
