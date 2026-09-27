package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codexcont/cpa-key-policy-plus-plugin/internal/policyplus"
	_ "modernc.org/sqlite"
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
	state.priceBook = policyplus.PriceBook{
		Source: policyplus.CostSourceCPAMPCachedPriceBook,
		Prices: map[string]policyplus.ModelPrice{
			"gpt-5.5": {Model: "gpt-5.5", InputPerMillion: 1, OutputPerMillion: 10},
		},
	}
	state.priceBookChecked = time.Now()
	state.lastConfigError = ""
	state.lastConfigErrorAt = time.Time{}
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
		KeyHash:           "sha256:" + policyplus.SHA256Hex("sk-alice-secret"),
		Enabled:           true,
		Preview:           policyplus.HashPreview(policyplus.SHA256Hex("sk-alice-secret")),
		Source:            policyplus.NativeCPASource,
		SourcePresent:     true,
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

func createCPAMPPriceDB(t *testing.T, prices map[string]policyplus.ModelPrice) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table model_prices (
		model text primary key,
		prompt_per_1m real not null,
		completion_per_1m real not null,
		cache_per_1m real not null,
		cache_read_per_1m real not null default 0,
		cache_creation_per_1m real not null default 0,
		source text,
		source_model_id text,
		raw_json text,
		updated_at_ms integer not null,
		synced_at_ms integer
	)`); err != nil {
		t.Fatal(err)
	}
	for model, price := range prices {
		if _, err := db.Exec(`insert into model_prices(model, prompt_per_1m, completion_per_1m, cache_per_1m, cache_read_per_1m, cache_creation_per_1m, source, source_model_id, raw_json, updated_at_ms, synced_at_ms)
			values(?, ?, ?, ?, ?, ?, 'test', ?, '{}', ?, ?)`,
			model, price.InputPerMillion, price.OutputPerMillion, price.CachePerMillion,
			price.CacheReadPerMillion, price.CacheCreationPerMillion, model, time.Now().UnixMilli(), time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func unwrapPlusEnvelope(t *testing.T, raw []byte, out any) {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("envelope error: %#v", env.Error)
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		t.Fatal(err)
	}
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
	if reg.SchemaVersion != schemaVersion || reg.SchemaVersion < 2 || !reg.Capabilities.RequestInterceptor || !reg.Capabilities.Executor {
		t.Fatalf("plus must expose schema-v2 request termination instead of model-route fake executor denials: %#v", reg)
	}
	if !reg.Capabilities.ModelRouter {
		t.Fatalf("model routing pins carpool members to their pool account: %#v", reg.Capabilities)
	}
	if reg.Capabilities.ExecutorModelScope != executorModelScopeBoth {
		t.Fatalf("executor model scope = %q", reg.Capabilities.ExecutorModelScope)
	}
	if len(reg.Capabilities.ExecutorInputFormats) != 1 || reg.Capabilities.ExecutorInputFormats[0] != executorFormatOpenAIResponse {
		t.Fatalf("executor input formats = %#v", reg.Capabilities.ExecutorInputFormats)
	}
	if len(reg.Capabilities.ExecutorOutputFormats) != 1 || reg.Capabilities.ExecutorOutputFormats[0] != executorFormatOpenAIResponse {
		t.Fatalf("executor output formats = %#v", reg.Capabilities.ExecutorOutputFormats)
	}
}

func TestLifecycleConfigErrorKeepsPlusRegistrationAndPreviousStore(t *testing.T) {
	setupTestState(t)
	oldStore := loadedStore()
	oldCfg := loadedConfig()
	if oldStore == nil {
		t.Fatal("expected initial store")
	}
	badParent := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(badParent, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	badPath := filepath.Join(badParent, "policyplus.sqlite")
	raw, err := handleMethod(methodPluginReconfigure, mustJSON(t, lifecycleRequest{ConfigYAML: []byte("enabled: true\nexclusive_auth: false\nstate_db_path: " + badPath + "\n")}))
	if err != nil {
		t.Fatal(err)
	}
	var reg registration
	unwrapPlusEnvelope(t, raw, &reg)
	if reg.Metadata.Name != pluginID || !reg.Capabilities.ManagementAPI || !reg.Capabilities.FrontendAuthProvider {
		t.Fatalf("failed reconfigure must still return full registration: %#v", reg)
	}
	if loadedStore() != oldStore {
		t.Fatal("failed reconfigure replaced the previous store")
	}
	cfg := loadedConfig()
	if cfg.StateDBPath != oldCfg.StateDBPath || cfg.ExclusiveAuth != oldCfg.ExclusiveAuth {
		t.Fatalf("failed reconfigure changed active config: %#v old=%#v", cfg, oldCfg)
	}
	status := plusStatusPayload()
	if status["store_available"] != true || !strings.Contains(fmt.Sprint(status["last_config_error"]), "not-a-dir") {
		t.Fatalf("status missing config error or previous store availability: %#v", status)
	}
}

func TestPlusLifecycleConfigParseErrorStillReturnsRegistration(t *testing.T) {
	setupTestState(t)
	oldStore := loadedStore()
	raw, err := handleMethod(methodPluginReconfigure, []byte(`{"config_yaml":`))
	if err != nil {
		t.Fatal(err)
	}
	var reg registration
	unwrapPlusEnvelope(t, raw, &reg)
	if reg.Metadata.Name != pluginID || !reg.Capabilities.ManagementAPI || !reg.Capabilities.FrontendAuthProvider {
		t.Fatalf("parse error must still return full registration: %#v", reg)
	}
	if loadedStore() != oldStore {
		t.Fatal("parse error replaced the previous store")
	}
	status := plusStatusPayload()
	if status["store_available"] != true || fmt.Sprint(status["last_config_error"]) == "" {
		t.Fatalf("status missing parse error: %#v", status)
	}
}

func TestInitialPlusLifecycleConfigErrorStillRegistersAndFailsClosed(t *testing.T) {
	state.mu.Lock()
	if state.store != nil {
		_ = state.store.Close()
	}
	state.cfg = policyplus.DefaultConfig()
	state.store = nil
	state.keyState = policyplus.KeyPolicyState{}
	state.keyStatePath = ""
	state.keyStateModTime = time.Time{}
	state.keyStateLastCheck = time.Time{}
	state.rpmBuckets = map[string][]time.Time{}
	state.priceBook = policyplus.PriceBook{}
	state.priceBookChecked = time.Time{}
	state.lastConfigError = ""
	state.lastConfigErrorAt = time.Time{}
	state.mu.Unlock()
	t.Cleanup(shutdownPlugin)

	badParent := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(badParent, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	badPath := filepath.Join(badParent, "policyplus.sqlite")
	raw, err := handleMethod(methodPluginRegister, mustJSON(t, lifecycleRequest{ConfigYAML: []byte("enabled: true\nstate_db_path: " + badPath + "\n")}))
	if err != nil {
		t.Fatal(err)
	}
	var reg registration
	unwrapPlusEnvelope(t, raw, &reg)
	if reg.Metadata.Name != pluginID || !reg.Capabilities.ManagementAPI || !reg.Capabilities.FrontendAuthProvider {
		t.Fatalf("register must still return full registration: %#v", reg)
	}
	raw, err = managementRegister()
	if err != nil {
		t.Fatal(err)
	}
	var mgmt managementRegistrationResponse
	unwrapPlusEnvelope(t, raw, &mgmt)
	foundAdmin := false
	foundStatus := false
	for _, resource := range mgmt.Resources {
		if resource.Path == "/admin" && resource.Menu == "CPA Key Policy+" {
			foundAdmin = true
		}
		if resource.Path == "/admin/api/status" {
			foundStatus = true
		}
	}
	if !foundAdmin || !foundStatus {
		t.Fatalf("management resources missing after config error: %#v", mgmt.Resources)
	}
	raw, err = managementHandle(mustJSON(t, managementRequest{Method: http.MethodGet, Path: "/v0/resource/plugins/cpa-key-policy-plus/admin/api/keys"}))
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeManagementResponse(t, raw)
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(resp.Body), "store_unavailable") {
		t.Fatalf("admin keys should fail closed without store: status=%d body=%s", resp.StatusCode, resp.Body)
	}
	raw, err = managementHandle(mustJSON(t, managementRequest{Method: http.MethodGet, Path: "/v0/resource/plugins/cpa-key-policy-plus/admin/api/status"}))
	if err != nil {
		t.Fatal(err)
	}
	resp = decodeManagementResponse(t, raw)
	var statusBody struct {
		Plus map[string]any `json:"plus"`
	}
	if err := json.Unmarshal(resp.Body, &statusBody); err != nil {
		t.Fatal(err)
	}
	if statusBody.Plus["store_available"] != false || !strings.Contains(fmt.Sprint(statusBody.Plus["last_config_error"]), "not-a-dir") {
		t.Fatalf("status API missing initial config error: %#v", statusBody.Plus)
	}
}

func TestConfigureKeepsEmptyLegacyImportPathsDisabledAndRetainsIdentitySync(t *testing.T) {
	setupTestState(t)
	dir := t.TempDir()
	statePath := filepath.Join(dir, "key-policy-state.json")
	stateJSON := []byte(`[{"id":"identity-key","key_hash":"sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08","enabled":true,"models":["gpt-5.5"]}]`)
	if err := os.WriteFile(statePath, stateJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	configYAML := fmt.Sprintf("state_db_path: %q\nkey_policy_state_path: %q\nlegacy_quota_db_path: ''\ngovernor_state_db_path: ''\nsession_secret: test-secret\n", filepath.Join(dir, "policyplus.sqlite"), statePath)
	if err := configure(mustJSON(t, lifecycleRequest{ConfigYAML: []byte(configYAML)})); err != nil {
		t.Fatal(err)
	}
	cfg := loadedConfig()
	if cfg.LegacyQuotaDBPath != "" || cfg.GovernorStateDBPath != "" {
		t.Fatalf("empty legacy import paths must stay disabled: %#v", cfg)
	}
	keys, err := loadedStore().ListKeys(context.Background())
	if err != nil || len(keys) != 1 || keys[0].ID != "identity-key" {
		t.Fatalf("identity source must remain active while legacy imports are disabled: keys=%#v err=%v", keys, err)
	}
}

func TestPlusOptionalSQLiteFailuresDoNotBreakLifecycleRegistration(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "policyplus.sqlite")
	nativeConfigPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(nativeConfigPath, []byte("api-keys:\n  - sk-native-one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	badLegacyPath := filepath.Join(dir, "legacy-not-sqlite.db")
	badLegacyDB, err := sql.Open("sqlite", badLegacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := badLegacyDB.Exec(`create table key_limits(unexpected text)`); err != nil {
		t.Fatal(err)
	}
	if err := badLegacyDB.Close(); err != nil {
		t.Fatal(err)
	}
	badGovernorPath := filepath.Join(dir, "governor-not-sqlite.db")
	badGovernorDB, err := sql.Open("sqlite", badGovernorPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := badGovernorDB.Exec(`create table key_limits(unexpected text)`); err != nil {
		t.Fatal(err)
	}
	if err := badGovernorDB.Close(); err != nil {
		t.Fatal(err)
	}
	badParent := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(badParent, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	badAliasPath := filepath.Join(badParent, "cpamp.sqlite")
	badPricePath := filepath.Join(badParent, "prices.sqlite")
	config := fmt.Sprintf(`enabled: true
exclusive_auth: true
state_db_path: %s
native_keys_config_path: %s
legacy_quota_db_path: %s
governor_state_db_path: %s
cpamp_alias_db_path: %s
cpamp_price_db_path: %s
`, statePath, nativeConfigPath, badLegacyPath, badGovernorPath, badAliasPath, badPricePath)
	raw, err := handleMethod(methodPluginReconfigure, mustJSON(t, lifecycleRequest{ConfigYAML: []byte(config)}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(shutdownPlugin)
	var reg registration
	unwrapPlusEnvelope(t, raw, &reg)
	if reg.Metadata.Name != pluginID || !reg.Capabilities.ManagementAPI || !reg.Capabilities.FrontendAuthProvider {
		t.Fatalf("optional DB failures must still return full registration: %#v", reg)
	}
	if loadedStore() == nil {
		t.Fatal("main state store should be available despite optional DB failures")
	}
	status := plusStatusPayload()
	if status["store_available"] != true || status["last_config_error"] != nil {
		t.Fatalf("optional DB failures should not poison lifecycle status: %#v", status)
	}
	store := loadedStore()
	if count, err := store.AuditCount(context.Background(), "legacy_import_failed"); err != nil || count < 2 {
		t.Fatalf("legacy DB failures should be audited, count=%d err=%v", count, err)
	}
	if count, err := store.AuditCount(context.Background(), "native_alias_read_failed"); err != nil || count < 1 {
		t.Fatalf("CPAMP alias DB failure should be audited, count=%d err=%v", count, err)
	}
	raw, err = adminKeys(managementRequest{})
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeManagementResponse(t, raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin keys should remain usable: status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var body struct {
		Keys    []map[string]any `json:"keys"`
		Pricing map[string]any   `json:"pricing"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Keys) != 1 {
		t.Fatalf("native key sync should continue without aliases: %#v", body.Keys)
	}
	if body.Pricing["source"] != policyplus.CostSourceCPAMPPriceUnavailable {
		t.Fatalf("bad CPAMP price DB should surface as unavailable pricing: %#v", body.Pricing)
	}
}

