// Package shipping calls the Kotlin shipping service over HTTP.
package shipping

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Client reads rates from the shipping HTTP API.
type Client struct {
	base string
	http *http.Client
}

// NewClient returns a Client for the shipping API at base, using hc for requests.
func NewClient(base string, hc *http.Client) *Client {
	return &Client{base: base, http: hc}
}

// rate is the JSON body of GET /rates, served by samples/kotlin/shipping.
type rate struct {
	CustomerID string `json:"customer_id"`
	Region     string `json:"region"`
	Cents      int64  `json:"cents"`
}

// Rate makes the HTTP hop GET {base}/rates?customer_id=ID&units=N to shipping
// and returns the customer's region with the delivery price.
func (c *Client) Rate(ctx context.Context, customerID string, units int64) (string, int64, error) {
	u, err := url.Parse(c.base)
	if err != nil {
		return "", 0, fmt.Errorf("parse base url: %w", err)
	}
	u = u.JoinPath("rates")
	u.RawQuery = url.Values{"customer_id": {customerID}, "units": {strconv.FormatInt(units, 10)}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("get rate: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("get rate: unexpected status %s", resp.Status)
	}
	var r rate
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", 0, fmt.Errorf("decode rate: %w", err)
	}
	return r.Region, r.Cents, nil
}
