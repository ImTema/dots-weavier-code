// Package customer is the customer domain: an in-memory store.
package customer

import (
	"errors"
	"strings"
	"sync"

	"example.com/samples/orders/api"
)

// ErrNotFound is returned for unknown customer IDs.
var ErrNotFound = errors.New("customer not found")

// Store is a concurrency-safe in-memory customer store.
type Store struct {
	mu   sync.RWMutex
	byID map[string]api.Customer
}

// NewStore returns a store holding a copy of customers.
func NewStore(customers []api.Customer) *Store {
	s := &Store{byID: make(map[string]api.Customer, len(customers))}
	for _, c := range customers {
		s.byID[c.ID] = c
	}
	return s
}

// Seed returns demo customers.
func Seed() []api.Customer {
	return []api.Customer{
		{ID: "c1", Tier: api.TierGold, Region: "EU"},
		{ID: "c2", Tier: api.TierSilver, Region: "US", Phone: "+15550100"},
		{ID: "c3", Region: "UK"},
	}
}

// Lookup returns the customer with id. A missing tier defaults to standard.
func (s *Store) Lookup(id string) (api.Customer, error) {
	s.mu.RLock()
	c, ok := s.byID[strings.TrimSpace(id)]
	s.mu.RUnlock()
	if !ok {
		return api.Customer{}, ErrNotFound
	} else if c.Tier == "" {
		c.Tier = api.TierStandard
	}
	return c, nil
}

// IsVIP reports whether c gets VIP treatment.
func IsVIP(c api.Customer) bool {
	if c.Tier == api.TierGold {
		return true
	} else if c.Tier == api.TierSilver && c.Phone != "" {
		return true
	}
	return false
}
