package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codexcont/cpa-key-policy-plus-plugin/internal/policyplus"
)

type cooldownHarness struct {
	cooling bool
	until   time.Time
	posts   []string
	answer  int
}

// stubCPACooldown answers probes with usage, lists acc-pro as cooled down by
// CPA while h.cooling, and records reset-quota calls.
func stubCPACooldown(t *testing.T, usage string) *cooldownHarness {
	t.Helper()
	h := &cooldownHarness{cooling: true, until: time.Now().Add(72 * time.Hour), answer: http.StatusOK}
	stubProbeHost(t, func() (int, string) { return http.StatusOK, usage })
	inner := hostCall
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		if method == methodHostAuthList {
			file := map[string]any{"auth_index": "acc-pro", "name": "codex-pro.json", "provider": "codex", "status": "active"}
			if h.cooling {
				file["status"], file["unavailable"], file["next_retry_after"] = "error", true, h.until.Format(time.RFC3339Nano)
			}
			return json.Marshal(map[string]any{"files": []any{file}})
		}
		return inner(method, payload)
	}
	oldPost := cpaManagementPost
	t.Cleanup(func() { cpaManagementPost = oldPost })
	cpaManagementPost = func(url, key string, body []byte) (int, error) {
		h.posts = append(h.posts, url+" "+key+" "+string(body))
		if h.answer == http.StatusOK {
			h.cooling = false
		}
		return h.answer, nil
	}
	resetPoolCaches()
	return h
}

func writeManagementKey(t *testing.T, key string) {
	t.Helper()
	state.mu.Lock()
	dir := filepath.Dir(state.cfg.StateDBPath)
	state.mu.Unlock()
	if err := os.WriteFile(filepath.Join(dir, cpaCooldownKeyFile), []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func cooldownView(t *testing.T) map[string]any {
	t.Helper()
	view := probeView(context.Background(), loadedStore(), loadedPools(context.Background())[0])
	cd, _ := view["cpa_cooldown"].(map[string]any)
	return cd
}

func proPoolWithAlice(t *testing.T, f poolFixture) {
	t.Helper()
	savePool(t, policyplus.PoolVisibilityAnonymous, "",
		policyplus.PoolMember{KeyID: f.owner.ID},
		policyplus.PoolMember{KeyID: f.alice.ID, SharePercent: share(10)},
	)
	observe(t, "acc-pro", 100, f.reset, f.now.Add(-2*time.Hour))
}

func TestProbeLiftsCPAsStaleCooldownAfterAReset(t *testing.T) {
	f := setupPoolFixture(t)
	h := stubCPACooldown(t, codexUsageBody(0, f.now.Add(7*24*time.Hour).Unix(), false))
	proPoolWithAlice(t, f)
	writeManagementKey(t, "mgmt-test-key")
	if err := refreshAccountQuota(context.Background(), loadedStore(), loadedPools(context.Background())[0]); err != nil {
		t.Fatal(err)
	}
	if len(h.posts) != 1 || !strings.HasPrefix(h.posts[0], "http://127.0.0.1:") || !strings.Contains(h.posts[0], "/v0/management/reset-quota mgmt-test-key ") ||
		!strings.Contains(h.posts[0], `{"auth_index":"acc-pro"}`) {
		t.Fatalf("one reset-quota call for the account: %q", h.posts)
	}
	if cd := cooldownView(t); cd == nil || cd["cleared_ms"] == nil || cd["until_ms"] != nil || cd["error"] != nil {
		t.Fatalf("the admin page notes the lifted cooldown: %+v", cd)
	}
	if count, _ := loadedStore().AuditCount(context.Background(), "cpa_cooldown_cleared"); count != 1 {
		t.Fatalf("the lift is audited, count=%d", count)
	}
}

func TestStaleCooldownIsOnlyReportedWithoutAKeyFile(t *testing.T) {
	f := setupPoolFixture(t)
	h := stubCPACooldown(t, codexUsageBody(0, f.now.Add(7*24*time.Hour).Unix(), false))
	proPoolWithAlice(t, f)
	if err := refreshAccountQuota(context.Background(), loadedStore(), loadedPools(context.Background())[0]); err != nil {
		t.Fatal(err)
	}
	if len(h.posts) != 0 {
		t.Fatalf("no key, no call: %q", h.posts)
	}
	cd := cooldownView(t)
	if cd == nil || cd["auto"] != false || cd["until_ms"] != h.until.UnixMilli() {
		t.Fatalf("the admin page names the stale cooldown and that it is not lifted automatically: %+v", cd)
	}
}

func TestRefusedKeyIsNotSentAgain(t *testing.T) {
	f := setupPoolFixture(t)
	h := stubCPACooldown(t, codexUsageBody(0, f.now.Add(7*24*time.Hour).Unix(), false))
	h.answer = http.StatusUnauthorized
	proPoolWithAlice(t, f)
	writeManagementKey(t, "wrong-key")
	pool := loadedPools(context.Background())[0]
	if err := refreshAccountQuota(context.Background(), loadedStore(), pool); err != nil {
		t.Fatal(err)
	}
	// Later probes, past the retry interval, keep the refused key to themselves.
	cpaCooldowns.Lock()
	cpaCooldowns.accounts["acc-pro"].AttemptAt = time.Now().Add(-time.Hour)
	cpaCooldowns.Unlock()
	resetProbeStatus()
	if err := refreshAccountQuota(context.Background(), loadedStore(), pool); err != nil {
		t.Fatal(err)
	}
	if len(h.posts) != 1 {
		t.Fatalf("a refused key is sent once: %q", h.posts)
	}
	if cd := cooldownView(t); cd == nil || cd["auto"] != false || !strings.Contains(cd["error"].(string), "HTTP 401") {
		t.Fatalf("the admin page says the key was refused: %+v", cd)
	}
	// A new key file is tried.
	writeManagementKey(t, "right-key")
	h.answer = http.StatusOK
	cpaCooldowns.Lock()
	cpaCooldowns.accounts["acc-pro"].AttemptAt = time.Time{}
	cpaCooldowns.Unlock()
	resetProbeStatus()
	if err := refreshAccountQuota(context.Background(), loadedStore(), pool); err != nil {
		t.Fatal(err)
	}
	if len(h.posts) != 2 || !strings.Contains(h.posts[1], " right-key ") {
		t.Fatalf("a changed key is tried: %q", h.posts)
	}
}

func TestCooldownStaysWhileTheAccountIsStillFull(t *testing.T) {
	f := setupPoolFixture(t)
	h := stubCPACooldown(t, codexUsageBody(100, f.reset, true))
	proPoolWithAlice(t, f)
	writeManagementKey(t, "mgmt-test-key")
	if err := refreshAccountQuota(context.Background(), loadedStore(), loadedPools(context.Background())[0]); err != nil {
		t.Fatal(err)
	}
	if len(h.posts) != 0 {
		t.Fatalf("a full account keeps CPA's cooldown: %q", h.posts)
	}
	if cd := cooldownView(t); cd != nil {
		t.Fatalf("nothing to report for a cooldown that is right: %+v", cd)
	}
}
