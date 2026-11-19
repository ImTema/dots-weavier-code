package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"example.com/samples/orders/api"
	"example.com/samples/orders/internal/customer"
	"example.com/samples/orders/internal/httpapi"
)

func TestGetCustomer(t *testing.T) {
	store := customer.NewStore(customer.Seed())
	srv := httptest.NewServer(httpapi.New(nil, store, nil).Routes())
	defer srv.Close()

	tests := []struct {
		id       string
		wantCode int
		wantTier string
	}{
		{"c1", http.StatusOK, api.TierGold},
		{"c3", http.StatusOK, api.TierStandard},
		{"nope", http.StatusNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			resp, err := http.Get(srv.URL + api.CustomerPath + tt.id)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantCode {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantCode)
			}
			if tt.wantTier == "" {
				return
			}
			var c api.Customer
			if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
				t.Fatal(err)
			}
			if c.Tier != tt.wantTier {
				t.Fatalf("tier = %q, want %q", c.Tier, tt.wantTier)
			}
		})
	}
}
