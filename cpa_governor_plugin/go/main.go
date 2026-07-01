package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"codexcont/cpa-governor-plugin/internal/governor"
	"gopkg.in/yaml.v3"
)

const pluginID = "cpa-governor"

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
	Executor                      bool     `json:"executor"`
	ExecutorModelScope            string   `json:"executor_model_scope"`
	ExecutorInputFormats          []string `json:"executor_input_formats"`
	ExecutorOutputFormats         []string `json:"executor_output_formats"`
	UsagePlugin                   bool     `json:"usage_plugin"`
	ManagementAPI                 bool     `json:"management_api"`
}

type runtimeState struct {
	mu                sync.RWMutex
	cfg               governor.Config
	store             *governor.Store
	keyState          governor.KeyPolicyState
	keyStatePath      string
	keyStateModTime   time.Time
	keyStateLastCheck time.Time
	rpmBuckets        map[string][]time.Time
	concurrency       map[string]int
}

var state = runtimeState{
	rpmBuckets:  map[string][]time.Time{},
	concurrency: map[string]int{},
}

func main() { runPreviewIfRequested() }

func runPreviewIfRequested() {
	addr := strings.TrimSpace(os.Getenv("CPA_GOVERNOR_PREVIEW_ADDR"))
	if addr == "" {
		return
	}
	cfg := governor.DefaultConfig()
	cfg.StateDBPath = ":memory:"
	cfg.SessionSecret = "preview-secret"
	cfg.CodexContEnabled = true
	store, err := governor.OpenStore(cfg.StateDBPath)
	if err != nil {
		panic(err)
	}
	previewKey := governor.KeyRecord{
		ID:              "preview-key",
		Name:            "演示用户",
		KeyHash:         "sha256:" + governor.SHA256Hex("cpa_preview"),
		Enabled:         true,
		Preview:         governor.HashPreview(governor.SHA256Hex("cpa_preview")),
		RPM:             60,
		Concurrency:     2,
		Models:          []string{"gpt-5.5", "gpt-5.4"},
		FiveHourUSD:     floatPtr(2),
		DailyLimitUSD:   floatPtr(8),
		WeeklyLimitUSD:  floatPtr(40),
		MonthlyLimitUSD: floatPtr(120),
		Prices: map[string]governor.ModelPrice{
			"gpt-5.5": {Model: "gpt-5.5", InputPerMillion: 5, OutputPerMillion: 30, CacheReadPerMillion: 0.5},
		},
	}
	_ = store.UpsertKey(context.Background(), previewKey)
	_ = store.InsertUsage(context.Background(), governor.UsageEvent{
		RequestID:   "req-preview-1",
		KeyID:       previewKey.ID,
		KeyPreview:  previewKey.Preview,
		Model:       "gpt-5.5",
		Endpoint:    "/v1/responses",
		RequestedAt: time.Now().Add(-3 * time.Minute),
		LatencyMS:   14320,
		Usage: governor.TokenUsage{
			InputTokens:     120000,
			CachedTokens:    103000,
			OutputTokens:    2100,
			ReasoningTokens: 516,
			TotalTokens:     122100,
		},
		Cost: 0.151,
		CostBreakdown: governor.CostForUsage(previewKey.Prices["gpt-5.5"], governor.TokenUsage{
			InputTokens:     120000,
			CachedTokens:    103000,
			OutputTokens:    2100,
			ReasoningTokens: 516,
			TotalTokens:     122100,
		}, "gpt-5.5"),
	})
	state.mu.Lock()
	state.cfg = cfg
	state.store = store
	state.keyState = governor.KeyPolicyState{Keys: []governor.KeyRecord{previewKey}}
	state.rpmBuckets = map[string][]time.Time{}
	state.concurrency = map[string]int{}
	state.mu.Unlock()
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/resource/plugins/cpa-governor/admin", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		w.Header().Set("content-type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(adminHTML()))
	})
	mux.HandleFunc("/v0/resource/plugins/cpa-governor/user", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		w.Header().Set("content-type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(userHTML()))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		rawReq, _ := json.Marshal(managementRequest{
			Method:  r.Method,
			Path:    r.URL.Path,
			Headers: r.Header,
			Query:   r.URL.Query(),
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
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case methodPluginRegister, methodPluginReconfigure:
		if err := configure(request); err != nil {
			return nil, err
		}
		return okEnvelope(pluginRegistration())
	case methodFrontendAuthIdentifier:
		return okEnvelope(map[string]string{"identifier": pluginID})
	case methodFrontendAuthAuthenticate:
		return frontendAuth(request)
	case methodModelRoute:
		return routeModel(request)
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
	cfg := governor.DefaultConfig()
	if len(raw) > 0 {
		var req lifecycleRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return err
		}
		if len(req.ConfigYAML) > 0 {
			if err := yaml.Unmarshal(req.ConfigYAML, &cfg); err != nil {
				return err
			}
		}
	}
	cfg = cfg.Normalize()
	store, err := governor.OpenStore(cfg.StateDBPath)
	if err != nil {
		return err
	}
	var keyState governor.KeyPolicyState
	if strings.TrimSpace(cfg.KeyPolicyStatePath) != "" {
		loaded, err := governor.LoadKeyPolicyState(cfg.KeyPolicyStatePath)
		if err == nil {
			keyState = loaded
			_ = store.ImportKeys(context.Background(), loaded)
		}
	}
	state.mu.Lock()
	old := state.store
	state.cfg = cfg
	state.store = store
	state.keyState = keyState
	state.keyStatePath = strings.TrimSpace(cfg.KeyPolicyStatePath)
	state.keyStateModTime = time.Time{}
	state.keyStateLastCheck = time.Time{}
	state.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	_ = refreshKeyPolicyState(true)
	return nil
}

