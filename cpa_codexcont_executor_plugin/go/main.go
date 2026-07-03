package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"codexcont/cpa-codexcont-executor-plugin/internal/executor"
	"gopkg.in/yaml.v3"
)

const pluginID = "cpa-codexcont-executor"

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
	mu    sync.RWMutex
	cfg   executor.Config
	store *executor.Store
}

var state = runtimeState{cfg: executor.DefaultConfig()}

var hostCall = func(method string, payload any) (json.RawMessage, error) {
	return nil, fmt.Errorf("host callback unavailable for %s", method)
}

func main() {
	if os.Getenv("CPA_CODEXCONT_EXECUTOR_NOOP") != "" {
		return
	}
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case methodPluginRegister:
		return pluginRegister(request)
	case methodPluginReconfigure:
		return pluginReconfigure(request)
	case methodModelRoute:
		return routeModel(request)
	case methodExecutorIdentifier:
		return okEnvelope(map[string]any{"id": pluginID, "type": "codexcont_executor"})
	case methodExecutorExecute:
		return executorExecute(request)
	case methodExecutorExecuteStream:
		return executorExecuteStream(request)
	case methodExecutorCountTokens:
		return okEnvelope(executorResponse{Payload: []byte(`{"input_tokens":0}`)})
	case methodManagementRegister:
		return managementRegister()
	case methodManagementHandle:
		return managementHandle(request)
	default:
		return errorEnvelope("unknown_method", method), nil
	}
}

func pluginRegister(raw []byte) ([]byte, error) {
	if len(raw) > 0 {
		if err := applyLifecycleConfig(raw); err != nil {
			return errorEnvelope("config_error", err.Error()), nil
		}
	}
	return okEnvelope(registration{
		SchemaVersion: schemaVersion,
		Metadata: metadata{
			Name:             pluginID,
			Version:          "0.1.0",
			Author:           "konbakuyomu/CodexCont",
			GitHubRepository: "https://local/CodexCont",
			ConfigFields: []configField{
				{Name: "enabled", Type: configBoolean, Description: "Enable the plugin."},
				{Name: "route_enabled", Type: configBoolean, Description: "Route streaming Responses requests to the CodexCont executor."},
				{Name: "state_db_path", Type: configString, Description: "SQLite path for safe executor summaries."},
				{Name: "fail_mode", Type: configEnum, EnumValues: []string{"fallback", "fail_closed"}, Description: "Fallback behavior when the executor is disabled."},
				{Name: "truncation_step", Type: configInteger, Description: "Reasoning truncation step. Default 518."},
				{Name: "max_continue", Type: configInteger, Description: "Maximum hidden continuation rounds."},
				{Name: "marker_text", Type: configString, Description: "Hidden commentary continuation marker."},
			},
		},
		Capabilities: capabilities{
			ModelRouter:           true,
			Executor:              true,
			ExecutorModelScope:    "responses",
			ExecutorInputFormats:  []string{"openai", "responses"},
			ExecutorOutputFormats: []string{"openai", "responses"},
			ManagementAPI:         true,
		},
	})
}

func pluginReconfigure(raw []byte) ([]byte, error) {
	if err := applyLifecycleConfig(raw); err != nil {
		return errorEnvelope("config_error", err.Error()), nil
	}
	return okEnvelope(map[string]any{"configured": true})
}

func applyLifecycleConfig(raw []byte) error {
	var req lifecycleRequest
	if err := json.Unmarshal(raw, &req); err != nil && len(raw) > 0 {
		return err
	}
	cfg := executor.DefaultConfig()
	if len(req.ConfigYAML) > 0 {
		if err := yaml.Unmarshal(req.ConfigYAML, &cfg); err != nil {
			return err
		}
	}
	cfg = cfg.Normalize()
	store, err := executor.OpenStore(cfg.StateDBPath)
	if err != nil {
		return err
	}
	state.mu.Lock()
	old := state.store
	state.cfg = cfg
	state.store = store
	state.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return nil
}

func loadedConfig() executor.Config {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.cfg.Normalize()
}

func loadedStore() *executor.Store {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.store
}

func shutdownPlugin() {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.store != nil {
		_ = state.store.Close()
		state.store = nil
	}
}

