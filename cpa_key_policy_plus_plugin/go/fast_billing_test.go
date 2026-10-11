package main

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"codexcont/cpa-key-policy-plus-plugin/internal/policyplus"
)

func recordFastUsage(t *testing.T, key policyplus.KeyRecord, requested, served string) policyplus.UsageEvent {
	t.Helper()
	body, _ := json.Marshal(usageRecord{
		Model:               "gpt-5.5",
		AuthID:              key.ID,
		RequestedAt:         time.Now(),
		ServiceTier:         requested,
		ResponseServiceTier: served,
		Detail:              usageDetail{InputTokens: 100000, OutputTokens: 10000, TotalTokens: 110000},
	})
	if _, err := usageHandle(body); err != nil {
		t.Fatal(err)
	}
	events, err := loadedStore().RecentEvents(context.Background(), key.ID, 1)
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	return events[0]
}

// standardCost is 100k input and 10k output on setupTestState's gpt-5.5 price.
const standardCost = (100000*1 + 10000*10) / 1_000_000.0

func TestUsageHandleBillsTheTierTheUpstreamServed(t *testing.T) {
	key := setupTestState(t)
	// A capped key asked for priority on a model without a fast price, but the
	// upstream ran it as standard: it is billed as standard, with no hold.
	event := recordFastUsage(t, key, "priority", "default")
	if event.BillingPending || event.ServiceTier != "default" || math.Abs(event.Cost-standardCost) > 1e-12 {
		t.Fatalf("served standard tier must bill at the standard price: %#v", event)
	}
	keys, err := loadedStore().ListKeys(context.Background())
	if err != nil || len(keys) != 1 || keys[0].BillingHoldReason != "" {
		t.Fatalf("no hold for a request served as standard: keys=%#v err=%v", keys, err)
	}
}

func TestUsageHandleBillsServedFastTierAtItsPrice(t *testing.T) {
	key := setupTestState(t)
	state.mu.Lock()
	price := state.priceBook.Prices["gpt-5.5"]
	price.ServiceTiers = []policyplus.ModelPriceServiceTier{{
		Mode: "fast", ServiceTier: "priority", InputPerMillion: 2.5, OutputPerMillion: 25,
		InputConfigured: true, OutputConfigured: true,
	}}
	state.priceBook.Prices["gpt-5.5"] = price
	state.mu.Unlock()
	// The request asked for nothing in particular; the upstream served priority.
	event := recordFastUsage(t, key, "auto", "priority")
	if event.BillingPending || event.ServiceTier != "priority" || event.CostBreakdown.ServiceTierRule != "fast/priority" ||
		math.Abs(event.Cost-2.5*standardCost) > 1e-12 {
		t.Fatalf("served priority must bill at the fast price: %#v", event)
	}
}

func TestUsageHandleEstimatesFastForKeysWithoutUSDWindows(t *testing.T) {
	key := setupTestState(t)
	key.DailyLimitUSD = nil
	key.WeeklyLimitUSD = nil
	key.QuotaUnlimited = true
	saveKey(t, key)
	event := recordFastUsage(t, key, "priority", "")
	if event.BillingPending || event.ServiceTier != "priority" || event.CostBreakdown.ServiceTierRule != "estimated" ||
		math.Abs(event.Cost-2*standardCost) > 1e-12 {
		t.Fatalf("fast without a fast price must be estimated at twice the standard price, not free: %#v", event)
	}
	keys, err := loadedStore().ListKeys(context.Background())
	if err != nil || len(keys) != 1 || keys[0].BillingHoldReason != "" {
		t.Fatalf("an estimate leaves no hold: keys=%#v err=%v", keys, err)
	}
}