func pluginRegistration() registration {
	cfg := loadedConfig()
	return registration{
		SchemaVersion: schemaVersion,
		Metadata: metadata{
			Name:             "cpa-governor",
			Version:          "0.1.0",
			Author:           "konbakuyomu/CodexCont",
			GitHubRepository: "https://local/CodexCont",
			ConfigFields: []configField{
				{Name: "enabled", Type: configBoolean, Description: "Enable Governor key authentication and management surfaces."},
				{Name: "exclusive_auth", Type: configBoolean, Description: "When true, Governor frontend auth is exclusive."},
				{Name: "state_db_path", Type: configString, Description: "SQLite path for Governor state."},
				{Name: "key_policy_state_path", Type: configString, Description: "Optional CPA Key Policy state JSON path to import/mirror."},
				{Name: "session_secret", Type: configString, Description: "Secret used to sign user portal sessions."},
				{Name: "codexcont_enabled", Type: configBoolean, Description: "Enable CodexCont engine status integration."},
				{Name: "codexcont_route", Type: configBoolean, Description: "Route protected Responses requests to Governor executor. Keep false until cutover validation."},
				{Name: "codexcont_url", Type: configString, Description: "Internal CodexCont engine base URL."},
				{Name: "fail_mode", Type: configEnum, EnumValues: []string{"fallback", "fail_closed"}, Description: "Behavior when engine is unavailable."},
			},
		},
		Capabilities: capabilities{
			FrontendAuthProvider:          true,
			FrontendAuthProviderExclusive: cfg.ExclusiveAuth,
			ModelRouter:                   true,
			Executor:                      true,
			ExecutorModelScope:            "static",
			ExecutorInputFormats:          []string{"openai", "responses"},
			ExecutorOutputFormats:         []string{"openai", "responses"},
			UsagePlugin:                   true,
			ManagementAPI:                 true,
		},
	}
}

func loadedConfig() governor.Config {
	state.mu.RLock()
	defer state.mu.RUnlock()
	if state.cfg.StateDBPath == "" {
		return governor.DefaultConfig()
	}
	return state.cfg
}

func loadedStore() *governor.Store {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.store
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
	if !ok || !record.Enabled {
		return okEnvelope(frontendAuthResponse{Authenticated: false})
	}
	model := requestedModelFromBody(req.Body)
	if model != "" && !governor.ModelAllowed(record.Models, model) {
		return okEnvelope(frontendAuthResponse{Authenticated: false})
	}
	if !allowRPM(record) {
		return okEnvelope(frontendAuthResponse{Authenticated: false})
	}
	if !allowQuota(record) {
		return okEnvelope(frontendAuthResponse{Authenticated: false})
	}
	if !acquireConcurrency(record) {
		return okEnvelope(frontendAuthResponse{Authenticated: false})
	}
	return okEnvelope(frontendAuthResponse{
		Authenticated: true,
		Principal:     record.ID,
		Metadata: map[string]string{
			"provider": "cpa-governor",
			"key_id":   record.ID,
			"key_name": record.Name,
			"preview":  record.Preview,
		},
	})
}

func findKeyByRaw(rawKey string) (governor.KeyRecord, bool) {
	_ = refreshKeyPolicyState(false)
	state.mu.RLock()
	keyState := state.keyState
	store := state.store
	state.mu.RUnlock()
	if key, ok := keyState.FindByRawKey(rawKey); ok {
		return key, true
	}
	if store != nil {
		key, ok, err := store.FindKeyByHash(context.Background(), governor.SHA256Hex(rawKey))
		if err == nil && ok {
			return key, true
		}
	}
	return governor.KeyRecord{}, false
}