func routeModel(raw []byte) ([]byte, error) {
	var req modelRouteRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	cfg := loadedConfig()
	if !cfg.Enabled || !cfg.RouteEnabled || !req.Stream || !isResponsesRequest(req.SourceFormat, req.Body) {
		return okEnvelope(modelRouteResponse{Handled: false})
	}
	return okEnvelope(modelRouteResponse{
		Handled:    true,
		TargetKind: routeTargetSelf,
		Reason:     "cpa_codexcont_executor_enabled",
	})
}

func isResponsesRequest(source string, body []byte) bool {
	source = strings.ToLower(strings.TrimSpace(source))
	if strings.Contains(source, "response") || source == "openai" {
		return true
	}
	return strings.Contains(string(body), `"stream"`) && strings.Contains(string(body), `"model"`)
}

func executorUnavailable() ([]byte, error) {
	cfg := loadedConfig()
	if cfg.RouteEnabled && cfg.FailMode == "fail_closed" {
		return errorEnvelope("codexcont_executor_disabled", "CodexCont executor route is disabled"), nil
	}
	return errorEnvelope("codexcont_executor_disabled", "CodexCont executor is not handling this request"), nil
}

func executorExecute(raw []byte) ([]byte, error) {
	var req executorCallRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	cfg := loadedConfig()
	if !cfg.Enabled || !cfg.RouteEnabled {
		return executorUnavailable()
	}
	payload := firstBytes(req.ExecutorRequest.Payload, req.ExecutorRequest.OriginalRequest)
	result, err := hostCall(methodHostModelExecute, hostModelExecutionRequest{
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
	if !cfg.Enabled || !cfg.RouteEnabled {
		return executorUnavailable()
	}
	if strings.TrimSpace(req.StreamID) == "" {
		return errorEnvelope("stream_id_required", "stream_id is required for executor.execute_stream"), nil
	}
	payload := firstBytes(req.ExecutorRequest.Payload, req.ExecutorRequest.OriginalRequest)
	var base map[string]any
	if err := json.Unmarshal(payload, &base); err != nil {
		return errorEnvelope("invalid_responses_payload", err.Error()), nil
	}
	go foldHostStream(req, cfg, base)
	return okEnvelope(executorStreamResponse{Headers: http.Header{"Content-Type": []string{"text/event-stream"}}})
}

func foldHostStream(req executorCallRequest, cfg executor.Config, base map[string]any) {
	ctx := context.Background()
	targetStreamID := req.StreamID
	var streamErr string
	defer func() {
		_, _ = hostCall(methodHostStreamClose, hostStreamCloseRequest{StreamID: targetStreamID, Error: streamErr})
	}()
	opener := func(ctx context.Context, body []byte, _ int) (executor.StreamReader, error) {
		return openHostModelStream(ctx, req, body)
	}
	emitter := func(ctx context.Context, payload []byte) error {
		_, err := hostCall(methodHostStreamEmit, hostStreamEmitRequest{StreamID: targetStreamID, Payload: payload})
		return err
	}
	result, err := executor.FoldStream(ctx, cfg, base, opener, emitter)
	if err != nil {
		streamErr = brief(err.Error(), 400)
		_, _ = hostCall(methodHostStreamEmit, hostStreamEmitRequest{StreamID: targetStreamID, Error: streamErr})
		return
	}
	if result != nil {
		saveFoldSummary(req.ExecutorRequest, result)
	}
}

type hostStreamReader struct {
	streamID string
}

func openHostModelStream(_ context.Context, req executorCallRequest, body []byte) (executor.StreamReader, error) {
	result, err := hostCall(methodHostModelExecuteStream, hostModelExecutionRequest{
		EntryProtocol:  firstNonEmpty(req.ExecutorRequest.SourceFormat, "openai"),
		ExitProtocol:   firstNonEmpty(req.ExecutorRequest.Format, "openai"),
		Model:          firstNonEmpty(req.ExecutorRequest.Model, modelFromBody(body)),
		Stream:         true,
		Body:           body,
		Headers:        cloneHeader(req.ExecutorRequest.Headers),
		Query:          cloneValues(req.ExecutorRequest.Query),
		Alt:            req.ExecutorRequest.Alt,
		HostCallbackID: req.HostCallbackID,
	})
	if err != nil {
		return nil, err
	}
	var resp hostModelStreamResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("upstream returned %d", resp.StatusCode)
	}
	if resp.StreamID == "" {
		return nil, fmt.Errorf("host returned empty stream id")
	}
	return &hostStreamReader{streamID: resp.StreamID}, nil
}

func (r *hostStreamReader) Read(context.Context) ([]byte, bool, error) {
	result, err := hostCall(methodHostModelStreamRead, hostModelStreamReadRequest{StreamID: r.streamID})
	if err != nil {
		return nil, false, err
	}
	var chunk hostModelStreamReadResponse
	if err := json.Unmarshal(result, &chunk); err != nil {
		return nil, false, err
	}
	if chunk.Error != "" {
		return chunk.Payload, chunk.Done, fmt.Errorf("%s", chunk.Error)
	}
	return chunk.Payload, chunk.Done, nil
}

func (r *hostStreamReader) Close() error {
	_, err := hostCall(methodHostModelStreamClose, hostModelStreamCloseRequest{StreamID: r.streamID})
	return err
}

func saveFoldSummary(req executorRequest, result *executor.FoldResult) {
	store := loadedStore()
	if store == nil || result == nil {
		return
	}
	summary := cloneSummary(result.Summary)
	keyID := strings.TrimSpace(req.AuthID)
	if keyID != "" {
		summary["key_identity"] = map[string]any{"known": true, "id": keyID}
	}
	model := firstNonEmpty(req.Model, fmt.Sprint(summary["model"]))
	requestID := firstNonEmpty(result.RequestID, fmt.Sprint(summary["request_id"]))
	_ = store.SaveCodexSummary(context.Background(), requestID, keyID, model, result.Protection, summary)
}

func managementRegister() ([]byte, error) {
	return okEnvelope(managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: "/plugins/cpa-codexcont-executor/status", Menu: "CodexCont Executor", Description: "CodexCont executor status."},
			{Method: http.MethodGet, Path: "/plugins/cpa-codexcont-executor/summaries", Description: "Safe CodexCont executor summaries."},
		},
		Resources: []resourceRoute{},
	})
}

