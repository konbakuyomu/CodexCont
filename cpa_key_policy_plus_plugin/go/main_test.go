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

func setupTestState(t *testing.T) policyplus.KeyRecord {
	t.Helper()
	state.mu.Lock()
	state.cfg = policyplus.DefaultConfig()
	state.cfg.StateDBPath = filepath.Join(t.TempDir(), "policyplus.sqlite")
	state.cfg.SessionSecret = "test-secret"
	state.cfg.ExclusiveAuth = true
	state.keyState = policyplus.KeyPolicyState{}
	state.keyStatePath = ""
	state.rpmBuckets = map[string][]time.Time{}
	state.concurrency = map[string]int{}
	old := state.store
	state.store = nil
	state.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	store, err := policyplus.OpenStore(state.cfg.StateDBPath)
	if err != nil {
		t.Fatal(err)
	}
	key := policyplus.KeyRecord{
		ID:                "alice-key",
		Name:              "Alice",
		KeyHash:           "sha256:" + policyplus.SHA256Hex("cpa_alice_secret"),
		Enabled:           true,
		Preview:           "cpa_ali...cret",
		RPM:               10,
		Concurrency:       2,
		MaxActiveSessions: 1,
		Models:            []string{"gpt-5.5"},
		DailyLimitUSD:     floatPtr(10),
		WeeklyLimitUSD:    floatPtr(50),
	}
	if err := store.UpsertKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.store = store
	state.mu.Unlock()
	t.Cleanup(func() {
		state.mu.Lock()
		if state.store != nil {
			_ = state.store.Close()
		}
		state.store = nil
		state.mu.Unlock()
	})
	return key
}

func TestPluginRegistrationIsPolicyPlusExclusiveAuth(t *testing.T) {
	setupTestState(t)
	raw, err := okEnvelope(pluginRegistration())
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var reg registration
	if err := json.Unmarshal(env.Result, &reg); err != nil {
		t.Fatal(err)
	}
	if reg.Metadata.Name != pluginID {
		t.Fatalf("plugin name = %s", reg.Metadata.Name)
	}
	if !reg.Capabilities.FrontendAuthProvider || !reg.Capabilities.FrontendAuthProviderExclusive {
		t.Fatalf("frontend auth capabilities = %#v", reg.Capabilities)
	}
	if reg.Capabilities.ModelRouter || reg.Capabilities.Executor {
		t.Fatalf("plus must not steal Governor/CodexCont routing: %#v", reg.Capabilities)
	}
}

func TestFrontendAuthEnforcesActiveSessionLimit(t *testing.T) {
	setupTestState(t)
	req := frontendAuthRequest{
		Headers: http.Header{"Authorization": []string{"Bearer cpa_alice_secret"}},
		Body:    []byte(`{"model":"gpt-5.5","prompt_cache_key":"window-a"}`),
	}
	if !authOK(t, req) {
		t.Fatal("first session should authenticate")
	}
	req.Body = []byte(`{"model":"gpt-5.5","prompt_cache_key":"window-a"}`)
	if !authOK(t, req) {
		t.Fatal("same session should refresh and authenticate")
	}
	req.Body = []byte(`{"model":"gpt-5.5","prompt_cache_key":"window-b"}`)
	if authOK(t, req) {
		t.Fatal("second active session should be rejected")
	}
}

func TestFrontendAuthAllowsMissingSessionAndAudits(t *testing.T) {
	setupTestState(t)
	req := frontendAuthRequest{
		Headers: http.Header{"Authorization": []string{"Bearer cpa_alice_secret"}},
		Body:    []byte(`{"model":"gpt-5.5"}`),
	}
	if !authOK(t, req) {
		t.Fatal("missing session identity should not be rejected in v1")
	}
}

