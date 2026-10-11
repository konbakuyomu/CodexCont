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

func priceFastGPT55(t *testing.T) {
	t.Helper()
	state.mu.Lock()
	price := state.priceBook.Prices["gpt-5.5"]
	price.ServiceTiers = []policyplus.ModelPriceServiceTier{{
		Mode: "fast", ServiceTier: "priority", InputPerMillion: 2.5, OutputPerMillion: 25,
		InputConfigured: true, OutputConfigured: true,
	}}
	state.priceBook.Prices["gpt-5.5"] = price
	state.mu.Unlock()
}

func TestUsageHandleBillsRequestedFastEvenWhenTheResponseSaysDefault(t *testing.T) {
	key := setupTestState(t)
	priceFastGPT55(t)
	// The ChatGPT backend reports "default" for priority requests it serves
	// faster; the request is billed at the fast price.
	for _, requested := range []string{"priority", "fast"} {
		event := recordFastUsage(t, key, requested, "default")
		if event.BillingPending || event.ServiceTier != requested || event.CostBreakdown.ServiceTierRule != "fast/priority" ||
			math.Abs(event.Cost-2.5*standardCost) > 1e-12 {
			t.Fatalf("%s answered as default must bill at the fast price: %#v", requested, event)
		}
	}
	if event := recordFastUsage(t, key, "auto", "default"); event.ServiceTier != "auto" || math.Abs(event.Cost-standardCost) > 1e-12 {
		t.Fatalf("a standard request stays standard: %#v", event)
	}
}

func TestUsageHandleBillsServedFastTierAtItsPrice(t *testing.T) {
	key := setupTestState(t)
	priceFastGPT55(t)
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
	event := recordFastUsage(t, key, "priority", "default")
	if event.BillingPending || event.ServiceTier != "priority" || event.CostBreakdown.ServiceTierRule != "estimated" ||
		math.Abs(event.Cost-2*standardCost) > 1e-12 {
		t.Fatalf("fast without a fast price must be estimated at twice the standard price, not free: %#v", event)
	}
	keys, err := loadedStore().ListKeys(context.Background())
	if err != nil || len(keys) != 1 || keys[0].BillingHoldReason != "" {
		t.Fatalf("an estimate leaves no hold: keys=%#v err=%v", keys, err)
	}
}
