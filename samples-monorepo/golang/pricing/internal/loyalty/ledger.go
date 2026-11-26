// Package loyalty is the loyalty domain: how many orders each customer has placed.
package loyalty

import "sync"

// Ledger counts placed orders per customer. It is fed by OrderPlaced events,
// which arrive at least once, so it ignores order IDs it has already seen.
type Ledger struct {
	mu     sync.Mutex
	seen   map[string]bool
	counts map[string]int
}

// NewLedger returns an empty Ledger.
func NewLedger() *Ledger {
	return &Ledger{seen: map[string]bool{}, counts: map[string]int{}}
}

// Record counts orderID for customerID. It reports false for an anonymous
// customer or an order already recorded.
func (l *Ledger) Record(orderID, customerID string) bool {
	if orderID == "" || customerID == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen[orderID] {
		return false
	}
	// ponytail: in memory, lost on restart; persist or rebuild from the topic's retained messages if that matters.
	l.seen[orderID] = true
	l.counts[customerID]++
	return true
}

// Orders returns how many distinct orders customerID has placed.
func (l *Ledger) Orders(customerID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.counts[customerID]
}