func refreshKeyPolicyState(force bool) error {
	state.mu.RLock()
	path := strings.TrimSpace(state.keyStatePath)
	lastCheck := state.keyStateLastCheck
	lastMod := state.keyStateModTime
	store := state.store
	state.mu.RUnlock()
	if path == "" || store == nil {
		return nil
	}
	now := time.Now()
	if !force && now.Sub(lastCheck) < 2*time.Second {
		return nil
	}
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
	loaded, err := governor.LoadKeyPolicyState(path)
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

func routeModel(raw []byte) ([]byte, error) {
	var req modelRouteRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	cfg := loadedConfig()
	if !cfg.Enabled || !cfg.CodexContRoute {
		return okEnvelope(modelRouteResponse{Handled: false})
	}
	if !isResponsesRequest(req.SourceFormat, req.Body) {
		return okEnvelope(modelRouteResponse{Handled: false})
	}
	return okEnvelope(modelRouteResponse{
		Handled:    true,
		TargetKind: routeTargetSelf,
		Reason:     "cpa_governor_codexcont_engine_enabled",
	})
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

func allowRPM(key governor.KeyRecord) bool {
	if key.RPM <= 0 {
		return true
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
		return false
	}
	state.rpmBuckets[key.ID] = append(kept, now)
	return true
}

func allowQuota(key governor.KeyRecord) bool {
	store := loadedStore()
	if store == nil {
		return true
	}
	ctx := context.Background()
	now := time.Now()
	limits := []struct {
		name  string
		limit *float64
	}{
		{governor.Range5H, key.FiveHourUSD},
		{governor.Range24H, key.DailyLimitUSD},
		{governor.Range7D, key.WeeklyLimitUSD},
		{governor.RangeMonth, key.MonthlyLimitUSD},
	}
	for _, item := range limits {
		used, err := store.UsageSum(ctx, key.ID, governor.WindowFor(item.name, now))
		if err != nil {
			continue
		}
		if !governor.CheckLimit(used, item.limit).Allowed {
			return false
		}
	}
	return true
}

func acquireConcurrency(key governor.KeyRecord) bool {
	if key.Concurrency <= 0 {
		return true
	}
	state.mu.Lock()
	if state.concurrency[key.ID] >= key.Concurrency {
		state.mu.Unlock()
		return false
	}
	state.concurrency[key.ID]++
	state.mu.Unlock()
	go func() {
		time.Sleep(10 * time.Minute)
		releaseConcurrency(key.ID)
	}()
	return true
}

func releaseConcurrency(keyID string) {
	if strings.TrimSpace(keyID) == "" {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.concurrency[keyID] > 0 {
		state.concurrency[keyID]--
	}
}

func executorUnavailable() ([]byte, error) {
	cfg := loadedConfig()
	if cfg.CodexContRoute && cfg.FailMode == "fail_closed" {
		return errorEnvelope("codexcont_executor_pending", "CPA Governor protected executor is not enabled in this build"), nil
	}
	return errorEnvelope("codexcont_executor_pending", "CPA Governor protected executor is in passive mode; disable codexcont_enabled to route through CPA provider path"), nil
}

func executorExecute(raw []byte) ([]byte, error) {
	var req executorCallRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	cfg := loadedConfig()
	if !cfg.CodexContRoute {
		return executorUnavailable()
	}
	payload := req.ExecutorRequest.Payload
	if len(payload) == 0 {
		payload = req.ExecutorRequest.OriginalRequest
	}
	result, err := callHost(methodHostModelExecute, hostModelExecutionRequest{
		EntryProtocol:  firstNonEmpty(req.ExecutorRequest.SourceFormat, "openai"),
		ExitProtocol:   firstNonEmpty(req.ExecutorRequest.Format, "openai"),
		Model:          req.ExecutorRequest.Model,
		Stream:         false,
		Body:           payload,
		Headers:        cloneHeader(req.ExecutorRequest.Headers),
		Query:          cloneValues(req.ExecutorRequest.Query),
		Alt:            req.ExecutorRequest.Alt,
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
	cfg := loadedConfig()
	if !cfg.CodexContRoute {
		return executorUnavailable()
	}
	if strings.TrimSpace(req.StreamID) == "" {
		return errorEnvelope("stream_id_required", "stream_id is required for executor.execute_stream"), nil
	}
	payload := req.ExecutorRequest.Payload
	if len(payload) == 0 {
		payload = req.ExecutorRequest.OriginalRequest
	}
	result, err := callHost(methodHostModelExecuteStream, hostModelExecutionRequest{
		EntryProtocol:  firstNonEmpty(req.ExecutorRequest.SourceFormat, "openai"),
		ExitProtocol:   firstNonEmpty(req.ExecutorRequest.Format, "openai"),
		Model:          req.ExecutorRequest.Model,
		Stream:         true,
		Body:           payload,
		Headers:        cloneHeader(req.ExecutorRequest.Headers),
		Query:          cloneValues(req.ExecutorRequest.Query),
		Alt:            req.ExecutorRequest.Alt,
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
	keys, _ := store.ListKeys(context.Background())
	keyByID := map[string]governor.KeyRecord{}
	for _, key := range keys {
		keyByID[key.ID] = key
	}
	key := keyByID[rec.APIKey]
	if key.ID == "" {
		key = keyByID[rec.Source]
	}
	if key.ID != "" {
		releaseConcurrency(key.ID)
	}
	usage := governor.TokenUsage{
		InputTokens:         rec.Detail.InputTokens,
		OutputTokens:        rec.Detail.OutputTokens,
		CachedTokens:        rec.Detail.CachedTokens,
		CacheReadTokens:     rec.Detail.CacheReadTokens,
		CacheCreationTokens: rec.Detail.CacheCreationTokens,
		ReasoningTokens:     rec.Detail.ReasoningTokens,
		TotalTokens:         rec.Detail.TotalTokens,
	}
	var cost float64
	var breakdown governor.CostBreakdown
	if key.ID != "" {
		if price, ok := governor.PriceForModel(key.Prices, firstNonEmpty(rec.Alias, rec.Model)); ok {
			breakdown = governor.CostForUsage(price, usage, firstNonEmpty(rec.Alias, rec.Model))
			cost = breakdown.Costs["total"]
		}
	}
	event := governor.UsageEvent{
		RequestID:     rec.ResponseHeaders.Get("x-request-id"),
		KeyID:         key.ID,
		KeyPreview:    key.Preview,
		Model:         firstNonEmpty(rec.Alias, rec.Model),
		Endpoint:      rec.Source,
		RequestedAt:   rec.RequestedAt,
		LatencyMS:     rec.Latency.Milliseconds(),
		Failed:        rec.Failed,
		Failure:       governor.Brief(rec.Failure.Body, 600),
		Usage:         usage,
		Cost:          cost,
		CostBreakdown: breakdown,
	}
	_ = store.InsertUsage(context.Background(), event)
	return okEnvelope(map[string]any{})
}

func managementRegister() ([]byte, error) {
	resp := managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: "/plugins/cpa-governor/keys"},
			{Method: http.MethodPut, Path: "/plugins/cpa-governor/keys/limits"},
			{Method: http.MethodPost, Path: "/plugins/cpa-governor/keys/reset"},
			{Method: http.MethodGet, Path: "/plugins/cpa-governor/events"},
			{Method: http.MethodGet, Path: "/plugins/cpa-governor/codexcont"},
			{Method: http.MethodPut, Path: "/plugins/cpa-governor/codexcont"},
		},
		Resources: []resourceRoute{
			{Path: "/admin", Menu: "CPA Governor", Description: "Governor admin dashboard"},
			{Path: "/admin/api/keys"},
			{Path: "/admin/api/keys/limits"},
			{Path: "/admin/api/keys/reset"},
			{Path: "/admin/api/events"},
			{Path: "/admin/api/codexcont"},
			{Path: "/user", Menu: "CPA Usage", Description: "Self-service usage dashboard"},
			{Path: "/user/api/session"},
			{Path: "/user/api/me"},
			{Path: "/user/api/usage"},
			{Path: "/user/api/events"},
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
	case path == "/v0/resource/plugins/cpa-governor/admin" || path == "/admin":
		return managementHTML(adminHTML())
	case path == "/v0/resource/plugins/cpa-governor/user" || path == "/user":
		return managementHTML(userHTML())
	case strings.HasSuffix(path, "/admin/api/keys"):
		return adminKeys(req)
	case strings.HasSuffix(path, "/admin/api/keys/limits"):
		return adminSetLimits(req)
	case strings.HasSuffix(path, "/admin/api/keys/reset"):
		return adminReset(req)
	case strings.HasSuffix(path, "/admin/api/events"):
		return adminEvents(req)
	case strings.HasSuffix(path, "/admin/api/codexcont"):
		return adminCodexCont(req)
	case strings.Contains(path, "/user/api/session"):
		return userSession(req)
	case strings.Contains(path, "/user/api/me"):
		return userMe(req)
	case strings.Contains(path, "/user/api/usage"):
		return userUsage(req)
	case strings.Contains(path, "/user/api/events"):
		return userEvents(req)
	case strings.Contains(path, "/user/api/codexcont"):
		return jsonResponse(http.StatusOK, map[string]any{"ok": true, "codexcont": codexcontStatus()})
	case strings.HasSuffix(path, "/plugins/cpa-governor/keys"):
		return adminKeys(req)
	case strings.HasSuffix(path, "/plugins/cpa-governor/keys/limits"):
		return adminSetLimits(req)
	case strings.HasSuffix(path, "/plugins/cpa-governor/keys/reset"):
		return adminReset(req)
	case strings.HasSuffix(path, "/plugins/cpa-governor/events"):
		return adminEvents(req)
	case strings.HasSuffix(path, "/plugins/cpa-governor/codexcont"):
		return adminCodexCont(req)
	default:
		return jsonResponse(http.StatusNotFound, map[string]any{"ok": false, "error": "not_found"})
	}
}

func adminKeys(_ managementRequest) ([]byte, error) {
	_ = refreshKeyPolicyState(false)
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	keys, err := store.ListKeys(context.Background())
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
	}
	safe := make([]map[string]any, 0, len(keys))
	now := time.Now()
	for _, key := range keys {
		row := key.Safe()
		row["usage"] = usageWindows(context.Background(), store, key.ID, now)
		safe = append(safe, row)
	}
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "keys": safe, "codexcont": codexcontStatus()})
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
		windows = []string{governor.Range5H, governor.Range24H, governor.Range7D, governor.RangeMonth}
	}
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	for _, window := range windows {
		if err := store.Reset(context.Background(), body.ID, window, time.Now()); err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		}
	}
	return jsonResponse(http.StatusOK, map[string]any{"ok": true})
}

