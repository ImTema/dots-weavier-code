package pricing_test

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"example.com/samples/fulfillment/internal/pricing"
	pricingv1 "example.com/samples/pricing/api/pricing/v1"
)

type fakePricing struct {
	pricingv1.UnimplementedPricingServiceServer
}

func (fakePricing) Quote(_ context.Context, req *pricingv1.QuoteRequest) (*pricingv1.QuoteResponse, error) {
	if req.GetRegion() != "UK" || len(req.GetItems()) != 1 {
		return nil, status.Error(codes.InvalidArgument, "unexpected quote")
	}
	return &pricingv1.QuoteResponse{TaxCents: req.GetItems()[0].GetUnitCents() / 5, TotalCents: 99999}, nil
}

func TestTax(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer()
	pricingv1.RegisterPricingServiceServer(s, fakePricing{})
	go s.Serve(lis)
	defer s.Stop()
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := pricing.New(pricingv1.NewPricingServiceClient(conn))

	if tax, err := c.Tax(context.Background(), "c3", "UK", 300); tax != 60 || err != nil {
		t.Errorf("Tax(UK, 300) = %d, %v; want 60", tax, err)
	}
	if _, err := c.Tax(context.Background(), "c3", "US", 300); err == nil {
		t.Error("Tax on a rejected quote: want an error")
	}
}