func managementHandle(raw []byte) ([]byte, error) {
	var req managementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	path := strings.TrimSpace(req.Path)
	switch {
	case strings.HasSuffix(path, "/status"):
		return jsonResponse(http.StatusOK, map[string]any{"ok": true, "executor": statusPayload()})
	case strings.HasSuffix(path, "/summaries"):
		return summariesResponse(req)
	default:
		return jsonResponse(http.StatusNotFound, map[string]any{"ok": false, "error": "not_found"})
	}
}

func summariesResponse(req managementRequest) ([]byte, error) {
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	limit := 100
	items, err := store.RecentCodexSummaries(context.Background(), req.Query.Get("key_id"), limit)
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, item.Summary)
	}
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "summaries": out})
}

func statusPayload() map[string]any {
	cfg := loadedConfig()
	return map[string]any{
		"plugin_id":       pluginID,
		"enabled":         cfg.Enabled,
		"route_enabled":   cfg.RouteEnabled,
		"state_db_path":   cfg.StateDBPath,
		"truncation_step": cfg.TruncationStep,
		"max_continue":    cfg.MaxContinue,
		"mode":            "executor_only",
	}
}

func okEnvelope(result any) ([]byte, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func jsonResponse(status int, body any) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return okEnvelope(managementResponse{
		StatusCode: status,
		Headers: http.Header{
			"Content-Type":  []string{"application/json; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
		},
		Body: raw,
	})
}

func firstBytes(values ...[]byte) []byte {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func cloneHeader(in http.Header) http.Header {
	out := http.Header{}
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func cloneValues(in map[string][]string) map[string][]string {
	out := map[string][]string{}
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func cloneSummary(in map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range in {
		out[key] = value
	}
	return out
}

func modelFromBody(body []byte) string {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return ""
	}
	return fmt.Sprint(raw["model"])
}

func brief(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit]
}
