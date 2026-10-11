package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"codexcont/cpa-key-policy-plus-plugin/internal/policyplus"
)

func aliceRow(t *testing.T) poolRow {
	t.Helper()
	pool := loadedPools(context.Background())[0]
	row, ok := computePoolLedger(context.Background(), loadedStore(), pool, time.Now()).row("alice-key")
	if !ok {
		t.Fatal("alice has no row")
	}
	return row
}

func TestUsedUpShareStaysHeldThroughSmallCorrections(t *testing.T) {
	f := setupPoolFixture(t)
	stubPoolHost(t, false)
	savePool(t, policyplus.PoolVisibilityAnonymous, "",
		policyplus.PoolMember{KeyID: f.owner.ID},
		policyplus.PoolMember{KeyID: f.alice.ID, SharePercent: share(10)},
	)
	observe(t, "acc-pro", 0, f.reset, f.now.Add(-3*time.Hour))
	// $210 at the plan's $2000 per 100% is 10.5 points not yet seen by the
	// account: alice is over her 10% share.
	spend(t, f.alice, "acc-pro", f.now.Add(-2*time.Hour), 210)
	if d := evaluatePolicy(f.alice, "gpt-5.5", false); d.Allowed || d.Code != "pool_share_exceeded" {
		t.Fatalf("alice ran out of her share: %+v", d)
	}
	// The account then shows 9.9%: the estimate was a little high and 0.1
	// point is left. That is not enough to let her back in.
	observe(t, "acc-pro", 9.9, f.reset, f.now.Add(-time.Hour))
	if row := aliceRow(t); row.State != "over" || *row.RemainingPP < 0.09 || *row.RemainingPP > 0.11 {
		t.Fatalf("a correction below a quarter point keeps her held: %+v remaining %v", row, *row.RemainingPP)
	}
	if d := evaluatePolicy(f.alice, "gpt-5.5", false); d.Allowed {
		t.Fatalf("held through a small correction: %+v", d)
	}
	// A larger share frees enough to let her in.
	savePool(t, policyplus.PoolVisibilityAnonymous, "",
		policyplus.PoolMember{KeyID: f.owner.ID},
		policyplus.PoolMember{KeyID: f.alice.ID, SharePercent: share(10.3)},
	)
	if d := evaluatePolicy(f.alice, "gpt-5.5", false); !d.Allowed {
		t.Fatalf("0.4 point left releases her: %+v", d)
	}
	// Back at 10%, she is not held again until she runs out again.
	savePool(t, policyplus.PoolVisibilityAnonymous, "",
		policyplus.PoolMember{KeyID: f.owner.ID},
		policyplus.PoolMember{KeyID: f.alice.ID, SharePercent: share(10)},
	)
	if d := evaluatePolicy(f.alice, "gpt-5.5", false); !d.Allowed {
		t.Fatalf("0.1 point left without having run out is still usable: %+v", d)
	}
}

func TestHeldShareClearsOnANewCycle(t *testing.T) {
	f := setupPoolFixture(t)
	stubPoolHost(t, false)
	savePool(t, policyplus.PoolVisibilityAnonymous, "",
		policyplus.PoolMember{KeyID: f.owner.ID},
		policyplus.PoolMember{KeyID: f.alice.ID, SharePercent: share(10)},
	)
	observe(t, "acc-pro", 0, f.reset, f.now.Add(-3*time.Hour))
	spend(t, f.alice, "acc-pro", f.now.Add(-2*time.Hour), 210)
	if d := evaluatePolicy(f.alice, "gpt-5.5", false); d.Allowed {
		t.Fatalf("alice ran out of her share: %+v", d)
	}
	// A reset credit: the account starts a new window at 0%.
	observe(t, "acc-pro", 0, f.now.Add(7*24*time.Hour-time.Hour).Unix(), f.now.Add(-time.Hour))
	if d := evaluatePolicy(f.alice, "gpt-5.5", false); !d.Allowed {
		t.Fatalf("the new cycle starts her over at once: %+v", d)
	}
}

