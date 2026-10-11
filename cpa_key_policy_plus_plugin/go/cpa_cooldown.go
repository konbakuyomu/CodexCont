package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"codexcont/cpa-key-policy-plus-plugin/internal/policyplus"
	"gopkg.in/yaml.v3"
)

// CPA cools a credential that answers "usage limit reached" down until the
// reset time the error names. A reset credit or an early reset makes that
// cooldown stale: the account takes requests again, but CPA keeps skipping it
// until the old reset time. When a probe finds such an account open, the
// plugin asks CPA to lift the cooldown, as CPA's own "reset quota" management
// action does. That takes CPA's management key, which the operator puts in
// cpa_management_key beside the plugin's database; without the file the
// plugin only reports the stale cooldown on the admin page.

const (
	cpaCooldownKeyFile = "cpa_management_key"
	// cpaCooldownRetry spaces out attempts for one account.
	cpaCooldownRetry = 10 * time.Minute
	// cpaCooldownSlack ignores cooldowns about to end anyway.
	cpaCooldownSlack = 2 * time.Minute
	// cpaCooldownShown keeps a lifted cooldown on the admin page for a while.
	cpaCooldownShown = time.Hour
)

type cpaCooldownStatus struct {
	AttemptAt time.Time
	ClearedAt time.Time
	Error     string
}

var cpaCooldowns = struct {
	sync.Mutex
	accounts map[string]*cpaCooldownStatus
	// refused is the fingerprint of a key CPA turned down; it is not sent
	// again, so a wrong key cannot get the host banned for failed attempts.
	refused string
}{accounts: map[string]*cpaCooldownStatus{}}

func resetCPACooldowns() {
	cpaCooldowns.Lock()
	defer cpaCooldowns.Unlock()
	cpaCooldowns.accounts = map[string]*cpaCooldownStatus{}
	cpaCooldowns.refused = ""
}

// cpaManagementKey reads the operator's key file. The fingerprint tells keys
// apart without keeping the key around.
func cpaManagementKey() (key, fingerprint string) {
	state.mu.Lock()
	path := filepath.Join(filepath.Dir(state.cfg.StateDBPath), cpaCooldownKeyFile)
	state.mu.Unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	key = strings.TrimSpace(string(raw))
	if key == "" {
		return "", ""
	}
	return key, policyplus.SHA256Hex(key)[:16]
}

// cpaManagementURL is CPA's reset-quota endpoint on this host, at the port
// CPA's own config names.
func cpaManagementURL() string {
	port := 8317
	state.mu.Lock()
	path := state.cfg.NativeKeysConfigPath
	state.mu.Unlock()
	if raw, err := os.ReadFile(path); err == nil {
		var body struct {
			Port int `yaml:"port"`
		}
		if yaml.Unmarshal(raw, &body) == nil && body.Port > 0 {
			port = body.Port
		}
	}
	return fmt.Sprintf("http://127.0.0.1:%d/v0/management/reset-quota", port)
}

// cpaManagementPost talks to CPA directly, never through the outbound proxy.
var cpaManagementPost = func(url, key string, body []byte) (int, error) {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, nil
}

// cpaStaleCooldown is the time CPA cools an account until, when that is more
// than a moment away.
func cpaStaleCooldown(authIndex string, now time.Time) (time.Time, bool) {
	for _, host := range hostAuthAccounts() {
		if host.AuthIndex == authIndex && !host.Disabled && host.Unavailable && host.NextRetryAfter.After(now.Add(cpaCooldownSlack)) {
			return host.NextRetryAfter, true
		}
	}
	return time.Time{}, false
}

// quotaOpen reports an account whose usage leaves room in every window.
func quotaOpen(limitReached bool, windows ...*policyplus.QuotaWindow) bool {
	if limitReached || len(windows) == 0 || windows[0] == nil {
		return false
	}
	for _, window := range windows {
		if window != nil && window.Used >= 100 {
			return false
		}
	}
	return true
}

func refreshHostAuths() {
	poolState.mu.Lock()
	poolState.hostAuthsAt = time.Time{}
	poolState.mu.Unlock()
}

// liftStaleCPACooldown runs after a probe read an account's usage with CPA's
// credential: the account is open, so a cooldown CPA still holds is stale.
func liftStaleCPACooldown(ctx context.Context, store *policyplus.Store, authIndex string, sig policyplus.QuotaSignal, now time.Time) {
	if !quotaOpen(sig.LimitReached, sig.Weekly, sig.Short) {
		return
	}
	refreshHostAuths()
	until, stale := cpaStaleCooldown(authIndex, now)
	if !stale {
		return
	}
	key, fingerprint := cpaManagementKey()
	cpaCooldowns.Lock()
	status := cpaCooldowns.accounts[authIndex]
	if status == nil {
		status = &cpaCooldownStatus{}
		cpaCooldowns.accounts[authIndex] = status
	}
	if key == "" || fingerprint == cpaCooldowns.refused || now.Sub(status.AttemptAt) < cpaCooldownRetry {
		cpaCooldowns.Unlock()
		return
	}
	status.AttemptAt = now
	cpaCooldowns.Unlock()

	body, _ := json.Marshal(map[string]string{"auth_index": authIndex})
	code, err := cpaManagementPost(cpaManagementURL(), key, body)

	cpaCooldowns.Lock()
	switch {
	case err != nil:
		status.Error = "连不上 CPA 管理接口"
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		status.Error = fmt.Sprintf("CPA 不接受这把管理密钥（HTTP %d），换密钥文件后才会再试", code)
		cpaCooldowns.refused = fingerprint
	case code != http.StatusOK:
		status.Error = fmt.Sprintf("CPA 管理接口返回 HTTP %d", code)
	default:
		status.Error = ""
		status.ClearedAt = now
	}
	cleared := status.Error == ""
	cpaCooldowns.Unlock()
	refreshHostAuths()
	if store != nil {
		action := "cpa_cooldown_cleared"
		if !cleared {
			action = "cpa_cooldown_clear_failed"
		}
		_ = store.Audit(ctx, "system", action, authIndex, map[string]any{"cpa_until": until.Unix(), "http_status": code})
	}
}

// cpaCooldownView tells the admin page about a cooldown CPA holds on an
// account the latest numbers show open, and about one lifted lately.
func cpaCooldownView(ctx context.Context, store *policyplus.Store, authIndex string, now time.Time) map[string]any {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" || store == nil {
		return nil
	}
	key, fingerprint := cpaManagementKey()
	view := map[string]any{"auto": key != "" && fingerprint != cpaRefusedFingerprint()}
	shown := false
	if latest := accountStateFor(ctx, store, authIndex, now).Latest; latest != nil && quotaOpen(latest.LimitReached, latest.Weekly, latest.Short) {
		if until, stale := cpaStaleCooldown(authIndex, now); stale {
			view["until_ms"] = until.UnixMilli()
			shown = true
		}
	}
	cpaCooldowns.Lock()
	if status := cpaCooldowns.accounts[authIndex]; status != nil {
		if !status.ClearedAt.IsZero() && now.Sub(status.ClearedAt) < cpaCooldownShown {
			view["cleared_ms"] = status.ClearedAt.UnixMilli()
			shown = true
		}
		if status.Error != "" && shown {
			view["error"] = status.Error
		}
	}
	cpaCooldowns.Unlock()
	if !shown {
		return nil
	}
	return view
}

func cpaRefusedFingerprint() string {
	cpaCooldowns.Lock()
	defer cpaCooldowns.Unlock()
	return cpaCooldowns.refused
}
