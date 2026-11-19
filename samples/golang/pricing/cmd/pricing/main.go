// Command pricing serves pricing.v1.PricingService over gRPC.
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

	"google.golang.org/grpc"

	pricingv1 "example.com/samples/pricing/api/pricing/v1"
	"example.com/samples/pricing/internal/customers"
	"example.com/samples/pricing/internal/server"
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
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	tiers := customers.NewClient(*ordersURL, &http.Client{Timeout: 3 * time.Second})
	s := grpc.NewServer()
	pricingv1.RegisterPricingServiceServer(s, server.New(tiers))

	go func() {
		<-ctx.Done()
		s.GracefulStop()
	}()
	slog.Info("pricing listening", "addr", *addr)
	return s.Serve(lis)
}
