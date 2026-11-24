// Package fulfiller adapts order.Scheduler to the fulfillment project's gRPC contract.
package fulfiller

import (
	"context"
	"fmt"

	fulfillmentv1 "example.com/samples/fulfillment/api/fulfillment/v1"
	"example.com/samples/orders/internal/order"
)

// Client schedules shipments through fulfillment.v1.FulfillmentService.
type Client struct {
	rpc fulfillmentv1.FulfillmentServiceClient
}

// New returns a Client using rpc.
func New(rpc fulfillmentv1.FulfillmentServiceClient) *Client { return &Client{rpc: rpc} }

// Schedule makes the gRPC hop to fulfillment.v1.FulfillmentService/Schedule
// and returns the shipment ID.
func (c *Client) Schedule(ctx context.Context, o order.Order) (string, error) {
	resp, err := c.rpc.Schedule(ctx, &fulfillmentv1.ScheduleRequest{
		OrderId:    o.ID,
		CustomerId: o.CustomerID,
		Units:      o.Units,
		TotalCents: o.TotalCents,
	})
	if err != nil {
		return "", fmt.Errorf("schedule: %w", err)
	}
	return resp.GetShipmentId(), nil
}
