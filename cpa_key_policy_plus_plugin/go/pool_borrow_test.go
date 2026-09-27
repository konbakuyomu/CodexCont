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

// stubLendingHost lists a Plus and a Pro credential with their prefixed
// models. A non-zero plusRetry reports Plus cooling down in CPA until then.
func stubLendingHost(t *testing.T, plusRetry time.Time) {
	t.Helper()
	oldHostCall := hostCall
	t.Cleanup(func() { hostCall = oldHostCall })
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		switch method {
		case methodHostModelsList:
			return json.Marshal(hostModelsListResponse{Models: []hostModelListEntry{
				{ID: "gpt-5.5", OwnedBy: "openai", Provider: "codex"},
				{ID: "plus/gpt-5.5", OwnedBy: "openai", Provider: "codex"},
				{ID: "pro/gpt-5.5", OwnedBy: "openai", Provider: "codex"},
				{ID: "grok-4", OwnedBy: "xai", Provider: "xai"},
			}})
		case methodHostAuthList:
			plus := map[string]any{"auth_index": "acc-plus", "provider": "codex", "status": "active"}
			if !plusRetry.IsZero() {
				plus["status"], plus["unavailable"], plus["next_retry_after"] = "error", true, plusRetry.Format(time.RFC3339Nano)
			}
			return json.Marshal(map[string]any{"files": []any{
				map[string]any{"auth_index": "acc-pro", "provider": "codex", "status": "active"},
				plus,
			}})
		}
		return nil, fmt.Errorf("unexpected host callback %s", method)
	}
	resetPoolCaches()
}

// saveLendingPools puts alice and carol on Plus, and the owner and bob on a Pro
// pool that may lend its account to bursting members of other pools.
func saveLendingPools(t *testing.T, f poolFixture, lend bool) {
	t.Helper()
	ctx := context.Background()
	pools := []policyplus.Pool{
		{ID: "plus", Name: "Plus", AuthIndex: "acc-plus", ModelPrefix: "plus", Visibility: policyplus.PoolVisibilityAnonymous, Members: []policyplus.PoolMember{
			{KeyID: f.alice.ID, SharePercent: share(10)},
			{KeyID: f.carol.ID, SharePercent: share(50)},
		}},
		{ID: "pro", Name: "Pro 20x", AuthIndex: "acc-pro", ModelPrefix: "pro", Visibility: policyplus.PoolVisibilityAnonymous, LendDuringBurst: lend, Members: []policyplus.PoolMember{
			{KeyID: f.owner.ID},
			{KeyID: f.bob.ID, SharePercent: share(30)},
		}},
	}
	if err := loadedStore().SavePools(ctx, pools); err != nil {
		t.Fatal(err)
	}
	invalidatePoolConfig()
	if err := loadedStore().SyncLendWindows(ctx, loadedPools(ctx), time.Now()); err != nil {
		t.Fatal(err)
	}
}