func adminEvents(req managementRequest) ([]byte, error) {
	keyID := req.Query.Get("key_id")
	if keyID == "" {
		keyID = "all"
	}
	return eventsResponseFromRequest(req, keyID)
}

func adminCodexCont(req managementRequest) ([]byte, error) {
	if req.Method == http.MethodPut {
		var body struct {
			Enabled  *bool  `json:"enabled"`
			URL      string `json:"url"`
			FailMode string `json:"fail_mode"`
		}
		_ = json.Unmarshal(req.Body, &body)
		state.mu.Lock()
		if body.Enabled != nil {
			state.cfg.CodexContEnabled = *body.Enabled
		}
		if strings.TrimSpace(body.URL) != "" {
			state.cfg.CodexContURL = strings.TrimRight(strings.TrimSpace(body.URL), "/")
		}
		if strings.TrimSpace(body.FailMode) != "" {
			state.cfg.FailMode = strings.TrimSpace(body.FailMode)
		}
		cfg := state.cfg
		state.mu.Unlock()
		return jsonResponse(http.StatusOK, map[string]any{"ok": true, "codexcont": cfg})
	}
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "codexcont": codexcontStatus()})
}

func userSession(req managementRequest) ([]byte, error) {
	key := bearer(req.Headers.Get("Authorization"))
	if key == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": "missing_api_key"})
	}
	record, ok := findKeyByRaw(key)
	if !ok {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": "invalid_api_key"})
	}
	if !record.Enabled {
		return jsonResponse(http.StatusForbidden, map[string]any{"ok": false, "error": "api_key_disabled"})
	}
	cfg := loadedConfig()
	token, err := governor.SignSession(governor.SessionPayload{
		KeyID:     record.ID,
		KeyHash:   record.KeyHash,
		ExpiresAt: time.Now().Add(governor.SessionTTL()).Unix(),
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
			"content-type": []string{"application/json; charset=utf-8"},
			"set-cookie":   []string{fmt.Sprintf("cpa_governor_session=%s; Path=/; HttpOnly; SameSite=Lax; Max-Age=86400", token)},
		},
		Body: body,
	}
	return okEnvelope(resp)
}

