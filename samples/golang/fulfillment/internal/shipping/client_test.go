package shipping_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"example.com/samples/fulfillment/internal/shipping"
)

func TestRate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rates" || r.URL.Query().Get("units") != "12" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"customer_id":"c3","region":"UK","cents":1250}`))
	}))
	defer srv.Close()
	c := shipping.NewClient(srv.URL, srv.Client())

	if region, cents, err := c.Rate(context.Background(), "c3", 12); region != "UK" || cents != 1250 || err != nil {
		t.Errorf("Rate(12) = %q, %d, %v; want UK, 1250", region, cents, err)
	}
	if _, _, err := c.Rate(context.Background(), "c3", 3); err == nil {
		t.Error("Rate(3) on a 400: want an error")
	}
}
