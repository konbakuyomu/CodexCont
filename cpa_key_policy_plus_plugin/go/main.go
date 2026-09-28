package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"codexcont/cpa-key-policy-plus-plugin/internal/policyplus"
	_ "embed"
	"gopkg.in/yaml.v3"
)

const (
	pluginID                     = "cpa-key-policy-plus"
	plusSessionCookieName        = "cpa_key_policy_plus_session"
	executorModelScopeBoth       = "both"
	executorFormatOpenAIResponse = "openai-response"
	policyDenyMetadataPrefix     = "policy_deny_"
	codexProcessingStaleAfter    = 15 * time.Minute
	priceBookRefreshTTL          = 30 * time.Second
)

//go:embed assets/admin.html
var adminHTMLTemplate string

//go:embed assets/user.html
var userHTMLTemplate string

//go:embed assets/shared.css
var sharedCSSTemplate string

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32       `json:"schema_version"`
	Metadata      metadata     `json:"metadata"`
	Capabilities  capabilities `json:"capabilities"`
}

type metadata struct {
	Name             string        `json:"Name"`
	Version          string        `json:"Version"`
	Author           string        `json:"Author"`
	GitHubRepository string        `json:"GitHubRepository"`
	ConfigFields     []configField `json:"ConfigFields"`
}

type capabilities struct {
	FrontendAuthProvider          bool     `json:"frontend_auth_provider"`
	FrontendAuthProviderExclusive bool     `json:"frontend_auth_provider_exclusive"`
	ModelRouter                   bool     `json:"model_router"`
	RequestInterceptor            bool     `json:"request_interceptor"`
	Executor                      bool     `json:"executor"`
	ExecutorModelScope            string   `json:"executor_model_scope"`
	ExecutorInputFormats          []string `json:"executor_input_formats"`
	ExecutorOutputFormats         []string `json:"executor_output_formats"`
	UsagePlugin                   bool     `json:"usage_plugin"`
	ManagementAPI                 bool     `json:"management_api"`
}

type runtimeState struct {
	mu                sync.RWMutex
	cfg               policyplus.Config
	store             *policyplus.Store
	keyState          policyplus.KeyPolicyState
	keyStatePath      string
	keyStateModTime   time.Time
	keyStateLastCheck time.Time
	rpmBuckets        map[string][]time.Time
	priceBook         policyplus.PriceBook
	priceBookChecked  time.Time
	lastConfigError   string
	lastConfigErrorAt time.Time
}

type policyDecision struct {
	Allowed    bool
	StatusCode int
	Type       string
	Code       string
	Message    string
	Param      string
	Window     string
	UsedUSD    float64
	LimitUSD   float64
	UsedCount  int
	LimitCount int
	KeyID      string
	KeyName    string
}

var state = runtimeState{
	rpmBuckets: map[string][]time.Time{},
}

func main() { runPreviewIfRequested() }

func runPreviewIfRequested() {
	addr := strings.TrimSpace(os.Getenv("CPA_KEY_POLICY_PLUS_PREVIEW_ADDR"))
	if addr == "" {
		return
	}
	cfg := policyplus.DefaultConfig()
	previewDir, err := os.MkdirTemp("", "cpa-key-policy-plus-preview-*")
	if err != nil {
		panic(err)
	}
	cfg.StateDBPath = previewDir + "/policyplus.sqlite"
	cfg.SessionSecret = "preview-secret"
	cfg.CodexContEnabled = true
	cfg.CodexContURL = "http://" + addr
	store, err := policyplus.OpenStore(cfg.StateDBPath)
	if err != nil {
		panic(err)
	}
	previewRawKey := "sk-preview-abcdefghijklmnopqrstuvwxyz0123456789AB"
	previewKey := policyplus.KeyRecord{
		ID:              "preview-key",
		Name:            "演示用户",
		KeyHash:         "sha256:" + policyplus.SHA256Hex(previewRawKey),
		Enabled:         true,
		Preview:         policyplus.HashPreview(policyplus.SHA256Hex(previewRawKey)),
		Source:          policyplus.NativeCPASource,
		SourcePresent:   true,
		RPM:             60,
		Concurrency:     0,
		Models:          []string{"gpt-5.5", "gpt-5.4"},
		FiveHourUSD:     floatPtr(2),
		DailyLimitUSD:   floatPtr(8),
		WeeklyLimitUSD:  floatPtr(40),
		MonthlyLimitUSD: floatPtr(120),
		Prices: map[string]policyplus.ModelPrice{
			"gpt-5.5": {Model: "gpt-5.5", InputPerMillion: 5, OutputPerMillion: 30, CacheReadPerMillion: 0.5},
		},
	}
	disabledKey := policyplus.KeyRecord{
		ID:                "preview-disabled",
		Name:              "禁用演示",
		KeyHash:           "sha256:" + policyplus.SHA256Hex("sk-preview-disabled-abcdefghijklmnopqrstuvwxyz0123"),
		Enabled:           false,
		Preview:           policyplus.HashPreview(policyplus.SHA256Hex("sk-preview-disabled-abcdefghijklmnopqrstuvwxyz0123")),
		Source:            policyplus.NativeCPASource,
		SourcePresent:     true,
		RPM:               30,
		Concurrency:       0,
		MaxActiveSessions: 0,
		Models:            []string{"gpt-5.4-mini"},
		DailyLimitUSD:     floatPtr(3),
	}
	noLimitKey := policyplus.KeyRecord{
		ID:                "preview-no-limit",
		Name:              "无限额演示",
		KeyHash:           "sha256:" + policyplus.SHA256Hex("sk-preview-nolimit-abcdefghijklmnopqrstuvwxyz0123"),
		Enabled:           true,
		Preview:           policyplus.HashPreview(policyplus.SHA256Hex("sk-preview-nolimit-abcdefghijklmnopqrstuvwxyz0123")),
		Source:            policyplus.NativeCPASource,
		SourcePresent:     true,
		RPM:               0,
		Concurrency:       0,
		MaxActiveSessions: 0,
	}
	_ = store.UpsertKey(context.Background(), previewKey)
	_ = store.UpsertKey(context.Background(), disabledKey)
	_ = store.UpsertKey(context.Background(), noLimitKey)
	// Illustrative preview prices so the pages render a synced price book.
	_ = store.SaveCachedPriceBook(context.Background(), policyplus.PriceBook{
		Source:   policyplus.CostSourceCPAMPPriceBook,
		LoadedAt: time.Now(),
		Prices: map[string]policyplus.ModelPrice{
			"gpt-5.5":      {Model: "gpt-5.5", InputPerMillion: 5, OutputPerMillion: 30, CacheReadPerMillion: 0.5},
			"gpt-5.4":      {Model: "gpt-5.4", InputPerMillion: 2.5, OutputPerMillion: 15, CacheReadPerMillion: 0.25},
			"gpt-5.4-mini": {Model: "gpt-5.4-mini", InputPerMillion: 0.75, OutputPerMillion: 4.5, CacheReadPerMillion: 0.075},
		},
	})
	_ = store.InsertUsage(context.Background(), policyplus.UsageEvent{
		RequestID:       "req-preview-a",
		KeyID:           previewKey.ID,
		KeyPreview:      previewKey.Preview,
		Model:           "gpt-5.5",
		RequestedModel:  "gpt-5.5",
		ActualModel:     "gpt-5.5",
		Provider:        "openai",
		ExecutorType:    "codex",
		Endpoint:        "/v1/responses",
		RequestedAt:     time.Now().Add(-3 * time.Minute),
		LatencyMS:       14320,
		TTFTMS:          1180,
		ReasoningEffort: "high",
		ServiceTier:     "default",
		StatusCode:      200,
		Usage: policyplus.TokenUsage{
			InputTokens:     120000,
			CachedTokens:    103000,
			OutputTokens:    2100,
			ReasoningTokens: 516,
			TotalTokens:     122100,
		},
		Cost: 0.151,
		CostBreakdown: policyplus.CostForUsage(previewKey.Prices["gpt-5.5"], policyplus.TokenUsage{
			InputTokens:     120000,
			CachedTokens:    103000,
			OutputTokens:    2100,
			ReasoningTokens: 516,
			TotalTokens:     122100,
		}, "gpt-5.5"),
	})
	_ = store.SaveCodexSummary(context.Background(), "req-preview-a", previewKey.ID, "gpt-5.5", "auto_continued", map[string]any{
		"request_id":                        "req-preview-a",
		"model":                             "gpt-5.5",
		"path":                              "/v1/responses",
		"started_at":                        time.Now().Add(-2 * time.Minute).Format(time.RFC3339),
		"updated_at":                        time.Now().Add(-90 * time.Second).Format(time.RFC3339),
		"duration_ms":                       5570,
		"status":                            "completed",
		"protection":                        "auto_continued",
		"key_identity":                      previewKey.Safe(),
		"latest_round":                      2,
		"latest_reasoning_tokens":           181,
		"first_truncation_round":            1,
		"first_truncation_reasoning_tokens": 516,
		"first_truncation_decision":         "continue",
		"continuation_count":                1,
		"stopped_reason":                    "completed",
		"rounds":                            []map[string]any{{"round": 1, "reasoning_tokens": 516, "decision": "continue", "truncation_match": true}, {"round": 2, "reasoning_tokens": 181, "decision": "clean", "truncation_match": false}},
	})
	_ = store.InsertUsage(context.Background(), policyplus.UsageEvent{
		RequestID:   "req-preview-disabled",
		KeyID:       disabledKey.ID,
		KeyPreview:  disabledKey.Preview,
		Model:       "gpt-5.4-mini",
		RequestedAt: time.Now().Add(-25 * time.Minute),
		Cost:        0.18,
	})
	state.mu.Lock()
	state.cfg = cfg
	state.store = store
	state.keyState = policyplus.KeyPolicyState{Keys: []policyplus.KeyRecord{previewKey, disabledKey, noLimitKey}}
	state.rpmBuckets = map[string][]time.Time{}
	state.mu.Unlock()
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/resource/plugins/cpa-key-policy-plus/admin", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		w.Header().Set("content-type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(adminHTML()))
	})
	mux.HandleFunc("/v0/resource/plugins/cpa-key-policy-plus/user", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		w.Header().Set("content-type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(userHTML()))
	})
	writePreviewStatus := func(w http.ResponseWriter) {
		w.Header().Set("content-type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"counters": map[string]any{
				"total_requests":  4,
				"active_requests": 17,
				"continuations":   1,
				"truncation_hits": 1,
				"failures":        0,
			},
			"last_error": nil,
		})
	}
	writePreviewRequests := func(w http.ResponseWriter) {
		w.Header().Set("content-type", "application/json; charset=utf-8")
		now := time.Now().UTC()
		identity := previewKey.Safe()
		_ = json.NewEncoder(w).Encode(map[string]any{"requests": []map[string]any{
			{
				"request_id":                        "req-preview-a",
				"model":                             "gpt-5.5",
				"path":                              "/v1/responses",
				"started_at":                        now.Add(-4 * time.Minute).Format(time.RFC3339),
				"updated_at":                        now.Add(-3 * time.Minute).Format(time.RFC3339),
				"duration_ms":                       5570,
				"status":                            "completed",
				"protection":                        "auto_continued",
				"key_identity":                      identity,
				"latest_round":                      2,
				"latest_reasoning_tokens":           181,
				"first_truncation_round":            1,
				"first_truncation_reasoning_tokens": 516,
				"continuation_count":                1,
				"rounds":                            []map[string]any{{"round": 1, "reasoning_tokens": 516, "decision": "continue", "truncation_match": true}, {"round": 2, "reasoning_tokens": 181, "decision": "clean", "truncation_match": false}},
			},
			{
				"request_id":              "req-preview-stale",
				"model":                   "gpt-5.5",
				"path":                    "/v1/responses",
				"started_at":              now.Add(-45 * time.Minute).Format(time.RFC3339),
				"updated_at":              now.Add(-44 * time.Minute).Format(time.RFC3339),
				"status":                  "processing",
				"protection":              "processing",
				"key_identity":            identity,
				"latest_round":            1,
				"latest_reasoning_tokens": 140,
				"continuation_count":      0,
				"rounds":                  []map[string]any{{"round": 1, "reasoning_tokens": 140, "decision": "clean", "truncation_match": false}},
			},
			{
				"request_id":              "req-preview-live",
				"model":                   "gpt-5.5",
				"path":                    "/v1/responses",
				"started_at":              now.Add(-30 * time.Second).Format(time.RFC3339),
				"updated_at":              now.Add(-5 * time.Second).Format(time.RFC3339),
				"status":                  "processing",
				"protection":              "processing",
				"key_identity":            identity,
				"latest_round":            1,
				"latest_reasoning_tokens": 140,
				"continuation_count":      0,
				"rounds":                  []map[string]any{{"round": 1, "reasoning_tokens": 140, "decision": "clean", "truncation_match": false}},
			},
		}})
	}
	mux.HandleFunc("/governor/codexcont/admin/status", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		writePreviewStatus(w)
	})
	mux.HandleFunc("/admin/status", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		writePreviewStatus(w)
	})
	mux.HandleFunc("/governor/codexcont/admin/requests", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		writePreviewRequests(w)
	})
	mux.HandleFunc("/admin/requests", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		writePreviewRequests(w)
	})
	mux.HandleFunc("/governor/codexcont/admin/logs/stream", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		w.Header().Set("content-type", "text/event-stream")
		w.Header().Set("cache-control", "no-cache")
		_, _ = w.Write([]byte("event: ready\ndata: {\"ok\":true}\n\n"))
		_, _ = w.Write([]byte("event: log\ndata: {\"ts\":\"2026-07-02T02:09:59Z\",\"level\":\"info\",\"event\":\"round_decision\",\"message\":\"preview round decision\",\"fields\":{\"request_id\":\"req-preview-a\"}}\n\n"))
	})
	mux.HandleFunc("/engine/healthz", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		w.Header().Set("content-type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"ok":true,"mode":"preview"}`))
	})
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rawReq, _ := json.Marshal(managementRequest{
			Method:  r.Method,
			Path:    r.URL.Path,
			Headers: r.Header,
			Query:   r.URL.Query(),
			Body:    body,
		})
		rawResp, _ := managementHandle(rawReq)
		var env envelope
		_ = json.Unmarshal(rawResp, &env)
		var resp managementResponse
		_ = json.Unmarshal(env.Result, &resp)
		for key, values := range resp.Headers {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		if resp.StatusCode != 0 {
			w.WriteHeader(resp.StatusCode)
		}
		_, _ = w.Write(resp.Body)
	})
	fmt.Printf("CPA Key Policy+ preview: http://%s/v0/resource/plugins/cpa-key-policy-plus/user (key %s) and /admin\n", addr, previewRawKey)
	if err := http.ListenAndServe(addr, mux); err != nil {
		panic(err)
	}
}

