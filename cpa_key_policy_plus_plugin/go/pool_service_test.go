package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"codexcont/cpa-key-policy-plus-plugin/internal/policyplus"
)

type poolFixture struct {
	alice, bob, owner, carol policyplus.KeyRecord
	reset                    int64
	now                      time.Time
}

func poolTestKey(t *testing.T, id, name, raw string) policyplus.KeyRecord {
	t.Helper()
	key := policyplus.KeyRecord{
		ID:            id,
		Name:          name,
		KeyHash:       "sha256:" + policyplus.SHA256Hex(raw),
		Enabled:       true,
		Preview:       policyplus.HashPreview(policyplus.SHA256Hex(raw)),
		Source:        policyplus.NativeCPASource,
		SourcePresent: true,
	}
	if err := loadedStore().UpsertKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	return key
}

// stubPoolHost answers the host callbacks pools use: the model registry
// (optionally listing pro/ models) and the credential list.
func stubPoolHost(t *testing.T, prefixed bool) {
	t.Helper()
	oldHostCall := hostCall
	t.Cleanup(func() { hostCall = oldHostCall })
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		switch method {
		case methodHostModelsList:
			models := []hostModelListEntry{
				{ID: "gpt-5.5", OwnedBy: "openai", Provider: "codex"},
				{ID: "grok-4", OwnedBy: "xai", Provider: "xai"},
			}
			if prefixed {
				models = append(models, hostModelListEntry{ID: "pro/gpt-5.5", OwnedBy: "openai", Provider: "codex"})
			}
			return json.Marshal(hostModelsListResponse{Models: models})
		case methodHostAuthList:
			return json.Marshal(map[string]any{"files": []any{
				map[string]any{"auth_index": "acc-pro", "name": "codex-pro.json", "provider": "codex", "email": "13812345678@qq.com", "status": "active"},
				map[string]any{"auth_index": "acc-xai", "name": "xai.json", "provider": "xai"},
			}})
		default:
			return nil, fmt.Errorf("unexpected host callback %s", method)
		}
	}
	resetPoolCaches()
}

func setupPoolFixture(t *testing.T) poolFixture {
	t.Helper()
	alice := setupTestState(t)
	resetPoolCaches()
	t.Cleanup(resetPoolCaches)
	// Share limits are the subject here; drop the USD windows and model list.
	alice.DailyLimitUSD, alice.WeeklyLimitUSD, alice.Models = nil, nil, nil
	if err := loadedStore().SaveKeySettings(context.Background(), alice); err != nil {
		t.Fatal(err)
	}
	f := poolFixture{
		alice: alice,
		bob:   poolTestKey(t, "bob-key", "Bob", "sk-bob-secret"),
		owner: poolTestKey(t, "owner-key", "admin", "sk-owner-secret"),
		carol: poolTestKey(t, "carol-key", "Carol", "sk-carol-secret"),
		now:   time.Now(),
	}
	f.reset = f.now.Add(72 * time.Hour).Unix()
	return f
}

func share(v float64) *float64 { return &v }

func savePool(t *testing.T, visibility, prefix string, members ...policyplus.PoolMember) {
	t.Helper()
	pool := policyplus.Pool{ID: "pro", Name: "Pro 20x", AuthIndex: "acc-pro", ModelPrefix: prefix, Visibility: visibility, Members: members}
	if err := loadedStore().SavePools(context.Background(), []policyplus.Pool{pool}); err != nil {
		t.Fatal(err)
	}
	invalidatePoolConfig()
}

func observe(t *testing.T, auth string, used float64, resetAt int64, at time.Time) {
	t.Helper()
	sig := policyplus.QuotaSignal{AuthIndex: auth, Provider: "codex", PlanType: "pro", ObservedAtMS: at.UnixMilli(),
		Weekly: &policyplus.QuotaWindow{Minutes: policyplus.WeeklyWindowMinutes, Used: used, ResetAt: resetAt}}
	if _, err := loadedStore().RecordQuotaSignal(context.Background(), sig); err != nil {
		t.Fatal(err)
	}
	invalidateAccountState(auth)
}

