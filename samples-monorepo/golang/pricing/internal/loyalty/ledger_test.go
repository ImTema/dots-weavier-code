package loyalty_test

import (
	"testing"

	"example.com/samples/pricing/internal/loyalty"
)

func TestRecord(t *testing.T) {
	l := loyalty.NewLedger()
	steps := []struct {
		order, customer string
		want            bool
		wantCount       int
	}{
		{"o-1", "c1", true, 1},
		{"o-1", "c1", false, 1}, // redelivery
		{"o-2", "c1", true, 2},
		{"o-3", "", false, 2},
		{"", "c1", false, 2},
	}
	for _, s := range steps {
		if got := l.Record(s.order, s.customer); got != s.want {
			t.Errorf("Record(%q, %q) = %v, want %v", s.order, s.customer, got, s.want)
		}
		if got := l.Orders("c1"); got != s.wantCount {
			t.Errorf("after %q: Orders = %d, want %d", s.order, got, s.wantCount)
		}
	}
}