func floatPtr(value float64) *float64 { return &value }

func shutdownPlugin() {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.store != nil {
		_ = state.store.Close()
		state.store = nil
	}
	resetPoolCaches()
	state.lastConfigError = ""
	state.lastConfigErrorAt = time.Time{}
}

// handleMethodSafely turns a panic into an error for that one call: the plugin
// runs inside CPA's process, where an unrecovered panic stops CPA itself.
func handleMethodSafely(method string, request []byte) (raw []byte, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			raw, err = nil, fmt.Errorf("internal error in %s: %v", method, recovered)
		}
	}()
	return handleMethod(method, request)
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case methodPluginRegister, methodPluginReconfigure:
		configure(request)
		return okEnvelope(pluginRegistration())
	case methodFrontendAuthIdentifier:
		return okEnvelope(map[string]string{"identifier": pluginID})
	case methodFrontendAuthAuthenticate:
		return frontendAuth(request)
	case methodModelRoute:
		return routeModel(request)
	case methodRequestInterceptBefore:
		return requestInterceptBeforeAuth(request)
	case methodRequestInterceptAfter:
		return requestInterceptAfterAuth(request)
	case methodExecutorIdentifier:
		return okEnvelope(map[string]string{"identifier": pluginID})
	case methodExecutorExecute:
		return executorExecute(request)
	case methodExecutorExecuteStream:
		return executorExecuteStream(request)
	case methodExecutorCountTokens:
		return okEnvelope(executorResponse{Payload: []byte(`{"input_tokens":0}`)})
	case methodUsageHandle:
		return usageHandle(request)
	case methodManagementRegister:
		return managementRegister()
	case methodManagementHandle:
		return managementHandle(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func configure(raw []byte) error {
	cfg := policyplus.DefaultConfig()
	if len(raw) > 0 {
		var req lifecycleRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			recordLifecycleConfigError(err)
			return err
		}
		if len(req.ConfigYAML) > 0 {
			if err := yaml.Unmarshal(req.ConfigYAML, &cfg); err != nil {
				recordLifecycleConfigError(err)
				return err
			}
		}
	}
	cfg = cfg.Normalize()
	store, err := policyplus.OpenStore(cfg.StateDBPath)
	if err != nil {
		recordLifecycleConfigError(err)
		return err
	}
	cfg = applyStoredSettings(cfg, store)
	var keyState policyplus.KeyPolicyState
	if strings.TrimSpace(cfg.KeyPolicyStatePath) != "" {
		loaded, err := policyplus.LoadKeyPolicyState(cfg.KeyPolicyStatePath)
		if err == nil {
			keyState = loaded
			_ = store.ImportKeys(context.Background(), loaded)
		}
	}
	importLegacySQLite(store, cfg.LegacyQuotaDBPath, "usage-admin")
	importLegacySQLite(store, cfg.GovernorStateDBPath, "governor")
	syncNativeKeysFromConfig(store, cfg)
	state.mu.Lock()
	old := state.store
	state.cfg = cfg
	state.store = store
	state.keyState = keyState
	state.keyStatePath = strings.TrimSpace(cfg.KeyPolicyStatePath)
	state.keyStateModTime = time.Time{}
	state.keyStateLastCheck = time.Time{}
	state.priceBook = policyplus.PriceBook{}
	state.priceBookChecked = time.Time{}
	state.lastConfigError = ""
	state.lastConfigErrorAt = time.Time{}
	state.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	resetPoolCaches()
	_ = refreshKeyPolicyState(true)
	_ = syncNativeKeysFromLoadedConfig()
	_ = currentPriceBook(context.Background(), true)
	if keys, err := store.ListKeys(context.Background()); err == nil {
		maybeSyncCPAMPAliases(keys)
	}
	// A pool that lends its account has an open lend window from startup on.
	if pools, err := store.ListPools(context.Background()); err == nil {
		_ = store.SyncLendWindows(context.Background(), pools, time.Now())
	}
	return nil
}

func recordLifecycleConfigError(err error) {
	if err == nil {
		return
	}
	state.mu.Lock()
	state.lastConfigError = err.Error()
	state.lastConfigErrorAt = time.Now().UTC()
	state.mu.Unlock()
}

func syncNativeKeysFromLoadedConfig() error {
	state.mu.RLock()
	cfg := state.cfg
	store := state.store
	state.mu.RUnlock()
	return syncNativeKeysFromConfig(store, cfg)
}

func syncNativeKeysFromConfig(store *policyplus.Store, cfg policyplus.Config) error {
	if store == nil || strings.TrimSpace(cfg.NativeKeysConfigPath) == "" {
		return nil
	}
	rawKeys, err := policyplus.LoadNativeKeysFromCPAConfig(cfg.NativeKeysConfigPath)
	if err != nil {
		_ = store.Audit(context.Background(), "system", "native_key_sync_failed", "cpa_config", map[string]any{"error": policyplus.Brief(err.Error(), 240)})
		return err
	}
	aliasPaths := cfg.AliasDBPaths()
	aliases, aliasErrors, err := policyplus.LoadAPIKeyAliasesFromSQLitePaths(context.Background(), aliasPaths)
	if len(aliasErrors) > 0 {
		_ = store.Audit(context.Background(), "system", "native_alias_read_partial", "cpamp", map[string]any{"errors": aliasErrors})
	}
	if err != nil && len(aliasPaths) > 0 {
		_ = store.Audit(context.Background(), "system", "native_alias_read_failed", "cpamp", map[string]any{"error": policyplus.Brief(err.Error(), 240)})
	}
	inputs := make([]policyplus.NativeKeySyncInput, 0, len(rawKeys))
	for _, rawKey := range rawKeys {
		hash := policyplus.SHA256Hex(rawKey)
		alias := aliases[hash]
		inputs = append(inputs, policyplus.NativeKeySyncInput{RawKey: rawKey, Alias: alias})
	}
	if err := store.SyncNativeKeys(context.Background(), inputs); err != nil {
		_ = store.Audit(context.Background(), "system", "native_key_sync_failed", "store", map[string]any{"error": policyplus.Brief(err.Error(), 240)})
		return err
	}
	return nil
}

func applyStoredSettings(cfg policyplus.Config, store *policyplus.Store) policyplus.Config {
	if store == nil {
		return cfg
	}
	settings, err := store.LoadSettings(context.Background())
	if err != nil {
		return cfg
	}
	if value, ok := settings["codexcont_enabled"]; ok {
		if parsed, err := strconv.ParseBool(value); err == nil {
			cfg.CodexContEnabled = parsed
		}
	}
	if value := strings.TrimSpace(settings["codexcont_url"]); value != "" {
		cfg.CodexContURL = strings.TrimRight(value, "/")
	}
	if value := strings.TrimSpace(settings["fail_mode"]); value != "" {
		cfg.FailMode = strings.ToLower(value)
	}
	return cfg.Normalize()
}

func currentPriceBook(ctx context.Context, force bool) policyplus.PriceBook {
	state.mu.RLock()
	cfg := state.cfg
	store := state.store
	cached := state.priceBook
	checked := state.priceBookChecked
	state.mu.RUnlock()
	if !force && !checked.IsZero() && time.Since(checked) < priceBookRefreshTTL && cached.Source != "" {
		return cached
	}
	paths := cfg.PriceDBPaths()
	book, warnings, err := policyplus.LoadCPAMPPriceBookFromSQLitePaths(ctx, paths)
	if err == nil && len(book.Prices) > 0 {
		if store != nil {
			_ = store.SaveCachedPriceBook(ctx, book)
		}
		state.mu.Lock()
		state.priceBook = book
		state.priceBookChecked = time.Now()
		state.mu.Unlock()
		return book
	}
	if store != nil {
		if fallback, ok, cacheErr := store.LoadCachedPriceBook(ctx); cacheErr == nil && ok {
			fallback.Warnings = append(fallback.Warnings, warnings...)
			if err != nil {
				fallback.Warnings = append(fallback.Warnings, "CPAMP 价格源暂时不可读，已使用最近缓存。")
			}
			state.mu.Lock()
			state.priceBook = fallback
			state.priceBookChecked = time.Now()
			state.mu.Unlock()
			return fallback
		}
	}
	if err != nil {
		warnings = append(warnings, "CPAMP 价格源不可用且没有最近有效缓存；受限 Key 的未定价模型会被拒绝，不按 $0 放行。")
	}
	book = policyplus.PriceBook{
		Source:   policyplus.CostSourceCPAMPPriceUnavailable,
		LoadedAt: time.Now().UTC(),
		Prices:   map[string]policyplus.ModelPrice{},
		Warnings: warnings,
	}
	state.mu.Lock()
	state.priceBook = book
	state.priceBookChecked = time.Now()
	state.mu.Unlock()
	return book
}

func pricingStatus(book policyplus.PriceBook) map[string]any {
	return map[string]any{
		"source":        book.NormalizedSource(),
		"path":          book.Path,
		"model_count":   len(book.Prices),
		"models":        book.PricedModelIDs(),
		"loaded_at":     book.LoadedAt,
		"updated_at_ms": book.UpdatedAtMS,
		"synced_at_ms":  book.SyncedAtMS,
		"warnings":      append([]string(nil), book.Warnings...),
	}
}

func importLegacySQLite(store *policyplus.Store, path, source string) {
	path = strings.TrimSpace(path)
	if store == nil || path == "" {
		return
	}
	if _, err := os.Stat(path); err != nil {
		_ = store.Audit(context.Background(), "system", "legacy_import_skipped", source, map[string]any{
			"path":  path,
			"error": policyplus.Brief(err.Error(), 240),
		})
		return
	}
	result, err := store.ImportLegacyQuotaSQLite(context.Background(), path, source)
	if err != nil {
		_ = store.Audit(context.Background(), "system", "legacy_import_failed", source, map[string]any{
			"path":  path,
			"error": policyplus.Brief(err.Error(), 240),
		})
		return
	}
	if result.Limits > 0 || result.Resets > 0 {
		_ = store.Audit(context.Background(), "system", "legacy_import_completed", source, map[string]any{
			"path":   path,
			"limits": result.Limits,
			"resets": result.Resets,
		})
	}
}

func pluginRegistration() registration {
	cfg := loadedConfig()
	return registration{
		SchemaVersion: schemaVersion,
		Metadata: metadata{
			Name:             "cpa-key-policy-plus",
			Version:          "0.1.0",
			Author:           "konbakuyomu/CodexCont",
			GitHubRepository: "https://local/CodexCont",
			ConfigFields: []configField{
				{Name: "enabled", Type: configBoolean, Description: "Enable Key Policy Plus user-key authentication and management surfaces."},
				{Name: "exclusive_auth", Type: configBoolean, Description: "When true, Key Policy Plus is the exclusive frontend auth provider for user keys."},
				{Name: "state_db_path", Type: configString, Description: "SQLite path for Key Policy Plus state."},
				{Name: "key_policy_state_path", Type: configString, Description: "Optional old CPA Key Policy state JSON path to import once or mirror during cutover."},
				{Name: "legacy_quota_db_path", Type: configString, Description: "Optional old usage-admin SQLite path for one-way 5H/month limit and reset import."},
				{Name: "governor_state_db_path", Type: configString, Description: "Optional old Governor SQLite path for one-way limit and reset import."},
				{Name: "codex_summary_db_path", Type: configString, Description: "Optional legacy executor SQLite history path; read only when codexcont_enabled is true."},
				{Name: "native_keys_config_path", Type: configString, Description: "Optional CPA config YAML path whose top-level api-keys are synced as native policy keys."},
				{Name: "cpamp_alias_db_path", Type: configString, Description: "Optional CPAMP manager SQLite path for read-only api_key_aliases lookup."},
				{Name: "cpamp_alias_db_paths", Type: configString, Description: "Optional comma/semicolon-separated fallback CPAMP SQLite paths for read-only api_key_aliases lookup."},
				{Name: "cpamp_price_db_path", Type: configString, Description: "Optional CPAMP manager SQLite path for read-only model_prices billing lookup."},
				{Name: "cpamp_price_db_paths", Type: configString, Description: "Optional comma/semicolon-separated fallback CPAMP SQLite paths for read-only model_prices lookup."},
				{Name: "session_secret", Type: configString, Description: "Secret used to sign user portal sessions."},
				{Name: "codexcont_enabled", Type: configBoolean, Description: "Enable optional legacy executor-history lookup; keep false for native CPA cutover."},
				{Name: "codexcont_route", Type: configBoolean, Description: "Deprecated in Key Policy Plus; keep false and let Governor own CodexCont routing."},
				{Name: "codexcont_url", Type: configString, Description: "Internal CodexCont engine base URL."},
				{Name: "fail_mode", Type: configEnum, EnumValues: []string{"fallback", "fail_closed"}, Description: "Behavior when engine is unavailable."},
				{Name: "sub2pool_url", Type: configString, Description: "Optional Sub2Pool base URL (e.g. http://sub2pool:8000) whose read-only capacity estimate is shown next to each carpool pool."},
				{Name: "sub2pool_api_key", Type: configString, Description: "Optional Sub2Pool read-only API key (sub2pool_...) used with sub2pool_url."},
				{Name: "cpamp_url", Type: configString, Description: "CPAMP manager base URL used to name Key Policy+ keys in CPAMP's request monitor (default http://cpamp:18317)."},
				{Name: "cpamp_admin_key_file", Type: configString, Description: "File holding the CPAMP admin key; defaults to cpamp-admin-key next to state_db_path. Without it no aliases are pushed."},
			},
		},
		Capabilities: capabilities{
			FrontendAuthProvider:          true,
			FrontendAuthProviderExclusive: cfg.ExclusiveAuth,
			// Native request interception is the authoritative policy denial
			// path on CPA schema v2+: ModelRouter cannot represent an HTTP
			// status, so routing never carries denials. The router only pins
			// carpool members to their pool account (pro/gpt-5.5), and only
			// once a pool has a model prefix CPA actually serves.
			ModelRouter:           true,
			RequestInterceptor:    true,
			Executor:              true,
			ExecutorModelScope:    executorModelScopeBoth,
			ExecutorInputFormats:  []string{executorFormatOpenAIResponse},
			ExecutorOutputFormats: []string{executorFormatOpenAIResponse},
			UsagePlugin:           true,
			ManagementAPI:         true,
		},
	}
}

