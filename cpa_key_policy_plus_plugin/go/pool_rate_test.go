package main

import (
	"testing"

	"codexcont/cpa-key-policy-plus-plugin/internal/policyplus"
)

func TestEffectiveCapacityIsFixedForTheCycle(t *testing.T) {
	measured := func(usd, pp float64) policyplus.CycleLedger {
		return policyplus.CycleLedger{Capacity: policyplus.CapacityEstimate{USD: usd, DeltaPP: pp, Basis: "cycle"}}
	}
	state := func(ledgers ...policyplus.CycleLedger) accountState {
		return accountState{Plan: "pro", Ledgers: ledgers, Current: ledgers[len(ledgers)-1]}
	}
	cases := []struct {
		name  string
		state accountState
		usd   float64
		basis string
	}{
		{"previous measurement wins over the running one", state(measured(1700, 20), measured(2400, 8)), 1700, "locked"},
		{"latest well-measured cycle", state(measured(1700, 20), measured(2200, 15), measured(2400, 8)), 2200, "locked"},
		{"skips a thin previous cycle", state(measured(1700, 20), measured(900, 2), measured(2400, 8)), 1700, "locked"},
		{"first cycle measures itself", state(measured(2400, 8)), 2400, "cycle"},
		{"thin previous before own rises", state(measured(900, 2), measured(2400, 1)), 900, "previous"},
		{"nothing measured", state(policyplus.CycleLedger{}), policyplus.PriorCapacityUSD("pro"), "prior"},
	}
	for _, c := range cases {
		got := effectiveCapacity(c.state)
		if got.USD != c.usd || got.Basis != c.basis {
			t.Fatalf("%s: got %v/%s, want %v/%s", c.name, got.USD, got.Basis, c.usd, c.basis)
		}
	}
}
