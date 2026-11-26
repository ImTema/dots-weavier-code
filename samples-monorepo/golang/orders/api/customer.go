// Package api is the HTTP contract orders exposes to other projects.
package api

// CustomerPath is served as GET CustomerPath + "{id}".
const CustomerPath = "/customers/"

const (
	TierStandard = "standard"
	TierSilver   = "silver"
	TierGold     = "gold"
)

type Customer struct {
	ID     string `json:"id"`
	Tier   string `json:"tier"`
	Region string `json:"region"`
	Phone  string `json:"phone,omitempty"`
}

type Item struct {
	SKU       string `json:"sku"`
	UnitCents int64  `json:"unit_cents"`
	Qty       int32  `json:"qty"`
}

type PlaceOrderRequest struct {
	CustomerID string `json:"customer_id"`
	Items      []Item `json:"items"`
}

type PlaceOrderResponse struct {
	OrderID    string `json:"order_id"`
	TotalCents int64  `json:"total_cents"`
}