func spend(t *testing.T, key policyplus.KeyRecord, auth string, at time.Time, cost float64) {
	t.Helper()
	event := policyplus.UsageEvent{
		RequestID:    fmt.Sprintf("%s-%d", key.ID, at.UnixNano()),
		KeyID:        key.ID,
		KeyPreview:   key.Preview,
		Model:        "gpt-5.5",
		Provider:     "codex",
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

func TestUsageHandleRecordsCodexQuotaHeadersAndServingAccount(t *testing.T) {
	f := setupPoolFixture(t)
	requested := f.now.Add(-10 * time.Second)
	headers := http.Header{}
	headers.Set("X-Codex-Plan-Type", "pro")
	headers.Set("X-Codex-Primary-Used-Percent", "5")
	headers.Set("X-Codex-Primary-Window-Minutes", "10080")
	headers.Set("X-Codex-Primary-Reset-At", fmt.Sprint(f.reset))
	body, _ := json.Marshal(usageRecord{
		Provider:        "codex",
		Model:           "gpt-5.5",
		APIKey:          f.alice.ID,
		AuthID:          "codex-pro.json",
		AuthIndex:       "acc-pro",
		RequestedAt:     requested,
		TTFT:            time.Second,
		Latency:         3 * time.Second,
		Detail:          usageDetail{InputTokens: 1000, OutputTokens: 1000, TotalTokens: 2000},
		ResponseHeaders: headers,
	})
	if _, err := usageHandle(body); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	accounts, err := loadedStore().QuotaAccounts(ctx)
	if err != nil || len(accounts) != 1 || accounts[0].AuthIndex != "acc-pro" || accounts[0].Weekly == nil || accounts[0].Weekly.Used != 5 || accounts[0].PlanType != "pro" {
		t.Fatalf("quota snapshot = %+v err %v", accounts, err)
	}
	obs, err := loadedStore().QuotaObservations(ctx, "acc-pro", 0)
	if err != nil || len(obs) != 1 || obs[0].ObservedAtMS != requested.Add(time.Second).UnixMilli() || obs[0].CycleResetAt != f.reset {
		t.Fatalf("observation must be stamped when the headers arrived (requested + TTFT): %+v err %v", obs, err)
	}
	costs, err := loadedStore().AuthCostEvents(ctx, "acc-pro", 0, f.now.Add(time.Minute).UnixMilli())
	if err != nil || len(costs) != 1 || costs[0].KeyID != f.alice.ID || costs[0].AtMS != requested.Add(3*time.Second).UnixMilli() {
		t.Fatalf("usage must record the serving account and finish time: %+v err %v", costs, err)
	}
}

func TestPoolShareBlocksMemberOnlyForPoolModelsAndTheirBurstLiftsIt(t *testing.T) {
	f := setupPoolFixture(t)
	stubPoolHost(t, false)
	savePool(t, policyplus.PoolVisibilityAnonymous, "",
		policyplus.PoolMember{KeyID: f.owner.ID},
		policyplus.PoolMember{KeyID: f.alice.ID, SharePercent: share(10)},
		policyplus.PoolMember{KeyID: f.bob.ID, SharePercent: share(20)},
	)
	observe(t, "acc-pro", 10, f.reset, f.now.Add(-2*time.Hour))
	spend(t, f.alice, "acc-pro", f.now.Add(-90*time.Minute), 50)
	spend(t, f.owner, "acc-pro", f.now.Add(-80*time.Minute), 0) // zero cost rows are ignored
	observe(t, "acc-pro", 20, f.reset, f.now.Add(-time.Hour))

	denied := evaluatePolicy(f.alice, "gpt-5.5", false)
	if denied.Allowed || denied.Code != "pool_share_exceeded" || !strings.Contains(denied.Message, "Pro 20x") || !strings.Contains(denied.Message, "北京时间") {
		t.Fatalf("alice used her 10%% share and must be blocked: %+v", denied)
	}
	if d := evaluatePolicy(f.alice, "grok-4", false); !d.Allowed {
		t.Fatalf("models the pool account does not serve stay open: %+v", d)
	}
	if d := evaluatePolicy(f.owner, "gpt-5.5", false); !d.Allowed {
		t.Fatalf("the owner is never share limited: %+v", d)
	}
	if d := evaluatePolicy(f.bob, "gpt-5.5", false); !d.Allowed {
		t.Fatalf("bob has share left: %+v", d)
	}

	burst := func(body map[string]any) managementResponse {
		t.Helper()
		body["pool_id"], body["confirm"] = "pro", true
		return adminPost(t, "/v0/management/plugins/cpa-key-policy-plus/pools/burst", body)
	}
	if start := burst(map[string]any{"key_id": f.alice.ID, "action": "start", "mode": "no_carry"}); start.StatusCode != http.StatusBadRequest || !strings.Contains(string(start.Body), "riders_not_notified") {
		t.Fatalf("no-carry burst must require notifying riders: %d %s", start.StatusCode, start.Body)
	}
	if start := burst(map[string]any{"key_id": f.owner.ID, "action": "start", "mode": "carry"}); start.StatusCode != http.StatusBadRequest || !strings.Contains(string(start.Body), "not_a_share_member") {
		t.Fatalf("the owner has no share to burst past: %d %s", start.StatusCode, start.Body)
	}
	if start := burst(map[string]any{"key_id": f.carol.ID, "action": "start", "mode": "carry"}); start.StatusCode != http.StatusNotFound {
		t.Fatalf("carol is not in the pool: %d %s", start.StatusCode, start.Body)
	}
	if start := burst(map[string]any{"key_id": f.alice.ID, "action": "start", "mode": "carry"}); start.StatusCode != http.StatusOK {
		t.Fatalf("burst start = %d %s", start.StatusCode, start.Body)
	}
	if d := evaluatePolicy(f.alice, "gpt-5.5", false); !d.Allowed {
		t.Fatalf("alice's burst lifts her share limit: %+v", d)
	}
	listed := loadedPools(context.Background())
	if _, on := listed[0].BurstOf(f.bob.ID, time.Now()); on {
		t.Fatal("a burst is per member: bob is not bursting")
	}
	if stop := burst(map[string]any{"key_id": f.alice.ID, "action": "stop"}); stop.StatusCode != http.StatusOK {
		t.Fatalf("burst stop = %d %s", stop.StatusCode, stop.Body)
	}
	if d := evaluatePolicy(f.alice, "gpt-5.5", false); d.Allowed {
		t.Fatalf("share limits return after stopping the burst: %+v", d)
	}
}

func TestRouteModelPinsPoolMembersOnlyWhenCPAListsThePrefixedModel(t *testing.T) {
	f := setupPoolFixture(t)
	stubPoolHost(t, true)
	savePool(t, policyplus.PoolVisibilityAnonymous, "pro",
		policyplus.PoolMember{KeyID: f.owner.ID},
		policyplus.PoolMember{KeyID: f.alice.ID, SharePercent: share(30)},
	)
	route := func(rawKey, model string) modelRouteResponse {
		t.Helper()
		raw, err := routeModel(mustJSON(t, modelRouteRequest{RequestedModel: model, Headers: http.Header{"Authorization": {"Bearer " + rawKey}}}))
		if err != nil {
			t.Fatal(err)
		}
		var resp modelRouteResponse
		unwrapPlusEnvelope(t, raw, &resp)
		return resp
	}
	got := route("sk-alice-secret", "gpt-5.5")
	if !got.Handled || got.TargetKind != "provider" || got.Target != "codex" || got.TargetModel != "pro/gpt-5.5" {
		t.Fatalf("member must be pinned to pro/gpt-5.5: %+v", got)
	}
	if got := route("sk-owner-secret", "gpt-5.5(high)"); got.TargetModel != "pro/gpt-5.5(high)" {
		t.Fatalf("owner is pinned too and the thinking suffix survives: %+v", got)
	}
	if got := route("sk-carol-secret", "gpt-5.5"); got.Handled {
		t.Fatalf("keys outside the pool keep normal rotation: %+v", got)
	}
	if got := route("sk-alice-secret", "grok-4"); got.Handled {
		t.Fatalf("models without a pro/ twin are not rewritten: %+v", got)
	}

	stubPoolHost(t, false)
	if got := route("sk-alice-secret", "gpt-5.5"); got.Handled {
		t.Fatalf("without the CPA credential prefix, routing must not invent pro/ models: %+v", got)
	}
}

func adminPost(t *testing.T, path string, body any) managementResponse {
	t.Helper()
	method := http.MethodPost
	if strings.HasSuffix(path, "/save") {
		method = http.MethodPut
	}
	raw, err := managementHandle(mustJSON(t, managementRequest{Method: method, Path: path, Body: mustJSON(t, body), Headers: http.Header{}}))
	if err != nil {
		t.Fatal(err)
	}
	return decodeManagementResponse(t, raw)
}

func TestAdminPoolsAPISavesConfigurationAndReportsTheLedger(t *testing.T) {
	f := setupPoolFixture(t)
	stubPoolHost(t, true)
	bad := adminPost(t, "/v0/management/plugins/cpa-key-policy-plus/pools/save", map[string]any{"pools": []any{
		map[string]any{"id": "pro", "name": "Pro", "members": []any{
			map[string]any{"key_id": f.alice.ID, "share_percent": 70},
			map[string]any{"key_id": f.bob.ID, "share_percent": 40},
		}},
	}})
	if bad.StatusCode != http.StatusBadRequest || !strings.Contains(string(bad.Body), "invalid_pools") {
		t.Fatalf("overbooked shares must be rejected: %d %s", bad.StatusCode, bad.Body)
	}
	bad = adminPost(t, "/v0/management/plugins/cpa-key-policy-plus/pools/save", map[string]any{"pools": []any{
		map[string]any{"id": "pro", "name": "Pro", "members": []any{map[string]any{"key_id": "ghost"}}},
	}})
	if bad.StatusCode != http.StatusBadRequest || !strings.Contains(string(bad.Body), "unknown_key") {
		t.Fatalf("unknown keys must be rejected: %d %s", bad.StatusCode, bad.Body)
	}
	saved := adminPost(t, "/v0/management/plugins/cpa-key-policy-plus/pools/save", map[string]any{"pools": []any{
		map[string]any{"id": "pro", "name": "Pro 20x", "auth_index": "acc-pro", "model_prefix": "pro", "visibility": "anonymous", "members": []any{
			map[string]any{"key_id": f.owner.ID, "share_percent": nil},
			map[string]any{"key_id": f.alice.ID, "share_percent": 25},
			map[string]any{"key_id": f.bob.ID, "share_percent": 25},
		}},
	}})
	if saved.StatusCode != http.StatusOK {
		t.Fatalf("save = %d %s", saved.StatusCode, saved.Body)
	}
	observe(t, "acc-pro", 10, f.reset, f.now.Add(-3*time.Hour))
	spend(t, f.alice, "acc-pro", f.now.Add(-170*time.Minute), 10)
	spend(t, f.bob, "acc-pro", f.now.Add(-165*time.Minute), 30)
	observe(t, "acc-pro", 12, f.reset, f.now.Add(-150*time.Minute))
	spend(t, f.alice, "acc-pro", f.now.Add(-100*time.Minute), 40)
	observe(t, "acc-pro", 14, f.reset, f.now.Add(-90*time.Minute))
	spend(t, f.carol, "acc-pro", f.now.Add(-30*time.Minute), 5) // outside the pool, still pending

	resp := portalGet(t, "/v0/management/plugins/cpa-key-policy-plus/pools", "", url.Values{"tz_offset": {"-480"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pools = %d %s", resp.StatusCode, resp.Body)
	}
	var body struct {
		Pools []struct {
			ID    string    `json:"id"`
			Rows  []poolRow `json:"rows"`
			Cycle struct {
				Used     float64                     `json:"used_percent"`
				Capacity policyplus.CapacityEstimate `json:"capacity"`
				Status   string                      `json:"status"`
			} `json:"cycle"`
			WeekLine  []policyplus.LinePoint `json:"week_line"`
			MonthLine []policyplus.DayClose  `json:"month_line"`
			Warnings  []string               `json:"warnings"`
		} `json:"pools"`
		Accounts []map[string]any `json:"accounts"`
		Keys     []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("%v: %s", err, resp.Body)
	}
	if len(body.Pools) != 1 || body.Pools[0].Cycle.Used != 14 || body.Pools[0].Cycle.Status != "live" {
		t.Fatalf("pool cycle = %+v", body.Pools)
	}
	pool := body.Pools[0]
	rows := map[string]poolRow{}
	for _, row := range pool.Rows {
		rows[row.KeyID] = row
	}
	if !approxEqual(rows[f.alice.ID].AttributedPP, 0.5+2) || !approxEqual(rows[f.bob.ID].AttributedPP, 1.5) {
		t.Fatalf("attribution alice %.4f bob %.4f", rows[f.alice.ID].AttributedPP, rows[f.bob.ID].AttributedPP)
	}
	if rows[f.carol.ID].Role != "outside" || rows[f.carol.ID].PendingPP <= 0 {
		t.Fatalf("carol spent on the account outside the pool: %+v", rows[f.carol.ID])
	}
	if rows[f.owner.ID].Role != "owner" || rows[f.owner.ID].AvailablePP != nil {
		t.Fatalf("owner row = %+v", rows[f.owner.ID])
	}
	if heat := rows[f.alice.ID].Heat; heat == nil || len(heat.Hours) != 168 || len(heat.Days) != 30 {
		t.Fatalf("each member needs a 7x24 and a 30-day heatmap: %+v", heat)
	}
	if !approxEqual(pool.Cycle.Capacity.USD, 2000) || len(pool.WeekLine) != 1 || len(pool.MonthLine) != 30 {
		t.Fatalf("capacity %+v week %d month %d", pool.Cycle.Capacity, len(pool.WeekLine), len(pool.MonthLine))
	}
	var sawAccount, sawXAI bool
	for _, account := range body.Accounts {
		if account["auth_index"] == "acc-pro" {
			sawAccount = true
			if account["label"] != "13***@qq.com" || account["pool_id"] != "pro" || account["quota_signal"] != true {
				t.Fatalf("account label must be masked and linked to its pool: %+v", account)
			}
		}
		if account["auth_index"] == "acc-xai" {
			// Accounts without a quota signal can back a budget pool.
			sawXAI = true
			if account["provider"] != "xai" || account["quota_signal"] != false {
				t.Fatalf("an xai account is listed as having no quota signal: %+v", account)
			}
		}
	}
	if !sawAccount || !sawXAI || len(body.Keys) != 4 {
		t.Fatalf("accounts %+v keys %d", body.Accounts, len(body.Keys))
	}
}

func TestUserPoolViewFollowsPoolVisibility(t *testing.T) {
	f := setupPoolFixture(t)
	stubPoolHost(t, false)
	members := []policyplus.PoolMember{
		{KeyID: f.owner.ID},
		{KeyID: f.alice.ID, SharePercent: share(40)},
		{KeyID: f.bob.ID, SharePercent: share(40)},
	}
	observe(t, "acc-pro", 1, f.reset, f.now.Add(-2*time.Hour))
	spend(t, f.bob, "acc-pro", f.now.Add(-100*time.Minute), 20)
	observe(t, "acc-pro", 2, f.reset, f.now.Add(-90*time.Minute))
	cookie := plusSessionCookie(t, "sk-alice-secret")
	get := func() map[string]any {
		t.Helper()
		resp := portalGet(t, "/v0/resource/plugins/cpa-key-policy-plus/user/api/pool", cookie, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("user pool = %d %s", resp.StatusCode, resp.Body)
		}
		if strings.Contains(string(resp.Body), f.bob.ID) || strings.Contains(string(resp.Body), f.bob.Preview) {
			t.Fatalf("member view must not leak other keys: %s", resp.Body)
		}
		var body struct {
			Pool map[string]any `json:"pool"`
		}
		if err := json.Unmarshal(resp.Body, &body); err != nil {
			t.Fatal(err)
		}
		return body.Pool
	}
	labels := func(pool map[string]any) []string {
		out := []string{}
		for _, row := range pool["team"].([]any) {
			out = append(out, row.(map[string]any)["label"].(string))
		}
		return out
	}

	savePool(t, policyplus.PoolVisibilityAnonymous, "", members...)
	pool := get()
	if got := strings.Join(labels(pool), ","); got != "车主,你,车友 A" && got != "车主,车友 A,你" {
		t.Fatalf("anonymous labels = %s", got)
	}
	if pool["me"].(map[string]any)["share_percent"].(float64) != 40 {
		t.Fatalf("me = %+v", pool["me"])
	}

	savePool(t, policyplus.PoolVisibilityNamed, "", members...)
	if got := strings.Join(labels(get()), ","); !strings.Contains(got, "Bob") || !strings.Contains(got, "你") {
		t.Fatalf("named labels = %s", got)
	}

	savePool(t, policyplus.PoolVisibilitySelf, "", members...)
	pool = get()
	if got := labels(pool); len(got) != 1 || got[0] != "你" {
		t.Fatalf("self-only view = %v", got)
	}
	if others := pool["others_pp"].(float64); !approxEqual(others, 1) {
		t.Fatalf("hidden riders fold into others_pp (bob 1): %v", others)
	}
	if untracked := pool["untracked_pp"].(float64); !approxEqual(untracked, 1) {
		t.Fatalf("usage from before tracking began is reported apart (start 1): %v", untracked)
	}
	if lent := pool["lent_pp"].(float64); lent != 0 {
		t.Fatalf("nothing was lent: %v", lent)
	}

	carolCookie := plusSessionCookie(t, "sk-carol-secret")
	resp := portalGet(t, "/v0/resource/plugins/cpa-key-policy-plus/user/api/pool", carolCookie, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(resp.Body), `"pool":null`) {
		t.Fatalf("keys outside every pool get pool:null: %s", resp.Body)
	}
}

func TestMemberBurstCarrySettlesWhenTheAccountEntersANewCycle(t *testing.T) {
	f := setupPoolFixture(t)
	stubPoolHost(t, false)
	savePool(t, policyplus.PoolVisibilityAnonymous, "",
		policyplus.PoolMember{KeyID: f.owner.ID},
		policyplus.PoolMember{KeyID: f.alice.ID, SharePercent: share(10)},
		policyplus.PoolMember{KeyID: f.bob.ID, SharePercent: share(20)},
	)
	cycle1 := f.now.Add(time.Hour).Unix()
	observe(t, "acc-pro", 10, cycle1, f.now.Add(-3*time.Hour))
	spend(t, f.alice, "acc-pro", f.now.Add(-150*time.Minute), 50)
	observe(t, "acc-pro", 20, cycle1, f.now.Add(-2*time.Hour))
	start := adminPost(t, "/v0/management/plugins/cpa-key-policy-plus/pools/burst", map[string]any{"pool_id": "pro", "key_id": f.alice.ID, "action": "start", "mode": "carry", "confirm": true})
	if start.StatusCode != http.StatusOK {
		t.Fatalf("burst start = %d %s", start.StatusCode, start.Body)
	}
	var admin struct {
		Pools []struct {
			Rows []poolRow `json:"rows"`
		} `json:"pools"`
	}
	if err := json.Unmarshal(start.Body, &admin); err != nil || len(admin.Pools) != 1 {
		t.Fatalf("burst start body = %s", start.Body)
	}
	for _, row := range admin.Pools[0].Rows {
		if (row.KeyID == f.alice.ID) != (row.Burst != nil) {
			t.Fatalf("only alice's row carries a burst: %+v", row)
		}
	}
	spend(t, f.alice, "acc-pro", f.now.Add(-90*time.Minute), 30)
	observe(t, "acc-pro", 25, cycle1, f.now.Add(-time.Hour))
	// The account resets and the first request of the new cycle is observed.
	cycle2 := f.now.Add(7 * 24 * time.Hour).Unix()
	observe(t, "acc-pro", 0, cycle2, f.now.Add(-time.Minute))

	resp := portalGet(t, "/v0/management/plugins/cpa-key-policy-plus/pools", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pools = %d %s", resp.StatusCode, resp.Body)
	}
	carry, err := loadedStore().PoolCarry(context.Background(), "pro", cycle2)
	if err != nil || !approxEqual(carry[f.alice.ID], -5) || !approxEqual(carry[f.bob.ID], 5) {
		t.Fatalf("alice borrowed 5pp of bob's idle share: %+v err %v", carry, err)
	}
	history, _ := loadedStore().PoolSettlements(context.Background(), "pro", 5)
	if len(history) != 1 || history[0].Status != "settled" || history[0].Mode != "member" {
		t.Fatalf("settlement history = %+v", history)
	}
	var detail policyplus.BurstSettlement
	if err := json.Unmarshal(history[0].Detail, &detail); err != nil || len(detail.Bursts) != 1 || detail.Bursts[0].KeyID != f.alice.ID {
		t.Fatalf("settlement must name alice's burst: %s", history[0].Detail)
	}
	cookie := plusSessionCookie(t, "sk-alice-secret")
	user := portalGet(t, "/v0/resource/plugins/cpa-key-policy-plus/user/api/pool", cookie, nil)
	var body struct {
		Pool struct {
			Me map[string]any `json:"me"`
		} `json:"pool"`
	}
	if err := json.Unmarshal(user.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Pool.Me["burst"] != nil || !approxEqual(body.Pool.Me["available_pp"].(float64), 5) {
		t.Fatalf("next cycle: burst over, alice has 10-5 available: %+v", body.Pool)
	}
}

func TestMemberPageShowsTheirOwnBurst(t *testing.T) {
	f := setupPoolFixture(t)
	stubPoolHost(t, false)
	savePool(t, policyplus.PoolVisibilityNamed, "",
		policyplus.PoolMember{KeyID: f.alice.ID, SharePercent: share(10)},
		policyplus.PoolMember{KeyID: f.bob.ID, SharePercent: share(20)},
	)
	observe(t, "acc-pro", 10, f.reset, f.now.Add(-time.Hour))
	if resp := adminPost(t, "/v0/management/plugins/cpa-key-policy-plus/pools/burst", map[string]any{"pool_id": "pro", "key_id": f.bob.ID, "action": "start", "mode": "no_carry", "notified": true, "confirm": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("burst start = %d %s", resp.StatusCode, resp.Body)
	}
	bob := userPoolView(t, "sk-bob-secret")
	burst, _ := bob["me"].(map[string]any)["burst"].(map[string]any)
	if burst == nil || burst["active"] != true || burst["mode"] != "no_carry" || burst["cycle_reset_at"].(float64) != float64(f.reset) {
		t.Fatalf("bob's page shows his burst: %+v", bob["me"])
	}
	resp := portalGet(t, "/v0/resource/plugins/cpa-key-policy-plus/user/api/pool", plusSessionCookie(t, "sk-alice-secret"), nil)
	if strings.Contains(string(resp.Body), f.bob.ID) {
		t.Fatalf("a member page never carries other key ids: %s", resp.Body)
	}
	alice := userPoolView(t, "sk-alice-secret")
	if alice["me"].(map[string]any)["burst"] != nil {
		t.Fatalf("alice is not bursting: %+v", alice["me"])
	}
}

func TestPoolRoutesAreRegistered(t *testing.T) {
	raw, err := managementRegister()
	if err != nil {
		t.Fatal(err)
	}
	var reg managementRegistrationResponse
	unwrapPlusEnvelope(t, raw, &reg)
	routes := map[string]bool{}
	for _, route := range reg.Routes {
		routes[route.Method+" "+route.Path] = true
	}
	for _, want := range []string{
		"GET /plugins/cpa-key-policy-plus/pools",
		"PUT /plugins/cpa-key-policy-plus/pools/save",
		"POST /plugins/cpa-key-policy-plus/pools/burst",
	} {
		if !routes[want] {
			t.Fatalf("management route %s missing: %#v", want, reg.Routes)
		}
	}
	resources := map[string]bool{}
	for _, resource := range reg.Resources {
		resources[resource.Path] = true
	}
	if !resources["/user/api/pool"] || !resources["/admin/api/pools"] {
		t.Fatalf("pool resources missing: %#v", reg.Resources)
	}
}

func approxEqual(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestAdminPoolsShowsSub2PoolEstimateWhenConfigured(t *testing.T) {
	f := setupPoolFixture(t)
	stubPoolHost(t, false)
	var gotAuth, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotQuery = r.Header.Get("Authorization"), r.URL.RawQuery
		if r.URL.Path != "/api/v1/dashboard" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"data":{"cycle":{"observed_at":"2026-09-27T08:00:00Z","upstream_used_percent":12,"effective_usd_per_percent":18.5,"capacity_lower_usd":1700,"capacity_upper_usd":2000}}}`))
	}))
	defer server.Close()
	state.mu.Lock()
	state.cfg.Sub2PoolURL = server.URL
	state.cfg.Sub2PoolAPIKey = "sub2pool_test"
	state.mu.Unlock()
	pool := policyplus.Pool{ID: "pro", Name: "Pro 20x", AuthIndex: "acc-pro", Sub2PoolAccountID: 3, Members: []policyplus.PoolMember{{KeyID: f.owner.ID}}}
	if err := loadedStore().SavePools(context.Background(), []policyplus.Pool{pool}); err != nil {
		t.Fatal(err)
	}
	invalidatePoolConfig()
	resp := portalGet(t, "/v0/management/plugins/cpa-key-policy-plus/pools", "", nil)
	var body struct {
		Pools []struct {
			Cycle struct {
				Sub2Pool sub2poolView `json:"sub2pool"`
			} `json:"cycle"`
			Sub2PoolAccountID int64 `json:"sub2pool_account_id"`
		} `json:"pools"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil || len(body.Pools) != 1 {
		t.Fatalf("pools = %s err %v", resp.Body, err)
	}
	got := body.Pools[0]
	if got.Sub2PoolAccountID != 3 || !approxEqual(got.Cycle.Sub2Pool.USD, 1850) || got.Cycle.Sub2Pool.Low != 1700 || got.Cycle.Sub2Pool.Used != 12 {
		t.Fatalf("sub2pool estimate = %+v", got)
	}
	if gotAuth != "Bearer sub2pool_test" || gotQuery != "account_id=3" {
		t.Fatalf("sub2pool request auth=%q query=%q", gotAuth, gotQuery)
	}
	if strings.Contains(string(resp.Body), "sub2pool_test") {
		t.Fatal("the Sub2Pool API key must never reach the page")
	}
}

func TestAdminPoolsWarnsWhenThePinnedAccountIsUnavailable(t *testing.T) {
	f := setupPoolFixture(t)
	oldHostCall := hostCall
	t.Cleanup(func() { hostCall = oldHostCall })
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		switch method {
		case methodHostModelsList:
			return json.Marshal(hostModelsListResponse{Models: []hostModelListEntry{{ID: "plus/gpt-5.5", OwnedBy: "openai"}}})
		case methodHostAuthList:
			return json.Marshal(map[string]any{"files": []any{
				map[string]any{"auth_index": "acc-plus", "provider": "codex", "email": "someone@gmail.com", "status": "error", "unavailable": true},
			}})
		}
		return nil, fmt.Errorf("unexpected host callback %s", method)
	}
	resetPoolCaches()
	pool := policyplus.Pool{ID: "plus", Name: "Plus", AuthIndex: "acc-plus", ModelPrefix: "plus", Members: []policyplus.PoolMember{{KeyID: f.bob.ID, SharePercent: share(100)}}}
	if err := loadedStore().SavePools(context.Background(), []policyplus.Pool{pool}); err != nil {
		t.Fatal(err)
	}
	invalidatePoolConfig()
	resp := portalGet(t, "/v0/management/plugins/cpa-key-policy-plus/pools", "", nil)
	if !strings.Contains(string(resp.Body), "当前不可用") || !strings.Contains(string(resp.Body), "直接失败") {
		t.Fatalf("pinned pools on an unavailable account need a warning: %s", resp.Body)
	}
}

func TestHiddenAccountShowsMembersOnlyTheirOwnShare(t *testing.T) {
	f := setupPoolFixture(t)
	stubPoolHost(t, false)
	observe(t, "acc-pro", 1, f.reset, f.now.Add(-2*time.Hour))
	spend(t, f.bob, "acc-pro", f.now.Add(-100*time.Minute), 20)
	observe(t, "acc-pro", 2, f.reset, f.now.Add(-90*time.Minute))
	pool := policyplus.Pool{ID: "pro", Name: "Pro 20x", AuthIndex: "acc-pro", Visibility: policyplus.PoolVisibilityNamed, HideAccount: true, Members: []policyplus.PoolMember{
		{KeyID: f.owner.ID},
		{KeyID: f.alice.ID, SharePercent: share(40)},
		{KeyID: f.bob.ID, SharePercent: share(40)},
	}}
	if err := loadedStore().SavePools(context.Background(), []policyplus.Pool{pool}); err != nil {
		t.Fatal(err)
	}
	invalidatePoolConfig()
	get := func(raw string) map[string]any {
		t.Helper()
		resp := portalGet(t, "/v0/resource/plugins/cpa-key-policy-plus/user/api/pool", plusSessionCookie(t, raw), nil)
		var body struct {
			Pool map[string]any `json:"pool"`
		}
		if resp.StatusCode != http.StatusOK || json.Unmarshal(resp.Body, &body) != nil {
			t.Fatalf("user pool = %d %s", resp.StatusCode, resp.Body)
		}
		return body.Pool
	}

	member := get("sk-alice-secret")
	account := member["account"].(map[string]any)
	if member["account_hidden"] != true || account["used_percent"] != nil || account["reset_at"] == nil {
		t.Fatalf("member account view = %+v", member)
	}
	if team := member["team"].([]any); len(team) != 1 || team[0].(map[string]any)["label"] != "你" {
		t.Fatalf("member team = %+v", team)
	}
	for _, field := range []string{"others_pp", "lent_pp", "untracked_pp"} {
		if member[field].(float64) != 0 {
			t.Fatalf("%s leaks account usage: %v", field, member[field])
		}
	}
	if member["me"].(map[string]any)["share_percent"].(float64) != 40 {
		t.Fatalf("me = %+v", member["me"])
	}

	owner := get("sk-owner-secret")
	if owner["account_hidden"] != false || owner["account"].(map[string]any)["used_percent"] == nil || len(owner["team"].([]any)) != 3 {
		t.Fatalf("the owner keeps the full view: %+v", owner)
	}
	listed, _ := loadedStore().ListPools(context.Background())
	if len(listed) != 1 || !listed[0].HideAccount {
		t.Fatalf("hide_account not stored: %+v", listed)
	}
}
