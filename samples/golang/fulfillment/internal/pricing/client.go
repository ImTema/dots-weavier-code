// Package pricing asks the pricing service for tax over gRPC.
package pricing

import (
	"context"
	"fmt"

	pricingv1 "example.com/samples/pricing/api/pricing/v1"
)

// insuranceSKU names the one line quoted when taxing an insurance premium.
const insuranceSKU = "shipping-insurance"

// Client taxes amounts through pricing.v1.PricingService, which owns the tax rules.
type Client struct {
	rpc pricingv1.PricingServiceClient
}

// New returns a Client using rpc.
func New(rpc pricingv1.PricingServiceClient) *Client { return &Client{rpc: rpc} }

// Tax makes the gRPC hop to pricing.v1.PricingService/Quote for one line worth
// cents and returns only its tax.
func (c *Client) Tax(ctx context.Context, customerID, region string, cents int64) (int64, error) {
	resp, err := c.rpc.Quote(ctx, &pricingv1.QuoteRequest{
		CustomerId: customerID,
		Region:     region,
		Items:      []*pricingv1.Item{{Sku: insuranceSKU, UnitCents: cents, Qty: 1}},
	})
	if err != nil {
		return 0, fmt.Errorf("quote: %w", err)
	}
	return resp.GetTaxCents(), nil
}
