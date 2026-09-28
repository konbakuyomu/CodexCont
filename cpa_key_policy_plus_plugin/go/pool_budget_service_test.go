package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"codexcont/cpa-key-policy-plus-plugin/internal/policyplus"
)

// spendModel records a settled cost of key for model on auth.
func spendModel(t *testing.T, key policyplus.KeyRecord, auth, model string, at time.Time, cost float64) {
	t.Helper()
	provider := "codex"
	if strings.HasPrefix(model, "grok") {
		provider = "xai"
	}
	event := policyplus.UsageEvent{
		RequestID:    fmt.Sprintf("%s-%s-%d", key.ID, model, at.UnixNano()),
		KeyID:        key.ID,
		KeyPreview:   key.Preview,
		Model:        model,
		Provider:     provider,
		RequestedAt:  at.Add(-time.Second),
		LatencyMS:    1000,
		Cost:         cost,
		AuthIndex:    auth,
		ConsumedAtMS: at.UnixMilli(),
	}
	if err := loadedStore().InsertUsage(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	invalidateAccountState(auth)
}

func priceGrok(t *testing.T) {
	t.Helper()
	state.mu.Lock()
	state.priceBook.Prices["grok-4"] = policyplus.ModelPrice{Model: "grok-4", InputPerMillion: 2, OutputPerMillion: 10}
	state.mu.Unlock()
}

// saveProAndGrokPools puts bob and carol in both the codex pool and a
// weekly-budget pool on the xai account.
func saveProAndGrokPools(t *testing.T, f poolFixture, budget float64, resetAt int64) {
	t.Helper()
	pools := []policyplus.Pool{
		{ID: "pro", Name: "Pro 20x", AuthIndex: "acc-pro", Members: []policyplus.PoolMember{
			{KeyID: f.owner.ID}, {KeyID: f.bob.ID, SharePercent: share(40)}, {KeyID: f.carol.ID, SharePercent: share(40)},
		}},
		{ID: "grok", Name: "Grok", AuthIndex: "acc-xai", Provider: "xai", BudgetUSD: &budget, BudgetResetAt: resetAt, Members: []policyplus.PoolMember{
			{KeyID: f.owner.ID}, {KeyID: f.bob.ID, SharePercent: share(50)}, {KeyID: f.carol.ID, SharePercent: share(50)},
		}},
	}
	if err := loadedStore().SavePools(context.Background(), pools); err != nil {
		t.Fatal(err)
	}
	invalidatePoolConfig()
	resetPoolCaches()
}

func TestBudgetPoolMetersGrokSharesApartFromTheCodexPool(t *testing.T) {
	f := setupPoolFixture(t)
	stubPoolHost(t, false)
	priceGrok(t)
	resetAt := f.now.Add(48 * time.Hour).Unix()
	saveProAndGrokPools(t, f, 100, resetAt)
	observe(t, "acc-pro", 10, f.reset, f.now.Add(-2*time.Hour))

	spendModel(t, f.bob, "acc-xai", "grok-4", f.now.Add(-time.Hour), 30)
	if d := checkPoolShare(f.bob, "grok-4"); !d.Allowed {
		t.Fatalf("30%% of a 50%% Grok share is fine: %+v", d)
	}
	spendModel(t, f.bob, "acc-xai", "grok-4", f.now.Add(-30*time.Minute), 25)
	d := checkPoolShare(f.bob, "grok-4")
	if d.Allowed || d.PoolName != "Grok" || !approxEqual(d.UsedPP, 55) || !approxEqual(d.AvailablePP, 50) || d.ResetAt != resetAt {
		t.Fatalf("55%% of the $100 budget uses up bob's 50%% Grok share: %+v", d)
	}
	if d := checkPoolShare(f.bob, "gpt-5.5"); !d.Allowed {
		t.Fatalf("the Grok share does not touch bob's codex share: %+v", d)
	}
	if d := evaluatePolicy(f.bob, "grok-4", false); d.Allowed || d.Code != "pool_share_exceeded" || !strings.Contains(d.Message, "Grok") {
		t.Fatalf("the Grok request is refused on the Grok share: %+v", d)
	}
	if d := checkPoolShare(f.carol, "grok-4"); !d.Allowed {
		t.Fatalf("carol has used nothing: %+v", d)
	}
	startBurst(t, "grok", f.bob.ID)
	if d := checkPoolShare(f.bob, "grok-4"); !d.Allowed {
		t.Fatalf("a Grok burst lifts the Grok share: %+v", d)
	}

	resp := portalGet(t, "/v0/resource/plugins/cpa-key-policy-plus/user/api/pool", plusSessionCookie(t, "sk-bob-secret"), nil)
	var body struct {
		Pool  map[string]any   `json:"pool"`
		Pools []map[string]any `json:"pools"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("user pools = %d %s", resp.StatusCode, resp.Body)
	}
	if len(body.Pools) != 2 || body.Pool["name"] != "Pro 20x" || body.Pools[1]["name"] != "Grok" || body.Pools[1]["budgeted"] != true {
		t.Fatalf("bob sees the codex pool first, then the Grok budget pool: %+v", body.Pools)
	}
	capacity, _ := body.Pools[1]["capacity"].(map[string]any)
	if capacity["usd"] != 100.0 || capacity["basis"] != "budget" {
		t.Fatalf("the Grok pool's 100%% is its budget: %+v", capacity)
	}

	resp = portalGet(t, "/v0/management/plugins/cpa-key-policy-plus/pools", "", nil)
	var admin struct {
		Pools []map[string]any `json:"pools"`
	}
	if err := json.Unmarshal(resp.Body, &admin); err != nil || len(admin.Pools) != 2 {
		t.Fatalf("admin pools = %d %s", resp.StatusCode, resp.Body)
	}
	grok := admin.Pools[1]
	cycle, _ := grok["cycle"].(map[string]any)
	if grok["budgeted"] != true || grok["provider"] != "xai" || cycle["status"] != "budget" || !approxEqual(cycle["used_percent"].(float64), 55) {
		t.Fatalf("admin view of the budget pool: %+v", grok)
	}
	for _, w := range grok["warnings"].([]any) {
		if strings.Contains(fmt.Sprint(w), "还没有观测") {
			t.Fatalf("a budget pool waits for no quota observation: %v", w)
		}
	}
}

func TestBudgetPoolStartsOverEachWeek(t *testing.T) {
	f := setupPoolFixture(t)
	stubPoolHost(t, false)
	priceGrok(t)
	// The cycle ended an hour ago; last week's spend no longer counts.
	resetAt := f.now.Add(-time.Hour).Unix()
	saveProAndGrokPools(t, f, 100, resetAt)
	spendModel(t, f.bob, "acc-xai", "grok-4", f.now.Add(-2*time.Hour), 80)
	if d := checkPoolShare(f.bob, "grok-4"); !d.Allowed {
		t.Fatalf("a new budget week starts at zero: %+v", d)
	}
	account := accountStateFor(context.Background(), loadedStore(), "acc-xai", f.now)
	if !account.InCycle(f.now) || account.Current.CycleResetAt != resetAt+policyplus.BudgetCycleSeconds || len(account.Ledgers) < 2 {
		t.Fatalf("budget cycles step a week from the anchor: %+v", account.Current)
	}
	if prev := account.Ledgers[len(account.Ledgers)-2]; !approxEqual(prev.Attributed[f.bob.ID], 80) {
		t.Fatalf("last week's ledger keeps bob's 80%%: %+v", prev.Attributed)
	}
}

func TestOffPoolModelsStayUnderTheKeysUSDWindows(t *testing.T) {
	f := setupPoolFixture(t)
	stubPoolHost(t, false)
	priceGrok(t)
	ctx := context.Background()
	bob := f.bob
	bob.WeeklyLimitUSD = floatPtr(10)
	if err := loadedStore().SaveKeySettings(ctx, bob); err != nil {
		t.Fatal(err)
	}
	savePool(t, policyplus.PoolVisibilityAnonymous, "",
		policyplus.PoolMember{KeyID: f.owner.ID},
		policyplus.PoolMember{KeyID: bob.ID, SharePercent: share(40)},
	)
	observe(t, "acc-pro", 10, f.reset, f.now.Add(-2*time.Hour))
	// $50 of codex usage is the share's business, not the $10 window's.
	spendModel(t, bob, "acc-pro", "gpt-5.5", f.now.Add(-time.Hour), 50)
	if d := evaluatePolicy(bob, "grok-4", false); !d.Allowed {
		t.Fatalf("pool usage does not count toward the off-pool window: %+v", d)
	}
	if d := evaluatePolicy(bob, "gpt-5.5", false); !d.Allowed {
		t.Fatalf("pool models are governed by the share: %+v", d)
	}
	spendModel(t, bob, "acc-xai", "grok-4", f.now.Add(-30*time.Minute), 12)
	d := evaluatePolicy(bob, "grok-4", false)
	if d.Allowed || d.Code != "weekly_quota_exceeded" || !approxEqual(d.UsedUSD, 12) || !strings.Contains(d.Message, "池外模型") {
		t.Fatalf("Grok is held to bob's own $10 window: %+v", d)
	}
	if d := evaluatePolicy(bob, "gpt-5.5", false); !d.Allowed {
		t.Fatalf("an exhausted off-pool window does not block pool models: %+v", d)
	}

	resp := portalGet(t, "/v0/management/plugins/cpa-key-policy-plus/keys", "", nil)
	var keys struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(resp.Body, &keys); err != nil {
		t.Fatal(err)
	}
	for _, row := range keys.Keys {
		if row["id"] != bob.ID {
			continue
		}
		week := row["quota"].(map[string]any)[policyplus.Range7D].(map[string]any)
		usage := row["usage"].(map[string]any)
		if row["quota_scope"] != "off_pool" || !approxEqual(week["used_usd"].(float64), 12) || !approxEqual(usage[policyplus.Range7D].(float64), 62) {
			t.Fatalf("bob's window row measures off-pool usage only: scope %v week %+v usage %+v", row["quota_scope"], week, usage)
		}
		pool := row["pool"].(map[string]any)
		if seats, _ := pool["seats"].([]any); len(seats) != 1 || pool["usd_windows_off"] != true {
			t.Fatalf("bob's pool view: %+v", pool)
		}
		return
	}
	t.Fatal("bob's key row missing")
}

func TestKeyRowListsEveryPoolAndBurst(t *testing.T) {
	f := setupPoolFixture(t)
	stubPoolHost(t, false)
	saveProAndGrokPools(t, f, 100, f.now.Add(48*time.Hour).Unix())
	observe(t, "acc-pro", 10, f.reset, f.now.Add(-2*time.Hour))
	startBurst(t, "pro", f.bob.ID)
	view := poolKeyView(f.bob.ID)
	seats, _ := view["seats"].([]map[string]any)
	if len(seats) != 2 || seats[0]["pool"] != "Pro 20x" || seats[0]["bursting"] != true || seats[1]["pool"] != "Grok" || seats[1]["budgeted"] != true {
		t.Fatalf("both pools listed, codex first, with the burst: %+v", seats)
	}
	if _, on := seats[1]["bursting"]; on {
		t.Fatalf("the burst belongs to the codex pool only: %+v", seats[1])
	}
	if got := routeFor(t, "sk-bob-secret", "grok-4"); got.Handled {
		t.Fatalf("the Grok pool has no prefix, so Grok requests are not rerouted: %+v", got)
	}
}