func loadedConfig() policyplus.Config {
	state.mu.RLock()
	defer state.mu.RUnlock()
	if state.cfg.StateDBPath == "" {
		return policyplus.DefaultConfig()
	}
	return state.cfg
}

func loadedStore() *policyplus.Store {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.store
}

func loadedLifecycleStatus() (bool, string, time.Time) {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.store != nil, state.lastConfigError, state.lastConfigErrorAt
}

func frontendAuth(raw []byte) ([]byte, error) {
	var req frontendAuthRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return okEnvelope(frontendAuthResponse{Authenticated: false})
	}
	cfg := loadedConfig()
	if !cfg.Enabled {
		return okEnvelope(frontendAuthResponse{Authenticated: false})
	}
	key := bearer(req.Headers.Get("Authorization"))
	if key == "" {
		return okEnvelope(frontendAuthResponse{Authenticated: false})
	}
	record, ok := findKeyByRaw(key)
	if !ok || !evaluateIdentityPolicy(record).Allowed {
		return okEnvelope(frontendAuthResponse{Authenticated: false})
	}
	return okEnvelope(frontendAuthResponse{
		Authenticated: true,
		Principal:     record.ID,
		Metadata:      authMetadata(record),
	})
}

func policyDecisionForRawKey(rawKey, model string, consumeRPM bool) (policyplus.KeyRecord, policyDecision, bool) {
	return policyDecisionForRawKeyWithServiceTier(rawKey, model, "", consumeRPM)
}

func policyDecisionForRawKeyWithServiceTier(rawKey, model, serviceTier string, consumeRPM bool) (policyplus.KeyRecord, policyDecision, bool) {
	submitted := policyplus.NormalizeSubmittedKey(rawKey)
	if strings.HasPrefix(strings.ToLower(submitted), "cpa_") {
		return policyplus.KeyRecord{}, policyDecision{}, false
	}
	record, ok := findKeyByRaw(rawKey)
	if ok {
		return record, evaluatePolicyWithServiceTier(record, model, serviceTier, consumeRPM), true
	}
	if !isNativeSubmittedKey(submitted) {
		return policyplus.KeyRecord{}, policyDecision{}, false
	}
	hash := policyplus.SHA256Hex(submitted)
	preview := policyplus.HashPreview(hash)
	record = policyplus.KeyRecord{
		ID:            policyplus.NativeKeyIDFromHash(hash),
		Name:          preview,
		KeyHash:       "sha256:" + hash,
		Preview:       preview,
		Source:        policyplus.NativeCPASource,
		SourcePresent: false,
	}
	base := policyDecision{
		Allowed:    true,
		StatusCode: http.StatusOK,
		KeyID:      record.ID,
		KeyName:    preview,
	}
	decision := denyDecision(base, "invalid_request_error", "policy_missing", "policy_missing", "", fmt.Sprintf("CPA Key Policy+ 已拦截：%s 没有对应的 Plus 策略，请先在管理页同步并启用策略。", preview))
	return record, decision, true
}

func isNativeSubmittedKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	return strings.HasPrefix(lower, "sk-") || strings.HasPrefix(lower, "sk_")
}

func authMetadata(record policyplus.KeyRecord) map[string]string {
	return map[string]string{
		"provider":       "cpa-key-policy-plus",
		"key_id":         record.ID,
		"key_name":       record.Name,
		"key_alias":      record.Alias,
		"preview":        record.Preview,
		"source":         record.Source,
		"source_present": strconv.FormatBool(record.SourcePresent),
	}
}

func decisionMetadata(metadata map[string]string, decision policyDecision) map[string]string {
	out := map[string]string{}
	for k, v := range metadata {
		out[k] = v
	}
	out[policyDenyMetadataPrefix+"code"] = decision.Code
	out[policyDenyMetadataPrefix+"message"] = decision.Message
	out[policyDenyMetadataPrefix+"window"] = decision.Window
	out[policyDenyMetadataPrefix+"param"] = decision.Param
	return out
}

func findKeyByRaw(rawKey string) (policyplus.KeyRecord, bool) {
	_ = syncNativeKeysFromLoadedConfig()
	_ = refreshKeyPolicyState(false)
	if key, ok := lookupKeyByRaw(rawKey); ok {
		return key, true
	}
	if strings.HasPrefix(strings.ToLower(policyplus.NormalizeSubmittedKey(rawKey)), "cpa_") {
		_ = refreshKeyPolicyState(true)
		return lookupKeyByRaw(rawKey)
	}
	return policyplus.KeyRecord{}, false
}

func lookupKeyByRaw(rawKey string) (policyplus.KeyRecord, bool) {
	state.mu.RLock()
	keyState := state.keyState
	store := state.store
	state.mu.RUnlock()
	if store != nil {
		key, ok, err := store.FindKeyByHash(context.Background(), policyplus.SHA256Hex(rawKey))
		if err == nil && ok {
			return key, true
		}
	}
	if key, ok := keyState.FindByRawKey(rawKey); ok {
		return key, true
	}
	return policyplus.KeyRecord{}, false
}

func refreshKeyPolicyState(force bool) error {
	state.mu.RLock()
	path := strings.TrimSpace(state.keyStatePath)
	lastMod := state.keyStateModTime
	store := state.store
	state.mu.RUnlock()
	if path == "" || store == nil {
		return nil
	}
	now := time.Now()
	info, err := os.Stat(path)
	if err != nil {
		state.mu.Lock()
		state.keyStateLastCheck = now
		state.mu.Unlock()
		return err
	}
	if !force && !info.ModTime().After(lastMod) {
		state.mu.Lock()
		state.keyStateLastCheck = now
		state.mu.Unlock()
		return nil
	}
	loaded, err := policyplus.LoadKeyPolicyState(path)
	if err != nil {
		state.mu.Lock()
		state.keyStateLastCheck = now
		state.mu.Unlock()
		return err
	}
	if err := store.ImportKeys(context.Background(), loaded); err != nil {
		return err
	}
	state.mu.Lock()
	state.keyState = loaded
	state.keyStateModTime = info.ModTime()
	state.keyStateLastCheck = now
	state.mu.Unlock()
	return nil
}

// routeModel only pins carpool members to their pool account. Policy denials
// are left unhandled here: the request interceptor terminates them with a real
// HTTP 429, which a routed fake executor could not.
func routeModel(raw []byte) ([]byte, error) {
	var req modelRouteRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	unhandled := modelRouteResponse{Handled: false}
	if !loadedConfig().Enabled || !poolRoutingConfigured(context.Background()) {
		return okEnvelope(unhandled)
	}
	rawKey := bearer(req.Headers.Get("Authorization"))
	if rawKey == "" {
		return okEnvelope(unhandled)
	}
	key, ok := findKeyByRaw(rawKey)
	if !ok {
		return okEnvelope(unhandled)
	}
	if route, pinned := poolRouteTarget(key.ID, firstNonEmpty(req.RequestedModel, requestedModelFromBody(req.Body))); pinned {
		return okEnvelope(route)
	}
	return okEnvelope(unhandled)
}

// requestInterceptBeforeAuth is the only policy-denial path that can produce
// a real downstream HTTP response in CPA schema v2+. Frontend auth supplies
// identity; model routing and executor responses cannot encode an HTTP status.
func requestInterceptBeforeAuth(raw []byte) ([]byte, error) {
	var req requestInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return okEnvelope(requestInterceptResponse{})
	}
	if !loadedConfig().Enabled {
		return okEnvelope(requestInterceptResponse{})
	}
	model := firstNonEmpty(requestedModelFromBody(req.Body), req.RequestedModel, req.Model)
	decision, ok := policyDecisionForIntercept(req, model, requestedServiceTierFromBody(req.Body))
	if !ok || decision.Allowed {
		return okEnvelope(requestInterceptResponse{})
	}
	return okEnvelope(policyTerminationResponse(decision))
}

// requestInterceptAfterAuth deliberately performs no second policy decision:
// the before-auth interceptor already consumed the one RPM slot for this
// inference attempt and either terminated it or let it proceed.
func requestInterceptAfterAuth(_ []byte) ([]byte, error) {
	return okEnvelope(requestInterceptResponse{})
}

func policyTerminationResponse(decision policyDecision) requestInterceptResponse {
	return requestInterceptResponse{
		Terminate:       true,
		StatusCode:      decision.StatusCode,
		ResponseHeaders: policyDenyHeaders(decision),
		ResponseBody:    policyDenyBody(decision),
	}
}

func isResponsesRequest(source string, body []byte) bool {
	source = strings.ToLower(strings.TrimSpace(source))
	if strings.Contains(source, "response") || source == "openai" {
		return true
	}
	return strings.Contains(string(body), `"stream"`) && strings.Contains(string(body), `"model"`)
}

func requestedModelFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return ""
	}
	if text, ok := raw["model"].(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func requestedServiceTierFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return ""
	}
	for _, field := range []string{"service_tier", "serviceTier"} {
		if value, ok := raw[field].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func policyDecisionForHeaders(headers http.Header, model string, consumeRPM bool) (policyDecision, bool) {
	return policyDecisionForHeadersWithServiceTier(headers, model, "", consumeRPM)
}

func policyDecisionForHeadersWithServiceTier(headers http.Header, model, serviceTier string, consumeRPM bool) (policyDecision, bool) {
	rawKey := bearer(headers.Get("Authorization"))
	if rawKey == "" {
		return policyDecision{}, false
	}
	_, decision, ok := policyDecisionForRawKeyWithServiceTier(rawKey, model, serviceTier, consumeRPM)
	if !ok {
		return policyDecision{}, false
	}
	return decision, true
}

// policyDecisionForIntercept normally uses the original Authorization header.
// Some CPA execution paths can replace that header after frontend auth; in
// that case use only the host-injected identity metadata created by this exact
// plugin, never arbitrary client metadata.
func policyDecisionForIntercept(req requestInterceptRequest, model, serviceTier string) (policyDecision, bool) {
	if decision, ok := policyDecisionForHeadersWithServiceTier(req.Headers, model, serviceTier, true); ok {
		return decision, true
	}
	if metadataString(req.Metadata, "provider") != pluginID {
		return policyDecision{}, false
	}
	keyID := metadataString(req.Metadata, "key_id")
	if keyID == "" {
		return policyDecision{}, false
	}
	store := loadedStore()
	if store == nil {
		return policyDecision{}, false
	}
	keys, err := store.ListKeys(context.Background())
	if err != nil {
		return policyDecision{}, false
	}
	for _, key := range keys {
		if key.ID == keyID {
			return evaluatePolicyWithServiceTier(key, model, serviceTier, true), true
		}
	}
	return policyDecision{}, false
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func evaluatePolicy(key policyplus.KeyRecord, model string, consumeRPM bool) policyDecision {
	return evaluatePolicyWithServiceTier(key, model, "", consumeRPM)
}

func evaluatePolicyWithServiceTier(key policyplus.KeyRecord, model, serviceTier string, consumeRPM bool) policyDecision {
	base := policyDecision{
		Allowed:    true,
		StatusCode: http.StatusOK,
		KeyID:      key.ID,
		KeyName:    safeKeyDisplayName(key),
	}
	if key.ID == "" {
		return denyDecision(base, "invalid_request_error", "missing_policy", "policy_missing", "", "CPA Key Policy+ 已拦截：当前 Key 没有对应的 Plus 策略。")
	}
	if key.Source == policyplus.NativeCPASource && !key.SourcePresent {
		return denyDecision(base, "invalid_request_error", "api_key_source_removed", "source_removed", "", fmt.Sprintf("CPA Key Policy+ 已拦截：%s 已从 CPA 官方 Key 列表移除。", safeKeyDisplayName(key)))
	}
	if !key.Enabled || key.Archived {
		return denyDecision(base, "invalid_request_error", "api_key_disabled", "disabled", "", fmt.Sprintf("CPA Key Policy+ 已拦截：%s 当前已禁用。", safeKeyDisplayName(key)))
	}
	if strings.TrimSpace(key.BillingHoldReason) != "" {
		return denyDecision(base, "rate_limit_exceeded", "billing_pending", "billing", "billing", fmt.Sprintf("CPA Key Policy+ 已暂停：%s 有待核算费用（模型 %s，原因 %s）。在 CPAMP 价格核验并完成 pending billing resolve 前，不会继续放行请求。", safeKeyDisplayName(key), firstNonEmpty(key.BillingHoldModel, "未知"), key.BillingHoldReason))
	}
	if model != "" && !policyplus.ModelAllowed(key.Models, model) {
		return denyDecision(base, "invalid_request_error", "model_not_allowed", "model", "", fmt.Sprintf("CPA Key Policy+ 已拦截：%s 不允许使用模型 %s。", safeKeyDisplayName(key), model))
	}
	if hasCostQuota(key) {
		priceBook := currentPriceBook(context.Background(), false)
		price, priced := priceBook.PriceFor(model)
		if strings.TrimSpace(model) == "" || !priced || !policyplus.PriceAllowsAdmission(price, serviceTier) {
			return denyDecision(base, "rate_limit_exceeded", "model_price_unavailable", "pricing", "model", fmt.Sprintf("CPA Key Policy+ 已拦截：%s 的模型 %s 没有已验证的 CPAMP 价格。受限 Key 已暂停该模型，避免按 $0 计入周限；请先同步价格或恢复最近有效缓存。", safeKeyDisplayName(key), firstNonEmpty(strings.TrimSpace(model), "(未提供模型)")))
		}
	}
	if rpm := checkRPM(key, consumeRPM); !rpm.Allowed {
		base.UsedCount = rpm.Used
		base.LimitCount = rpm.Limit
		base.Window = "rpm"
		return denyDecision(base, "rate_limit_exceeded", "rpm_rate_limit_exceeded", "rpm", "", fmt.Sprintf("CPA Key Policy+ 已拦截：%s 触发 RPM 限制，最近 1 分钟请求 %d / 上限 %d。", safeKeyDisplayName(key), rpm.Used, rpm.Limit))
	}
	// A pool share replaces the USD windows for the models its pool serves.
	// Every other model stays under the windows, which then count only the
	// usage of models no share governs.
	if _, _, pooled := poolShareFor(key.ID, model); !pooled {
		keep := offPoolModels(context.Background(), key.ID)
		if quota := checkQuota(key, keep); !quota.Allowed {
			base.Window = quota.Window
			base.Param = quota.Window
			base.UsedUSD = quota.Used
			base.LimitUSD = quota.Limit
			scope := ""
			if keep != nil {
				scope = "池外模型的 "
			}
			return denyDecision(base, "rate_limit_exceeded", quota.Code, quota.Window, quota.Window, fmt.Sprintf("CPA Key Policy+ 已拦截：%s 触发%s%s费用限额，已用 $%.2f / 上限 $%.2f。", safeKeyDisplayName(key), scope, windowDisplayName(quota.Window), quota.Used, quota.Limit))
		}
	}
	if share := checkPoolShare(key, model); !share.Allowed {
		base.Window = "pool"
		return denyDecision(base, "rate_limit_exceeded", "pool_share_exceeded", "pool", "pool", fmt.Sprintf("CPA Key Policy+ 已拦截：%s 在拼车池「%s」本周期的份额已用完（已用 %.2f%% / 可用 %.2f%%），%s 重置。", safeKeyDisplayName(key), share.PoolName, share.UsedPP, share.AvailablePP, beijingTimeLabel(share.ResetAt)))
	}
	return base
}

func evaluateIdentityPolicy(key policyplus.KeyRecord) policyDecision {
	base := policyDecision{
		Allowed:    true,
		StatusCode: http.StatusOK,
		KeyID:      key.ID,
		KeyName:    safeKeyDisplayName(key),
	}
	if key.ID == "" {
		return denyDecision(base, "invalid_request_error", "missing_policy", "policy_missing", "", "CPA Key Policy+ 已拦截：当前 Key 没有对应的 Plus 策略。")
	}
	if key.Source == policyplus.NativeCPASource && !key.SourcePresent {
		return denyDecision(base, "invalid_request_error", "api_key_source_removed", "source_removed", "", fmt.Sprintf("CPA Key Policy+ 已拦截：%s 已从 CPA 官方 Key 列表移除。", safeKeyDisplayName(key)))
	}
	if !key.Enabled || key.Archived {
		return denyDecision(base, "invalid_request_error", "api_key_disabled", "disabled", "", fmt.Sprintf("CPA Key Policy+ 已拦截：%s 当前已禁用。", safeKeyDisplayName(key)))
	}
	return base
}

func denyDecision(base policyDecision, typ, code, reason, param, message string) policyDecision {
	base.Allowed = false
	base.StatusCode = http.StatusTooManyRequests
	base.Type = typ
	base.Code = code
	base.Param = param
	if base.Param == "" {
		base.Param = reason
	}
	base.Message = message
	return base
}

func safeKeyDisplayName(key policyplus.KeyRecord) string {
	for _, value := range []string{key.Name, key.Alias, key.Preview, key.ID} {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return "当前 Key"
}

type rpmDecision struct {
	Allowed bool
	Used    int
	Limit   int
}

func checkRPM(key policyplus.KeyRecord, consume bool) rpmDecision {
	if key.RPM <= 0 {
		return rpmDecision{Allowed: true}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-time.Minute)
	bucket := state.rpmBuckets[key.ID]
	kept := bucket[:0]
	for _, ts := range bucket {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	if len(kept) >= key.RPM {
		state.rpmBuckets[key.ID] = kept
		return rpmDecision{Allowed: false, Used: len(kept), Limit: key.RPM}
	}
	if consume {
		state.rpmBuckets[key.ID] = append(kept, now)
	} else {
		state.rpmBuckets[key.ID] = kept
	}
	return rpmDecision{Allowed: true, Used: len(kept), Limit: key.RPM}
}

type quotaDecision struct {
	Allowed bool
	Window  string
	Used    float64
	Limit   float64
	Code    string
}

// checkQuota tests the key's USD windows over the models keep accepts (nil:
// every model).
func checkQuota(key policyplus.KeyRecord, keep func(model string) bool) quotaDecision {
	store := loadedStore()
	if store == nil {
		return quotaDecision{Allowed: true}
	}
	ctx := context.Background()
	now := time.Now()
	limits := []struct {
		name  string
		limit *float64
		code  string
	}{
		{policyplus.Range5H, key.FiveHourUSD, "five_hour_quota_exceeded"},
		{policyplus.Range24H, key.DailyLimitUSD, "daily_quota_exceeded"},
		{policyplus.Range7D, key.WeeklyLimitUSD, "weekly_quota_exceeded"},
		{policyplus.RangeMonth, key.MonthlyLimitUSD, "monthly_quota_exceeded"},
	}
	for _, item := range limits {
		if item.limit == nil {
			continue
		}
		used, err := store.UsageSumMatching(ctx, key.ID, policyplus.WindowFor(item.name, now), keep)
		if err != nil {
			continue
		}
		if !policyplus.CheckLimit(used, item.limit).Allowed {
			return quotaDecision{Allowed: false, Window: item.name, Used: used, Limit: *item.limit, Code: item.code}
		}
	}
	return quotaDecision{Allowed: true}
}

func hasCostQuota(key policyplus.KeyRecord) bool {
	return key.FiveHourUSD != nil || key.DailyLimitUSD != nil || key.WeeklyLimitUSD != nil || key.MonthlyLimitUSD != nil
}

func windowDisplayName(window string) string {
	switch window {
	case policyplus.Range5H:
		return "5小时"
	case policyplus.Range24H:
		return "24小时"
	case policyplus.Range7D:
		return "7天"
	case policyplus.RangeMonth:
		return "本月"
	default:
		return window
	}
}

func executorUnavailable() ([]byte, error) {
	cfg := loadedConfig()
	if cfg.CodexContRoute && cfg.FailMode == "fail_closed" {
		return errorEnvelope("codexcont_executor_pending", "CPA Key Policy+ protected executor is not enabled in this build"), nil
	}
	return errorEnvelope("codexcont_executor_pending", "CPA Key Policy+ protected executor is in passive mode; disable codexcont_enabled to route through CPA provider path"), nil
}

func executorExecute(raw []byte) ([]byte, error) {
	var req executorCallRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	execReq := normalizedExecutorRequest(req)
	if decision, ok := policyDenyForExecutor(execReq); ok {
		return okEnvelope(policyDenyExecutorResponse(decision))
	}
	cfg := loadedConfig()
	if !cfg.CodexContRoute {
		return executorUnavailable()
	}
	payload := execReq.Payload
	if len(payload) == 0 {
		payload = execReq.OriginalRequest
	}
	result, err := callHost(methodHostModelExecute, hostModelExecutionRequest{
		EntryProtocol:  firstNonEmpty(execReq.SourceFormat, "openai"),
		ExitProtocol:   firstNonEmpty(execReq.Format, "openai"),
		Model:          execReq.Model,
		Stream:         false,
		Body:           payload,
		Headers:        cloneHeader(execReq.Headers),
		Query:          cloneValues(execReq.Query),
		Alt:            execReq.Alt,
		HostCallbackID: req.HostCallbackID,
	})
	if err != nil {
		return errorEnvelope("host_model_execute_error", err.Error()), nil
	}
	var resp hostModelExecutionResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		return errorEnvelope("host_model_execute_decode_error", err.Error()), nil
	}
	if resp.StatusCode >= 400 {
		return errorEnvelope("host_model_execute_http_error", fmt.Sprintf("upstream returned %d", resp.StatusCode)), nil
	}
	return okEnvelope(executorResponse{Payload: resp.Body, Headers: resp.Headers})
}

func executorExecuteStream(raw []byte) ([]byte, error) {
	var req executorCallRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	execReq := normalizedExecutorRequest(req)
	if decision, ok := policyDenyForExecutor(execReq); ok {
		if strings.TrimSpace(req.StreamID) == "" {
			return okEnvelope(executorStreamResponse{Headers: policyDenyHeaders(decision)})
		}
		go emitPolicyDenyStream(req.StreamID, decision)
		return okEnvelope(executorStreamResponse{Headers: policyDenyHeaders(decision)})
	}
	cfg := loadedConfig()
	if !cfg.CodexContRoute {
		return executorUnavailable()
	}
	if strings.TrimSpace(req.StreamID) == "" {
		return errorEnvelope("stream_id_required", "stream_id is required for executor.execute_stream"), nil
	}
	payload := execReq.Payload
	if len(payload) == 0 {
		payload = execReq.OriginalRequest
	}
	result, err := callHost(methodHostModelExecuteStream, hostModelExecutionRequest{
		EntryProtocol:  firstNonEmpty(execReq.SourceFormat, "openai"),
		ExitProtocol:   firstNonEmpty(execReq.Format, "openai"),
		Model:          execReq.Model,
		Stream:         true,
		Body:           payload,
		Headers:        cloneHeader(execReq.Headers),
		Query:          cloneValues(execReq.Query),
		Alt:            execReq.Alt,
		HostCallbackID: req.HostCallbackID,
	})
	if err != nil {
		return errorEnvelope("host_model_stream_error", err.Error()), nil
	}
	var resp hostModelStreamResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		return errorEnvelope("host_model_stream_decode_error", err.Error()), nil
	}
	if resp.StatusCode >= 400 {
		return errorEnvelope("host_model_stream_http_error", fmt.Sprintf("upstream returned %d", resp.StatusCode)), nil
	}
	if resp.StreamID == "" {
		return errorEnvelope("host_model_stream_empty", "host returned empty stream id"), nil
	}
	go forwardHostStream(req.StreamID, resp.StreamID)
	return okEnvelope(executorStreamResponse{Headers: resp.Headers})
}

func policyDenyForExecutor(req executorRequest) (policyDecision, bool) {
	model := firstNonEmpty(req.Model, requestedModelFromBody(req.OriginalRequest), requestedModelFromBody(req.Payload))
	serviceTier := firstNonEmpty(requestedServiceTierFromBody(req.OriginalRequest), requestedServiceTierFromBody(req.Payload))
	decision, ok := policyDecisionForHeadersWithServiceTier(req.Headers, model, serviceTier, false)
	if !ok || decision.Allowed {
		return policyDecision{}, false
	}
	return decision, true
}

func normalizedExecutorRequest(req executorCallRequest) executorRequest {
	if !isZeroExecutorRequest(req.executorRequest) {
		return req.executorRequest
	}
	return req.NestedExecutorRequest
}

func isZeroExecutorRequest(req executorRequest) bool {
	return strings.TrimSpace(req.AuthID) == "" &&
		strings.TrimSpace(req.AuthProvider) == "" &&
		strings.TrimSpace(req.Model) == "" &&
		strings.TrimSpace(req.Format) == "" &&
		!req.Stream &&
		strings.TrimSpace(req.Alt) == "" &&
		len(req.Headers) == 0 &&
		len(req.Query) == 0 &&
		len(req.OriginalRequest) == 0 &&
		strings.TrimSpace(req.SourceFormat) == "" &&
		len(req.Payload) == 0 &&
		len(req.Metadata) == 0 &&
		len(req.StorageJSON) == 0 &&
		len(req.AuthMetadata) == 0 &&
		len(req.AuthAttributes) == 0
}

func policyDenyExecutorResponse(decision policyDecision) executorResponse {
	return executorResponse{
		Payload: policyDenyBody(decision),
		Headers: policyDenyHeaders(decision),
		Metadata: map[string]any{
			"policy_denied": true,
			"code":          decision.Code,
			"window":        decision.Window,
		},
	}
}

func policyDenyHeaders(decision policyDecision) http.Header {
	headers := http.Header{}
	headers.Set("Content-Type", "application/json; charset=utf-8")
	headers.Set("X-CPA-Policy-Reason", decision.Code)
	if window := firstNonEmpty(decision.Window, decision.Param); window != "" {
		headers.Set("X-CPA-Policy-Window", window)
	}
	headers.Set("Retry-After", "60")
	return headers
}

func policyDenyBody(decision policyDecision) []byte {
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": decision.Message,
			"type":    firstNonEmpty(decision.Type, "rate_limit_exceeded"),
			"code":    decision.Code,
			"param":   decision.Param,
		},
	})
	return body
}

func emitPolicyDenyStream(streamID string, decision policyDecision) {
	defer func() {
		_, _ = callHost(methodHostStreamClose, hostStreamCloseRequest{StreamID: streamID})
	}()
	payload := append([]byte("event: error\ndata: "), policyDenyBody(decision)...)
	payload = append(payload, []byte("\n\n")...)
	_, _ = callHost(methodHostStreamEmit, hostStreamEmitRequest{
		StreamID: streamID,
		Payload:  payload,
	})
}