func TestAdminSaveKeysPersistsUnifiedLimits(t *testing.T) {
	setupTestState(t)
	enabled := false
	body := map[string]any{"keys": []map[string]any{{
		"id":                  "alice-key",
		"name":                "Alice Plus",
		"enabled":             enabled,
		"rpm":                 5,
		"concurrency":         1,
		"max_active_sessions": 3,
		"models":              []string{"gpt-5.4"},
		"five_hour_usd":       1.25,
		"daily_usd":           2.5,
		"weekly_usd":          7.5,
		"monthly_usd":         20.0,
	}}}
	rawBody, _ := json.Marshal(body)
	raw, err := adminSaveKeys(managementRequest{Body: rawBody})
	if err != nil {
		t.Fatal(err)
	}
	bodyBytes := decodeManagementBody(t, raw)
	if !strings.Contains(string(bodyBytes), "Alice Plus") {
		t.Fatalf("save response missing updated key: %s", bodyBytes)
	}
	store := loadedStore()
	keys, err := store.ListKeys(context.Background())
	if err != nil || len(keys) != 1 {
		t.Fatalf("ListKeys err=%v keys=%#v", err, keys)
	}
	got := keys[0]
	if got.Enabled || got.RPM != 5 || got.Concurrency != 1 || got.MaxActiveSessions != 3 {
		t.Fatalf("updated key = %#v", got)
	}
	if got.FiveHourUSD == nil || *got.FiveHourUSD != 1.25 || got.MonthlyLimitUSD == nil || *got.MonthlyLimitUSD != 20 {
		t.Fatalf("limits not persisted: %#v", got)
	}
}

func TestAdminCreateKeyReturnsRawKeyOnlyOnce(t *testing.T) {
	setupTestState(t)
	raw, err := adminCreateKey(managementRequest{Body: []byte(`{"name":"Bob"}`)})
	if err != nil {
		t.Fatal(err)
	}
	bodyBytes := decodeManagementBody(t, raw)
	var resp struct {
		OK     bool   `json:"ok"`
		RawKey string `json:"raw_key"`
	}
	if err := json.Unmarshal(bodyBytes, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || !strings.HasPrefix(resp.RawKey, "cpa_") {
		t.Fatalf("create response = %s", bodyBytes)
	}
	if strings.Contains(string(bodyBytes), policyplus.SHA256Hex(resp.RawKey)) {
		t.Fatalf("response leaked full hash: %s", bodyBytes)
	}
}

func TestUserHTMLUsesPolicyPlusHeader(t *testing.T) {
	html := userHTML()
	if !strings.Contains(html, "X-CPA-Key-Policy-Plus-Key") {
		t.Fatal("user page must send the Plus login header")
	}
	if strings.Contains(html, "X-CPA-Governor-Key") {
		t.Fatal("user page should not keep the Governor login header")
	}
}

func TestAdminHTMLHasRenderedSharedCSS(t *testing.T) {
	html := adminHTML()
	if strings.Contains(html, "{{CSS}}") || strings.Contains(html, "{{SHARED_CSS}}") {
		t.Fatalf("admin html still has css placeholder")
	}
	if !strings.Contains(html, "--panel") || !strings.Contains(html, "CPA Key Policy+") {
		t.Fatal("admin html should embed shared style and key policy UI")
	}
}

func TestSessionInvalidatedWhenKeyHashChanges(t *testing.T) {
	key := setupTestState(t)
	req := managementRequest{Headers: http.Header{"X-CPA-Key-Policy-Plus-Key": []string{"cpa_alice_secret"}}}
	raw, err := userSession(req)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var resp managementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	cookie := strings.Join(resp.Headers.Values("Set-Cookie"), "; ")
	if cookie == "" {
		t.Fatal("session did not set cookie")
	}
	if _, ok := keyFromSession(managementRequest{Headers: http.Header{"Cookie": []string{cookie}}}); !ok {
		t.Fatal("fresh session should resolve")
	}
	key.KeyHash = "sha256:" + policyplus.SHA256Hex("cpa_rotated_secret")
	if err := loadedStore().UpsertKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, ok := keyFromSession(managementRequest{Headers: http.Header{"Cookie": []string{cookie}}}); ok {
		t.Fatal("old session should not survive key rotation")
	}
}

func authOK(t *testing.T, req frontendAuthRequest) bool {
	t.Helper()
	rawReq, _ := json.Marshal(req)
	raw, err := frontendAuth(rawReq)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var resp frontendAuthResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Authenticated
}

func decodeManagementBody(t *testing.T, raw []byte) []byte {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var resp managementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Body
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
