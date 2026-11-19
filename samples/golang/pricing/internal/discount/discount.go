// Package discount picks the best applicable discount rule.
package discount

// Rule is one discount policy. Calls through Rule in Best are sure interface
// hops: Rules builds the slice from concrete types in this package.
type Rule interface {
	Discount(subtotal, units int64) int64
}

// TierRule takes a percentage off the subtotal.
type TierRule struct{ Percent int64 }

func (r TierRule) Discount(subtotal, _ int64) int64 {
	if r.Percent <= 0 {
		return 0
	}
	return subtotal * r.Percent / 100
}

// VolumeRule takes a fixed amount off baskets of at least MinUnits.
type VolumeRule struct{ MinUnits, OffCents int64 }

func (r VolumeRule) Discount(subtotal, units int64) int64 {
	if units < r.MinUnits {
		return 0
	} else if r.OffCents > subtotal {
		return subtotal
	}
	return r.OffCents
}

// LoyaltyRule takes a percentage off for customers with at least MinOrders
// earlier orders. Orders comes from OrderPlaced events.
type LoyaltyRule struct {
	Orders, MinOrders int
	Percent           int64
}

func (r LoyaltyRule) Discount(subtotal, _ int64) int64 {
	if r.Orders < r.MinOrders {
		return 0
	}
	return subtotal * r.Percent / 100
}

// Rules returns the rules a customer is eligible for, given their tier and
// how many orders they placed before.
func Rules(tier string, priorOrders int) []Rule {
	rules := []Rule{
		VolumeRule{MinUnits: 10, OffCents: 500},
		LoyaltyRule{Orders: priorOrders, MinOrders: 3, Percent: 10},
	}
	if tier == "gold" {
		rules = append(rules, TierRule{Percent: 15})
	} else if tier == "silver" {
		rules = append(rules, TierRule{Percent: 5})
	}
	return rules
}

// Best returns the largest discount any rule grants. Rules do not stack.
func Best(rules []Rule, subtotal, units int64) int64 {
	var best int64
	for _, r := range rules {
		if d := r.Discount(subtotal, units); d > best {
			best = d
		}
	}
	return best
}
