// Command pricing serves pricing.v1.PricingService over gRPC and consumes
// OrderPlaced events from Pub/Sub to track customer loyalty.
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
	"google.golang.org/grpc/status"

	pricingv1 "example.com/samples/pricing/api/pricing/v1"
	"example.com/samples/pricing/internal/customers"
	"example.com/samples/pricing/internal/events"
	"example.com/samples/pricing/internal/loyalty"
	"example.com/samples/pricing/internal/server"
	"example.com/samples/pricing/internal/shipping"
)

func main() {
	if err := run(); err != nil {
		slog.Error("pricing stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", ":9090", "gRPC listen address")
	ordersURL := flag.String("orders", "http://localhost:8080", "orders HTTP base URL")
	shippingURL := flag.String("shipping", "http://localhost:8081", "shipping HTTP base URL")
	project := flag.String("pubsub-project", "samples-local", "Pub/Sub project ID; set PUBSUB_EMULATOR_HOST to use an emulator")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ps, err := pubsub.NewClient(ctx, *project)
	if err != nil {
		return fmt.Errorf("pubsub client: %w", err)
	}
	defer ps.Close()

	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	ledger := loyalty.NewLedger()
	hc := &http.Client{Timeout: 3 * time.Second}
	s := grpc.NewServer()
	pricingv1.RegisterPricingServiceServer(s, server.New(
		customers.NewClient(*ordersURL, hc),
		shipping.NewClient(*shippingURL, hc),
		ledger,
	))

	go func() {
		if err := consume(ctx, ps, ledger); err != nil {
			slog.Error("order events", "err", err)
			stop()
		}
	}()
	go func() {
		<-ctx.Done()
		s.GracefulStop()
	}()
	slog.Info("pricing listening", "addr", *addr)
	return s.Serve(lis)
}

// consume subscribes to OrderPlaced and feeds the ledger until ctx is done.
// orders owns the topic, so until orders has created it, it retries.
func consume(ctx context.Context, ps *pubsub.Client, ledger *loyalty.Ledger) error {
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
	return events.NewSubscriber(ps, ledger).Run(ctx)
}