func usageHandle(raw []byte) ([]byte, error) {
	var rec usageRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, err
	}
	_ = refreshKeyPolicyState(false)
	store := loadedStore()
	if store == nil {
		return okEnvelope(map[string]any{})
	}
	ingestQuotaSignal(context.Background(), store, rec)
	noteUpstreamLimit(rec)
	keys, _ := store.ListKeys(context.Background())
	keyByID := map[string]policyplus.KeyRecord{}
	for _, key := range keys {
		keyByID[key.ID] = key
	}
	key := keyByID[rec.AuthID]
	if key.ID == "" {
		key = keyByID[rec.APIKey]
	}
	if key.ID == "" {
		key = keyByID[rec.Source]
	}
	visibleModel := visibleUsageModel(key, rec)
	usage := policyplus.TokenUsage{
		InputTokens:         rec.Detail.InputTokens,
		OutputTokens:        rec.Detail.OutputTokens,
		CachedTokens:        rec.Detail.CachedTokens,
		CacheReadTokens:     rec.Detail.CacheReadTokens,
		CacheCreationTokens: rec.Detail.CacheCreationTokens,
		ReasoningTokens:     rec.Detail.ReasoningTokens,
		TotalTokens:         rec.Detail.TotalTokens,
	}
	actualModel := strings.TrimSpace(rec.Model)
	billingModel := firstNonEmpty(actualModel, visibleModel)
	if hasCostQuota(key) && isZeroTokenInternalPolicyTrace(rec, usage) {
		_ = store.Audit(context.Background(), "system", "usage_zero_token_policy_trace", key.ID, map[string]any{
			"model":  billingModel,
			"reason": "zero_token_internal_rejection",
		})
		return okEnvelope(map[string]any{})
	}
	priceBook := currentPriceBook(context.Background(), false)
	breakdown, priced := policyplus.CostForUsageFromPriceBook(priceBook, usage, billingModel, rec.ServiceTier)
	event := policyplus.UsageEvent{
		RequestID:       firstNonEmpty(rec.ResponseHeaders.Get("x-request-id"), rec.ResponseHeaders.Get("x-openai-request-id")),
		KeyID:           key.ID,
		KeyPreview:      key.Preview,
		Model:           visibleModel,
		RequestedModel:  visibleModel,
		ActualModel:     actualModel,
		Provider:        rec.Provider,
		ExecutorType:    rec.ExecutorType,
		Endpoint:        rec.Source,
		RequestedAt:     rec.RequestedAt,
		LatencyMS:       rec.Latency.Milliseconds(),
		TTFTMS:          rec.TTFT.Milliseconds(),
		ReasoningEffort: rec.ReasoningEffort,
		ServiceTier:     rec.ServiceTier,
		StatusCode:      rec.Failure.StatusCode,
		Failed:          rec.Failed,
		Failure:         policyplus.Brief(rec.Failure.Body, 600),
		Usage:           usage,
		Cost:            breakdown.Costs["total"],
		CostBreakdown:   breakdown,
		AuthIndex:       strings.TrimSpace(rec.AuthIndex),
		AuthID:          strings.TrimSpace(rec.AuthID),
		ConsumedAtMS:    rec.RequestedAt.Add(rec.Latency).UnixMilli(),
	}
	if !priced && hasCostQuota(key) {
		reason := "model_or_service_tier_price_unavailable"
		if actualModel != "" && !strings.EqualFold(actualModel, visibleModel) {
			reason = "actual_model_price_unavailable"
		}
		breakdown.Source = policyplus.CostSourceCPAMPBillingPending
		breakdown.Model = billingModel
		breakdown.PriceMissing = true
		breakdown.BillingPending = true
		breakdown.BillingPendingReason = reason
		breakdown.BillingPriceModel = billingModel
		event.Cost = 0
		event.CostBreakdown = breakdown
		event.BillingPending = true
		event.BillingReason = reason
		event.BillingModel = billingModel
		if err := store.InsertUsage(context.Background(), event); err != nil {
			return nil, err
		}
		return okEnvelope(map[string]any{})
	}
	if err := store.InsertUsage(context.Background(), event); err != nil {
		return nil, err
	}
	return okEnvelope(map[string]any{})
}

func isZeroTokenInternalPolicyTrace(rec usageRecord, usage policyplus.TokenUsage) bool {
	if rec.Failed || rec.Failure.StatusCode != 0 {
		return false
	}
	return usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.CachedTokens == 0 &&
		usage.CacheReadTokens == 0 && usage.CacheCreationTokens == 0 && usage.ReasoningTokens == 0 && usage.TotalTokens == 0
}

func visibleUsageModel(key policyplus.KeyRecord, rec usageRecord) string {
	if model := strings.TrimSpace(rec.Alias); model != "" {
		return model
	}
	reported := strings.TrimSpace(rec.Model)
	if reported == "" || key.ID == "" {
		return reported
	}
	allowed := cleanStrings(key.Models)
	for _, model := range allowed {
		if strings.EqualFold(model, reported) {
			return reported
		}
	}
	if len(allowed) == 1 {
		return allowed[0]
	}
	return reported
}

func managementRegister() ([]byte, error) {
	resp := managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: "/plugins/cpa-key-policy-plus/keys"},
			{Method: http.MethodGet, Path: "/plugins/cpa-key-policy-plus/models"},
			{Method: http.MethodPut, Path: "/plugins/cpa-key-policy-plus/keys/save"},
			{Method: http.MethodPut, Path: "/plugins/cpa-key-policy-plus/keys/limits"},
			{Method: http.MethodPut, Path: "/plugins/cpa-key-policy-plus/keys/weekly-only"},
			{Method: http.MethodPut, Path: "/plugins/cpa-key-policy-plus/keys/billing/resolve"},
			{Method: http.MethodPost, Path: "/plugins/cpa-key-policy-plus/keys/reset"},
			{Method: http.MethodGet, Path: "/plugins/cpa-key-policy-plus/events"},
			{Method: http.MethodGet, Path: "/plugins/cpa-key-policy-plus/codexcont"},
			{Method: http.MethodPut, Path: "/plugins/cpa-key-policy-plus/codexcont"},
			{Method: http.MethodGet, Path: "/plugins/cpa-key-policy-plus/pools"},
			{Method: http.MethodPut, Path: "/plugins/cpa-key-policy-plus/pools/save"},
			{Method: http.MethodPost, Path: "/plugins/cpa-key-policy-plus/pools/burst"},
		},
		Resources: []resourceRoute{
			{Path: "/admin", Menu: "CPA Key Policy+", Description: "Unified user key policy dashboard"},
			{Path: "/admin/api/status"},
			{Path: "/admin/api/keys"},
			{Path: "/admin/api/models"},
			{Path: "/admin/api/keys/save"},
			{Path: "/admin/api/keys/limits"},
			{Path: "/admin/api/keys/weekly-only"},
			{Path: "/admin/api/keys/billing/resolve"},
			{Path: "/admin/api/keys/reset"},
			{Path: "/admin/api/events"},
			{Path: "/admin/api/codexcont"},
			{Path: "/admin/api/pools"},
			{Path: "/user", Description: "Self-service usage dashboard"},
			{Path: "/user/api/session"},
			{Path: "/user/api/logout"},
			{Path: "/user/api/me"},
			{Path: "/user/api/usage"},
			{Path: "/user/api/events"},
			{Path: "/user/api/trend"},
			{Path: "/user/api/pool"},
			{Path: "/user/api/codexcont"},
		},
	}
	return okEnvelope(resp)
}

func managementHandle(raw []byte) ([]byte, error) {
	var req managementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	path := strings.TrimSpace(req.Path)
	switch {
	case path == "/v0/resource/plugins/cpa-key-policy-plus/admin" || path == "/admin":
		return managementHTML(adminHTML())
	case path == "/v0/resource/plugins/cpa-key-policy-plus/user" || path == "/user":
		return managementHTML(userHTML())
	case strings.HasSuffix(path, "/admin/api/status"):
		return plusStatus()
	case strings.HasSuffix(path, "/admin/api/keys"):
		return adminKeys(req)
	case strings.HasSuffix(path, "/admin/api/models"):
		return adminModels(req)
	case strings.HasSuffix(path, "/admin/api/keys/create"):
		return adminCreateKey(req)
	case strings.HasSuffix(path, "/admin/api/keys/save"):
		return adminSaveKeys(req)
	case strings.HasSuffix(path, "/admin/api/keys/limits"):
		return adminSetLimits(req)
	case strings.HasSuffix(path, "/admin/api/keys/weekly-only"):
		return adminEnableWeeklyOnly(req)
	case strings.HasSuffix(path, "/admin/api/keys/billing/resolve"):
		return adminResolvePendingBilling(req)
	case strings.HasSuffix(path, "/admin/api/keys/reset"):
		return adminReset(req)
	case strings.HasSuffix(path, "/admin/api/keys/archive"):
		return adminArchiveKey(req)
	case strings.HasSuffix(path, "/admin/api/keys/delete"):
		return adminDeleteKey(req)
	case strings.HasSuffix(path, "/admin/api/events"):
		return adminEvents(req)
	case strings.HasSuffix(path, "/admin/api/codexcont"):
		return adminCodexCont(req)
	case strings.HasSuffix(path, "/admin/api/pools"):
		return adminPools(req)
	case strings.HasSuffix(path, "/admin/api/pools/save"):
		return adminSavePools(req)
	case strings.HasSuffix(path, "/admin/api/pools/burst"):
		return adminPoolBurst(req)
	case strings.Contains(path, "/user/api/session"):
		return userSession(req)
	case strings.Contains(path, "/user/api/logout"):
		return userLogout(req)
	case strings.Contains(path, "/user/api/trend"):
		return userTrend(req)
	case strings.Contains(path, "/user/api/pool"):
		return userPool(req)
	case strings.Contains(path, "/user/api/me"):
		return userMe(req)
	case strings.Contains(path, "/user/api/usage"):
		return userUsage(req)
	case strings.Contains(path, "/user/api/events"):
		return userEvents(req)
	case strings.Contains(path, "/user/api/codexcont"):
		return userCodexCont(req)
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/keys"):
		return adminKeys(req)
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/models"):
		return adminModels(req)
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/keys/create"):
		return adminCreateKey(req)
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/keys/save"):
		return adminSaveKeys(req)
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/keys/limits"):
		return adminSetLimits(req)
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/keys/weekly-only"):
		return adminEnableWeeklyOnly(req)
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/keys/billing/resolve"):
		return adminResolvePendingBilling(req)
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/keys/reset"):
		return adminReset(req)
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/keys/archive"):
		return adminArchiveKey(req)
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/keys/delete"):
		return adminDeleteKey(req)
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/events"):
		return adminEvents(req)
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/codexcont"):
		return adminCodexCont(req)
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/pools"):
		return adminPools(req)
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/pools/save"):
		return adminSavePools(req)
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/pools/burst"):
		return adminPoolBurst(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/keys"):
		return adminKeys(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/models"):
		return adminModels(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/keys/create"):
		return adminCreateKey(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/keys/save"):
		return adminSaveKeys(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/keys/limits"):
		return adminSetLimits(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/keys/weekly-only"):
		return adminEnableWeeklyOnly(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/keys/billing/resolve"):
		return adminResolvePendingBilling(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/keys/reset"):
		return adminReset(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/keys/archive"):
		return adminArchiveKey(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/keys/delete"):
		return adminDeleteKey(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/events"):
		return adminEvents(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/codexcont"):
		return adminCodexCont(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/pools"):
		return adminPools(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/pools/save"):
		return adminSavePools(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/pools/burst"):
		return adminPoolBurst(req)
	default:
		return jsonResponse(http.StatusNotFound, map[string]any{"ok": false, "error": "not_found"})
	}
}

func plusStatus() ([]byte, error) {
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "plus": plusStatusPayload()})
}

func plusStatusPayload() map[string]any {
	cfg := loadedConfig()
	storeAvailable, lastConfigError, lastConfigErrorAt := loadedLifecycleStatus()
	out := map[string]any{
		"plugin_id":       pluginID,
		"enabled":         cfg.Enabled,
		"exclusive_auth":  cfg.ExclusiveAuth,
		"state_db_path":   cfg.StateDBPath,
		"store_available": storeAvailable,
		"mode":            "key_policy_plus",
	}
	if lastConfigError != "" {
		out["last_config_error"] = lastConfigError
		out["last_config_error_at"] = lastConfigErrorAt.Format(time.RFC3339)
	}
	return out
}

func adminKeys(req managementRequest) ([]byte, error) {
	if err := syncNativeKeysFromLoadedConfig(); err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": "native_key_sync_failed", "message": policyplus.Brief(err.Error(), 240)})
	}
	_ = refreshKeyPolicyState(false)
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	keys, err := store.ListKeys(context.Background())
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
	}
	priceBook := currentPriceBook(context.Background(), false)
	includeRemoved := truthyQuery(req.Query.Get("include_removed")) || truthyQuery(req.Query.Get("show_removed"))
	aliases := cpampAliasTargets(keys)
	keys = currentAdminKeyRows(keys, includeRemoved)
	safe := make([]map[string]any, 0, len(keys))
	now := time.Now()
	for _, key := range keys {
		row := key.Safe()
		usage := usageWindows(context.Background(), store, key.ID, now)
		quota := keyQuotaView(context.Background(), store, key, usage, now, row)
		row["usage"] = usage
		row["quota"] = quota
		if pool := poolKeyView(key.ID); pool != nil {
			row["pool"] = pool
		}
		if rate, err := store.RequestRate(context.Background(), key.ID, now.Add(-7*24*time.Hour), now); err == nil {
			row["rpm_stats"] = rate
		}
		if alias, ok := aliases[cpampPrincipalHash(key.ID)]; ok {
			row["cpamp_alias"] = alias
			row["cpamp_hash"] = cpampPrincipalHash(key.ID)[:12]
		}
		safe = append(safe, row)
	}
	maybeSyncCPAMPAliases(keys)
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "keys": safe, "codexcont": codexcontStatus(), "pricing": pricingStatus(priceBook), "cpamp_alias": cpampAliasStatus()})
}

func truthyQuery(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func currentAdminKeyRows(keys []policyplus.KeyRecord, includeRemoved bool) []policyplus.KeyRecord {
	out := make([]policyplus.KeyRecord, 0, len(keys))
	for _, key := range keys {
		if key.Source != policyplus.NativeCPASource {
			continue
		}
		if !includeRemoved && (!key.SourcePresent || key.Hidden) {
			continue
		}
		out = append(out, key)
	}
	return out
}

func adminModels(_ managementRequest) ([]byte, error) {
	priceBook := currentPriceBook(context.Background(), false)
	models, warnings := adminModelCatalog()
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "models": models, "warnings": warnings, "pricing": pricingStatus(priceBook)})
}

func adminModelCatalog() ([]policyplus.ModelOption, []string) {
	warnings := []string{}
	registryModels, registryWarnings := hostRegistryModelCatalog()
	warnings = append(warnings, registryWarnings...)
	hostModels, hostWarnings := hostAuthModelHints()
	warnings = append(warnings, hostWarnings...)
	configured := configuredModelOptions()
	models := policyplus.MergeModelOptions(registryModels, hostModels, configured)
	if len(models) == 0 {
		warnings = append(warnings, "当前没有从 CPA 或 Plus 配置中发现模型；可以先创建允许全部模型的 Key，或在编辑模型时手动输入模型名。")
	}
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].Known != models[j].Known {
			return models[i].Known
		}
		return strings.ToLower(models[i].ID) < strings.ToLower(models[j].ID)
	})
	return models, warnings
}

func hostRegistryModelCatalog() ([]policyplus.ModelOption, []string) {
	result, err := callHost(methodHostModelsList, map[string]any{})
	if err != nil {
		if models, fallbackErr := localCPAAPIModelCatalog(); fallbackErr == nil {
			return models, nil
		}
		return nil, []string{"CPA 模型注册表不可用，已使用宿主 auth 提示和 Plus 当前配置兜底。"}
	}
	var body hostModelsListResponse
	if err := json.Unmarshal(result, &body); err != nil {
		return nil, []string{"CPA 模型注册表格式无法解析，已使用宿主 auth 提示和 Plus 当前配置兜底。"}
	}
	out := make([]policyplus.ModelOption, 0, len(body.Models))
	seen := map[string]bool{}
	for _, item := range body.Models {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if seen[key] {
			continue
		}
		seen[key] = true
		display := strings.TrimSpace(item.DisplayName)
		if display == "" {
			display = id
		}
		out = append(out, policyplus.ModelOption{
			ID:          id,
			DisplayName: display,
			Type:        strings.TrimSpace(item.Type),
			OwnedBy:     strings.TrimSpace(item.OwnedBy),
			Provider:    strings.TrimSpace(item.Provider),
			AuthID:      strings.TrimSpace(item.AuthID),
			AuthName:    strings.TrimSpace(item.AuthName),
			Source:      "cpa_registry",
			Known:       true,
		})
	}
	return out, nil
}

func localCPAAPIModelCatalog() ([]policyplus.ModelOption, error) {
	cfg := loadedConfig()
	configPath := strings.TrimSpace(cfg.NativeKeysConfigPath)
	if configPath == "" {
		return nil, fmt.Errorf("native CPA config path is empty")
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read native CPA config: %w", err)
	}
	var cpaConfig struct {
		Port    int      `yaml:"port"`
		APIKeys []string `yaml:"api-keys"`
	}
	if err := yaml.Unmarshal(raw, &cpaConfig); err != nil {
		return nil, fmt.Errorf("decode native CPA config: %w", err)
	}
	if cpaConfig.Port <= 0 {
		cpaConfig.Port = 8317
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: nil},
		Timeout:   5 * time.Second,
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/v1/models", cpaConfig.Port)
	for _, rawKey := range cpaConfig.APIKeys {
		apiKey := policyplus.NormalizeSubmittedKey(rawKey)
		if apiKey == "" {
			continue
		}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("build local CPA models request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		var body struct {
			Data []hostModelListEntry `json:"data"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || decodeErr != nil || len(body.Data) == 0 {
			continue
		}
		out := make([]policyplus.ModelOption, 0, len(body.Data))
		seen := map[string]bool{}
		for _, item := range body.Data {
			id := strings.TrimSpace(item.ID)
			key := strings.ToLower(id)
			if id == "" || seen[key] {
				continue
			}
			seen[key] = true
			display := strings.TrimSpace(item.DisplayName)
			if display == "" {
				display = id
			}
			out = append(out, policyplus.ModelOption{
				ID:          id,
				DisplayName: display,
				Type:        strings.TrimSpace(item.Type),
				OwnedBy:     strings.TrimSpace(item.OwnedBy),
				Source:      "cpa_registry",
				Known:       true,
			})
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	return nil, fmt.Errorf("local CPA models endpoint is unavailable")
}

func configuredModelOptions() []policyplus.ModelOption {
	_ = syncNativeKeysFromLoadedConfig()
	store := loadedStore()
	if store == nil {
		return nil
	}
	keys, err := store.ListKeys(context.Background())
	if err != nil {
		return nil
	}
	ids := []string{}
	for _, key := range keys {
		ids = append(ids, key.Models...)
		for model := range key.Prices {
			ids = append(ids, model)
		}
	}
	return policyplus.ModelOptionsFromIDs(cleanStrings(ids), "plus_configured", false)
}

func hostAuthModelHints() ([]policyplus.ModelOption, []string) {
	result, err := callHost(methodHostAuthList, map[string]any{})
	if err != nil {
		return nil, []string{"宿主 auth 列表不可用，已使用 Plus 当前配置模型兜底。"}
	}
	var body struct {
		Files []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(result, &body); err != nil {
		return nil, []string{"宿主 auth 列表格式无法解析，已使用 Plus 当前配置模型兜底。"}
	}
	ids := []string{}
	for _, file := range body.Files {
		for _, field := range []string{"models", "available_models", "model_aliases"} {
			ids = append(ids, modelIDsFromAny(file[field])...)
		}
	}
	return policyplus.ModelOptionsFromIDs(cleanStrings(ids), "host_auth", true), nil
}

func modelIDsFromAny(raw any) []string {
	options := policyplus.NormalizeModelOptions(raw, "host_auth")
	out := make([]string, 0, len(options))
	for _, option := range options {
		out = append(out, option.ID)
	}
	return out
}

func adminCreateKey(req managementRequest) ([]byte, error) {
	_ = req
	return jsonResponse(http.StatusGone, map[string]any{
		"ok":      false,
		"error":   "native_key_lifecycle_owned_by_cpa",
		"message": "Key 新增、删除、复制和别名已交给 CPA/CPAMP 管理；Plus 只编辑已同步 Key 的策略。",
	})
}

func adminSaveKeys(req managementRequest) ([]byte, error) {
	var body struct {
		Keys []struct {
			ID                string                           `json:"id"`
			Name              string                           `json:"name"`
			Enabled           *bool                            `json:"enabled"`
			RPM               int                              `json:"rpm"`
			Concurrency       int                              `json:"concurrency"`
			MaxActiveSessions int                              `json:"max_active_sessions"`
			Models            []string                         `json:"models"`
			Prices            map[string]policyplus.ModelPrice `json:"prices"`
			FiveHourUSD       *float64                         `json:"five_hour_usd"`
			DailyUSD          *float64                         `json:"daily_usd"`
			WeeklyUSD         *float64                         `json:"weekly_usd"`
			MonthlyUSD        *float64                         `json:"monthly_usd"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_json"})
	}
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	if err := syncNativeKeysFromLoadedConfig(); err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": "native_key_sync_failed", "message": policyplus.Brief(err.Error(), 240)})
	}
	existing, err := store.ListKeys(context.Background())
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
	}
	byID := map[string]policyplus.KeyRecord{}
	for _, key := range existing {
		byID[key.ID] = key
	}
	for _, item := range body.Keys {
		key, ok := byID[strings.TrimSpace(item.ID)]
		if !ok {
			return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "unknown_key"})
		}
		if key.Source == policyplus.NativeCPASource && !key.SourcePresent {
			return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "source_removed_key_read_only"})
		}
		if item.Enabled != nil {
			key.Enabled = *item.Enabled
		}
		key.RPM = item.RPM
		key.Concurrency = 0
		key.MaxActiveSessions = 0
		key.Models = cleanStrings(item.Models)
		key.Prices = nil
		key.FiveHourUSD = item.FiveHourUSD
		key.DailyLimitUSD = item.DailyUSD
		key.WeeklyLimitUSD = item.WeeklyUSD
		key.MonthlyLimitUSD = item.MonthlyUSD
		if err := store.SaveKeySettings(context.Background(), key); err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		}
	}
	return adminKeys(req)
}

