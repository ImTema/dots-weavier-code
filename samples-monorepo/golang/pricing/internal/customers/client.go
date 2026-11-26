// Package customers calls the orders project over HTTP to learn a customer's tier.
package customers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	ordersapi "example.com/samples/orders/api"
)

// Client reads customers from the orders HTTP API.
type Client struct {
	base string
	http *http.Client
}

// NewClient returns a Client for the orders API at base, using hc for requests.
func NewClient(base string, hc *http.Client) *Client {
	return &Client{base: base, http: hc}
}

// Tier makes the HTTP hop GET {base}/customers/{id} to orders.
// Anonymous and unknown customers price as standard.
func (c *Client) Tier(ctx context.Context, id string) (string, error) {
	if id == "" {
		return ordersapi.TierStandard, nil
	}
	u, err := url.JoinPath(c.base, ordersapi.CustomerPath, url.PathEscape(id))
	if err != nil {
		return "", fmt.Errorf("build url: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("get customer: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ordersapi.TierStandard, nil
	} else if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get customer: unexpected status %s", resp.Status)
	}
	var cust ordersapi.Customer
	if err := json.NewDecoder(resp.Body).Decode(&cust); err != nil {
		return "", fmt.Errorf("decode customer: %w", err)
	}
	return cust.Tier, nil
}
