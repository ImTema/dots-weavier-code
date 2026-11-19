// Package server is the gRPC transport for pricing.
package server

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pricingv1 "example.com/samples/pricing/api/pricing/v1"
	"example.com/samples/pricing/internal/quote"
)

// TierSource resolves a customer's pricing tier. main wires in the HTTP client
// to orders, so calls through it are sure interface hops.
type TierSource interface {
	Tier(ctx context.Context, customerID string) (string, error)
}

// Server implements pricingv1.PricingServiceServer.
type Server struct {
	pricingv1.UnimplementedPricingServiceServer
	tiers TierSource
}

// New returns a Server resolving tiers through tiers.
func New(tiers TierSource) *Server { return &Server{tiers: tiers} }

// Quote is the gRPC handler for pricing.v1.PricingService/Quote.
func (s *Server) Quote(ctx context.Context, req *pricingv1.QuoteRequest) (*pricingv1.QuoteResponse, error) {
	if len(req.GetItems()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no items")
	}
	tier, err := s.tiers.Tier(ctx, req.GetCustomerId())
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "resolve tier: %v", err)
	}

	lines := make([]quote.Line, 0, len(req.GetItems()))
	for _, it := range req.GetItems() {
		lines = append(lines, quote.Line{SKU: it.GetSku(), UnitCents: it.GetUnitCents(), Qty: int64(it.GetQty())})
	}
	q, err := quote.Calculate(lines, tier, req.GetRegion())
	if errors.Is(err, quote.ErrInvalidLine) || errors.Is(err, quote.ErrUnknownRegion) {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	} else if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pricingv1.QuoteResponse{
		SubtotalCents: q.Subtotal,
		DiscountCents: q.Discount,
		TaxCents:      q.Tax,
		TotalCents:    q.Total,
	}, nil
}