func TestFrontendAuthIgnoresRetiredActiveSessionLimit(t *testing.T) {
	setupTestState(t)
	req := frontendAuthRequest{
		Headers: http.Header{"Authorization": []string{"Bearer sk-alice-secret"}},
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
	if !authOK(t, req) {
		t.Fatal("retired Codex window limit should not reject a second session")
	}
}

func TestFrontendAuthAllowsMissingSessionAndAudits(t *testing.T) {
	setupTestState(t)
	req := frontendAuthRequest{
		Headers: http.Header{"Authorization": []string{"Bearer sk-alice-secret"}},
		Body:    []byte(`{"model":"gpt-5.5"}`),
	}
	if !authOK(t, req) {
		t.Fatal("missing session identity should not be rejected in v1")
	}
}

func TestFrontendAuthKeepsModelListingOnIdentityPath(t *testing.T) {
	setupTestState(t)
	raw, err := frontendAuth(mustJSON(t, frontendAuthRequest{
		Path:    "/v1/models",
		Headers: http.Header{"Authorization": []string{"Bearer sk-alice-secret"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var response frontendAuthResponse
	if err := json.Unmarshal(env.Result, &response); err != nil || !response.Authenticated || response.Principal != "alice-key" {
		t.Fatalf("model listing identity auth response=%#v err=%v", response, err)
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
		"models":              []string{"gpt-5.4", "custom-unknown"},
		"prices": map[string]any{
			"custom-unknown": map[string]any{"input_per_million": 1.2},
		},
		"five_hour_usd": 1.25,
		"daily_usd":     2.5,
		"weekly_usd":    7.5,
		"monthly_usd":   20.0,
	}}}
	rawBody, _ := json.Marshal(body)
	raw, err := adminSaveKeys(managementRequest{Body: rawBody})
	if err != nil {
		t.Fatal(err)
	}
	bodyBytes := decodeManagementBody(t, raw)
	if strings.Contains(string(bodyBytes), "Alice Plus") || !strings.Contains(string(bodyBytes), "Alice") {
		t.Fatalf("save response should keep CPA/CPAMP readonly alias: %s", bodyBytes)
	}
	store := loadedStore()
	keys, err := store.ListKeys(context.Background())
	if err != nil || len(keys) != 1 {
		t.Fatalf("ListKeys err=%v keys=%#v", err, keys)
	}
	got := keys[0]
	if got.Enabled || got.RPM != 5 || got.Concurrency != 0 || got.MaxActiveSessions != 0 {
		t.Fatalf("updated key = %#v", got)
	}
	if got.Name != "Alice" {
		t.Fatalf("Plus save must not rename native key alias, got %q", got.Name)
	}
	if got.FiveHourUSD == nil || *got.FiveHourUSD != 1.25 || got.MonthlyLimitUSD == nil || *got.MonthlyLimitUSD != 20 {
		t.Fatalf("limits not persisted: %#v", got)
	}
	if len(got.Models) != 2 || got.Models[1] != "custom-unknown" {
		t.Fatalf("models should preserve unknown selected model: %#v", got.Models)
	}
	if len(got.Prices) != 0 {
		t.Fatalf("Plus save should ignore per-key price edits after CPAMP pricing cutover: %#v", got.Prices)
	}
}

func TestAdminWeeklyOnlyMigrationEndpointPreservesExistingPolicy(t *testing.T) {
	key := setupTestState(t)
	key.RPM = 23
	key.Models = []string{"gpt-5.5", "gpt-6-astra"}
	key.FiveHourUSD = floatPtr(60)
	key.DailyLimitUSD = floatPtr(100)
	key.WeeklyLimitUSD = floatPtr(500)
	key.MonthlyLimitUSD = floatPtr(1500)
	if err := loadedStore().SaveKeySettings(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if err := loadedStore().InsertUsage(context.Background(), policyplus.UsageEvent{RequestID: "weekly-api-history", KeyID: key.ID, RequestedAt: time.Now(), Cost: 3}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"id":"alice-key","weekly_only":true,"weekly_usd":999}`)
	raw, err := managementHandle(mustJSON(t, managementRequest{
		Method: http.MethodPut,
		Path:   "/plugins/cpa-key-policy-plus/keys/weekly-only",
		Body:   body,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(decodeManagementBody(t, raw)), `"quota_mode":"weekly_only"`) {
		t.Fatalf("weekly-only endpoint response missing mode: %s", raw)
	}
	keys, err := loadedStore().ListKeys(context.Background())
	if err != nil || len(keys) != 1 {
		t.Fatalf("ListKeys err=%v keys=%#v", err, keys)
	}
	got := keys[0]
	if !got.WeeklyOnly || got.FiveHourUSD != nil || got.DailyLimitUSD != nil || got.MonthlyLimitUSD != nil || got.WeeklyLimitUSD == nil || *got.WeeklyLimitUSD != 500 {
		t.Fatalf("weekly-only migration did not preserve weekly limit: %#v", got)
	}
	if got.RPM != 23 || len(got.Models) != 2 || got.Models[1] != "gpt-6-astra" {
		t.Fatalf("weekly-only migration changed policy fields: %#v", got)
	}
	if quota := quotaWindows(got, map[string]float64{policyplus.Range7D: 3}); len(quota) != 1 || quota[policyplus.Range7D] == nil {
		t.Fatalf("weekly-only portal quota projection = %#v", quota)
	}
	summary, err := loadedStore().UsageSummary(context.Background(), got.ID, policyplus.WindowFor(policyplus.Range24H, time.Now()))
	if err != nil || summary.Calls != 1 || summary.TotalCost != 3 {
		t.Fatalf("weekly-only endpoint changed history: summary=%#v err=%v", summary, err)
	}
}

func TestAdminCreateKeyRetiredToCPAMP(t *testing.T) {
	setupTestState(t)
	raw, err := adminCreateKey(managementRequest{Body: []byte(`{"name":"Bob","enabled":false,"rpm":9,"concurrency":4,"max_active_sessions":3,"models":["gpt-5.4-mini"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeManagementResponse(t, raw)
	bodyBytes := resp.Body
	keys, err := loadedStore().ListKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusGone || !strings.Contains(string(bodyBytes), "native_key_lifecycle_owned_by_cpa") {
		t.Fatalf("create should be retired to CPA/CPAMP: status=%d body=%s", resp.StatusCode, bodyBytes)
	}
	if strings.Contains(string(bodyBytes), "raw_key") || len(keys) != 1 {
		t.Fatalf("retired create must not return raw keys or persist Bob: keys=%#v body=%s", keys, bodyBytes)
	}
}

func TestManagementAliasCreateSaveAndReset(t *testing.T) {
	setupTestState(t)
	createRaw, _ := json.Marshal(map[string]any{"name": "Alias", "models": []string{"gpt-5.4"}})
	raw, err := managementHandle(mustJSON(t, managementRequest{
		Method: http.MethodPost,
		Path:   "/key-policy-plus/api/keys/create",
		Body:   createRaw,
	}))
	if err != nil {
		t.Fatal(err)
	}
	createResp := decodeManagementResponse(t, raw)
	createBody := createResp.Body
	if createResp.StatusCode != http.StatusGone || !strings.Contains(string(createBody), "native_key_lifecycle_owned_by_cpa") || strings.Contains(string(createBody), `"raw_key"`) {
		t.Fatalf("create alias should be retired: status=%d body=%s", createResp.StatusCode, createBody)
	}
	saveRaw, _ := json.Marshal(map[string]any{"keys": []map[string]any{{
		"id":                  "alice-key",
		"name":                "Alias Saved",
		"enabled":             true,
		"rpm":                 11,
		"concurrency":         2,
		"max_active_sessions": 1,
		"models":              []string{"gpt-5.5"},
	}}})
	raw, err = managementHandle(mustJSON(t, managementRequest{
		Method: http.MethodPut,
		Path:   "/key-policy-plus/api/keys/save",
		Body:   saveRaw,
	}))
	if err != nil {
		t.Fatal(err)
	}
	saveBody := decodeManagementBody(t, raw)
	if strings.Contains(string(saveBody), "Alias Saved") || !strings.Contains(string(saveBody), "Alice") {
		t.Fatalf("save via alias should update strategy but not readonly alias: %s", saveBody)
	}
	keys, err := loadedStore().ListKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if key.ID == "alice-key" && (key.Concurrency != 0 || key.MaxActiveSessions != 0) {
			t.Fatalf("alias save should force retired limits to zero: %#v", key)
		}
	}
	resetRaw, _ := json.Marshal(map[string]any{"id": "alice-key", "window": "5h"})
	raw, err = managementHandle(mustJSON(t, managementRequest{
		Method: http.MethodPost,
		Path:   "/key-policy-plus/api/keys/reset",
		Body:   resetRaw,
	}))
	if err != nil {
		t.Fatal(err)
	}
	resetBody := decodeManagementBody(t, raw)
	if !strings.Contains(string(resetBody), `"ok":true`) {
		t.Fatalf("reset via alias failed: %s", resetBody)
	}
}

func TestArchiveRouteReturnsGone(t *testing.T) {
	key := setupTestState(t)
	archiveRaw, _ := json.Marshal(map[string]any{"id": key.ID, "archived": true})
	raw, err := managementHandle(mustJSON(t, managementRequest{
		Method: http.MethodPost,
		Path:   "/key-policy-plus/api/keys/archive",
		Body:   archiveRaw,
	}))
	if err != nil {
		t.Fatal(err)
	}
	body := decodeManagementBody(t, raw)
	if !strings.Contains(string(body), `"native_key_lifecycle_owned_by_cpa"`) {
		t.Fatalf("archive route should return CPA/CPAMP lifecycle guidance: %s", body)
	}
}

func TestDeleteKeyRetiredAndDoesNotDisableNativePolicy(t *testing.T) {
	key := setupTestState(t)
	deleteRaw, _ := json.Marshal(map[string]any{"id": key.ID, "confirm": "delete"})
	raw, err := managementHandle(mustJSON(t, managementRequest{
		Method: http.MethodPost,
		Path:   "/key-policy-plus/api/keys/delete",
		Body:   deleteRaw,
	}))
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeManagementResponse(t, raw)
	body := resp.Body
	if resp.StatusCode != http.StatusGone || !strings.Contains(string(body), `"native_key_lifecycle_owned_by_cpa"`) {
		t.Fatalf("delete should be retired to CPA/CPAMP: status=%d body=%s", resp.StatusCode, body)
	}
	if !authOK(t, frontendAuthRequest{
		Headers: http.Header{"Authorization": []string{"Bearer sk-alice-secret"}},
		Body:    []byte(`{"model":"gpt-5.5","prompt_cache_key":"window-a"}`),
	}) {
		t.Fatal("retired Plus delete should not disable native policy")
	}
	raw, err = userSession(managementRequest{Headers: http.Header{"X-CPA-Key-Policy-Plus-Key": []string{"sk-alice-secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	body = decodeManagementBody(t, raw)
	if !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("retired Plus delete should not break user session: %s", body)
	}
}

func TestAdminKeysMirrorsCurrentNativeCPAKeys(t *testing.T) {
	setupTestState(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	aliasPath := filepath.Join(dir, "cpamp.sqlite")
	writeNativeConfig := func(keys ...string) {
		t.Helper()
		body := "api-keys:\n"
		for _, key := range keys {
			body += "  - " + key + "\n"
		}
		if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", aliasPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table api_key_aliases(api_key_hash text primary key, alias text, updated_at_ms integer)`); err != nil {
		t.Fatal(err)
	}
	upsertAlias := func(rawKey, alias string) {
		t.Helper()
		if _, err := db.Exec(`insert into api_key_aliases(api_key_hash, alias, updated_at_ms) values(?, ?, ?)
			on conflict(api_key_hash) do update set alias=excluded.alias, updated_at_ms=excluded.updated_at_ms`,
			"sha256:"+policyplus.SHA256Hex(rawKey), alias, time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	upsertAlias("sk-native-qq", "QQ专用")
	upsertAlias("sk-native-wei", "阿伟专用")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	writeNativeConfig("sk-native-qq", "sk-native-wei")

	legacy := policyplus.KeyRecord{
		ID:            "cpa_legacy_policy",
		Name:          "旧 CPI 下划线 Key",
		KeyHash:       "sha256:" + policyplus.SHA256Hex("cpa_legacy_policy"),
		Enabled:       true,
		Preview:       policyplus.HashPreview(policyplus.SHA256Hex("cpa_legacy_policy")),
		Source:        policyplus.LegacyPlusSource,
		SourcePresent: false,
		Hidden:        true,
	}
	if err := loadedStore().UpsertKey(context.Background(), legacy); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.cfg.NativeKeysConfigPath = configPath
	state.cfg.CPAMPAliasDBPath = aliasPath
	state.mu.Unlock()

	raw, err := adminKeys(managementRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var first struct {
		OK   bool                       `json:"ok"`
		Keys []map[string]any           `json:"keys"`
		Meta map[string]json.RawMessage `json:"codexcont"`
	}
	if err := json.Unmarshal(decodeManagementBody(t, raw), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Keys) != 2 {
		t.Fatalf("default admin list should mirror only current native keys: %#v", first.Keys)
	}
	names := map[string]bool{}
	for _, key := range first.Keys {
		names[fmt.Sprint(key["name"])] = true
		if key["source"] != policyplus.NativeCPASource || key["source_present"] != true || key["hidden"] == true {
			t.Fatalf("default row should be active native key only: %#v", key)
		}
	}
	if !names["QQ专用"] || !names["阿伟专用"] || names["旧 CPI 下划线 Key"] {
		t.Fatalf("unexpected admin key names: %#v rows=%#v", names, first.Keys)
	}
	for _, key := range first.Keys {
		if key["enabled"] != true {
			t.Fatalf("new official native keys should be enabled by default: %#v", key)
		}
	}

	db, err = sql.Open("sqlite", aliasPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update api_key_aliases set alias=? where api_key_hash=?`, "QQ官Key改名", "sha256:"+policyplus.SHA256Hex("sk-native-qq")); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	writeNativeConfig("sk-native-qq")
	raw, err = adminKeys(managementRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var second struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(decodeManagementBody(t, raw), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Keys) != 1 || second.Keys[0]["name"] != "QQ官Key改名" {
		t.Fatalf("admin list should refresh alias changes and official deletions: %#v", second.Keys)
	}

	raw, err = adminKeys(managementRequest{Query: map[string][]string{"include_removed": {"1"}}})
	if err != nil {
		t.Fatal(err)
	}
	var withRemoved struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(decodeManagementBody(t, raw), &withRemoved); err != nil {
		t.Fatal(err)
	}
	if len(withRemoved.Keys) < 2 {
		t.Fatalf("include_removed should expose removed native diagnostics: %#v", withRemoved.Keys)
	}
	seenRemovedQQ := false
	for _, key := range withRemoved.Keys {
		if key["source"] != policyplus.NativeCPASource || key["name"] == "旧 CPI 下划线 Key" {
			t.Fatalf("include_removed leaked non-native row: %#v", key)
		}
		if key["name"] == "阿伟专用" && key["source_present"] == false {
			seenRemovedQQ = true
		}
	}
	if !seenRemovedQQ {
		t.Fatalf("include_removed did not expose the official key removed by sync: %#v", withRemoved.Keys)
	}
}

func TestAdminKeysUsesAliasFallbackPath(t *testing.T) {
	setupTestState(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	emptyAliasPath := filepath.Join(dir, "empty-cpamp.sqlite")
	realAliasPath := filepath.Join(dir, "real-cpamp.sqlite")
	if err := os.WriteFile(configPath, []byte("api-keys:\n  - sk-alice\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	emptyDB, err := sql.Open("sqlite", emptyAliasPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emptyDB.Exec(`create table unrelated(id text)`); err != nil {
		t.Fatal(err)
	}
	if err := emptyDB.Close(); err != nil {
		t.Fatal(err)
	}
	realDB, err := sql.Open("sqlite", realAliasPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := realDB.Exec(`create table api_key_aliases(api_key_hash text primary key, alias text, updated_at_ms integer)`); err != nil {
		t.Fatal(err)
	}
	if _, err := realDB.Exec(`insert into api_key_aliases(api_key_hash, alias, updated_at_ms) values(?, ?, ?)`, "sha256:"+policyplus.SHA256Hex("sk-alice"), "alicea", time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := realDB.Close(); err != nil {
		t.Fatal(err)
	}

	state.mu.Lock()
	state.cfg.NativeKeysConfigPath = configPath
	state.cfg.CPAMPAliasDBPath = emptyAliasPath
	state.cfg.CPAMPAliasDBPaths = realAliasPath
	state.mu.Unlock()

	raw, err := adminKeys(managementRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(decodeManagementBody(t, raw), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Keys) != 1 || resp.Keys[0]["name"] != "alicea" || resp.Keys[0]["alias"] != "alicea" {
		t.Fatalf("admin list should use fallback CPAMP alias source: %#v", resp.Keys)
	}
	if preview := fmt.Sprint(resp.Keys[0]["preview"]); preview == "" || strings.Contains(preview, "sk-alice") {
		t.Fatalf("preview should stay safe and derived from the same native key: %#v", resp.Keys[0])
	}
}

func TestPolicyDecisionDenialsUseExplicitCodes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *policyplus.KeyRecord)
		model  string
		code   string
		param  string
	}{
		{
			name: "disabled",
			mutate: func(t *testing.T, key *policyplus.KeyRecord) {
				key.Enabled = false
			},
			model: "gpt-5.5",
			code:  "api_key_disabled",
			param: "disabled",
		},
		{
			name: "source removed",
			mutate: func(t *testing.T, key *policyplus.KeyRecord) {
				key.SourcePresent = false
			},
			model: "gpt-5.5",
			code:  "api_key_source_removed",
			param: "source_removed",
		},
		{
			name:  "model not allowed",
			model: "gpt-5.4",
			code:  "model_not_allowed",
			param: "model",
		},
		{
			name: "rpm",
			mutate: func(t *testing.T, key *policyplus.KeyRecord) {
				key.RPM = 1
			},
			model: "gpt-5.5",
			code:  "rpm_rate_limit_exceeded",
			param: "rpm",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key := setupTestState(t)
			if tc.mutate != nil {
				tc.mutate(t, &key)
			}
			if key.SourcePresent {
				if err := loadedStore().SaveKeySettings(context.Background(), key); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "source removed" {
				if err := loadedStore().SyncNativeKeys(context.Background(), nil); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "rpm" {
				first := evaluatePolicy(key, tc.model, true)
				if !first.Allowed {
					t.Fatalf("first RPM request should pass: %#v", first)
				}
			}
			decision := evaluatePolicy(key, tc.model, tc.name == "rpm")
			if decision.Allowed || decision.StatusCode != http.StatusTooManyRequests || decision.Code != tc.code || decision.Param != tc.param || !strings.Contains(decision.Message, "CPA Key Policy+") {
				t.Fatalf("decision = %#v", decision)
			}
		})
	}
}

func TestQuotaKeyRejectsUnpricedModelButAllowsCachedCPAMPPrice(t *testing.T) {
	key := setupTestState(t)
	key.FiveHourUSD = nil
	key.DailyLimitUSD = nil
	key.WeeklyLimitUSD = floatPtr(500)
	key.MonthlyLimitUSD = nil
	key.Models = []string{"gpt-6-astra", "model-without-price"}
	if err := loadedStore().SaveKeySettings(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.priceBook = policyplus.PriceBook{
		Source: policyplus.CostSourceCPAMPCachedPriceBook,
		Prices: map[string]policyplus.ModelPrice{
			"gpt-6-astra": {Model: "gpt-6-astra", InputPerMillion: 2, OutputPerMillion: 12},
		},
	}
	state.priceBookChecked = time.Now()
	state.mu.Unlock()

	if allowed := evaluatePolicy(key, "GPT-6-ASTRA", false); !allowed.Allowed {
		t.Fatalf("valid cached price must remain usable: %#v", allowed)
	}
	denied := evaluatePolicy(key, "model-without-price", false)
	if denied.Allowed || denied.Code != "model_price_unavailable" || denied.Param != "model" || !strings.Contains(denied.Message, "$0") {
		t.Fatalf("unpriced quota model decision = %#v", denied)
	}
}

func TestQuotaAdmissionRequiresApplicableCPAMPContextOrServiceTierPrice(t *testing.T) {
	key := setupTestState(t)
	key.FiveHourUSD = nil
	key.DailyLimitUSD = nil
	key.WeeklyLimitUSD = floatPtr(500)
	key.MonthlyLimitUSD = nil
	key.Models = []string{"context-only", "service-only", "partial-context"}
	if err := loadedStore().SaveKeySettings(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.priceBook = policyplus.PriceBook{
		Source: policyplus.CostSourceCPAMPCachedPriceBook,
		Prices: map[string]policyplus.ModelPrice{
			"context-only": {
				Model: "context-only",
				ContextTiers: []policyplus.ModelPriceContextTier{{
					ThresholdTokens: 100, InputPerMillion: 2, OutputPerMillion: 8, InputConfigured: true, OutputConfigured: true,
				}},
			},
			"service-only": {
				Model: "service-only",
				ServiceTiers: []policyplus.ModelPriceServiceTier{{
					Mode: "fast", ServiceTier: "priority", InputPerMillion: 3, OutputPerMillion: 12, InputConfigured: true, OutputConfigured: true,
				}},
			},
			"partial-context": {
				Model: "partial-context",
				ContextTiers: []policyplus.ModelPriceContextTier{{
					ThresholdTokens: 100, InputPerMillion: 2, InputConfigured: true,
				}},
				ServiceTiers: []policyplus.ModelPriceServiceTier{{
					Mode: "fast", ServiceTier: "priority", InputPerMillion: 3, OutputPerMillion: 12, InputConfigured: true, OutputConfigured: true,
				}},
			},
		},
	}
	state.priceBookChecked = time.Now()
	state.mu.Unlock()

	if decision := evaluatePolicy(key, "context-only", false); decision.Allowed || decision.Code != "model_price_unavailable" {
		t.Fatalf("context-only price must not admit unknown short context: %#v", decision)
	}
	if decision := evaluatePolicy(key, "service-only", false); decision.Allowed || decision.Code != "model_price_unavailable" {
		t.Fatalf("service-only price must not admit an unspecified tier: %#v", decision)
	}
	if decision := evaluatePolicyWithServiceTier(key, "service-only", "priority", false); !decision.Allowed {
		t.Fatalf("matching explicit service tier should be allowed: %#v", decision)
	}
	if decision := evaluatePolicyWithServiceTier(key, "service-only", "flex", false); decision.Allowed || decision.Code != "model_price_unavailable" {
		t.Fatalf("nonmatching service tier must be rejected: %#v", decision)
	}
	if decision := evaluatePolicyWithServiceTier(key, "partial-context", "priority", false); decision.Allowed || decision.Code != "model_price_unavailable" {
		t.Fatalf("incomplete possible context tier must be rejected: %#v", decision)
	}
}

func TestRouteModelRejectsQuotaKeyWithoutMatchingRequestedServiceTierPrice(t *testing.T) {
	setupTestState(t)
	raw, err := routeModel(mustJSON(t, modelRouteRequest{
		RequestedModel: "gpt-5.5",
		Headers:        http.Header{"Authorization": []string{"Bearer sk-alice-secret"}},
		Body:           []byte(`{"model":"gpt-5.5","service_tier":"priority"}`),
	}))
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var result modelRouteResponse
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Handled {
		t.Fatalf("routing must not carry policy denials; the interceptor answers with a real 429: %#v", result)
	}
	raw, err = handleMethod(methodRequestInterceptBefore, mustJSON(t, requestInterceptRequest{
		SourceFormat:   "openai-response",
		RequestedModel: "gpt-5.5",
		Headers:        http.Header{"Authorization": []string{"Bearer sk-alice-secret"}},
		Body:           []byte(`{"model":"gpt-5.5","service_tier":"priority"}`),
	}))
	if err != nil {
		t.Fatal(err)
	}
	var interceptEnv envelope
	if err := json.Unmarshal(raw, &interceptEnv); err != nil {
		t.Fatal(err)
	}
	var intercept requestInterceptResponse
	if err := json.Unmarshal(interceptEnv.Result, &intercept); err != nil {
		t.Fatal(err)
	}
	if !intercept.Terminate || intercept.StatusCode != http.StatusTooManyRequests || intercept.ResponseHeaders.Get("X-CPA-Policy-Reason") != "model_price_unavailable" {
		t.Fatalf("unpriced requested service tier must be terminated with 429: %#v", intercept)
	}
}

func TestPolicyDecisionQuotaWindowsUsePostAccountingOrder(t *testing.T) {
	windows := []struct {
		name  string
		set   func(*policyplus.KeyRecord)
		code  string
		param string
	}{
		{policyplus.Range5H, func(k *policyplus.KeyRecord) { k.FiveHourUSD = floatPtr(1) }, "five_hour_quota_exceeded", policyplus.Range5H},
		{policyplus.Range24H, func(k *policyplus.KeyRecord) { k.DailyLimitUSD = floatPtr(1) }, "daily_quota_exceeded", policyplus.Range24H},
		{policyplus.Range7D, func(k *policyplus.KeyRecord) { k.WeeklyLimitUSD = floatPtr(1) }, "weekly_quota_exceeded", policyplus.Range7D},
		{policyplus.RangeMonth, func(k *policyplus.KeyRecord) { k.MonthlyLimitUSD = floatPtr(1) }, "monthly_quota_exceeded", policyplus.RangeMonth},
	}
	for _, item := range windows {
		t.Run(item.name, func(t *testing.T) {
			key := setupTestState(t)
			key.FiveHourUSD = nil
			key.DailyLimitUSD = nil
			key.WeeklyLimitUSD = nil
			key.MonthlyLimitUSD = nil
			item.set(&key)
			if err := loadedStore().SaveKeySettings(context.Background(), key); err != nil {
				t.Fatal(err)
			}
			if err := loadedStore().InsertUsage(context.Background(), policyplus.UsageEvent{
				RequestID:   "quota-" + item.name,
				KeyID:       key.ID,
				RequestedAt: time.Now(),
				Cost:        1,
			}); err != nil {
				t.Fatal(err)
			}
			decision := evaluatePolicy(key, "gpt-5.5", false)
			if decision.Allowed || decision.Code != item.code || decision.Param != item.param || decision.Window != item.name || decision.UsedUSD != 1 || decision.LimitUSD != 1 {
				t.Fatalf("quota decision = %#v", decision)
			}
			if !strings.Contains(decision.Message, "已用 $1.00 / 上限 $1.00") {
				t.Fatalf("quota message should expose used/limit: %s", decision.Message)
			}
		})
	}
}

func TestRouteAndExecutorReturnPolicyDenied429(t *testing.T) {
	key := setupTestState(t)
	key.Enabled = false
	if err := loadedStore().SaveKeySettings(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	routeRaw, err := routeModel(mustJSON(t, modelRouteRequest{
		RequestedModel: "gpt-5.5",
		Headers:        http.Header{"Authorization": []string{"Bearer sk-alice-secret"}},
		Body:           []byte(`{"model":"gpt-5.5"}`),
	}))
	if err != nil {
		t.Fatal(err)
	}
	var routeEnv envelope
	if err := json.Unmarshal(routeRaw, &routeEnv); err != nil {
		t.Fatal(err)
	}
	var routeResp modelRouteResponse
	if err := json.Unmarshal(routeEnv.Result, &routeResp); err != nil {
		t.Fatal(err)
	}
	if routeResp.Handled {
		t.Fatalf("denied requests are not routed into the executor; the interceptor returns 429: %#v", routeResp)
	}

	execRaw, err := executorExecute(mustJSON(t, executorCallRequest{
		executorRequest: executorRequest{
			Model:   "gpt-5.5",
			Headers: http.Header{"Authorization": []string{"Bearer sk-alice-secret"}},
			Payload: []byte(`{"model":"gpt-5.5"}`),
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var execEnv envelope
	if err := json.Unmarshal(execRaw, &execEnv); err != nil {
		t.Fatal(err)
	}
	var execResp executorResponse
	if err := json.Unmarshal(execEnv.Result, &execResp); err != nil {
		t.Fatal(err)
	}
	if execResp.Headers.Get("X-CPA-Policy-Reason") != "api_key_disabled" || execResp.Headers.Get("Retry-After") == "" {
		t.Fatalf("executor deny response = %#v", execResp)
	}
	if execResp.Headers.Get("X-CPA-Policy-Window") != "disabled" {
		t.Fatalf("executor deny headers should include safe policy window/param: %#v", execResp.Headers)
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
			Param   string `json:"param"`
		} `json:"error"`
	}
	if err := json.Unmarshal(execResp.Payload, &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "api_key_disabled" || body.Error.Message == "" || body.Error.Param != "disabled" {
		t.Fatalf("deny body = %s", execResp.Payload)
	}
}

func TestFrontendAuthDoesNotEmitFakePolicyDenials(t *testing.T) {
	key := setupTestState(t)
	key.Enabled = false
	if err := loadedStore().SaveKeySettings(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	raw, err := frontendAuth(mustJSON(t, frontendAuthRequest{
		Path:    "/v1/models",
		Headers: http.Header{"Authorization": []string{"Bearer sk-alice-secret"}},
	}))
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
	if resp.Authenticated {
		t.Fatalf("non-model denied request should fail closed through normal auth path: %#v", resp)
	}

	raw, err = frontendAuth(mustJSON(t, frontendAuthRequest{
		Path:    "/v1/responses",
		Headers: http.Header{"Authorization": []string{"Bearer sk-alice-secret"}},
		Body:    []byte(`{"model":"gpt-5.5"}`),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Authenticated || len(resp.Metadata) != 0 {
		t.Fatalf("frontend auth must not pretend to return a policy HTTP response: %#v", resp)
	}
}

func TestRequestInterceptorTerminatesUnpricedInferenceWithReal429(t *testing.T) {
	key := setupTestState(t)
	key.Models = []string{"gpt-5.5", "gpt-cpa-verification-unpriced"}
	if err := loadedStore().SaveKeySettings(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	raw, err := handleMethod(methodRequestInterceptBefore, mustJSON(t, requestInterceptRequest{
		SourceFormat:   "openai-response",
		RequestedModel: "gpt-cpa-verification-unpriced",
		Headers:        http.Header{"Authorization": []string{"Bearer sk-alice-secret"}},
		Body:           []byte(`{"model":"gpt-cpa-verification-unpriced"}`),
	}))
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var resp requestInterceptResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Terminate || resp.StatusCode != http.StatusTooManyRequests || resp.ResponseHeaders.Get("Content-Type") == "" || resp.ResponseHeaders.Get("X-CPA-Policy-Reason") != "model_price_unavailable" {
		t.Fatalf("request termination response = %#v", resp)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp.ResponseBody, &body); err != nil || body.Error.Code != "model_price_unavailable" {
		t.Fatalf("termination body=%s err=%v", resp.ResponseBody, err)
	}
	keys, err := loadedStore().ListKeys(context.Background())
	if err != nil || len(keys) != 1 || keys[0].BillingHoldReason != "" {
		t.Fatalf("pre-execution termination must not create a billing hold: keys=%#v err=%v", keys, err)
	}

	raw, err = handleMethod(methodRequestInterceptBefore, mustJSON(t, requestInterceptRequest{SourceFormat: "openai-response", Headers: http.Header{"Authorization": []string{"Bearer sk-alice-secret"}}, Body: []byte(`{"not_model":"x"}`)}))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	resp = requestInterceptResponse{}
	if err := json.Unmarshal(env.Result, &resp); err != nil || !resp.Terminate || resp.ResponseHeaders.Get("X-CPA-Policy-Reason") != "model_price_unavailable" {
		t.Fatalf("missing model on intercepted inference must fail closed: %#v err=%v", resp, err)
	}

	raw, err = handleMethod(methodRequestInterceptBefore, mustJSON(t, requestInterceptRequest{SourceFormat: "openai-response", Body: []byte(`{"not_model":"x"}`)}))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	resp = requestInterceptResponse{}
	if err := json.Unmarshal(env.Result, &resp); err != nil || resp.Terminate {
		t.Fatalf("request without Plus credential must remain unhandled: %#v err=%v", resp, err)
	}

	raw, err = handleMethod(methodRequestInterceptBefore, mustJSON(t, requestInterceptRequest{
		SourceFormat:   "openai-response",
		RequestedModel: "gpt-cpa-verification-unpriced",
		Metadata:       map[string]any{"provider": pluginID, "key_id": key.ID},
		Body:           []byte(`{"model":"gpt-cpa-verification-unpriced"}`),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	resp = requestInterceptResponse{}
	if err := json.Unmarshal(env.Result, &resp); err != nil || !resp.Terminate || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("host-injected frontend identity metadata must preserve termination: %#v err=%v", resp, err)
	}
}

func TestRequestInterceptorConsumesRPMOnceAfterFrontendIdentity(t *testing.T) {
	key := setupTestState(t)
	key.RPM = 1
	if err := loadedStore().SaveKeySettings(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	frontRaw, err := frontendAuth(mustJSON(t, frontendAuthRequest{Path: "/v1/responses", Headers: http.Header{"Authorization": []string{"Bearer sk-alice-secret"}}, Body: []byte(`{"model":"gpt-5.5"}`)}))
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(frontRaw, &env); err != nil {
		t.Fatal(err)
	}
	var front frontendAuthResponse
	if err := json.Unmarshal(env.Result, &front); err != nil || !front.Authenticated {
		t.Fatalf("frontend identity response=%#v err=%v", front, err)
	}
	request := requestInterceptRequest{SourceFormat: "openai-response", RequestedModel: "gpt-5.5", Headers: http.Header{"Authorization": []string{"Bearer sk-alice-secret"}}, Body: []byte(`{"model":"gpt-5.5"}`)}
	first, err := requestInterceptBeforeAuth(mustJSON(t, request))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(first, &env); err != nil {
		t.Fatal(err)
	}
	var response requestInterceptResponse
	if err := json.Unmarshal(env.Result, &response); err != nil || response.Terminate {
		t.Fatalf("first request should consume the only RPM slot once: %#v err=%v", response, err)
	}
	second, err := requestInterceptBeforeAuth(mustJSON(t, request))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second, &env); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(env.Result, &response); err != nil || !response.Terminate || response.ResponseHeaders.Get("X-CPA-Policy-Reason") != "rpm_rate_limit_exceeded" {
		t.Fatalf("second request should receive RPM termination: %#v err=%v", response, err)
	}
}

func TestZeroTokenInternalPolicyTraceDoesNotCreatePendingBillingHold(t *testing.T) {
	key := setupTestState(t)
	key.Models = []string{"gpt-cpa-verification-unpriced"}
	if err := loadedStore().SaveKeySettings(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(usageRecord{
		Model:       "gpt-cpa-verification-unpriced",
		AuthID:      key.ID,
		RequestedAt: time.Now(),
		Detail:      usageDetail{},
	})
	if _, err := usageHandle(body); err != nil {
		t.Fatal(err)
	}
	events, err := loadedStore().RecentEvents(context.Background(), key.ID, 10)
	if err != nil || len(events) != 0 {
		t.Fatalf("zero-token internal rejection must not become billable pending event: events=%#v err=%v", events, err)
	}
	keys, err := loadedStore().ListKeys(context.Background())
	if err != nil || len(keys) != 1 || keys[0].BillingHoldReason != "" {
		t.Fatalf("zero-token internal rejection must not set a hold: keys=%#v err=%v", keys, err)
	}
	count, err := loadedStore().AuditCount(context.Background(), "usage_zero_token_policy_trace")
	if err != nil || count != 1 {
		t.Fatalf("zero-token internal rejection must retain an audit trace: count=%d err=%v", count, err)
	}
}

func TestExecutorRequestNormalizationKeepsNestedPayload(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","stream":true}`)
	req := normalizedExecutorRequest(executorCallRequest{
		NestedExecutorRequest: executorRequest{
			Model:   "gpt-5.5",
			Payload: body,
		},
	})
	if req.Model != "gpt-5.5" || string(req.Payload) != string(body) {
		t.Fatalf("normalized nested request = %#v", req)
	}
}

func TestAdminKeysIncludesQuotaProjection(t *testing.T) {
	setupTestState(t)
	store := loadedStore()
	if err := store.InsertUsage(context.Background(), policyplus.UsageEvent{
		RequestID:   "usage-a",
		KeyID:       "alice-key",
		RequestedAt: time.Now().Add(-time.Hour),
		Cost:        2.5,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := adminKeys(managementRequest{})
	if err != nil {
		t.Fatal(err)
	}
	body := decodeManagementBody(t, raw)
	text := string(body)
	for _, want := range []string{`"usage"`, `"quota"`, `"24h"`, `"remaining_usd"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("admin key projection missing %s: %s", want, body)
		}
	}
}

func TestUsageHandleMapsExecutorRecordsByAuthID(t *testing.T) {
	key := setupTestState(t)
	key.Models = []string{"gpt-5.4"}
	if err := loadedStore().SaveKeySettings(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	pricePath := createCPAMPPriceDB(t, map[string]policyplus.ModelPrice{
		"gpt-5.4":             {Model: "gpt-5.4", InputPerMillion: 10, OutputPerMillion: 20},
		"gpt-5.3-codex-spark": {Model: "gpt-5.3-codex-spark", InputPerMillion: 10, OutputPerMillion: 20},
	})
	state.mu.Lock()
	state.cfg.CPAMPPriceDBPath = pricePath
	state.priceBook = policyplus.PriceBook{}
	state.priceBookChecked = time.Time{}
	state.mu.Unlock()
	body, _ := json.Marshal(usageRecord{
		Provider:     "codex-account-3.json",
		ExecutorType: "codex",
		Model:        "gpt-5.3-codex-spark",
		Alias:        "gpt-5.4",
		AuthID:       key.ID,
		Source:       "codex-account-3.json",
		RequestedAt:  time.Now(),
		Detail: usageDetail{
			InputTokens:     10,
			OutputTokens:    5,
			ReasoningTokens: 2,
			TotalTokens:     15,
		},
	})
	raw, err := usageHandle(body)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("usage response = %s", raw)
	}
	events, err := loadedStore().RecentEvents(context.Background(), key.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	got := events[0]
	if got.KeyID != key.ID || got.Model != "gpt-5.4" || got.RequestedModel != "gpt-5.4" || got.ActualModel != "gpt-5.3-codex-spark" {
		t.Fatalf("usage event did not preserve explicit requested/actual models: %#v", got)
	}
	if got.Cost <= 0 {
		t.Fatalf("usage event should use CPAMP model price book, got cost=%f event=%#v", got.Cost, got)
	}
	if got.CostBreakdown.Source != policyplus.CostSourceCPAMPPriceBook {
		t.Fatalf("cost source = %#v", got.CostBreakdown)
	}
}

func TestUsageHandleTreatsAutoServiceTierAsDefaultPricedUsage(t *testing.T) {
	key := setupTestState(t)
	body, _ := json.Marshal(usageRecord{
		Model:       "gpt-5.5",
		AuthID:      key.ID,
		RequestedAt: time.Now(),
		ServiceTier: "auto",
		Detail: usageDetail{
			InputTokens:  100,
			OutputTokens: 10,
			TotalTokens:  110,
		},
	})
	if _, err := usageHandle(body); err != nil {
		t.Fatal(err)
	}
	events, err := loadedStore().RecentEvents(context.Background(), key.ID, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	if events[0].BillingPending || events[0].Failed || events[0].Cost <= 0 || events[0].ServiceTier != "auto" || events[0].StatusCode != 0 {
		t.Fatalf("auto tier should use normal base pricing: %#v", events[0])
	}
	keys, err := loadedStore().ListKeys(context.Background())
	if err != nil || len(keys) != 1 || keys[0].BillingHoldReason != "" {
		t.Fatalf("auto tier should not create a billing hold: keys=%#v err=%v", keys, err)
	}
}

func TestUsageHandleSkipsUnsupportedFastTierForQuotaKey(t *testing.T) {
	key := setupTestState(t)
	pricePath := createCPAMPPriceDB(t, map[string]policyplus.ModelPrice{
		"gpt-5.5": {Model: "gpt-5.5", InputPerMillion: 1, OutputPerMillion: 10, CachePerMillion: 0.1},
	})
	state.mu.Lock()
	state.cfg.CPAMPPriceDBPath = pricePath
	state.priceBook = policyplus.PriceBook{}
	state.priceBookChecked = time.Time{}
	state.mu.Unlock()
	body, _ := json.Marshal(usageRecord{
		Model:       "gpt-5.5",
		AuthID:      key.ID,
		RequestedAt: time.Now(),
		ServiceTier: "fast",
		Detail: usageDetail{
			InputTokens:  100000,
			OutputTokens: 10000,
			TotalTokens:  110000,
		},
	})
	if _, err := usageHandle(body); err != nil {
		t.Fatal(err)
	}
	events, err := loadedStore().RecentEvents(context.Background(), key.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || !events[0].BillingPending || events[0].Cost != 0 || events[0].CostBreakdown.Source != policyplus.CostSourceCPAMPBillingPending {
		t.Fatalf("events = %#v", events)
	}
	keys, err := loadedStore().ListKeys(context.Background())
	if err != nil || len(keys) != 1 || keys[0].BillingHoldReason == "" {
		t.Fatalf("unsupported Fast tier must leave a durable billing hold: keys=%#v err=%v", keys, err)
	}
	if decision := evaluatePolicy(keys[0], "gpt-5.5", false); decision.Allowed || decision.Code != "billing_pending" {
		t.Fatalf("billing hold must deny later requests: %#v", decision)
	}
	count, err := loadedStore().AuditCount(context.Background(), "billing_hold_set")
	if err != nil || count != 1 {
		t.Fatalf("unsupported Fast tier must record a durable hold audit, count=%d err=%v", count, err)
	}
}

func TestUsageHandlePersistsActualModelBillingPendingAndNarrowlyResolvesIt(t *testing.T) {
	key := setupTestState(t)
	body, _ := json.Marshal(usageRecord{
		Model:       "actual-unpriced-model",
		Alias:       "gpt-5.5",
		AuthID:      key.ID,
		RequestedAt: time.Now(),
		ServiceTier: "auto",
		Detail: usageDetail{
			InputTokens:  100,
			OutputTokens: 10,
			TotalTokens:  110,
		},
	})
	if _, err := usageHandle(body); err != nil {
		t.Fatal(err)
	}
	store := loadedStore()
	events, err := store.RecentEvents(context.Background(), key.ID, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	pending := events[0]
	if !pending.BillingPending || pending.BillingReason != "actual_model_price_unavailable" || pending.BillingModel != "actual-unpriced-model" || pending.ActualModel != "actual-unpriced-model" || pending.RequestedModel != "gpt-5.5" || pending.CostBreakdown.Source != policyplus.CostSourceCPAMPBillingPending {
		t.Fatalf("actual model drift must persist a visible pending event: %#v", pending)
	}
	summary, err := store.UsageSummary(context.Background(), key.ID, policyplus.WindowFor(policyplus.Range7D, time.Now()))
	if err != nil || summary.Calls != 1 || summary.BillingPending != 1 || summary.TotalCost != 0 {
		t.Fatalf("pending event must be explicit in summary: summary=%#v err=%v", summary, err)
	}
	keys, err := store.ListKeys(context.Background())
	if err != nil || len(keys) != 1 || keys[0].BillingHoldReason == "" || keys[0].BillingHoldModel != "actual-unpriced-model" {
		t.Fatalf("actual model drift must set durable hold: keys=%#v err=%v", keys, err)
	}
	if err := store.ImportKeys(context.Background(), policyplus.KeyPolicyState{Keys: []policyplus.KeyRecord{key}}); err != nil {
		t.Fatal(err)
	}
	keys, err = store.ListKeys(context.Background())
	if err != nil || len(keys) != 1 || keys[0].BillingHoldReason == "" {
		t.Fatalf("identity refresh must not clear billing hold: keys=%#v err=%v", keys, err)
	}
	if decision := evaluatePolicy(keys[0], "gpt-5.5", false); decision.Allowed || decision.Code != "billing_pending" {
		t.Fatalf("durable hold must stop later requests: %#v", decision)
	}

	state.mu.Lock()
	state.priceBook = policyplus.PriceBook{
		Source: policyplus.CostSourceCPAMPCachedPriceBook,
		Prices: map[string]policyplus.ModelPrice{
			"gpt-5.5":               {Model: "gpt-5.5", InputPerMillion: 1, OutputPerMillion: 10},
			"actual-unpriced-model": {Model: "actual-unpriced-model", InputPerMillion: 2, OutputPerMillion: 12},
		},
	}
	state.priceBookChecked = time.Now()
	state.mu.Unlock()
	if resolved, err := store.ResolvePendingBilling(context.Background(), key.ID, currentPriceBook(context.Background(), false)); err != nil || resolved != 1 {
		t.Fatalf("narrow pending reconciliation resolved=%d err=%v", resolved, err)
	}
	events, err = store.RecentEvents(context.Background(), key.ID, 10)
	if err != nil || len(events) != 1 || events[0].BillingPending || events[0].Cost <= 0 || events[0].CostBreakdown.Source != policyplus.CostSourceCPAMPCachedPriceBook {
		t.Fatalf("reconciled pending event = %#v err=%v", events, err)
	}
	keys, err = store.ListKeys(context.Background())
	if err != nil || len(keys) != 1 || keys[0].BillingHoldReason != "" {
		t.Fatalf("hold should clear only after narrow reconciliation: keys=%#v err=%v", keys, err)
	}
	if decision := evaluatePolicy(keys[0], "gpt-5.5", false); !decision.Allowed {
		t.Fatalf("resolved key should resume normal priced requests: %#v", decision)
	}
}

func TestAdminResolvePendingBillingEndpointUsesCachedVerifiedPrice(t *testing.T) {
	key := setupTestState(t)
	body, _ := json.Marshal(usageRecord{
		Model:       "actual-pending-model",
		Alias:       "gpt-5.5",
		AuthID:      key.ID,
		RequestedAt: time.Now(),
		ServiceTier: "auto",
		Detail: usageDetail{
			InputTokens:  100,
			OutputTokens: 10,
			TotalTokens:  110,
		},
	})
	if _, err := usageHandle(body); err != nil {
		t.Fatal(err)
	}
	store := loadedStore()
	book := policyplus.PriceBook{
		Source: policyplus.CostSourceCPAMPPriceBook,
		Prices: map[string]policyplus.ModelPrice{
			"actual-pending-model": {Model: "actual-pending-model", InputPerMillion: 2, OutputPerMillion: 12},
		},
	}
	if err := store.SaveCachedPriceBook(context.Background(), book); err != nil {
		t.Fatal(err)
	}
	raw, err := managementHandle(mustJSON(t, managementRequest{
		Method: http.MethodPut,
		Path:   "/plugins/cpa-key-policy-plus/keys/billing/resolve",
		Body:   []byte(`{"id":"alice-key"}`),
	}))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		OK       bool `json:"ok"`
		Resolved int  `json:"resolved_pending_events"`
	}
	if err := json.Unmarshal(decodeManagementBody(t, raw), &response); err != nil || !response.OK || response.Resolved != 1 {
		t.Fatalf("billing resolve response=%s parsed=%#v err=%v", raw, response, err)
	}
	events, err := store.RecentEvents(context.Background(), key.ID, 10)
	if err != nil || len(events) != 1 || events[0].BillingPending || events[0].Cost <= 0 {
		t.Fatalf("endpoint did not settle pending event: events=%#v err=%v", events, err)
	}
	keys, err := store.ListKeys(context.Background())
	if err != nil || len(keys) != 1 || keys[0].BillingHoldReason != "" {
		t.Fatalf("endpoint did not clear billing hold: keys=%#v err=%v", keys, err)
	}
}

func TestCurrentPriceBookDoesNotRepriceExistingUsage(t *testing.T) {
	key := setupTestState(t)
	store := loadedStore()
	if err := store.InsertUsage(context.Background(), policyplus.UsageEvent{
		RequestID:      "old-cost",
		KeyID:          key.ID,
		Model:          "gpt-5.5",
		RequestedModel: "gpt-5.5",
		RequestedAt:    time.Now().Add(-time.Hour),
		ServiceTier:    "priority",
		Usage: policyplus.TokenUsage{
			InputTokens:  100000,
			OutputTokens: 10000,
			TotalTokens:  110000,
		},
		Cost: 0.001,
		CostBreakdown: policyplus.CostBreakdown{
			Source: policyplus.CostSourceKeyPolicyPlusPriceBook,
			Model:  "gpt-5.5",
			Costs:  map[string]float64{"total": 0.001},
		},
	}); err != nil {
		t.Fatal(err)
	}
	pricePath := createCPAMPPriceDB(t, map[string]policyplus.ModelPrice{
		"gpt-5.5": {Model: "gpt-5.5", InputPerMillion: 1, OutputPerMillion: 10},
	})
	state.mu.Lock()
	state.cfg.CPAMPPriceDBPath = pricePath
	state.priceBook = policyplus.PriceBook{}
	state.priceBookChecked = time.Time{}
	state.mu.Unlock()
	book := currentPriceBook(context.Background(), true)
	if book.Source != policyplus.CostSourceCPAMPPriceBook {
		t.Fatalf("price book = %#v", book)
	}
	events, err := store.RecentEvents(context.Background(), key.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	if got := events[0].Cost; got != 0.001 {
		t.Fatalf("existing cost must stay frozen, got %.9f event=%#v", got, events[0])
	}
	if events[0].CostBreakdown.Source != policyplus.CostSourceKeyPolicyPlusPriceBook {
		t.Fatalf("existing breakdown must stay frozen: %#v", events[0].CostBreakdown)
	}
}

func TestVisibleUsageModelDoesNotGuessForMultiModelKeys(t *testing.T) {
	key := policyplus.KeyRecord{ID: "key-1", Models: []string{"gpt-5.4", "gpt-5.5"}}
	got := visibleUsageModel(key, usageRecord{Model: "provider-internal-unknown"})
	if got != "provider-internal-unknown" {
		t.Fatalf("multi-model key should not guess visible alias, got %q", got)
	}
	got = visibleUsageModel(key, usageRecord{Model: "gpt-5.3-codex-spark"})
	if got != "gpt-5.3-codex-spark" {
		t.Fatalf("reported model must not use a stale display alias, got %q", got)
	}
	got = visibleUsageModel(key, usageRecord{Model: "gpt-5.3-codex-spark", Alias: "gpt-5.3-codex-spark"})
	if got != "gpt-5.3-codex-spark" {
		t.Fatalf("reported alias must remain visible, got %q", got)
	}
	got = visibleUsageModel(key, usageRecord{Model: "gpt-5.3-codex-spark", Alias: "gpt-5.4"})
	if got != "gpt-5.4" {
		t.Fatalf("explicit alias should win, got %q", got)
	}
}

func TestAdminModelsFallsBackToConfiguredModels(t *testing.T) {
	key := setupTestState(t)
	key.Models = []string{"gpt-5.5", "legacy-custom"}
	key.Prices = map[string]policyplus.ModelPrice{
		"price-only": {Model: "price-only", InputPerMillion: 1},
	}
	if err := loadedStore().SaveKeySettings(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	raw, err := adminModels(managementRequest{})
	if err != nil {
		t.Fatal(err)
	}
	body := decodeManagementBody(t, raw)
	text := string(body)
	for _, want := range []string{"gpt-5.5", "legacy-custom", "price-only"} {
		if !strings.Contains(text, want) {
			t.Fatalf("model catalog missing %s: %s", want, text)
		}
	}
}

func TestAdminModelsPrefersCPARegistryCatalog(t *testing.T) {
	key := setupTestState(t)
	key.Models = []string{"manual-custom"}
	if err := loadedStore().SaveKeySettings(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	oldHostCall := hostCall
	t.Cleanup(func() { hostCall = oldHostCall })
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		switch method {
		case methodHostModelsList:
			return json.Marshal(hostModelsListResponse{Models: []hostModelListEntry{
				{ID: "gpt-5.5", DisplayName: "Registry GPT 5.5", Type: "codex", OwnedBy: "openai", Provider: "codex", AuthID: "codex-auth", AuthName: "codex.json"},
			}})
		case methodHostAuthList:
			return json.Marshal(map[string]any{"files": []any{
				map[string]any{
					"models": []any{
						map[string]any{"id": "gpt-5.5", "display_name": "Host GPT 5.5"},
						"host-only",
					},
				},
			}})
		default:
			return nil, fmt.Errorf("unexpected host callback %s", method)
		}
	}

	raw, err := adminModels(managementRequest{})
	if err != nil {
		t.Fatal(err)
	}
	body := decodeManagementBody(t, raw)
	var resp struct {
		OK       bool                     `json:"ok"`
		Models   []policyplus.ModelOption `json:"models"`
		Warnings []string                 `json:"warnings"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode models response: %v: %s", err, body)
	}
	byID := map[string]policyplus.ModelOption{}
	for _, model := range resp.Models {
		byID[model.ID] = model
	}
	got := byID["gpt-5.5"]
	if got.Source != "cpa_registry" || got.DisplayName != "Registry GPT 5.5" || got.Provider != "codex" || got.AuthName != "codex.json" {
		t.Fatalf("registry model metadata should win, got %#v", got)
	}
	if byID["host-only"].Source != "host_auth" {
		t.Fatalf("host-only model should be preserved from host auth: %#v", byID["host-only"])
	}
	if byID["manual-custom"].Source != "plus_configured" || byID["manual-custom"].Known {
		t.Fatalf("configured unknown model should be preserved: %#v", byID["manual-custom"])
	}
	if len(resp.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %#v", resp.Warnings)
	}
	keys, err := loadedStore().ListKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var after policyplus.KeyRecord
	for _, item := range keys {
		if item.ID == key.ID {
			after = item
			break
		}
	}
	if len(after.Models) != 1 || after.Models[0] != "manual-custom" {
		t.Fatalf("model discovery must not mutate key allowlist: %#v", after.Models)
	}
}

func TestAdminModelsFallsBackToLocalCPAAPIAndRefreshesAllProviders(t *testing.T) {
	setupTestState(t)
	const apiKey = "sk-local-cpa-models"
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		requestCount++
		if requestCount == 1 {
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-provider-model","owned_by":"openai","type":"openai"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-provider-model","owned_by":"openai","type":"openai"},{"id":"grok-4.3","display_name":"Grok 4.3","owned_by":"xai","type":"xai"},{"id":"claude-provider-model","owned_by":"anthropic","type":"claude"},{"id":"gemini-provider-model","owned_by":"google","type":"gemini"},{"id":"groq-provider-model","owned_by":"groq","type":"openai"}]}`))
	}))
	t.Cleanup(server.Close)
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: "+port+"\napi-keys:\n  - "+apiKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.cfg.NativeKeysConfigPath = configPath
	state.mu.Unlock()

	oldHostCall := hostCall
	t.Cleanup(func() { hostCall = oldHostCall })
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		switch method {
		case methodHostModelsList:
			return nil, fmt.Errorf("unsupported host callback")
		case methodHostAuthList:
			return json.Marshal(map[string]any{"files": []any{}})
		default:
			return nil, fmt.Errorf("unexpected host callback %s", method)
		}
	}

	firstModels, warnings := adminModelCatalog()
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}
	firstByID := map[string]policyplus.ModelOption{}
	for _, model := range firstModels {
		firstByID[model.ID] = model
	}
	if firstByID["gpt-provider-model"].Source != "cpa_registry" || !firstByID["gpt-provider-model"].Known {
		t.Fatalf("first CPA model catalog missing provider model: %#v", firstByID)
	}
	if _, exists := firstByID["grok-4.3"]; exists {
		t.Fatalf("first CPA model catalog unexpectedly contains later model: %#v", firstByID)
	}

	refreshedModels, refreshedWarnings := adminModelCatalog()
	if len(refreshedWarnings) != 0 {
		t.Fatalf("unexpected refresh warnings: %#v", refreshedWarnings)
	}
	refreshedByID := map[string]policyplus.ModelOption{}
	for _, model := range refreshedModels {
		refreshedByID[model.ID] = model
	}
	for _, id := range []string{"gpt-provider-model", "grok-4.3", "claude-provider-model", "gemini-provider-model", "groq-provider-model"} {
		model, exists := refreshedByID[id]
		if !exists || model.Source != "cpa_registry" || !model.Known {
			t.Fatalf("refreshed CPA catalog missing provider model %q: %#v", id, refreshedByID)
		}
	}
	if refreshedByID["grok-4.3"].OwnedBy != "xai" || refreshedByID["groq-provider-model"].OwnedBy != "groq" {
		t.Fatalf("refreshed CPA catalog lost provider metadata: %#v", refreshedByID)
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
	for _, want := range []string{"cpa_ Key 已停用", "这是 Key 预览", "只支持 sk- 开头的 Key"} {
		if !strings.Contains(html, want) {
			t.Fatalf("user page missing native-key guidance %q", want)
		}
	}
}

func TestUserHTMLUsesWeeklyPrimaryRangeForWeeklyOnlyKeys(t *testing.T) {
	html := userHTML()
	for _, removed := range []string{`id="rangeSelect"`, "rangeSelect", "state.range"} {
		if strings.Contains(html, removed) {
			t.Fatalf("user page should not keep mutable range control %q", removed)
		}
	}
	for _, want := range []string{
		"function primaryRange()",
		"state.me.quota_mode === \"weekly_only\" ? \"7d\" : \"24h\"",
		"function visibleQuotaWindows()",
		"const range = primaryRange();",
		"visibleQuotaWindows().map",
		"5 小时额度",
		"24 小时额度",
		"7 天额度",
		"本月额度",
		"未设置上限",
		"接近上限",
		"已超限",
		"api(`/usage?range=${encodeURIComponent(range)}`",
		"api(`/events?range=${encodeURIComponent(range)}&limit=100`",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("user fixed-range page missing %q", want)
		}
	}
	for _, removed := range []string{"24H / 7D", "5H / 本月"} {
		if strings.Contains(html, removed) {
			t.Fatalf("user fixed-range page should not keep combined quota card %q", removed)
		}
	}
}

func TestUserHTMLIgnoresRefreshCancelNoise(t *testing.T) {
	html := userHTML()
	for _, want := range []string{
		"function isRefreshCancel",
		`abort("refresh_cancelled")`,
		`controller.abort("refresh_timeout")`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("user refresh cancel handling missing %q", want)
		}
	}
	usageCancel := strings.Index(html, "if (isRefreshCancel(controller.signal)) return;")
	usageError := strings.Index(html, `state.errors.usage = "用量同步失败："`)
	if usageCancel < 0 || usageError < 0 || usageCancel > usageError {
		t.Fatal("usage refresh cancel must be ignored before setting a sync error")
	}
}

func TestUserHTMLOmitsRetiredCodexContViewsAndPolling(t *testing.T) {
	html := userHTML()
	for _, forbidden := range []string{
		"function roundSummaryText",
		"思维链保护",
		"fetchProtection",
		"state.protection",
		"/codexcont",
		"data-tab=\"codex\"",
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("retired CodexCont UI must not remain in user page: %q", forbidden)
		}
	}
}

func TestCodexRequestsDoNotReadExecutorStoreWhenDisabled(t *testing.T) {
	key := setupTestState(t)
	state.mu.Lock()
	state.cfg.CodexContEnabled = false
	state.cfg.CodexSummaryDBPath = filepath.Join(t.TempDir(), "missing", "executor.sqlite")
	state.mu.Unlock()
	requests, source := codexRequestsForKey(key, 10)
	if source != "disabled" || len(requests) != 0 {
		t.Fatalf("disabled CodexCont history should not read executor state: source=%s requests=%#v", source, requests)
	}
	status := codexcontStatus()
	if status["mode"] != "disabled" || status["history_source"] != "disabled" || status["health_ok"] != nil || status["url"] != nil {
		t.Fatalf("disabled CodexCont status should not expose offline probe state: %#v", status)
	}
}

func TestCodexRequestsPreferExecutorSummaryBridge(t *testing.T) {
	key := setupTestState(t)
	execPath := filepath.Join(t.TempDir(), "executor.sqlite")
	execStore, err := policyplus.OpenStore(execPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := execStore.SaveCodexSummary(context.Background(), "exec-a", key.ID, "gpt-5.5", "auto_continued", map[string]any{
		"request_id":         "exec-a",
		"model":              "gpt-5.5",
		"protection":         "auto_continued",
		"key_identity":       map[string]any{"known": true, "id": key.ID, "preview": key.Preview},
		"continuation_count": 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := execStore.SaveCodexSummary(context.Background(), "exec-b", "bob-key", "gpt-5.5", "protected_clean", map[string]any{
		"request_id":   "exec-b",
		"model":        "gpt-5.5",
		"protection":   "protected_clean",
		"key_identity": map[string]any{"known": true, "id": "bob-key"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := execStore.Close(); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.cfg.CodexSummaryDBPath = execPath
	state.cfg.CodexContEnabled = true
	state.mu.Unlock()
	requests, source := codexRequestsForKey(key, 10)
	if source != "codexcont_executor_store" || len(requests) != 1 || requests[0]["request_id"] != "exec-a" {
		t.Fatalf("source=%s requests=%#v", source, requests)
	}
}

func TestCodexRequestsProjectSafeExecutorDetailFields(t *testing.T) {
	key := setupTestState(t)
	execPath := filepath.Join(t.TempDir(), "executor.sqlite")
	execStore, err := policyplus.OpenStore(execPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := execStore.SaveCodexSummary(context.Background(), "exec-detail", key.ID, "gpt-5.5", "auto_continued", map[string]any{
		"request_id":                        "exec-detail",
		"model":                             "gpt-5.5",
		"status":                            "completed",
		"final_status":                      "completed",
		"protection":                        "auto_continued",
		"key_identity":                      map[string]any{"known": true, "id": key.ID, "preview": key.Preview},
		"folded":                            true,
		"passthrough":                       false,
		"first_truncation_round":            1,
		"first_truncation_reasoning_tokens": 516,
		"first_truncation_n":                1,
		"first_truncation_decision":         "continue",
		"latest_reasoning_tokens":           94,
		"continuation_count":                3,
		"failure_category":                  "upstream_transport_eof",
		"failure_detail":                    "safe diagnostic",
		"route_decision": map[string]any{
			"protected":    true,
			"mode":         "protect_all",
			"reason":       "protect_all",
			"model":        "gpt-5.5",
			"key_scope":    "alice-safe-alias",
			"body_bytes":   "Authorization: Bearer sk-should-not-leak",
			"max_continue": 8,
		},
		"diagnostics": map[string]any{
			"rounds": []map[string]any{
				{
					"round":                1,
					"model":                "gpt-5.5",
					"requested_model":      "gpt-5.5",
					"open_error_category":  "upstream_transport_eof",
					"open_error":           `Post "https://chatgpt.com/backend-api/codex/responses": EOF`,
					"authorization":        "Bearer sk-should-not-leak",
					"encrypted_content":    "should-not-leak",
					"raw_request_body":     "should-not-leak",
					"last_read_error":      "EOF",
					"last_read_error_body": "should-not-leak",
				},
			},
		},
		"rounds": []map[string]any{
			{"round": 1, "reasoning_tokens": 516, "decision": "continue"},
			{"round": 4, "reasoning_tokens": 94, "decision": "clean"},
		},
		"authorization":     "Bearer sk-should-not-leak",
		"encrypted_content": "should-not-leak",
		"request_body":      "should-not-leak",
		"response_body":     "should-not-leak",
		"full_hash":         strings.Repeat("a", 64),
	}); err != nil {
		t.Fatal(err)
	}
	if err := execStore.Close(); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.cfg.CodexSummaryDBPath = execPath
	state.cfg.CodexContEnabled = true
	state.mu.Unlock()
	requests, source := codexRequestsForKey(key, 10)
	if source != "codexcont_executor_store" || len(requests) != 1 {
		t.Fatalf("source=%s requests=%#v", source, requests)
	}
	got := requests[0]
	for _, field := range []string{"final_status", "folded", "passthrough", "first_truncation_n", "failure_category", "failure_detail", "rounds", "diagnostics_brief", "route_decision"} {
		if _, ok := got[field]; !ok {
			t.Fatalf("projected summary missing %s: %#v", field, got)
		}
	}
	routeDecision, _ := got["route_decision"].(map[string]any)
	if routeDecision["reason"] != "protect_all" || routeDecision["key_scope"] != "alice-safe-alias" || routeDecision["body_bytes"] != nil {
		t.Fatalf("route decision projection = %#v", routeDecision)
	}
	brief, _ := got["diagnostics_brief"].([]map[string]any)
	if len(brief) != 1 || brief[0]["open_error_category"] != "upstream_transport_eof" || brief[0]["authorization"] != nil {
		t.Fatalf("diagnostics brief projection = %#v", got["diagnostics_brief"])
	}
	rounds, _ := got["rounds"].([]any)
	if len(rounds) != 2 {
		t.Fatalf("rounds projection = %#v", got["rounds"])
	}
	raw, _ := json.Marshal(got)
	for _, forbidden := range []string{"sk-should-not-leak", "encrypted_content", "request_body", "response_body", strings.Repeat("a", 64)} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("unsafe field leaked in projection: %s", raw)
		}
	}
}

func TestCodexRequestsAllowExecutorPreviewMatchAndHideOtherKeys(t *testing.T) {
	key := setupTestState(t)
	execPath := filepath.Join(t.TempDir(), "executor.sqlite")
	execStore, err := policyplus.OpenStore(execPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := execStore.SaveCodexSummary(context.Background(), "exec-preview", key.ID, "gpt-5.5", "protected_clean", map[string]any{
		"request_id":   "exec-preview",
		"model":        "gpt-5.5",
		"protection":   "protected_clean",
		"key_identity": map[string]any{"known": true, "preview": key.Preview},
	}); err != nil {
		t.Fatal(err)
	}
	if err := execStore.SaveCodexSummary(context.Background(), "exec-other-preview", "other-key", "gpt-5.5", "auto_continued", map[string]any{
		"request_id":   "exec-other-preview",
		"model":        "gpt-5.5",
		"protection":   "auto_continued",
		"key_identity": map[string]any{"known": true, "id": "other-key", "preview": "other...preview"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := execStore.Close(); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.cfg.CodexSummaryDBPath = execPath
	state.cfg.CodexContEnabled = true
	state.mu.Unlock()
	requests, source := codexRequestsForKey(key, 10)
	if source != "codexcont_executor_store" || len(requests) != 1 || requests[0]["request_id"] != "exec-preview" {
		t.Fatalf("source=%s requests=%#v", source, requests)
	}
}

func TestCodexRequestsIncludeCurrentKeyProcessingFromExecutorStore(t *testing.T) {
	key := setupTestState(t)
	execPath := filepath.Join(t.TempDir(), "executor.sqlite")
	execStore, err := policyplus.OpenStore(execPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := execStore.SaveCodexSummary(context.Background(), "processing-live", key.ID, "gpt-5.5", "processing", map[string]any{
		"request_id":   "processing-live",
		"model":        "gpt-5.5",
		"started_at":   now.Add(-20 * time.Second).Format(time.RFC3339Nano),
		"updated_at":   now.Format(time.RFC3339Nano),
		"status":       "processing",
		"protection":   "processing",
		"key_identity": map[string]any{"known": true, "id": key.ID, "preview": key.Preview},
	}); err != nil {
		t.Fatal(err)
	}
	if err := execStore.Close(); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.cfg.CodexSummaryDBPath = execPath
	state.cfg.CodexContEnabled = true
	state.mu.Unlock()
	requests, source := codexRequestsForKey(key, 10)
	if source != "codexcont_executor_store" || len(requests) != 1 || requests[0]["request_id"] != "processing-live" || requests[0]["protection"] != "processing" {
		t.Fatalf("source=%s requests=%#v", source, requests)
	}
}

func TestCodexRequestsHideOtherKeyProcessingFromExecutorStore(t *testing.T) {
	key := setupTestState(t)
	execPath := filepath.Join(t.TempDir(), "executor.sqlite")
	execStore, err := policyplus.OpenStore(execPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := execStore.SaveCodexSummary(context.Background(), "processing-other", "other-key", "gpt-5.5", "processing", map[string]any{
		"request_id":   "processing-other",
		"model":        "gpt-5.5",
		"updated_at":   now.Format(time.RFC3339Nano),
		"status":       "processing",
		"protection":   "processing",
		"key_identity": map[string]any{"known": true, "id": "other-key", "preview": "other...key"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := execStore.Close(); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.cfg.CodexSummaryDBPath = execPath
	state.cfg.CodexContEnabled = true
	state.mu.Unlock()
	requests, source := codexRequestsForKey(key, 10)
	if source != "codexcont_executor_store" || len(requests) != 0 {
		t.Fatalf("other-key processing should stay hidden: source=%s requests=%#v", source, requests)
	}
}

func TestCodexRequestsHideStaleProcessingFromExecutorStore(t *testing.T) {
	key := setupTestState(t)
	execPath := filepath.Join(t.TempDir(), "executor.sqlite")
	execStore, err := policyplus.OpenStore(execPath)
	if err != nil {
		t.Fatal(err)
	}
	stale := time.Now().UTC().Add(-codexProcessingStaleAfter - time.Minute)
	if err := execStore.SaveCodexSummary(context.Background(), "processing-stale", key.ID, "gpt-5.5", "processing", map[string]any{
		"request_id":   "processing-stale",
		"model":        "gpt-5.5",
		"started_at":   stale.Add(-time.Minute).Format(time.RFC3339Nano),
		"updated_at":   stale.Format(time.RFC3339Nano),
		"status":       "processing",
		"protection":   "processing",
		"key_identity": map[string]any{"known": true, "id": key.ID, "preview": key.Preview},
	}); err != nil {
		t.Fatal(err)
	}
	if err := execStore.Close(); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.cfg.CodexSummaryDBPath = execPath
	state.cfg.CodexContEnabled = true
	state.mu.Unlock()
	requests, source := codexRequestsForKey(key, 10)
	if source != "codexcont_executor_store" || len(requests) != 0 {
		t.Fatalf("stale processing should be hidden: source=%s requests=%#v", source, requests)
	}
}

func TestCodexRequestsExecutorBridgeEmptyCurrentKeyIsNotError(t *testing.T) {
	key := setupTestState(t)
	execPath := filepath.Join(t.TempDir(), "executor.sqlite")
	execStore, err := policyplus.OpenStore(execPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := execStore.SaveCodexSummary(context.Background(), "exec-other", "other-key", "gpt-5.5", "protected_clean", map[string]any{
		"request_id":   "exec-other",
		"model":        "gpt-5.5",
		"protection":   "protected_clean",
		"key_identity": map[string]any{"known": true, "id": "other-key"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := execStore.Close(); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.cfg.CodexSummaryDBPath = execPath
	state.cfg.CodexContEnabled = true
	state.mu.Unlock()
	requests, source := codexRequestsForKey(key, 10)
	if source != "codexcont_executor_store" || len(requests) != 0 {
		t.Fatalf("empty current-key executor feed should not be an error: source=%s requests=%#v", source, requests)
	}
}

func TestCodexRequestsExecutorBridgeUnavailableIsExplicit(t *testing.T) {
	key := setupTestState(t)
	state.mu.Lock()
	state.cfg.CodexSummaryDBPath = filepath.Join(t.TempDir(), "missing", "executor.sqlite")
	state.cfg.CodexContEnabled = true
	state.mu.Unlock()
	requests, source := codexRequestsForKey(key, 10)
	if source != "codexcont_executor_store_unavailable" || len(requests) != 0 {
		t.Fatalf("unavailable executor store should fail soft with explicit source: source=%s requests=%#v", source, requests)
	}
}

func TestFrontendAuthMetadataIncludesSafeAlias(t *testing.T) {
	key := setupTestState(t)
	key.Alias = "alicea"
	if err := loadedStore().UpsertKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	raw, err := frontendAuth(mustJSON(t, frontendAuthRequest{
		Path:    "/v1/responses",
		Headers: http.Header{"Authorization": []string{"Bearer sk-alice-secret"}},
		Body:    []byte(`{"model":"gpt-5.5"}`),
	}))
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
	if !resp.Authenticated || resp.Metadata["key_alias"] != "alicea" || resp.Metadata["key_id"] != key.ID {
		t.Fatalf("frontend auth metadata missing safe identity: %#v", resp)
	}
	encoded, _ := json.Marshal(resp.Metadata)
	if strings.Contains(string(encoded), "sk-alice-secret") || strings.Contains(string(encoded), key.KeyHash) {
		t.Fatalf("frontend auth metadata leaked key material: %s", encoded)
	}
}

func TestAdminHTMLHasRenderedSharedCSS(t *testing.T) {
	html := adminHTML()
	if strings.Contains(html, "{{CSS}}") || strings.Contains(html, "{{SHARED_CSS}}") {
		t.Fatalf("admin html still has css placeholder")
	}
	if !strings.Contains(html, "--kp-line") || !strings.Contains(html, "CPA Key Policy+") {
		t.Fatal("admin html should embed shared style and key policy UI")
	}
	if strings.Contains(html, "/v0/resource/plugins/cpa-key-policy-plus/admin/api") {
		t.Fatal("admin html must not send mutating requests through GET-only resource routes")
	}
	if !strings.Contains(html, "/key-policy-plus/api") || !strings.Contains(html, "编辑模型白名单") {
		t.Fatal("admin html should use the management alias and structured model editor")
	}
	for _, removed := range []string{"请求并发", "Codex窗口", "显示归档", "归档隐藏", "恢复 Key"} {
		if strings.Contains(html, removed) {
			t.Fatalf("admin html should not contain retired control %q", removed)
		}
	}
	for _, removed := range []string{"新建 Key", "删除 Key", "复制完整 Key", "raw-key", "/keys/create", "/keys/delete"} {
		if strings.Contains(html, removed) {
			t.Fatalf("admin html should not expose Plus-side key lifecycle control %q", removed)
		}
	}
	for _, want := range []string{"Key 策略", "保存策略", "当前 Key", "需补策略", "配额", "套用 CPA 发现模型", "CPA 发现", "未在 CPAMP 配置价格", "cpa_registry", "${models.length} 个模型", "允许全部模型", "is-busy", "is-done"} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin html missing native policy UI marker %q", want)
		}
	}
	for _, removed := range []string{"新原生 Key 默认禁用", "新原生 Key 默认启用", "策略总数", "显示官方已移除", "include_removed", "CPA 原生", "已继承", "已计价", "quota-mini", "mini-grid", "用当前发现模型替换", "编辑模型/价格", "模型与价格", "price-table", "price-body"} {
		if strings.Contains(html, removed) {
			t.Fatalf("admin html should not advertise old/noisy policy UI marker %q", removed)
		}
	}
}

func TestSessionInvalidatedWhenKeyHashChanges(t *testing.T) {
	key := setupTestState(t)
	req := managementRequest{Headers: http.Header{"X-CPA-Key-Policy-Plus-Key": []string{"sk-alice-secret"}}}
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
	key.KeyHash = "sha256:" + policyplus.SHA256Hex("sk-rotated-secret")
	if err := loadedStore().UpsertKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, ok := keyFromSession(managementRequest{Headers: http.Header{"Cookie": []string{cookie}}}); ok {
		t.Fatal("old session should not survive key rotation")
	}
}

func TestSessionCookiePathCompatibility(t *testing.T) {
	setupTestState(t)
	raw, err := userSession(managementRequest{Headers: http.Header{"X-CPA-Key-Policy-Plus-Key": []string{"sk-alice-secret"}}})
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
	cookies := resp.Headers.Values("Set-Cookie")
	for _, want := range []string{
		"Path=/;",
		"Path=/v0/resource/plugins/cpa-key-policy-plus/user;",
		"Path=/key-policy-plus-user;",
	} {
		if !strings.Contains(strings.Join(cookies, "\n"), want) {
			t.Fatalf("session response should set compatible cookie path %s: %#v", want, cookies)
		}
	}
	valid := ""
	for _, rawCookie := range cookies {
		if strings.Contains(rawCookie, "Path=/;") {
			valid = strings.SplitN(rawCookie, ";", 2)[0]
			break
		}
	}
	if valid == "" {
		t.Fatalf("root session cookie not found: %#v", cookies)
	}
	staleFirst := "cpa_key_policy_plus_session=stale-invalid-token; " + valid
	if _, ok := keyFromSession(managementRequest{Headers: http.Header{"Cookie": []string{staleFirst}}}); !ok {
		t.Fatal("fresh session should resolve even when a stale path-specific cookie is sent first")
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
	return decodeManagementResponse(t, raw).Body
}

func decodeManagementResponse(t *testing.T, raw []byte) managementResponse {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var resp managementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
