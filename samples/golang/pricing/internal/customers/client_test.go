package customers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	ordersapi "example.com/samples/orders/api"
	"example.com/samples/pricing/internal/customers"
)

func TestTier(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case ordersapi.CustomerPath + "c1":
			_, _ = w.Write([]byte(`{"id":"c1","tier":"gold"}`))
		case ordersapi.CustomerPath + "boom":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := customers.NewClient(srv.URL, srv.Client())

	tests := []struct {
		id      string
		want    string
		wantErr bool
	}{
		{"c1", ordersapi.TierGold, false},
		{"", ordersapi.TierStandard, false},
		{"unknown", ordersapi.TierStandard, false},
		{"boom", "", true},
	}
	for _, tt := range tests {
		got, err := c.Tier(context.Background(), tt.id)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("Tier(%q) = %q, %v; want %q, err=%v", tt.id, got, err, tt.want, tt.wantErr)
		}
	}
}