func TestRefusalLooksTheAccountUpAtMostOnceInAWhile(t *testing.T) {
	f := setupPoolFixture(t)
	// The upstream still reports 20%: no reset happened.
	calls := stubProbeHost(t, func() (int, string) { return http.StatusOK, codexUsageBody(20, f.reset, false) })
	savePool(t, policyplus.PoolVisibilityAnonymous, "",
		policyplus.PoolMember{KeyID: f.owner.ID},
		policyplus.PoolMember{KeyID: f.alice.ID, SharePercent: share(10)},
	)
	observe(t, "acc-pro", 10, f.reset, f.now.Add(-2*time.Hour))
	spend(t, f.alice, "acc-pro", f.now.Add(-90*time.Minute), 50)
	observe(t, "acc-pro", 20, f.reset, f.now.Add(-time.Hour))
	if d := evaluatePolicy(f.alice, "gpt-5.5", false); d.Allowed || *calls != 1 {
		t.Fatalf("refused after one lookup found no reset: %+v calls %d", d, *calls)
	}
	for i := 0; i < 3; i++ {
		if d := evaluatePolicy(f.alice, "gpt-5.5", false); d.Allowed {
			t.Fatalf("still refused: %+v", d)
		}
	}
	if *calls != 1 {
		t.Fatalf("refusals right after a lookup do not look again: %d calls", *calls)
	}
}

func TestRefusalSkipsTheLookupWhenTheAccountWasJustHeardFrom(t *testing.T) {
	f := setupPoolFixture(t)
	calls := stubProbeHost(t, func() (int, string) { return http.StatusOK, codexUsageBody(20, f.reset, false) })
	savePool(t, policyplus.PoolVisibilityAnonymous, "",
		policyplus.PoolMember{KeyID: f.owner.ID},
		policyplus.PoolMember{KeyID: f.alice.ID, SharePercent: share(10)},
	)
	observe(t, "acc-pro", 10, f.reset, f.now.Add(-2*time.Hour))
	spend(t, f.alice, "acc-pro", f.now.Add(-90*time.Minute), 50)
	// Another member's response carried the account's usage seconds ago.
	observe(t, "acc-pro", 20, f.reset, time.Now().Add(-5*time.Second))
	if d := evaluatePolicy(f.alice, "gpt-5.5", false); d.Allowed || *calls != 0 {
		t.Fatalf("fresh numbers need no lookup: %+v calls %d", d, *calls)
	}
}

func TestSlowLookupDoesNotHoldTheRefusalUp(t *testing.T) {
	f := setupPoolFixture(t)
	release := make(chan struct{})
	reset := f.now.Add(7*24*time.Hour - time.Hour).Unix()
	stubProbeHost(t, func() (int, string) {
		<-release
		return http.StatusOK, codexUsageBody(0, reset, false)
	})
	old := probeDenyWait
	probeDenyWait = 50 * time.Millisecond
	t.Cleanup(func() { probeDenyWait = old })
	savePool(t, policyplus.PoolVisibilityAnonymous, "",
		policyplus.PoolMember{KeyID: f.owner.ID},
		policyplus.PoolMember{KeyID: f.alice.ID, SharePercent: share(10)},
	)
	observe(t, "acc-pro", 10, f.reset, f.now.Add(-2*time.Hour))
	spend(t, f.alice, "acc-pro", f.now.Add(-90*time.Minute), 50)
	observe(t, "acc-pro", 20, f.reset, f.now.Add(-time.Hour))
	started := time.Now()
	if d := evaluatePolicy(f.alice, "gpt-5.5", false); d.Allowed || time.Since(started) > 2*time.Second {
		t.Fatalf("refused without waiting for the slow lookup: %+v after %v", d, time.Since(started))
	}
	// The lookup finishes in the background and the next request sees the reset.
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for probeStatusOf("acc-pro").running || probeStatusOf("acc-pro").OKAt.IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("the lookup never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if d := evaluatePolicy(f.alice, "gpt-5.5", false); !d.Allowed {
		t.Fatalf("the background lookup found the reset: %+v", d)
	}
}
