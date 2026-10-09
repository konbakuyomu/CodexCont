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

// stubProbeHost extends stubPoolHost with the credential and the usage
// endpoint a probe reads. usage returns the HTTP status and body to answer.
func stubProbeHost(t *testing.T, usage func() (int, string)) *int {
	t.Helper()
	stubPoolHost(t, false)
	pools := hostCall
	calls := 0
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		switch method {
		case methodHostAuthGet:
			return json.Marshal(map[string]any{"auth_index": "acc-pro", "json": map[string]any{
				"type": "codex", "access_token": "tok-secret", "account_id": "acct-1",
			}})
		case methodHostHTTPDo:
			calls++
			raw, _ := json.Marshal(payload)
			var req struct {
				Method  string
				URL     string
				Headers map[string][]string
			}
			_ = json.Unmarshal(raw, &req)
			if req.URL != codexUsageURL || req.Method != http.MethodGet ||
				strings.Join(req.Headers["Authorization"], "") != "Bearer tok-secret" ||
				strings.Join(req.Headers["Chatgpt-Account-Id"], "") != "acct-1" {
				return nil, fmt.Errorf("unexpected probe request %s", raw)
			}
			status, body := usage()
			return json.Marshal(map[string]any{"StatusCode": status, "Body": []byte(body)})
		}
		return pools(method, payload)
	}
	resetProbeStatus()
	t.Cleanup(resetProbeStatus)
	return &calls
}

func codexUsageBody(used float64, resetAt int64, limitReached bool) string {
	return fmt.Sprintf(`{"plan_type":"pro","rate_limit":{"allowed":true,"limit_reached":%v,
		"primary_window":{"used_percent":%v,"limit_window_seconds":604800,"reset_after_seconds":1,"reset_at":%d},
		"secondary_window":null}}`, limitReached, used, resetAt)
}