func userMe(req managementRequest) ([]byte, error) {
	key, ok := keyFromSession(req)
	if !ok {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": "not_authenticated"})
	}
	row := key.Safe()
	if store := loadedStore(); store != nil {
		row["usage"] = usageWindows(context.Background(), store, key.ID, time.Now())
	}
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "me": row})
}

func userUsage(req managementRequest) ([]byte, error) {
	key, ok := keyFromSession(req)
	if !ok {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": "not_authenticated"})
	}
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	rangeName := req.Query.Get("range")
	if rangeName == "" {
		rangeName = governor.Range24H
	}
	used, _ := store.UsageSum(context.Background(), key.ID, governor.WindowFor(rangeName, time.Now()))
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "range": rangeName, "summary": map[string]any{"total_cost": used}, "limits": key.Safe()["limits"]})
}

func userEvents(req managementRequest) ([]byte, error) {
	key, ok := keyFromSession(req)
	if !ok {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": "not_authenticated"})
	}
	return eventsResponseFromRequest(req, key.ID)
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
	return eventsResponseWithRange(keyID, req.Query.Get("range"), limit)
}

func eventsResponseWithRange(keyID string, rangeName string, limit int) ([]byte, error) {
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	var events []governor.UsageEvent
	var err error
	if strings.TrimSpace(rangeName) == "" {
		events, err = store.RecentEvents(context.Background(), keyID, limit)
	} else {
		events, err = store.RecentEventsWindow(context.Background(), keyID, governor.WindowFor(rangeName, time.Now()), limit)
	}
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
	}
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "events": events})
}

func usageWindows(ctx context.Context, store *governor.Store, keyID string, now time.Time) map[string]float64 {
	out := map[string]float64{}
	for _, name := range []string{governor.Range5H, governor.Range24H, governor.Range7D, governor.RangeMonth} {
		value, _ := store.UsageSum(ctx, keyID, governor.WindowFor(name, now))
		out[name] = value
	}
	return out
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
				Error:    governor.Brief(err.Error(), 400),
			})
			return
		}
		var chunk hostModelStreamReadResponse
		if err := json.Unmarshal(result, &chunk); err != nil {
			_, _ = callHost(methodHostStreamEmit, hostStreamEmitRequest{
				StreamID: targetStreamID,
				Error:    "decode host stream chunk: " + governor.Brief(err.Error(), 300),
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
				Error:    governor.Brief(chunk.Error, 400),
			})
			return
		}
		if chunk.Done {
			return
		}
	}
}