// startBurst turns on a carry burst for one member through the admin API.
func startBurst(t *testing.T, poolID, keyID string) {
	t.Helper()
	resp := adminPost(t, "/v0/management/plugins/cpa-key-policy-plus/pools/burst", map[string]any{"pool_id": poolID, "key_id": keyID, "action": "start", "mode": "carry", "confirm": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("burst start = %d %s", resp.StatusCode, resp.Body)
	}
}

func routeFor(t *testing.T, rawKey, model string) modelRouteResponse {
	t.Helper()
	raw, err := routeModel(mustJSON(t, modelRouteRequest{RequestedModel: model, Headers: http.Header{"Authorization": {"Bearer " + rawKey}}}))
	if err != nil {
		t.Fatal(err)
	}
	var resp modelRouteResponse
	unwrapPlusEnvelope(t, raw, &resp)
	return resp
}

func userPoolView(t *testing.T, rawKey string) map[string]any {
	t.Helper()
	resp := portalGet(t, "/v0/resource/plugins/cpa-key-policy-plus/user/api/pool", plusSessionCookie(t, rawKey), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("user pool = %d %s", resp.StatusCode, resp.Body)
	}
	var body struct {
		Pool map[string]any `json:"pool"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatal(err)
	}
	return body.Pool
}

func TestBorrowSendsBurstingMembersOfAFullAccountToALendingPool(t *testing.T) {
	f := setupPoolFixture(t)
	stubLendingHost(t, time.Time{})
	saveLendingPools(t, f, true)
	observe(t, "acc-plus", 20, f.reset, f.now.Add(-2*time.Hour))
	observe(t, "acc-pro", 10, f.reset, f.now.Add(-2*time.Hour))
	if got := routeFor(t, "sk-alice-secret", "gpt-5.5"); got.TargetModel != "plus/gpt-5.5" {
		t.Fatalf("alice rides her own Plus: %+v", got)
	}

	// Plus answers "usage limit reached"; Pro lends, but nobody on Plus bursts.
	limited, _ := json.Marshal(usageRecord{
		Provider: "codex", Model: "gpt-5.5", APIKey: f.alice.ID, AuthIndex: "acc-plus",
		RequestedAt: f.now.Add(-time.Minute), Latency: time.Second, Failed: true,
		Failure: usageFailure{StatusCode: http.StatusTooManyRequests, Body: fmt.Sprintf(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_at":%d}}`, f.reset)},
	})
	if _, err := usageHandle(limited); err != nil {
		t.Fatal(err)
	}
	if got := routeFor(t, "sk-alice-secret", "gpt-5.5"); got.TargetModel != "plus/gpt-5.5" {
		t.Fatalf("only bursting members borrow, alice stays pinned: %+v", got)
	}

	startBurst(t, "plus", f.alice.ID)
	if got := routeFor(t, "sk-carol-secret", "gpt-5.5"); got.TargetModel != "plus/gpt-5.5" {
		t.Fatalf("carol is not bursting and stays on Plus: %+v", got)
	}
	got := routeFor(t, "sk-alice-secret", "gpt-5.5(high)")
	if got.TargetModel != "pro/gpt-5.5(high)" || got.Reason != "cpa_key_policy_plus_pool_borrow" || got.Target != "codex" {
		t.Fatalf("a full Plus sends alice to the lending Pro: %+v", got)
	}
	if got := routeFor(t, "sk-bob-secret", "gpt-5.5"); got.TargetModel != "pro/gpt-5.5" || got.Reason != "cpa_key_policy_plus_pool_route" {
		t.Fatalf("bob rides his own Pro: %+v", got)
	}
	if got := routeFor(t, "sk-alice-secret", "grok-4"); got.Handled {
		t.Fatalf("models the lender does not serve are not borrowed: %+v", got)
	}
	view := userPoolView(t, "sk-alice-secret")
	borrow, _ := view["borrow"].(map[string]any)
	if borrow == nil || borrow["reason"] != "account_full" || borrow["lender"] != "Pro 20x" || borrow["until"].(float64) != float64(f.reset) {
		t.Fatalf("alice's page must say she borrows Pro until Plus resets: %+v", view["borrow"])
	}
	if account := view["account"].(map[string]any); account["full"] != true {
		t.Fatalf("alice's page must say Plus is full: %+v", account)
	}

	saveLendingPools(t, f, false)
	if got := routeFor(t, "sk-alice-secret", "gpt-5.5"); got.TargetModel != "plus/gpt-5.5" {
		t.Fatalf("a pool that does not lend lends nothing: %+v", got)
	}
	saveLendingPools(t, f, true)

	// Plus serves a request made after the limit answer: it is back.
	served, _ := json.Marshal(usageRecord{
		Provider: "codex", Model: "gpt-5.5", APIKey: f.carol.ID, AuthIndex: "acc-plus",
		RequestedAt: time.Now(), Latency: time.Second,
		Detail: usageDetail{InputTokens: 10, OutputTokens: 10, TotalTokens: 20},
	})
	if _, err := usageHandle(served); err != nil {
		t.Fatal(err)
	}
	if got := routeFor(t, "sk-alice-secret", "gpt-5.5"); got.TargetModel != "plus/gpt-5.5" {
		t.Fatalf("a working Plus takes its members back: %+v", got)
	}

	// CPA cooling the Plus credential down counts as full too.
	stubLendingHost(t, time.Now().Add(time.Hour))
	if got := routeFor(t, "sk-alice-secret", "gpt-5.5"); got.TargetModel != "pro/gpt-5.5" {
		t.Fatalf("a Plus credential in CPA cooldown sends alice to Pro: %+v", got)
	}
	// A full lender lends nothing.
	observe(t, "acc-pro", 100, f.reset, time.Now())
	if got := routeFor(t, "sk-alice-secret", "gpt-5.5"); got.TargetModel != "plus/gpt-5.5" {
		t.Fatalf("a full Pro cannot take borrowers: %+v", got)
	}
}

