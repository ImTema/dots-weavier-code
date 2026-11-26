package payments_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"example.com/samples/orders/internal/order"
	"example.com/samples/orders/internal/payments"
)

// fakePayments approves charges up to 1000 cents as ch-1 and serves ch-1 back.
func fakePayments(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == payments.ChargesPath:
			var req struct {
				OrderID     string `json:"order_id"`
				AmountCents int64  `json:"amount_cents"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode: %v", err)
			}
			switch {
			case req.OrderID == "boom":
				w.WriteHeader(http.StatusInternalServerError)
			case req.AmountCents > 1000:
				w.WriteHeader(http.StatusPaymentRequired)
				_, _ = w.Write([]byte(`{"error":"declined: over the standard limit"}`))
			default:
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"charge_id":"ch-1","order_id":"` + req.OrderID + `","amount_cents":` + "500" + `}`))
			}
		case r.Method == http.MethodGet && r.URL.Path == payments.ChargesPath+"/ch-1":
			_, _ = w.Write([]byte(`{"charge_id":"ch-1","amount_cents":500}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestCharge(t *testing.T) {
	srv := fakePayments(t)
	defer srv.Close()
	c := payments.NewClient(srv.URL, srv.Client())
	ctx := context.Background()

	if id, err := c.Charge(ctx, "o-1", "c1", 500); err != nil || id != "ch-1" {
		t.Fatalf("Charge = %q, %v; want ch-1", id, err)
	}
	if _, err := c.Charge(ctx, "o-2", "c1", 5000); !errors.Is(err, order.ErrDeclined) {
		t.Fatalf("over the limit: err = %v, want ErrDeclined", err)
	}
	if _, err := c.Charge(ctx, "boom", "c1", 500); err == nil || errors.Is(err, order.ErrDeclined) {
		t.Fatalf("server error: err = %v, want a plain error", err)
	}
	if cents, err := c.Charged(ctx, "ch-1"); err != nil || cents != 500 {
		t.Fatalf("Charged = %d, %v; want 500", cents, err)
	}
	if _, err := c.Charged(ctx, "nope"); err == nil {
		t.Fatal("unknown charge: want error")
	}
}
