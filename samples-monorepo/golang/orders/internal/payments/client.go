// Package payments adapts order.Payments to the Java payments service's HTTP contract.
package payments

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"example.com/samples/orders/internal/order"
)

// ChargesPath is served by payments as POST ChargesPath and GET ChargesPath + "/{id}".
const ChargesPath = "/payments/charges"

// chargeRequest is the JSON body of POST /payments/charges, mirroring
// ChargeController.ChargeRequest in samples-monorepo/java/payments.
type chargeRequest struct {
	OrderID     string `json:"order_id"`
	CustomerID  string `json:"customer_id"`
	AmountCents int64  `json:"amount_cents"`
}

// charge mirrors ChargeController.ChargeResponse in samples-monorepo/java/payments.
type charge struct {
	ChargeID    string `json:"charge_id"`
	OrderID     string `json:"order_id"`
	CustomerID  string `json:"customer_id"`
	AmountCents int64  `json:"amount_cents"`
}

// Client charges orders through the payments HTTP API.
type Client struct {
	base string
	http *http.Client
}

// NewClient returns a Client for the payments API at base, using hc for requests.
func NewClient(base string, hc *http.Client) *Client {
	return &Client{base: base, http: hc}
}

// Charge makes the HTTP hop POST {base}/payments/charges.
// A declined charge is wrapped in order.ErrDeclined.
func (c *Client) Charge(ctx context.Context, orderID, customerID string, cents int64) (string, error) {
	u, err := url.JoinPath(c.base, ChargesPath)
	if err != nil {
		return "", fmt.Errorf("build url: %w", err)
	}
	body, err := json.Marshal(chargeRequest{OrderID: orderID, CustomerID: customerID, AmountCents: cents})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	var ch charge
	if err := c.do(req, http.StatusCreated, &ch); err != nil {
		return "", fmt.Errorf("charge order %s: %w", orderID, err)
	}
	return ch.ChargeID, nil
}

// Charged makes the HTTP hop GET {base}/payments/charges/{id}.
func (c *Client) Charged(ctx context.Context, chargeID string) (int64, error) {
	u, err := url.JoinPath(c.base, ChargesPath, url.PathEscape(chargeID))
	if err != nil {
		return 0, fmt.Errorf("build url: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	var ch charge
	if err := c.do(req, http.StatusOK, &ch); err != nil {
		return 0, fmt.Errorf("get charge %s: %w", chargeID, err)
	}
	return ch.AmountCents, nil
}

// do sends req and decodes a want response into v; 402 is order.ErrDeclined.
func (c *Client) do(req *http.Request, want int, v any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case want:
		return json.NewDecoder(resp.Body).Decode(v)
	case http.StatusPaymentRequired:
		var e struct{ Error string }
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&e)
		return fmt.Errorf("%w: %s", order.ErrDeclined, strings.TrimPrefix(e.Error, "declined: "))
	default:
		return fmt.Errorf("unexpected status %s", resp.Status)
	}
}