func keyFromSession(req managementRequest) (governor.KeyRecord, bool) {
	_ = refreshKeyPolicyState(false)
	cookie := req.Headers.Get("Cookie")
	token := ""
	for _, part := range strings.Split(cookie, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "cpa_governor_session=") {
			token = strings.TrimPrefix(part, "cpa_governor_session=")
			break
		}
	}
	cfg := loadedConfig()
	payload, ok := governor.VerifySession(token, cfg.SessionSecret, time.Now())
	if !ok {
		return governor.KeyRecord{}, false
	}
	store := loadedStore()
	if store == nil {
		return governor.KeyRecord{}, false
	}
	keys, err := store.ListKeys(context.Background())
	if err != nil {
		return governor.KeyRecord{}, false
	}
	for _, key := range keys {
		if key.ID == payload.KeyID && key.Enabled {
			return key, true
		}
	}
	return governor.KeyRecord{}, false
}

func codexcontStatus() map[string]any {
	cfg := loadedConfig()
	status := map[string]any{
		"enabled":   cfg.CodexContEnabled,
		"route":     cfg.CodexContRoute,
		"url":       cfg.CodexContURL,
		"fail_mode": cfg.FailMode,
		"mode":      "passive_until_executor_cutover",
	}
	if cfg.CodexContEnabled {
		health := probeCodexContHealth(cfg.CodexContURL)
		for key, value := range health {
			status[key] = value
		}
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
		out["health_error"] = governor.Brief(err.Error(), 200)
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
		Headers:    http.Header{"content-type": []string{"application/json; charset=utf-8"}},
		Body:       body,
	})
}