func TestEarlyResetEndsTheBurstAndRestartsShares(t *testing.T) {
	f := setupPoolFixture(t)
	stubPoolHost(t, false)
	savePool(t, policyplus.PoolVisibilityAnonymous, "",
		policyplus.PoolMember{KeyID: f.owner.ID},
		policyplus.PoolMember{KeyID: f.alice.ID, SharePercent: share(10)},
		policyplus.PoolMember{KeyID: f.bob.ID, SharePercent: share(20)},
	)
	cycle1 := f.now.Add(72 * time.Hour).Unix()
	observe(t, "acc-pro", 10, cycle1, f.now.Add(-4*time.Hour))
	spend(t, f.alice, "acc-pro", f.now.Add(-230*time.Minute), 15)
	observe(t, "acc-pro", 15, cycle1, f.now.Add(-220*time.Minute))
	start := adminPost(t, "/v0/management/plugins/cpa-key-policy-plus/pools/burst", map[string]any{"pool_id": "pro", "key_id": f.bob.ID, "action": "start", "mode": "carry", "confirm": true})
	if start.StatusCode != http.StatusOK {
		t.Fatalf("burst start = %d %s", start.StatusCode, start.Body)
	}
	spend(t, f.bob, "acc-pro", f.now.Add(-200*time.Minute), 100)
	observe(t, "acc-pro", 45, cycle1, f.now.Add(-3*time.Hour))
	if d := evaluatePolicy(f.bob, "gpt-5.5", false); !d.Allowed {
		t.Fatalf("bob bursts past his share in the first cycle: %+v", d)
	}

	// A reset credit starts a new 7-day window three days early. Bob keeps
	// going and passes his share again before anyone else sends a request.
	resetAt := f.now.Add(-time.Hour)
	cycle2 := resetAt.Add(7 * 24 * time.Hour).Unix()
	observe(t, "acc-pro", 0, cycle2, resetAt)
	spend(t, f.bob, "acc-pro", f.now.Add(-50*time.Minute), 80)
	observe(t, "acc-pro", 25, cycle2, f.now.Add(-40*time.Minute))

	if d := evaluatePolicy(f.bob, "gpt-5.5", false); d.Allowed || d.Code != "pool_share_exceeded" {
		t.Fatalf("the reset ended bob's burst, so his share applies in the new cycle: %+v", d)
	}
	if d := evaluatePolicy(f.alice, "gpt-5.5", false); !d.Allowed {
		t.Fatalf("alice starts the new cycle with her whole share: %+v", d)
	}
	listed := loadedPools(context.Background())
	if len(listed[0].Bursts) != 0 {
		t.Fatalf("the ended burst is settled and cleared: %+v", listed[0].Bursts)
	}
	history, _ := loadedStore().PoolSettlements(context.Background(), "pro", 5)
	if len(history) != 1 || history[0].CycleResetAt != cycle1 {
		t.Fatalf("the first cycle is settled at the reset: %+v", history)
	}
	var detail policyplus.BurstSettlement
	if err := json.Unmarshal(history[0].Detail, &detail); err != nil || detail.NextCycleResetAt != cycle2 {
		t.Fatalf("carry goes to the cycle the reset started: %s", history[0].Detail)
	}

	resp := adminPost(t, "/v0/management/plugins/cpa-key-policy-plus/pools/probe", map[string]any{"action": "noop"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown probe action = %d", resp.StatusCode)
	}
	view := portalGet(t, "/v0/management/plugins/cpa-key-policy-plus/pools", "", nil)
	var admin struct {
		Pools []struct {
			Cycle struct {
				ResetEarly *earlyReset `json:"reset_early"`
				UsedPct    float64     `json:"used_percent"`
			} `json:"cycle"`
			Resets []earlyReset `json:"resets"`
		} `json:"pools"`
	}
	if err := json.Unmarshal(view.Body, &admin); err != nil || len(admin.Pools) != 1 {
		t.Fatalf("pools body = %s", view.Body)
	}
	reset := admin.Pools[0].Cycle.ResetEarly
	// Codex starts the new window at the reset, to the second.
	if reset == nil || reset.ScheduledAt != cycle1 || reset.AtMS != (cycle2-7*24*3600)*1000 || admin.Pools[0].Cycle.UsedPct != 25 {
		t.Fatalf("the page names the early reset: %+v (body %s)", admin.Pools[0], view.Body)
	}
}

func TestProbeFindsAResetThatNoRequestCouldReveal(t *testing.T) {
	f := setupPoolFixture(t)
	nextReset := f.now.Add(7*24*time.Hour - 10*time.Minute).Unix()
	answer := codexUsageBody(0, nextReset, false)
	calls := stubProbeHost(t, func() (int, string) { return http.StatusOK, answer })
	savePool(t, policyplus.PoolVisibilityAnonymous, "",
		policyplus.PoolMember{KeyID: f.owner.ID},
		policyplus.PoolMember{KeyID: f.alice.ID, SharePercent: share(10)},
	)
	observe(t, "acc-pro", 10, f.reset, f.now.Add(-2*time.Hour))
	spend(t, f.alice, "acc-pro", f.now.Add(-90*time.Minute), 50)
	observe(t, "acc-pro", 20, f.reset, f.now.Add(-time.Hour))
	poolState.mu.Lock()
	poolState.limitHits["acc-pro"] = limitHit{Until: f.reset, At: f.now.Add(-time.Hour)}
	poolState.mu.Unlock()
	if d := evaluatePolicy(f.alice, "gpt-5.5", false); d.Allowed {
		t.Fatalf("alice is over her share before the reset: %+v", d)
	}

	runDueProbes(time.Now())
	if *calls != 1 {
		t.Fatalf("a member held back makes the account due for a probe: %d calls", *calls)
	}
	if d := evaluatePolicy(f.alice, "gpt-5.5", false); !d.Allowed {
		t.Fatalf("the probe found the reset, so alice starts over: %+v", d)
	}
	poolState.mu.Lock()
	_, stillFull := poolState.limitHits["acc-pro"]
	poolState.mu.Unlock()
	if stillFull {
		t.Fatal("a reset clears the remembered usage limit")
	}
	runDueProbes(time.Now())
	if *calls != 1 {
		t.Fatalf("fresh numbers make the next probe wait: %d calls", *calls)
	}

	view := portalGet(t, "/v0/management/plugins/cpa-key-policy-plus/pools", "", nil)
	var admin struct {
		Pools []struct {
			Probe map[string]any `json:"probe"`
		} `json:"pools"`
	}
	if err := json.Unmarshal(view.Body, &admin); err != nil || len(admin.Pools) != 1 || admin.Pools[0].Probe["ok_ms"] == nil || admin.Pools[0].Probe["enabled"] != true {
		t.Fatalf("probe status = %s", view.Body)
	}

	// Switched off, nothing is probed and a manual refresh says why.
	if resp := adminPost(t, "/v0/management/plugins/cpa-key-policy-plus/pools/probe", map[string]any{"action": "disable"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("disable = %d %s", resp.StatusCode, resp.Body)
	}
	resetProbeStatus()
	runDueProbes(time.Now().Add(time.Hour))
	if *calls != 1 {
		t.Fatalf("disabled probes must not run: %d calls", *calls)
	}
	if resp := adminPost(t, "/v0/management/plugins/cpa-key-policy-plus/pools/probe", map[string]any{"action": "refresh", "pool_id": "pro"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("refresh while disabled = %d %s", resp.StatusCode, resp.Body)
	}
}

func TestProbeFailuresBackOffAndNeverShowTheToken(t *testing.T) {
	f := setupPoolFixture(t)
	status := http.StatusUnauthorized
	calls := stubProbeHost(t, func() (int, string) { return status, `{"error":"token expired tok-secret"}` })
	savePool(t, policyplus.PoolVisibilityAnonymous, "", policyplus.PoolMember{KeyID: f.alice.ID, SharePercent: share(50)})
	observe(t, "acc-pro", 10, f.reset, f.now.Add(-time.Hour))

	resp := adminPost(t, "/v0/management/plugins/cpa-key-policy-plus/pools/probe", map[string]any{"action": "refresh", "pool_id": "pro"})
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(resp.Body), "HTTP 401") || strings.Contains(string(resp.Body), "tok-secret") {
		t.Fatalf("refresh failure = %d %s", resp.StatusCode, resp.Body)
	}
	target := probeTarget{pool: loadedPools(context.Background())[0], authIndex: "acc-pro", provider: "codex"}
	ctx, store := context.Background(), loadedStore()
	now := time.Now()
	// Nobody is held back: retries back off from the idle ten minutes.
	if probeDue(ctx, store, target, now.Add(3*time.Minute)) {
		t.Fatal("one failure on a quiet account waits ten minutes")
	}
	if !probeDue(ctx, store, target, now.Add(11*time.Minute)) {
		t.Fatal("after ten minutes the probe is retried")
	}
	_ = probeAccount(ctx, store, "acc-pro", "codex")
	if probeDue(ctx, store, target, now.Add(11*time.Minute)) || !probeDue(ctx, store, target, now.Add(21*time.Minute)) {
		t.Fatal("two failures wait twenty minutes")
	}
	// Someone held back: retries back off from two minutes.
	spend(t, f.alice, "acc-pro", now.Add(-30*time.Minute), 400)
	observe(t, "acc-pro", 70, f.reset, now.Add(-20*time.Minute))
	resetProbeStatus()
	_ = probeAccount(ctx, store, "acc-pro", "codex")
	if probeDue(ctx, store, target, now.Add(time.Minute)) || !probeDue(ctx, store, target, now.Add(3*time.Minute)) {
		t.Fatal("a held-back member makes retries come every two minutes")
	}
	resetProbeStatus()
	status = http.StatusOK
	*calls = 0
	if err := probeAccount(ctx, store, "acc-pro", "codex"); err == nil {
		t.Fatal("a 200 without usage numbers is still a failure")
	}
	if *calls != 1 {
		t.Fatalf("calls = %d", *calls)
	}

	// Probes are counted from the latest numbers, whether a request or a
	// probe brought them; a held-back member shortens the wait to two minutes.
	resetProbeStatus()
	observe(t, "acc-pro", 71, f.reset, now.Add(-time.Minute))
	if probeDue(ctx, store, target, now) {
		t.Fatal("numbers a minute old are fresh enough")
	}
	if !probeDue(ctx, store, target, now.Add(2*time.Minute)) {
		t.Fatal("three minutes without numbers while a member is held back: probe")
	}
}

func TestUsageHandleRecordsClaudeQuotaHeaders(t *testing.T) {
	f := setupPoolFixture(t)
	headers := http.Header{}
	headers.Set("Anthropic-Ratelimit-Unified-Status", "allowed")
	headers.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.42")
	headers.Set("Anthropic-Ratelimit-Unified-7d-Reset", fmt.Sprint(f.reset))
	headers.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.1")
	headers.Set("Anthropic-Ratelimit-Unified-5h-Reset", fmt.Sprint(f.now.Add(2*time.Hour).Unix()))
	body, _ := json.Marshal(usageRecord{
		Provider:        "claude",
		Model:           "claude-sonnet-4-5",
		APIKey:          f.alice.ID,
		AuthIndex:       "acc-claude",
		RequestedAt:     f.now.Add(-10 * time.Second),
		Latency:         2 * time.Second,
		ResponseHeaders: headers,
	})
	if _, err := usageHandle(body); err != nil {
		t.Fatal(err)
	}
	obs, err := loadedStore().QuotaObservations(context.Background(), "acc-claude", 0)
	if err != nil || len(obs) != 1 || obs[0].UsedPercent != 42 || obs[0].CycleResetAt != f.reset {
		t.Fatalf("claude weekly utilization becomes the cycle's percent: %+v err %v", obs, err)
	}
	if got := modelProvider("claude-sonnet-4-5"); got != "claude" {
		t.Fatalf("claude models belong to claude accounts: %q", got)
	}
	if !providerOwnsModel("claude", "anthropic") || providerLabel("claude") != "Claude" {
		t.Fatal("anthropic models are served by claude accounts")
	}
}
