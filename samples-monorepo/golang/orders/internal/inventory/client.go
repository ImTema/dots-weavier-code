// Package inventory adapts order.Reserver to the Java inventory service's gRPC contract.
package inventory

import (
	"context"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"example.com/samples/orders/api"
	inventoryv1 "example.com/samples/orders/internal/inventorypb/inventory/v1"
	"example.com/samples/orders/internal/order"
)

// Client reserves stock through inventory.v1.InventoryService.
type Client struct {
	rpc inventoryv1.InventoryServiceClient
}

// New returns a Client using rpc.
func New(rpc inventoryv1.InventoryServiceClient) *Client { return &Client{rpc: rpc} }

// Reserve makes the gRPC hop to inventory.v1.InventoryService/Reserve.
// Rejections the caller can act on are wrapped in order.ErrUnavailable.
func (c *Client) Reserve(ctx context.Context, customerID string, items []api.Item) (string, error) {
	req := &inventoryv1.ReserveRequest{
		CustomerId: customerID,
		Items:      make([]*inventoryv1.Item, 0, len(items)),
	}
	for _, it := range items {
		req.Items = append(req.Items, &inventoryv1.Item{Sku: it.SKU, Qty: it.Qty})
	}
	resp, err := c.rpc.Reserve(ctx, req)
	if err == nil {
		return resp.GetReservationId(), nil
	}
	switch status.Code(err) {
	case codes.FailedPrecondition, codes.NotFound, codes.InvalidArgument:
		return "", fmt.Errorf("%w: %s", order.ErrUnavailable, status.Convert(err).Message())
	default:
		return "", fmt.Errorf("reserve: %w", err)
	}
}

// Release makes the gRPC hop to inventory.v1.InventoryService/Release.
func (c *Client) Release(ctx context.Context, reservationID string) error {
	if _, err := c.rpc.Release(ctx, &inventoryv1.ReleaseRequest{ReservationId: reservationID}); err != nil {
		return fmt.Errorf("release %s: %w", reservationID, err)
	}
	return nil
}
