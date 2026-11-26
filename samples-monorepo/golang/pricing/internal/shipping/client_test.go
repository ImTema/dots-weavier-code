package shipping_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"example.com/samples/pricing/internal/shipping"
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

	tests := []struct {
		units   int64
		want    int64
		wantErr bool
	}{
		{12, 1250, false},
		{0, 0, false},
		{3, 0, true},
	}
	for _, tt := range tests {
		got, err := c.Rate(context.Background(), "c3", tt.units)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("Rate(%d) = %d, %v; want %d, err=%v", tt.units, got, err, tt.want, tt.wantErr)
		}
	}
}