func cleanPrices(items map[string]policyplus.ModelPrice) map[string]policyplus.ModelPrice {
	if items == nil {
		return nil
	}
	out := map[string]policyplus.ModelPrice{}
	for name, price := range items {
		model := strings.TrimSpace(price.Model)
		if model == "" {
			model = strings.TrimSpace(name)
		}
		if model == "" {
			continue
		}
		price.Model = model
		out[model] = price
	}
	return out
}

func cleanStrings(items []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func adminSetLimits(req managementRequest) ([]byte, error) {
	var body struct {
		Limits []struct {
			ID          string   `json:"id"`
			FiveHourUSD *float64 `json:"five_hour_usd"`
			MonthlyUSD  *float64 `json:"monthly_usd"`
		} `json:"limits"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_json"})
	}
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	if err := syncNativeKeysFromLoadedConfig(); err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": "native_key_sync_failed", "message": policyplus.Brief(err.Error(), 240)})
	}
	for _, item := range body.Limits {
		if strings.TrimSpace(item.ID) == "" {
			return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "missing_key_id"})
		}
		if err := store.SetLimits(context.Background(), item.ID, item.FiveHourUSD, item.MonthlyUSD); err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		}
	}
	return adminKeys(req)
}

// adminEnableWeeklyOnly is intentionally a single-key, non-destructive
// migration endpoint. Unlike keys/save, omitted fields cannot clear RPM,
// model allowlists, or identity metadata while the deployment converts an
// existing key to its retained weekly budget.
func adminEnableWeeklyOnly(req managementRequest) ([]byte, error) {
	var body struct {
		ID         string   `json:"id"`
		WeeklyOnly bool     `json:"weekly_only"`
		WeeklyUSD  *float64 `json:"weekly_usd"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_json"})
	}
	if strings.TrimSpace(body.ID) == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "missing_key_id"})
	}
	if !body.WeeklyOnly {
		return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "weekly_only_must_be_true"})
	}
	if body.WeeklyUSD != nil && *body.WeeklyUSD < 0 {
		return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "usd_limits_must_not_be_negative"})
	}
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	if err := syncNativeKeysFromLoadedConfig(); err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": "native_key_sync_failed", "message": policyplus.Brief(err.Error(), 240)})
	}
	keys, err := store.ListKeys(context.Background())
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
	}
	var key policyplus.KeyRecord
	for _, candidate := range keys {
		if candidate.ID == strings.TrimSpace(body.ID) {
			key = candidate
			break
		}
	}
	if key.ID == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "unknown_key"})
	}
	if key.Source == policyplus.NativeCPASource && !key.SourcePresent {
		return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "source_removed_key_read_only"})
	}
	if key.WeeklyLimitUSD == nil && body.WeeklyUSD == nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "weekly_limit_required"})
	}
	if err := store.EnableWeeklyOnly(context.Background(), key.ID, body.WeeklyUSD); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
	}
	return adminKeys(req)
}

// adminResolvePendingBilling is deliberately narrow: it recomputes only rows
// marked billing_pending and releases the durable hold only when every one of
// them has a verified current or cached CPAMP rule.
func adminResolvePendingBilling(req managementRequest) ([]byte, error) {
	var body struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_json"})
	}
	body.ID = strings.TrimSpace(body.ID)
	if body.ID == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "missing_key_id"})
	}
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	if err := syncNativeKeysFromLoadedConfig(); err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": "native_key_sync_failed", "message": policyplus.Brief(err.Error(), 240)})
	}
	keys, err := store.ListKeys(context.Background())
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
	}
	var key policyplus.KeyRecord
	for _, candidate := range keys {
		if candidate.ID == body.ID {
			key = candidate
			break
		}
	}
	if key.ID == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "unknown_key"})
	}
	if strings.TrimSpace(key.BillingHoldReason) == "" {
		return jsonResponse(http.StatusConflict, map[string]any{"ok": false, "error": "billing_hold_not_active"})
	}
	book := currentPriceBook(context.Background(), true)
	resolved, err := store.ResolvePendingBilling(context.Background(), key.ID, book)
	if err != nil {
		return jsonResponse(http.StatusConflict, map[string]any{"ok": false, "error": "billing_price_unavailable", "message": policyplus.Brief(err.Error(), 240)})
	}
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "resolved_pending_events": resolved, "pricing": pricingStatus(book)})
}