func TestBurstLiftsTheShareBlockAndBorrowingIsAccountedApart(t *testing.T) {
	f := setupPoolFixture(t)
	stubLendingHost(t, time.Time{})
	saveLendingPools(t, f, true)
	// Alice uses up her 10% share of Plus.
	observe(t, "acc-plus", 10, f.reset, f.now.Add(-3*time.Hour))
	spend(t, f.alice, "acc-plus", f.now.Add(-170*time.Minute), 50)
	observe(t, "acc-plus", 20, f.reset, f.now.Add(-150*time.Minute))
	observe(t, "acc-pro", 10, f.reset, f.now.Add(-3*time.Hour))
	if d := evaluatePolicy(f.alice, "gpt-5.5", false); d.Allowed || d.Code != "pool_share_exceeded" {
		t.Fatalf("alice's share is used up: %+v", d)
	}

	startBurst(t, "plus", f.alice.ID)
	if d := evaluatePolicy(f.alice, "gpt-5.5", false); !d.Allowed {
		t.Fatalf("a bursting member is not share-blocked: %+v", d)
	}
	if got := routeFor(t, "sk-alice-secret", "gpt-5.5"); got.TargetModel != "plus/gpt-5.5" {
		t.Fatalf("while Plus works, bursting alice stays on it: %+v", got)
	}
	// CPA cools Plus down: bursting alice borrows Pro, carol waits.
	stubLendingHost(t, time.Now().Add(time.Hour))
	if got := routeFor(t, "sk-alice-secret", "gpt-5.5"); got.TargetModel != "pro/gpt-5.5" || got.Reason != "cpa_key_policy_plus_pool_borrow" {
		t.Fatalf("alice borrows Pro: %+v", got)
	}
	if got := routeFor(t, "sk-carol-secret", "gpt-5.5"); got.TargetModel != "plus/gpt-5.5" {
		t.Fatalf("carol still has share on Plus: %+v", got)
	}

	// Alice's borrowed request and bob's own finish before Pro rises by 2.
	spend(t, f.alice, "acc-pro", f.now.Add(-90*time.Minute), 20)
	spend(t, f.bob, "acc-pro", f.now.Add(-85*time.Minute), 20)
	observe(t, "acc-pro", 12, f.reset, f.now.Add(-80*time.Minute))
	// Leaks and borrows only count from when a member joined the pool: the
	// requests above predate saveLendingPools, this one does not.
	spend(t, f.alice, "acc-pro", time.Now().Add(3*time.Second), 0.5)

	resp := portalGet(t, "/v0/management/plugins/cpa-key-policy-plus/pools", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pools = %d %s", resp.StatusCode, resp.Body)
	}
	var body struct {
		Pools []struct {
			ID    string    `json:"id"`
			Lend  bool      `json:"lend_during_burst"`
			Rows  []poolRow `json:"rows"`
			Cycle struct {
				LentPP    float64 `json:"lent_pp"`
				OutsidePP float64 `json:"outside_pp"`
			} `json:"cycle"`
			Leaks    policyplus.AwayUsage `json:"leaks"`
			Notes    []string             `json:"notes"`
			Warnings []string             `json:"warnings"`
		} `json:"pools"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil || len(body.Pools) != 2 {
		t.Fatalf("pools = %s err %v", resp.Body, err)
	}
	plus, pro := body.Pools[0], body.Pools[1]
	var aliceOnPro poolRow
	for _, row := range pro.Rows {
		if row.KeyID == f.alice.ID {
			aliceOnPro = row
		}
	}
	if !pro.Lend || aliceOnPro.Role != "borrowed" || aliceOnPro.FromPool != "Plus" || !approxEqual(aliceOnPro.AttributedPP, 1) {
		t.Fatalf("alice shows on Pro as a borrower from Plus: %+v", aliceOnPro)
	}
	if !approxEqual(pro.Cycle.LentPP, 1) || pro.Cycle.OutsidePP != 0 {
		t.Fatalf("Pro lent 1pp and has no strangers: %+v", pro.Cycle)
	}
	if plus.Leaks.LeakRequests != 0 || plus.Leaks.BorrowRequests != 1 || len(plus.Warnings) != 0 {
		t.Fatalf("a borrow is not a routing leak: %+v warnings %v", plus.Leaks, plus.Warnings)
	}
	if len(plus.Notes) != 2 || !strings.Contains(plus.Notes[0], "借用了其他车") || !strings.Contains(plus.Notes[1], "1 位爽蹬中的成员会借用「Pro 20x」") {
		t.Fatalf("Plus notes its members' borrowing and who rides Pro while it is down: %v", plus.Notes)
	}

	bob := userPoolView(t, "sk-bob-secret")
	if !approxEqual(bob["lent_pp"].(float64), 1) || bob["others_pp"].(float64) != 0 || bob["borrow"] != nil {
		t.Fatalf("Pro riders see what was lent apart from other people: %+v", bob)
	}
	alice := userPoolView(t, "sk-alice-secret")
	borrow, _ := alice["borrow"].(map[string]any)
	if borrow == nil || borrow["reason"] != "account_full" || borrow["lender"] != "Pro 20x" || !approxEqual(borrow["used_pp"].(float64), 1) {
		t.Fatalf("alice's page shows her borrowing and what she used on Pro: %+v", alice["borrow"])
	}

	// Plus enters a new cycle: alice's burst settles on Plus; what she used on
	// Pro never enters that settlement, and nothing was used past a share.
	cycle2 := f.now.Add(7 * 24 * time.Hour).Unix()
	observe(t, "acc-plus", 0, cycle2, f.now.Add(-time.Minute))
	if resp := portalGet(t, "/v0/management/plugins/cpa-key-policy-plus/pools", "", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("pools = %d %s", resp.StatusCode, resp.Body)
	}
	history, _ := loadedStore().PoolSettlements(context.Background(), "plus", 5)
	if len(history) != 1 || history[0].Status != "settled" {
		t.Fatalf("settlement history = %+v", history)
	}
	var detail policyplus.BurstSettlement
	if err := json.Unmarshal(history[0].Detail, &detail); err != nil || len(detail.Bursts) != 1 || detail.Bursts[0].KeyID != f.alice.ID || detail.BorrowedPP != 0 {
		t.Fatalf("settlement = %s err %v", history[0].Detail, err)
	}
	if carry, _ := loadedStore().PoolCarry(context.Background(), "plus", cycle2); len(carry) != 0 {
		t.Fatalf("alice stayed within her Plus share, so nothing carries: %+v", carry)
	}
	if got := routeFor(t, "sk-alice-secret", "gpt-5.5"); got.TargetModel != "plus/gpt-5.5" {
		t.Fatalf("with her burst settled alice is back on Plus: %+v", got)
	}
}

func TestUsageLimitUntilReadsCodexResetTimes(t *testing.T) {
	requested := time.Unix(1_790_000_000, 0)
	cases := []struct {
		body  string
		until int64
		ok    bool
	}{
		{`{"error":{"type":"usage_limit_reached","resets_at":1790003600}}`, 1_790_003_600, true},
		{`{"type":"usage_limit_reached","resets_in_seconds":120}`, 1_790_000_120, true},
		{`data: {"type":"error","code":"usage_limit_reached"}`, 1_790_000_300, true},
		{`{"error":{"type":"rate_limit_exceeded","resets_in_seconds":20}}`, 0, false},
		{``, 0, false},
	}
	for _, c := range cases {
		until, ok := usageLimitUntil(c.body, requested)
		if until != c.until || ok != c.ok {
			t.Fatalf("usageLimitUntil(%q) = %d %v, want %d %v", c.body, until, ok, c.until, c.ok)
		}
	}
}

func TestHandleMethodSafelyTurnsAPanicIntoAnError(t *testing.T) {
	setupPoolFixture(t)
	oldHostCall := hostCall
	t.Cleanup(func() { hostCall = oldHostCall })
	hostCall = func(string, any) (json.RawMessage, error) { panic("host exploded") }
	resetPoolCaches()
	raw, err := handleMethodSafely(methodManagementHandle, mustJSON(t, managementRequest{
		Method: http.MethodGet, Path: "/v0/management/plugins/cpa-key-policy-plus/pools", Headers: http.Header{},
	}))
	if err == nil || raw != nil || !strings.Contains(err.Error(), "host exploded") {
		t.Fatalf("a panic must come back as an error, got raw=%s err=%v", raw, err)
	}
}

func TestPoolShareReplacesUSDWindowsButPinnedMembersKeepThem(t *testing.T) {
	f := setupPoolFixture(t)
	stubPoolHost(t, true)
	ctx := context.Background()
	for _, key := range []policyplus.KeyRecord{f.bob, f.carol} {
		key.WeeklyLimitUSD = floatPtr(1)
		if err := loadedStore().SaveKeySettings(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	bob, carol := f.bob, f.carol
	bob.WeeklyLimitUSD, carol.WeeklyLimitUSD = floatPtr(1), floatPtr(1)
	spend(t, bob, "acc-pro", f.now.Add(-time.Hour), 5)
	spend(t, carol, "acc-pro", f.now.Add(-time.Hour), 5)
	if d := evaluatePolicy(bob, "gpt-5.5", false); d.Allowed || d.Code != "weekly_quota_exceeded" {
		t.Fatalf("outside any pool the USD window applies: %+v", d)
	}
	savePool(t, policyplus.PoolVisibilityAnonymous, "pro",
		policyplus.PoolMember{KeyID: f.owner.ID},
		policyplus.PoolMember{KeyID: bob.ID, SharePercent: share(40)},
		policyplus.PoolMember{KeyID: carol.ID, Role: policyplus.PoolRolePinned, SharePercent: share(30)},
	)
	observe(t, "acc-pro", 10, f.reset, f.now.Add(-2*time.Hour))
	if d := evaluatePolicy(bob, "gpt-5.5", false); !d.Allowed {
		t.Fatalf("a share member is held to the share, not the USD window: %+v", d)
	}
	if d := evaluatePolicy(carol, "gpt-5.5", false); d.Allowed || d.Code != "weekly_quota_exceeded" {
		t.Fatalf("a pinned member keeps the USD window: %+v", d)
	}
	if got := routeFor(t, "sk-carol-secret", "gpt-5.5"); got.TargetModel != "pro/gpt-5.5" {
		t.Fatalf("a pinned member rides the pool account: %+v", got)
	}
	pools := loadedPools(ctx)
	if len(pools) != 1 || !pools[0].Members[2].Pinned() || pools[0].Members[2].SharePercent != nil {
		t.Fatalf("a pinned member never holds a share: %+v", pools)
	}
	resp := portalGet(t, "/v0/management/plugins/cpa-key-policy-plus/keys", "", nil)
	var keys struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(resp.Body, &keys); err != nil {
		t.Fatal(err)
	}
	for _, row := range keys.Keys {
		pool, _ := row["pool"].(map[string]any)
		switch row["id"] {
		case bob.ID:
			if pool == nil || pool["role"] != "member" || pool["usd_windows_off"] != true {
				t.Fatalf("bob's key row must say the share governs him: %+v", pool)
			}
		case carol.ID:
			if pool == nil || pool["role"] != "pinned" || pool["usd_windows_off"] != false {
				t.Fatalf("carol's key row must say she is only pinned: %+v", pool)
			}
		}
	}
	// A pinned member cannot burst; bob's burst settles without touching her.
	if resp := adminPost(t, "/v0/management/plugins/cpa-key-policy-plus/pools/burst", map[string]any{"pool_id": "pro", "key_id": carol.ID, "action": "start", "mode": "carry", "confirm": true}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a pinned member has no share to burst past: %d %s", resp.StatusCode, resp.Body)
	}
	startBurst(t, "pro", bob.ID)
	observe(t, "acc-pro", 0, f.now.Add(7*24*time.Hour).Unix(), f.now.Add(-time.Minute))
	if resp := portalGet(t, "/v0/management/plugins/cpa-key-policy-plus/pools", "", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("pools = %d %s", resp.StatusCode, resp.Body)
	}
	history, _ := loadedStore().PoolSettlements(ctx, "pro", 5)
	if len(history) != 1 || history[0].Status != "settled" {
		t.Fatalf("settlement with a pinned member = %+v", history)
	}
	savePool(t, policyplus.PoolVisibilityAnonymous, "pro", policyplus.PoolMember{KeyID: f.owner.ID})
	if d := evaluatePolicy(bob, "gpt-5.5", false); d.Allowed || d.Code != "weekly_quota_exceeded" {
		t.Fatalf("leaving the pool brings the USD window back: %+v", d)
	}
}

func TestConfigureOpensLendWindowsForLendingPools(t *testing.T) {
	f := setupPoolFixture(t)
	ctx := context.Background()
	pools := []policyplus.Pool{
		{ID: "plus", Name: "Plus", AuthIndex: "acc-plus", ModelPrefix: "plus", Members: []policyplus.PoolMember{{KeyID: f.alice.ID, SharePercent: share(10)}}},
		{ID: "pro", Name: "Pro 20x", AuthIndex: "acc-pro", ModelPrefix: "pro", LendDuringBurst: true, Members: []policyplus.PoolMember{{KeyID: f.owner.ID}}},
	}
	// Saved straight to the store, as an older plugin version would have left it.
	if err := loadedStore().SavePools(ctx, pools); err != nil {
		t.Fatal(err)
	}
	statePath := loadedConfig().StateDBPath
	if err := configure(mustJSON(t, lifecycleRequest{ConfigYAML: []byte(fmt.Sprintf("state_db_path: %q\nsession_secret: test-secret\n", statePath))})); err != nil {
		t.Fatal(err)
	}
	if err := loadedStore().InsertUsage(ctx, policyplus.UsageEvent{RequestID: "borrow-1", KeyID: f.alice.ID, Model: "gpt-5.5", Provider: "codex",
		RequestedAt: time.Now().Add(time.Second), LatencyMS: 1000, Cost: 1, AuthIndex: "acc-pro"}); err != nil {
		t.Fatal(err)
	}
	keys, err := loadedStore().LendWindowKeys(ctx, "acc-pro", time.Now().Add(-time.Minute).Unix())
	if err != nil || !keys[f.alice.ID] {
		t.Fatalf("a lending pool must have an open lend window after startup: %+v err %v", keys, err)
	}
}
