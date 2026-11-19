// Package pricer adapts order.Pricer to the pricing project's gRPC contract.
package pricer

import (
	"context"
	"fmt"

	"example.com/samples/orders/api"
	pricingv1 "example.com/samples/pricing/api/pricing/v1"
)

// Client prices orders through pricing.v1.PricingService.
type Client struct {
	rpc pricingv1.PricingServiceClient
}

// New returns a Client using rpc.
func New(rpc pricingv1.PricingServiceClient) *Client { return &Client{rpc: rpc} }

// Price makes the gRPC hop to pricing.v1.PricingService/Quote.
func (p *Client) Price(ctx context.Context, c api.Customer, items []api.Item) (int64, error) {
	req := &pricingv1.QuoteRequest{
		CustomerId: c.ID,
		Region:     c.Region,
		Items:      make([]*pricingv1.Item, 0, len(items)),
	}
	for _, it := range items {
		req.Items = append(req.Items, &pricingv1.Item{Sku: it.SKU, UnitCents: it.UnitCents, Qty: it.Qty})
	}
	resp, err := p.rpc.Quote(ctx, req)
	if err != nil {
		return 0, fmt.Errorf("quote: %w", err)
	}
	return resp.GetTotalCents(), nil
}
