// Command orders is the entry point: an HTTP server for placing orders.
// Placing an order calls pricing over gRPC; pricing calls back GET /customers/{id}.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"example.com/samples/orders/internal/customer"
	"example.com/samples/orders/internal/httpapi"
	"example.com/samples/orders/internal/notify"
	"example.com/samples/orders/internal/order"
	"example.com/samples/orders/internal/pricer"
	pricingv1 "example.com/samples/pricing/api/pricing/v1"
)

func main() {
	if err := run(); err != nil {
		slog.Error("orders stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	pricingAddr := flag.String("pricing", "127.0.0.1:9090", "pricing gRPC address")
	sms := flag.Bool("sms", false, "notify by SMS instead of email")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	conn, err := grpc.NewClient(*pricingAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial pricing: %w", err)
	}
	defer conn.Close()

	var n notify.Notifier
	if *sms {
		n = notify.NewSMS()
	} else {
		n = notify.NewEmail("orders@example.com")
	}

	store := customer.NewStore(customer.Seed())
	svc := order.NewService(store, pricer.New(pricingv1.NewPricingServiceClient(conn)))
	srv := &http.Server{
		Addr:              *addr,
		Handler:           httpapi.New(svc, store, n).Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		slog.Info("orders listening", "addr", *addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