func managementHTML(html string) ([]byte, error) {
	return okEnvelope(managementResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"content-type": []string{"text/html; charset=utf-8"}},
		Body:       []byte(html),
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
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>CPA Governor</title><style>` + sharedCSS() + `</style></head><body><main class="shell"><header class="top"><div><h1>CPA Governor 管理</h1><p>统一管理 Key、限额、请求明细与 CodexCont 状态</p></div><div class="toolbar"><span id="live" class="pulse">实时轮询</span><button id="refresh">刷新</button></div></header><section class="grid" id="cards"></section><section class="panel"><div class="tabs"><button data-tab="keys" class="active">Key 管理</button><button data-tab="events">请求明细</button><button data-tab="codex">CodexCont</button></div><div id="content">加载中...</div></section></main><script>
const API=location.pathname.startsWith('/governor')?'/governor/api':'/v0/resource/plugins/cpa-governor/admin/api';
let tab='keys', snapshot={keys:[],codexcont:{}};
async function j(path, opts={}){const r=await fetch(API+path,{cache:'no-store',...opts});return r.json();}
function money(v){return v==null?'未设':('$'+Number(v).toFixed(3))}
function num(v){return Number(v||0).toLocaleString()}
function keyName(id){const k=(snapshot.keys||[]).find(x=>x.id===id);return k?(k.name+'<small>'+k.preview+'</small>'):(id||'未知 Key')}
function cardsRender(keys,data){cards.innerHTML='<article><b>'+keys.length+'</b><span>Key 数量</span></article><article><b>'+keys.filter(k=>k.enabled).length+'</b><span>启用</span></article><article><b>'+money(keys.reduce((s,k)=>s+Number((k.usage||{})["24h"]||0),0))+'</b><span>24H 估算</span></article><article><b>'+((data.codexcont||{}).enabled?'已开启':'未开启')+'</b><span>CodexCont 状态</span></article><article><b>'+((data.codexcont||{}).route?'已切流':'未切流')+'</b><span>插件执行器</span></article>'}
function renderKeys(){const keys=snapshot.keys||[]; content.innerHTML='<table><thead><tr><th>用户/Key</th><th>状态</th><th>RPM</th><th>并发</th><th>5H 用量</th><th>日用量</th><th>周用量</th><th>月用量</th><th>限额</th></tr></thead><tbody>'+keys.map(k=>'<tr><td><b>'+k.name+'</b><small>'+k.preview+'</small></td><td><span class="chip '+(k.enabled?'ok':'bad')+'">'+(k.enabled?'启用':'禁用')+'</span></td><td>'+ (k.rpm||'-') +'</td><td>'+ (k.concurrency||'-') +'</td><td>'+money((k.usage||{})["5h"])+'</td><td>'+money((k.usage||{})["24h"])+'</td><td>'+money((k.usage||{})["7d"])+'</td><td>'+money((k.usage||{})["month"])+'</td><td><small>5H '+money(k.limits.five_hour_usd)+' / 日 '+money(k.limits.daily_usd)+' / 周 '+money(k.limits.weekly_usd)+' / 月 '+money(k.limits.monthly_usd)+'</small></td></tr>').join('')+'</tbody></table>'}
async function renderEvents(){const ev=await j('/events?key_id=all'); const rows=ev.events||[]; content.innerHTML='<table><thead><tr><th>时间</th><th>用户/Key</th><th>模型</th><th>状态</th><th>延迟</th><th>Tokens</th><th>思考量</th><th>费用</th></tr></thead><tbody>'+rows.map(e=>'<tr><td>'+new Date(e.requested_at).toLocaleString()+'</td><td>'+keyName(e.key_id)+'</td><td>'+e.model+'</td><td><span class="chip '+(e.failed?'bad':'ok')+'">'+(e.failed?'失败':'成功')+'</span><small>'+((e.failure||'').slice(0,120))+'</small></td><td>'+num(e.latency_ms)+' ms</td><td>'+num((e.usage||{}).total_tokens)+'</td><td>'+num((e.usage||{}).reasoning_tokens)+'</td><td>'+money(e.cost)+'</td></tr>').join('')+'</tbody></table>'}
function renderCodex(){const c=snapshot.codexcont||{}; content.innerHTML='<div class="detail"><h2>CodexCont Engine</h2><p><span class="chip '+(c.enabled?'ok':'bad')+'">'+(c.enabled?'状态启用':'状态未启用')+'</span> <span class="chip '+(c.route?'warn':'')+'">'+(c.route?'插件执行器已切流':'生产仍走当前稳定链路')+'</span></p><dl><dt>Engine URL</dt><dd>'+c.url+'</dd><dt>失败策略</dt><dd>'+c.fail_mode+'</dd><dt>模式</dt><dd>'+c.mode+'</dd></dl></div>'}
async function load(){refresh.classList.add('spin'); const data=await j('/keys'); snapshot=data; const keys=data.keys||[]; cardsRender(keys,data); if(tab==='keys')renderKeys(); if(tab==='events')await renderEvents(); if(tab==='codex')renderCodex(); refresh.classList.remove('spin');}
document.querySelectorAll('.tabs button').forEach(b=>b.onclick=()=>{document.querySelectorAll('.tabs button').forEach(x=>x.classList.remove('active'));b.classList.add('active');tab=b.dataset.tab;load();});
refresh.onclick=load; load(); setInterval(load, 2000);
</script></body></html>`
}

func userHTML() string {
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>CPA 用量</title><style>` + sharedCSS() + `</style></head><body><main class="shell"><header class="top"><div><h1>CPA 用量自助页</h1><p>查看自己的额度、请求明细和思维链保护状态</p></div><div class="toolbar"><span class="pulse">实时轮询</span><button id="refresh" hidden>刷新</button></div></header><section class="panel" id="login"><input id="key" type="password" placeholder="粘贴你的 CPA Key"><button id="loginBtn">登录</button><p id="err"></p></section><section class="grid" id="cards" hidden></section><section class="panel" id="main" hidden><div class="tabs"><button data-tab="usage" class="active">额度与用量</button><button data-tab="codex">思维链保护</button><button data-tab="events">请求明细</button></div><div id="content"></div></section></main><script>
const BASE=location.pathname.startsWith('/governor-user')?'/governor-user':'/v0/resource/plugins/cpa-governor';
let tab='usage', me=null, events=[];
async function api(path,opts={}){const r=await fetch(BASE+path,{cache:'no-store',...opts});return r.json();}
loginBtn.onclick=async()=>{const r=await fetch(BASE+'/user/api/session',{headers:{Authorization:'Bearer '+key.value},cache:'no-store'});const d=await r.json(); if(!d.ok){err.textContent='登录失败：'+d.error;return} login.hidden=true; cards.hidden=false; main.hidden=false; refresh.hidden=false; load();}
function money(v){return v==null?'未设':('$'+Number(v).toFixed(3))}
function num(v){return Number(v||0).toLocaleString()}
function renderCards(k){cards.innerHTML='<article><b>'+k.name+'</b><span>'+k.preview+'</span></article><article><b>'+money(k.limits.five_hour_usd)+'</b><span>5H 限额</span></article><article><b>'+money(k.limits.daily_usd)+'</b><span>日限</span></article><article><b>'+money(k.limits.weekly_usd)+'</b><span>周限</span></article><article><b>'+money(k.limits.monthly_usd)+'</b><span>月限</span></article>'}
async function renderUsage(){const usage=await api('/user/api/usage?range=24h'); content.innerHTML='<h2>24H 当前用量：$'+Number((usage.summary||{}).total_cost||0).toFixed(4)+'</h2><div class="detail"><dl><dt>5H 限额</dt><dd>'+money(me.limits.five_hour_usd)+'</dd><dt>日限额</dt><dd>'+money(me.limits.daily_usd)+'</dd><dt>周限额</dt><dd>'+money(me.limits.weekly_usd)+'</dd><dt>月限额</dt><dd>'+money(me.limits.monthly_usd)+'</dd><dt>允许模型</dt><dd>'+(me.models||[]).join(', ')+'</dd></dl></div>'}
async function renderEvents(){const res=await api('/user/api/events?limit=50'); events=res.events||[]; content.innerHTML='<table><thead><tr><th>时间</th><th>模型</th><th>状态</th><th>延迟</th><th>Tokens</th><th>思考量</th><th>费用</th></tr></thead><tbody>'+events.map(e=>'<tr><td>'+new Date(e.requested_at).toLocaleString()+'</td><td>'+e.model+'</td><td><span class="chip '+(e.failed?'bad':'ok')+'">'+(e.failed?'失败':'成功')+'</span></td><td>'+num(e.latency_ms)+' ms</td><td>'+num((e.usage||{}).total_tokens)+'</td><td>'+num((e.usage||{}).reasoning_tokens)+'</td><td>'+money(e.cost)+'</td></tr>').join('')+'</tbody></table>'}
async function renderCodex(){const res=await api('/user/api/codexcont'); const c=res.codexcont||{}; content.innerHTML='<div class="detail"><h2>思维链保护状态</h2><p><span class="chip '+(c.enabled?'ok':'bad')+'">'+(c.enabled?'Engine 可用':'Engine 未启用')+'</span> <span class="chip '+(c.route?'warn':'')+'">'+(c.route?'插件内执行器已切流':'当前生产仍走稳定保护链')+'</span></p><dl><dt>你的 Key</dt><dd>'+me.name+' / '+me.preview+'</dd><dt>Engine URL</dt><dd>'+c.url+'</dd><dt>模式</dt><dd>'+c.mode+'</dd></dl></div>'}
async function load(){refresh.classList.add('spin'); const m=await api('/user/api/me'); if(!m.ok){refresh.classList.remove('spin');return;} me=m.me; renderCards(me); if(tab==='usage')await renderUsage(); if(tab==='events')await renderEvents(); if(tab==='codex')await renderCodex(); refresh.classList.remove('spin');}
document.querySelectorAll('.tabs button').forEach(b=>b.onclick=()=>{document.querySelectorAll('.tabs button').forEach(x=>x.classList.remove('active'));b.classList.add('active');tab=b.dataset.tab;load();});
refresh.onclick=load; setInterval(()=>{if(!main.hidden)load()},2000);
</script></body></html>`
}