func adminReset(req managementRequest) ([]byte, error) {
	var body struct {
		ID     string `json:"id"`
		Window string `json:"window"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_json"})
	}
	if body.Window == "" {
		body.Window = "all"
	}
	windows := []string{body.Window}
	if body.Window == "all" {
		windows = []string{policyplus.Range5H, policyplus.Range24H, policyplus.Range7D, policyplus.RangeMonth}
	}
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	if err := syncNativeKeysFromLoadedConfig(); err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": "native_key_sync_failed", "message": policyplus.Brief(err.Error(), 240)})
	}
	for _, window := range windows {
		if err := store.Reset(context.Background(), body.ID, window, time.Now()); err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		}
	}
	return jsonResponse(http.StatusOK, map[string]any{"ok": true})
}

func adminArchiveKey(req managementRequest) ([]byte, error) {
	_ = req
	return jsonResponse(http.StatusGone, map[string]any{
		"ok":      false,
		"error":   "native_key_lifecycle_owned_by_cpa",
		"message": "Key 生命周期已交给 CPA/CPAMP 管理；Plus 只保留策略和历史。",
	})
}

func adminDeleteKey(req managementRequest) ([]byte, error) {
	_ = req
	return jsonResponse(http.StatusGone, map[string]any{
		"ok":      false,
		"error":   "native_key_lifecycle_owned_by_cpa",
		"message": "Key 删除请在 CPA/CPAMP 中完成；Plus 会在同步后自动标记官方已移除并保留历史。",
	})
}

func adminEvents(req managementRequest) ([]byte, error) {
	keyID := req.Query.Get("key_id")
	if keyID == "" {
		keyID = "all"
	}
	return eventsResponseFromRequest(req, keyID)
}

func adminCodexCont(req managementRequest) ([]byte, error) {
	if req.Method == http.MethodPut || (req.Method == http.MethodGet && req.Query.Get("action") == "save") {
		var body struct {
			Enabled  *bool  `json:"enabled"`
			URL      string `json:"url"`
			FailMode string `json:"fail_mode"`
		}
		if req.Method == http.MethodGet {
			if rawEnabled := strings.TrimSpace(req.Query.Get("enabled")); rawEnabled != "" {
				if parsed, err := strconv.ParseBool(rawEnabled); err == nil {
					body.Enabled = &parsed
				}
			}
			body.URL = req.Query.Get("url")
			body.FailMode = req.Query.Get("fail_mode")
		} else {
			_ = json.Unmarshal(req.Body, &body)
		}
		store := loadedStore()
		state.mu.Lock()
		if body.Enabled != nil {
			state.cfg.CodexContEnabled = *body.Enabled
		}
		if strings.TrimSpace(body.URL) != "" {
			state.cfg.CodexContURL = strings.TrimRight(strings.TrimSpace(body.URL), "/")
		}
		if strings.TrimSpace(body.FailMode) != "" {
			state.cfg.FailMode = strings.ToLower(strings.TrimSpace(body.FailMode))
		}
		state.cfg = state.cfg.Normalize()
		cfg := state.cfg
		state.mu.Unlock()
		if store != nil {
			settings := map[string]string{
				"codexcont_enabled": strconv.FormatBool(cfg.CodexContEnabled),
				"codexcont_url":     cfg.CodexContURL,
				"fail_mode":         cfg.FailMode,
			}
			if err := store.SaveSettings(context.Background(), settings); err != nil {
				return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": "save_settings_failed"})
			}
		}
		return jsonResponse(http.StatusOK, map[string]any{"ok": true, "codexcont": codexcontStatus()})
	}
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "codexcont": codexcontStatus()})
}

func userSession(req managementRequest) ([]byte, error) {
	key := userSubmittedKey(req)
	if key == "" {
		hint := policyplus.ExplainUnmatchedSubmittedKey(key)
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": hint.Error, "message": hint.Message})
	}
	record, decision, ok := policyDecisionForRawKey(key, "", false)
	if !ok {
		hint := policyplus.ExplainUnmatchedSubmittedKey(key)
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": hint.Error, "message": hint.Message})
	}
	if record.ID != "" && decision.Code != "policy_missing" {
		decision = evaluateIdentityPolicy(record)
	}
	if !record.SourcePresent && decision.Code == "policy_missing" {
		// A mistyped or removed key: "no matching policy" would send the holder
		// to the admin page for a key that does not exist.
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": "key_not_found", "category": "auth", "message": "没有找到这个 Key：它不在 CPA 当前的 Key 列表里。请确认粘贴的是 CPAMP 里完整的原生 sk- Key，或联系管理员。"})
	}
	if !decision.Allowed {
		return jsonResponse(http.StatusForbidden, map[string]any{"ok": false, "error": decision.Code, "category": "auth", "message": decision.Message})
	}
	cfg := loadedConfig()
	token, err := policyplus.SignSession(policyplus.SessionPayload{
		KeyID:     record.ID,
		KeyHash:   record.KeyHash,
		ExpiresAt: time.Now().Add(policyplus.SessionTTL()).Unix(),
	}, cfg.SessionSecret)
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": "session_error"})
	}
	body, err := json.Marshal(map[string]any{"ok": true, "me": record.Safe()})
	if err != nil {
		return nil, err
	}
	resp := managementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":  []string{"application/json; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
			"Set-Cookie":    userSessionSetCookies(token),
		},
		Body: body,
	}
	return okEnvelope(resp)
}

var userSessionCookiePaths = []string{
	"/",
	"/v0/resource/plugins/cpa-key-policy-plus/user",
	"/key-policy-plus-user",
}

func userSessionSetCookies(token string) []string {
	out := make([]string, 0, len(userSessionCookiePaths))
	for _, path := range userSessionCookiePaths {
		out = append(out, (&http.Cookie{
			Name:     plusSessionCookieName,
			Value:    token,
			Path:     path,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int(policyplus.SessionTTL().Seconds()),
		}).String())
	}
	return out
}

// userSessionClearCookies expires every cookie keyFromSession accepts,
// including the legacy Governor session, on every path the login set.
func userSessionClearCookies() []string {
	out := make([]string, 0, 2*len(userSessionCookiePaths))
	for _, name := range []string{plusSessionCookieName, "cpa_governor_session"} {
		for _, path := range userSessionCookiePaths {
			out = append(out, (&http.Cookie{
				Name:     name,
				Value:    "",
				Path:     path,
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   -1,
			}).String())
		}
	}
	return out
}

func userLogout(_ managementRequest) ([]byte, error) {
	body, err := json.Marshal(map[string]any{"ok": true})
	if err != nil {
		return nil, err
	}
	return okEnvelope(managementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":  []string{"application/json; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
			"Set-Cookie":    userSessionClearCookies(),
		},
		Body: body,
	})
}

func userSubmittedKey(req managementRequest) string {
	raw := firstNonEmpty(
		headerFirst(req.Headers, "X-CPA-Key-Policy-Plus-Key"),
		headerFirst(req.Headers, "X-CPA-Governor-Key"),
		headerFirst(req.Headers, "X-CPA-User-Key"),
	)
	if raw == "" {
		raw = bearer(headerFirst(req.Headers, "Authorization"))
	}
	return policyplus.NormalizeSubmittedKey(raw)
}

func userMe(req managementRequest) ([]byte, error) {
	key, ok := keyFromSession(req)
	if !ok {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": "not_authenticated", "category": "auth", "message": "会话已过期，请重新登录。"})
	}
	row := key.Safe()
	priceBook := currentPriceBook(context.Background(), false)
	if store := loadedStore(); store != nil {
		now := time.Now()
		usage := usageWindows(context.Background(), store, key.ID, now)
		quota := keyQuotaView(context.Background(), store, key, usage, now, row)
		row["usage"] = usage
		row["quota"] = quota
	}
	if pool := poolKeyView(key.ID); pool != nil {
		row["pool"] = pool
	}
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "me": row, "pricing": pricingStatus(priceBook)})
}

func userUsage(req managementRequest) ([]byte, error) {
	key, ok := keyFromSession(req)
	if !ok {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": "not_authenticated", "category": "auth", "message": "会话已过期，请重新登录。"})
	}
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	priceBook := currentPriceBook(context.Background(), false)
	rangeName := req.Query.Get("range")
	if rangeName == "" {
		rangeName = policyplus.Range24H
	}
	summary, err := store.UsageSummary(context.Background(), key.ID, policyplus.WindowFor(rangeName, time.Now()))
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
	}
	settledCalls := summary.Calls - summary.BillingPending
	success := settledCalls - summary.Failed
	successRate := 0.0
	if settledCalls > 0 {
		successRate = float64(success) / float64(settledCalls)
	}
	return jsonResponse(http.StatusOK, map[string]any{
		"ok":      true,
		"range":   rangeName,
		"limits":  key.Safe()["limits"],
		"pricing": pricingStatus(priceBook),
		"summary": map[string]any{
			"calls":           summary.Calls,
			"success":         success,
			"failed":          summary.Failed,
			"billing_pending": summary.BillingPending,
			"success_rate":    successRate,
			"total_cost":      summary.TotalCost,
			"usage":           summary.Usage,
		},
	})
}

func userEvents(req managementRequest) ([]byte, error) {
	key, ok := keyFromSession(req)
	if !ok {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": "not_authenticated", "category": "auth", "message": "会话已过期，请重新登录。"})
	}
	return eventsResponseFromRequest(req, key.ID)
}

// userTrend returns the signed-in key's spend per hour (5h/24h) or per day
// (7d/month), bucketed on the viewer's clock and summing to the quota window.
func userTrend(req managementRequest) ([]byte, error) {
	key, ok := keyFromSession(req)
	if !ok {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": "not_authenticated", "category": "auth", "message": "会话已过期，请重新登录。"})
	}
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	rangeName := strings.ToLower(strings.TrimSpace(req.Query.Get("range")))
	switch rangeName {
	case "":
		rangeName = policyplus.Range24H
	case policyplus.Range5H, policyplus.Range24H, policyplus.Range7D, policyplus.RangeMonth:
	default:
		return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_range"})
	}
	bucket := time.Hour
	if rangeName == policyplus.Range7D || rangeName == policyplus.RangeMonth {
		bucket = 24 * time.Hour
	}
	series, err := store.UsageSeries(context.Background(), key.ID, policyplus.WindowFor(rangeName, time.Now()), bucket, viewerOffsetSeconds(req.Query.Get("tz_offset")))
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
	}
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "trend": series})
}

// viewerOffsetSeconds converts a browser Date.getTimezoneOffset() value
// (minutes behind UTC, so UTC+8 is -480) into seconds east of UTC. Missing or
// implausible values fall back to Beijing time, which the month window uses.
func viewerOffsetSeconds(raw string) int64 {
	minutes, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || minutes < -14*60 || minutes > 14*60 {
		return 8 * 60 * 60
	}
	return int64(-minutes) * 60
}

// annotateQuotaRecovery tells the pages when each window frees up:
// recovers_at for an exhausted rolling window if no new usage arrives, and
// resets_at for the Beijing-time month.
// keyQuotaView builds a key's USD window rows. For a key holding a pool
// share the windows govern only the models no share covers, so they are
// measured on that usage alone and row gets quota_scope "off_pool".
func keyQuotaView(ctx context.Context, store *policyplus.Store, key policyplus.KeyRecord, usage map[string]float64, now time.Time, row map[string]any) map[string]map[string]any {
	keep := offPoolModels(ctx, key.ID)
	if keep == nil {
		quota := quotaWindows(key, usage)
		annotateQuotaRecovery(ctx, store, key, quota, now, nil)
		return quota
	}
	off := map[string]float64{}
	for name := range usage {
		off[name], _ = store.UsageSumMatching(ctx, key.ID, policyplus.WindowFor(name, now), keep)
	}
	quota := quotaWindows(key, off)
	annotateQuotaRecovery(ctx, store, key, quota, now, keep)
	row["quota_scope"] = "off_pool"
	return quota
}

func annotateQuotaRecovery(ctx context.Context, store *policyplus.Store, key policyplus.KeyRecord, quota map[string]map[string]any, now time.Time, keep func(model string) bool) {
	if store == nil {
		return
	}
	for name, row := range quota {
		if name == policyplus.RangeMonth {
			row["resets_at"] = policyplus.MonthResetAt(now).Unix()
			continue
		}
		limit, ok := row["limit_usd"].(float64)
		used, _ := row["used_usd"].(float64)
		if !ok || used < limit {
			continue
		}
		if at, ok, err := store.WindowRecoveryAtMatching(ctx, key.ID, policyplus.WindowFor(name, now), limit, keep); err == nil && ok {
			row["recovers_at"] = at.Unix()
		}
	}
}

func userCodexCont(req managementRequest) ([]byte, error) {
	key, ok := keyFromSession(req)
	if !ok {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": "not_authenticated", "category": "auth", "message": "会话已过期，请重新登录。"})
	}
	limit := 80
	if rawLimit := strings.TrimSpace(req.Query.Get("limit")); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil {
			limit = parsed
		}
	}
	if limit <= 0 || limit > 200 {
		limit = 80
	}
	requests, source := codexRequestsForKey(key, limit)
	sortCodexSummariesNewestFirst(requests)
	return jsonResponse(http.StatusOK, map[string]any{
		"ok":        true,
		"codexcont": codexcontStatus(),
		"requests":  requests,
		"source":    source,
	})
}

func eventsResponse(keyID string, limit int) ([]byte, error) {
	return eventsResponseWithRange(keyID, "", limit)
}

func eventsResponseFromRequest(req managementRequest, keyID string) ([]byte, error) {
	limit := 100
	if rawLimit := strings.TrimSpace(req.Query.Get("limit")); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil {
			limit = parsed
		}
	}
	var before int64
	if rawBefore := strings.TrimSpace(req.Query.Get("before")); rawBefore != "" {
		if parsed, err := strconv.ParseInt(rawBefore, 10, 64); err == nil && parsed > 0 {
			before = parsed
		}
	}
	return eventsResponseWithWindow(keyID, req.Query.Get("range"), limit, before)
}

func eventsResponseWithRange(keyID string, rangeName string, limit int) ([]byte, error) {
	return eventsResponseWithWindow(keyID, rangeName, limit, 0)
}

// eventsResponseWithWindow pages a ranged event list backwards with before
// (unix seconds). Rows sharing the cursor second come back again; the page
// dedupes them by request id. Unranged lists ignore the cursor.
func eventsResponseWithWindow(keyID string, rangeName string, limit int, before int64) ([]byte, error) {
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	priceBook := currentPriceBook(context.Background(), false)
	var events []policyplus.UsageEvent
	var err error
	if strings.TrimSpace(rangeName) == "" {
		events, err = store.RecentEvents(context.Background(), keyID, limit)
	} else {
		window := policyplus.WindowFor(rangeName, time.Now())
		if before > 0 && before < window.To.Unix() {
			window.To = time.Unix(before, 0)
		}
		events, err = store.RecentEventsWindow(context.Background(), keyID, window, limit)
	}
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
	}
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "events": events, "pricing": pricingStatus(priceBook)})
}

func usageWindows(ctx context.Context, store *policyplus.Store, keyID string, now time.Time) map[string]float64 {
	out := map[string]float64{}
	for _, name := range []string{policyplus.Range5H, policyplus.Range24H, policyplus.Range7D, policyplus.RangeMonth} {
		value, _ := store.UsageSum(ctx, keyID, policyplus.WindowFor(name, now))
		out[name] = value
	}
	return out
}

func quotaWindows(key policyplus.KeyRecord, usage map[string]float64) map[string]map[string]any {
	limits := map[string]*float64{
		policyplus.Range5H:    key.FiveHourUSD,
		policyplus.Range24H:   key.DailyLimitUSD,
		policyplus.Range7D:    key.WeeklyLimitUSD,
		policyplus.RangeMonth: key.MonthlyLimitUSD,
	}
	out := map[string]map[string]any{}
	names := []string{policyplus.Range5H, policyplus.Range24H, policyplus.Range7D, policyplus.RangeMonth}
	if key.WeeklyOnly {
		names = []string{policyplus.Range7D}
	}
	for _, name := range names {
		used := usage[name]
		row := map[string]any{
			"used_usd":      used,
			"limit_usd":     nil,
			"remaining_usd": nil,
			"percent":       nil,
		}
		if limit := limits[name]; limit != nil {
			remaining := *limit - used
			if remaining < 0 {
				remaining = 0
			}
			percent := 0.0
			if *limit > 0 {
				percent = used / *limit
			}
			row["limit_usd"] = *limit
			row["remaining_usd"] = remaining
			row["percent"] = percent
		}
		out[name] = row
	}
	return out
}

func codexRequestsForKey(key policyplus.KeyRecord, limit int) ([]map[string]any, string) {
	cfg := loadedConfig()
	if !cfg.CodexContEnabled || strings.TrimSpace(cfg.CodexSummaryDBPath) == "" {
		return []map[string]any{}, "disabled"
	}
	if requests, source, ok := fetchExecutorCodexSummaries(key, limit); ok {
		return requests, source
	}
	store := loadedStore()
	if store == nil {
		return []map[string]any{}, "unavailable"
	}
	items, err := store.RecentCodexSummaries(context.Background(), key.ID, limit)
	if err != nil {
		return []map[string]any{}, "store_error"
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		safe := safeCodexSummary(item.Summary, key)
		if safe == nil {
			continue
		}
		out = append(out, safe)
	}
	sortCodexSummariesNewestFirst(out)
	return out, "governor_store"
}

func fetchExecutorCodexSummaries(key policyplus.KeyRecord, limit int) ([]map[string]any, string, bool) {
	cfg := loadedConfig()
	if !cfg.CodexContEnabled {
		return nil, "", false
	}
	path := strings.TrimSpace(cfg.CodexSummaryDBPath)
	if path == "" {
		return nil, "", false
	}
	items, err := policyplus.RecentCodexSummariesFromSQLite(context.Background(), path, key.ID, limit)
	if err != nil {
		return []map[string]any{}, "codexcont_executor_store_unavailable", true
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		safe := safeCodexSummaryWithTrustedRow(item, key)
		if safe == nil {
			continue
		}
		if staleCodexProcessingSummary(safe, item.UpdatedAt, time.Now()) {
			continue
		}
		out = append(out, safe)
	}
	if len(out) == 0 {
		fallback, err := policyplus.RecentCodexSummariesFromSQLite(context.Background(), path, "all", limit)
		if err == nil {
			for _, item := range fallback {
				safe := safeCodexSummary(item.Summary, key)
				if safe == nil {
					continue
				}
				if staleCodexProcessingSummary(safe, item.UpdatedAt, time.Now()) {
					continue
				}
				out = append(out, safe)
				if len(out) >= limit {
					break
				}
			}
		}
	}
	sortCodexSummariesNewestFirst(out)
	return out, "codexcont_executor_store", true
}

func safeCodexSummaryWithTrustedRow(item policyplus.CodexSummary, key policyplus.KeyRecord) map[string]any {
	summary := item.Summary
	if summary == nil {
		summary = map[string]any{}
	}
	if _, ok := summary["key_identity"].(map[string]any); !ok && strings.TrimSpace(item.KeyID) == key.ID {
		summary = cloneAnyMap(summary)
		summary["key_identity"] = map[string]any{"known": true, "id": key.ID, "preview": key.Preview}
	}
	return safeCodexSummary(summary, key)
}

func cloneAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func sortCodexSummariesNewestFirst(items []map[string]any) {
	sort.SliceStable(items, func(i, j int) bool {
		left, leftOK := codexSummaryDisplayTime(items[i])
		right, rightOK := codexSummaryDisplayTime(items[j])
		if leftOK != rightOK {
			return leftOK
		}
		if !leftOK {
			return false
		}
		return left.After(right)
	})
}

func codexSummaryDisplayTime(req map[string]any) (time.Time, bool) {
	for _, field := range []string{"started_at", "updated_at", "ended_at"} {
		if parsed, ok := parseCodexSummaryTime(req[field]); ok {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func staleCodexProcessingSummary(req map[string]any, rowUpdatedAt time.Time, now time.Time) bool {
	if req == nil {
		return false
	}
	status := strings.TrimSpace(fmt.Sprint(req["status"]))
	protection := strings.TrimSpace(fmt.Sprint(req["protection"]))
	if status != "processing" && protection != "processing" {
		return false
	}
	updated, ok := parseCodexSummaryTime(req["updated_at"])
	if !ok && !rowUpdatedAt.IsZero() {
		updated = rowUpdatedAt
		ok = true
	}
	if !ok {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}
	return now.Sub(updated) > codexProcessingStaleAfter
}

func parseCodexSummaryTime(value any) (time.Time, bool) {
	switch v := value.(type) {
	case time.Time:
		if v.IsZero() {
			return time.Time{}, false
		}
		return v, true
	case string:
		raw := strings.TrimSpace(v)
		if raw == "" {
			return time.Time{}, false
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, raw); err == nil {
				return parsed, true
			}
		}
		return time.Time{}, false
	case json.Number:
		if asInt, err := v.Int64(); err == nil {
			return unixLikeTime(asInt)
		}
		if asFloat, err := v.Float64(); err == nil {
			return unixLikeTime(int64(asFloat))
		}
	case float64:
		return unixLikeTime(int64(v))
	case int64:
		return unixLikeTime(v)
	case int:
		return unixLikeTime(int64(v))
	}
	return time.Time{}, false
}

func unixLikeTime(raw int64) (time.Time, bool) {
	if raw <= 0 {
		return time.Time{}, false
	}
	if raw > 1_000_000_000_000 {
		return time.UnixMilli(raw), true
	}
	return time.Unix(raw, 0), true
}

func safeCodexSummary(req map[string]any, key policyplus.KeyRecord) map[string]any {
	if req == nil {
		return nil
	}
	identity, _ := req["key_identity"].(map[string]any)
	if !codexIdentityMatches(identity, key) {
		return nil
	}
	fields := []string{
		"request_id", "model", "path", "started_at", "updated_at", "ended_at",
		"duration_ms", "status", "final_status", "protection", "latest_round",
		"latest_reasoning_tokens", "first_truncation_round",
		"first_truncation_reasoning_tokens", "first_truncation_n",
		"first_truncation_decision", "continuation_count", "stopped_reason",
		"failure_category", "failure_reason", "failure_detail", "folded", "passthrough",
		"passthrough_reason", "rounds",
	}
	out := map[string]any{}
	for _, field := range fields {
		if value, ok := req[field]; ok {
			out[field] = value
		}
	}
	if diagnostics, ok := req["diagnostics"].(map[string]any); ok {
		if brief := safeDiagnosticsBrief(diagnostics); len(brief) > 0 {
			out["diagnostics_brief"] = brief
		}
	}
	if decision, ok := req["route_decision"].(map[string]any); ok {
		if safe := safeRouteDecision(decision); len(safe) > 0 {
			out["route_decision"] = safe
		}
	}
	out["key_identity"] = key.Safe()
	return out
}

func safeRouteDecision(decision map[string]any) map[string]any {
	if decision == nil {
		return nil
	}
	out := map[string]any{}
	if protected, ok := decision["protected"].(bool); ok {
		out["protected"] = protected
	}
	for _, field := range []string{"body_bytes", "max_continue", "max_total_output_tokens"} {
		if value, ok := safeRouteDecisionNumber(decision[field]); ok {
			out[field] = value
		}
	}
	for _, field := range []string{"mode", "reason", "model", "key_scope", "fail_mode"} {
		value := safeRouteDecisionString(decision[field])
		if value != "" {
			out[field] = value
		}
	}
	return out
}

func safeRouteDecisionNumber(value any) (any, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return v, true
	case int32:
		return v, true
	case float64:
		return v, true
	case float32:
		return v, true
	case json.Number:
		return v, true
	default:
		return nil, false
	}
}

func safeRouteDecisionString(value any) string {
	text := policyplus.Brief(strings.TrimSpace(fmt.Sprint(value)), 120)
	if text == "" || text == "<nil>" {
		return ""
	}
	lower := strings.ToLower(text)
	for _, marker := range []string{"authorization", "bearer ", "sk-", "sk_", "cookie", "token", "secret"} {
		if strings.Contains(lower, marker) {
			return ""
		}
	}
	return text
}

func safeDiagnosticsBrief(diagnostics map[string]any) []map[string]any {
	rounds, _ := diagnostics["rounds"].([]any)
	out := make([]map[string]any, 0, len(rounds))
	for _, raw := range rounds {
		round, _ := raw.(map[string]any)
		if len(round) == 0 {
			continue
		}
		item := map[string]any{}
		for _, field := range []string{
			"round", "model", "requested_model", "body_model", "stream",
			"host_callback", "stream_id_present", "open_status_code",
			"open_error_category", "last_read_error_category",
			"upstream_error_type", "upstream_error_code",
			"first_read_payload_bytes", "last_read_payload_bytes",
			"read_count",
		} {
			if value, ok := round[field]; ok {
				item[field] = value
			}
		}
		for _, field := range []string{"open_error", "open_error_detail", "last_read_error", "last_read_error_detail", "upstream_error_message"} {
			if value := policyplus.Brief(strings.TrimSpace(fmt.Sprint(round[field])), 160); value != "" && value != "<nil>" {
				item[field] = value
			}
		}
		if len(item) > 0 {
			out = append(out, item)
		}
	}
	return out
}

func codexIdentityMatches(identity map[string]any, key policyplus.KeyRecord) bool {
	if strings.TrimSpace(key.ID) == "" || identity == nil {
		return false
	}
	id := strings.TrimSpace(fmt.Sprint(identity["id"]))
	if id != "" && id == key.ID {
		return true
	}
	preview := strings.TrimSpace(fmt.Sprint(identity["preview"]))
	return preview != "" && preview == key.Preview
}

func forwardHostStream(targetStreamID string, sourceStreamID string) {
	defer func() {
		_, _ = callHost(methodHostModelStreamClose, hostModelStreamCloseRequest{StreamID: sourceStreamID})
		_, _ = callHost(methodHostStreamClose, hostStreamCloseRequest{StreamID: targetStreamID})
	}()
	for {
		result, err := callHost(methodHostModelStreamRead, hostModelStreamReadRequest{StreamID: sourceStreamID})
		if err != nil {
			_, _ = callHost(methodHostStreamEmit, hostStreamEmitRequest{
				StreamID: targetStreamID,
				Error:    policyplus.Brief(err.Error(), 400),
			})
			return
		}
		var chunk hostModelStreamReadResponse
		if err := json.Unmarshal(result, &chunk); err != nil {
			_, _ = callHost(methodHostStreamEmit, hostStreamEmitRequest{
				StreamID: targetStreamID,
				Error:    "decode host stream chunk: " + policyplus.Brief(err.Error(), 300),
			})
			return
		}
		if len(chunk.Payload) > 0 {
			_, _ = callHost(methodHostStreamEmit, hostStreamEmitRequest{
				StreamID: targetStreamID,
				Payload:  chunk.Payload,
			})
		}
		if chunk.Error != "" {
			_, _ = callHost(methodHostStreamEmit, hostStreamEmitRequest{
				StreamID: targetStreamID,
				Error:    policyplus.Brief(chunk.Error, 400),
			})
			return
		}
		if chunk.Done {
			return
		}
	}
}

func keyFromSession(req managementRequest) (policyplus.KeyRecord, bool) {
	_ = syncNativeKeysFromLoadedConfig()
	_ = refreshKeyPolicyState(false)
	tokens := sessionTokensFromCookie(headerFirst(req.Headers, "Cookie"))
	cfg := loadedConfig()
	for _, token := range tokens {
		if key, ok := keyFromSessionToken(token, cfg.SessionSecret); ok {
			return key, true
		}
	}
	return policyplus.KeyRecord{}, false
}

func sessionTokensFromCookie(cookie string) []string {
	var tokens []string
	for _, part := range strings.Split(cookie, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, plusSessionCookieName+"=") {
			tokens = append(tokens, strings.TrimPrefix(part, plusSessionCookieName+"="))
			continue
		}
		if strings.HasPrefix(part, "cpa_governor_session=") {
			tokens = append(tokens, strings.TrimPrefix(part, "cpa_governor_session="))
		}
	}
	return tokens
}

func keyFromSessionToken(token string, secret string) (policyplus.KeyRecord, bool) {
	payload, ok := policyplus.VerifySession(token, secret, time.Now())
	if !ok {
		return policyplus.KeyRecord{}, false
	}
	store := loadedStore()
	if store == nil {
		return policyplus.KeyRecord{}, false
	}
	keys, err := store.ListKeys(context.Background())
	if err != nil {
		return policyplus.KeyRecord{}, false
	}
	for _, key := range keys {
		currentHash, errCurrent := policyplus.NormalizeHash(key.KeyHash)
		sessionHash, errSession := policyplus.NormalizeHash(payload.KeyHash)
		if key.ID == payload.KeyID && key.Enabled && !key.Archived && errCurrent == nil && errSession == nil && currentHash == sessionHash {
			return key, true
		}
	}
	return policyplus.KeyRecord{}, false
}

func codexcontStatus() map[string]any {
	cfg := loadedConfig()
	status := map[string]any{
		"enabled": cfg.CodexContEnabled,
		"route":   cfg.CodexContRoute,
		"mode":    "disabled",
	}
	if !cfg.CodexContEnabled {
		status["history_source"] = "disabled"
		return status
	}
	status["url"] = cfg.CodexContURL
	status["fail_mode"] = cfg.FailMode
	status["mode"] = "legacy_executor_history"
	health := probeCodexContHealth(cfg.CodexContURL)
	for key, value := range health {
		status[key] = value
	}
	return status
}

func probeCodexContHealth(baseURL string) map[string]any {
	out := map[string]any{
		"health_ok": false,
	}
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/") + "/engine/healthz")
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		out["health_error"] = "invalid_engine_url"
		return out
	}
	client := http.Client{Timeout: 800 * time.Millisecond}
	resp, err := client.Get(parsed.String())
	if err != nil {
		out["health_error"] = policyplus.Brief(err.Error(), 200)
		return out
	}
	defer resp.Body.Close()
	out["health_status"] = resp.StatusCode
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		out["health_ok"] = true
	}
	return out
}

func bearer(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(value), "bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return ""
}

func headerFirst(headers http.Header, name string) string {
	if headers == nil {
		return ""
	}
	if value := strings.TrimSpace(headers.Get(name)); value != "" {
		return value
	}
	for key, values := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func cloneHeader(headers http.Header) http.Header {
	if headers == nil {
		return nil
	}
	cloned := make(http.Header, len(headers))
	for key, values := range headers {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}

func cloneValues(values map[string][]string) map[string][]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string][]string, len(values))
	for key, items := range values {
		cloned[key] = append([]string(nil), items...)
	}
	return cloned
}

func okEnvelope(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func jsonResponse(status int, v any) ([]byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return okEnvelope(managementResponse{
		StatusCode: status,
		Headers: http.Header{
			"Content-Type":  []string{"application/json; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
		},
		Body: body,
	})
}

func managementHTML(html string) ([]byte, error) {
	return okEnvelope(managementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":  []string{"text/html; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
		},
		Body: []byte(html),
	})
}

var hostCall = func(method string, payload any) (json.RawMessage, error) {
	_ = payload
	return nil, fmt.Errorf("host callback %s is unavailable", method)
}

func callHost(method string, payload any) (json.RawMessage, error) {
	return hostCall(method, payload)
}

func adminHTML() string {
	return renderHTML(adminHTMLTemplate)
}

func userHTML() string {
	return renderHTML(userHTMLTemplate)
}

func sharedCSS() string {
	return sharedCSSTemplate
}

func renderHTML(tpl string) string {
	tpl = strings.ReplaceAll(tpl, "{{SHARED_CSS}}", sharedCSS())
	return strings.ReplaceAll(tpl, "{{CSS}}", sharedCSS())
}