func sharedCSS() string {
	return `:root{color-scheme:dark;--bg:#0b1020;--panel:#111827;--panel2:#0f172a;--line:#243047;--text:#e5e7eb;--muted:#94a3b8;--brand:#38bdf8;--ok:#22c55e;--bad:#fb7185;--warn:#fbbf24}*{box-sizing:border-box}body{margin:0;background:linear-gradient(180deg,#0b1020,#111827);color:var(--text);font:14px/1.5 ui-sans-serif,system-ui,Segoe UI,Arial}.shell{max-width:1180px;margin:0 auto;padding:24px}.top{display:flex;align-items:center;justify-content:space-between;gap:16px;margin-bottom:18px}.top h1{margin:0;font-size:24px}.top p{margin:4px 0 0;color:var(--muted)}.toolbar{display:flex;align-items:center;gap:10px;flex-wrap:wrap}button{border:1px solid var(--line);background:#172033;color:var(--text);border-radius:8px;padding:9px 13px;cursor:pointer}button:hover{border-color:var(--brand)}.spin{animation:spin .8s linear infinite}.pulse{color:var(--ok);animation:pulse 1.4s ease-in-out infinite}.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:12px;margin-bottom:14px}.grid article,.panel{background:rgba(17,24,39,.92);border:1px solid var(--line);border-radius:10px;box-shadow:0 12px 32px rgba(0,0,0,.25)}.grid article{padding:14px}.grid b{display:block;font-size:22px}.grid span,small{display:block;color:var(--muted);overflow-wrap:anywhere}.panel{padding:16px}.tabs{display:flex;gap:8px;margin-bottom:14px}.tabs .active{background:#0e7490;border-color:#38bdf8}table{width:100%;border-collapse:collapse;table-layout:fixed}th,td{border-bottom:1px solid var(--line);padding:10px;text-align:left;vertical-align:top;white-space:normal;overflow-wrap:break-word}th{color:#cbd5e1;font-size:12px;text-transform:uppercase}.chip{display:inline-block;border-radius:999px;padding:2px 8px;background:#334155}.chip.ok{color:#86efac}.chip.bad{color:#fecdd3}.chip.warn{color:#fde68a}.detail{background:#0f172a;border:1px solid var(--line);border-radius:8px;padding:14px}.detail h2{font-size:18px;margin:0 0 10px}.detail dl{display:grid;grid-template-columns:140px 1fr;gap:8px 12px}.detail dt{color:#94a3b8}.detail dd{margin:0;overflow-wrap:anywhere}input{width:min(520px,100%);padding:11px;border-radius:8px;border:1px solid var(--line);background:#0b1220;color:var(--text);margin-right:8px}@keyframes spin{to{transform:rotate(360deg)}}@keyframes pulse{0%,100%{opacity:.55}50%{opacity:1}}@media(max-width:620px){.shell{padding:14px}.top{align-items:flex-start;flex-direction:column}.panel{overflow-x:auto}table{min-width:860px;font-size:12px}th,td{padding:8px;white-space:normal;overflow-wrap:break-word}.tabs{min-width:max-content;overflow:auto}.detail dl{grid-template-columns:1fr}}`
}
