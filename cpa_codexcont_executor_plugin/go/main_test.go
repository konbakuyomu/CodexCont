package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"codexcont/cpa-codexcont-executor-plugin/internal/executor"
	_ "modernc.org/sqlite"
)

func unwrapEnvelope(t *testing.T, raw []byte, out any) {
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

func configureTestState(t *testing.T, routeEnabled bool) {
	t.Helper()
	configureTestStateWithConfig(t, func(cfg *executor.Config) {
		cfg.RouteEnabled = routeEnabled
	})
}

func configureTestStateWithConfig(t *testing.T, mutate func(*executor.Config)) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "executor.sqlite")
	store, err := executor.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := executor.DefaultConfig()
	cfg.StateDBPath = path
	if mutate != nil {
		mutate(&cfg)
	}
	cfg = cfg.Normalize()
	state.mu.Lock()
	if state.store != nil {
		_ = state.store.Close()
	}
	state.cfg = cfg
	state.store = store
	monitor = newSummaryMonitor(160)
	state.mu.Unlock()
	t.Cleanup(func() {
		state.mu.Lock()
		if state.store != nil {
			_ = state.store.Close()
			state.store = nil
		}
		state.mu.Unlock()
	})
}

func TestRegistrationIsExecutorOnly(t *testing.T) {
	raw, err := handleMethod(methodPluginRegister, nil)
	if err != nil {
		t.Fatal(err)
	}
	var reg registration
	unwrapEnvelope(t, raw, &reg)
	if reg.Metadata.Name != pluginID {
		t.Fatalf("name = %q", reg.Metadata.Name)
	}
	if !reg.Capabilities.FrontendAuthProvider || reg.Capabilities.FrontendAuthProviderExclusive {
		t.Fatalf("executor plugin should expose non-exclusive frontend auth shim: %#v", reg.Capabilities)
	}
	if !reg.Capabilities.ModelRouter || !reg.Capabilities.Executor || !reg.Capabilities.ManagementAPI || !reg.Capabilities.UsagePlugin {
		t.Fatalf("executor capabilities missing: %#v", reg.Capabilities)
	}
	if reg.Capabilities.ExecutorModelScope != executorModelScopeBoth {
		t.Fatalf("executor model scope = %q, want official CPA scope %q", reg.Capabilities.ExecutorModelScope, executorModelScopeBoth)
	}
	if strings.Join(reg.Capabilities.ExecutorInputFormats, ",") != executorFormatOpenAIResponse ||
		strings.Join(reg.Capabilities.ExecutorOutputFormats, ",") != executorFormatOpenAIResponse {
		t.Fatalf("executor must declare CPA's native Responses format to avoid request translation drift: %#v", reg.Capabilities)
	}
}

func TestExecutorIdentifierUsesOfficialField(t *testing.T) {
	raw, err := handleMethod(methodExecutorIdentifier, nil)
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Identifier string `json:"identifier"`
		ID         string `json:"id"`
	}
	unwrapEnvelope(t, raw, &resp)
	if resp.Identifier != pluginID {
		t.Fatalf("identifier = %q, want %q", resp.Identifier, pluginID)
	}
	if resp.ID != "" {
		t.Fatalf("executor.identifier must not rely on non-CPA id field: %#v", resp)
	}
}

func TestReconfigureReturnsFullRegistration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "executor.sqlite")
	t.Cleanup(shutdownPlugin)
	raw, err := handleMethod(methodPluginReconfigure, mustJSON(t, lifecycleRequest{ConfigYAML: []byte("enabled: true\nroute_enabled: false\nstate_db_path: " + path + "\nupstream_model_aliases:\n  gpt-5.4: gpt-5.3-codex-spark\n")}))
	if err != nil {
		t.Fatal(err)
	}
	var reg registration
	unwrapEnvelope(t, raw, &reg)
	if reg.Metadata.Name != pluginID || !reg.Capabilities.ManagementAPI {
		t.Fatalf("reconfigure must return full registration for CPA active snapshot: %#v", reg)
	}
	cfg := loadedConfig()
	if cfg.UpstreamModelAliases["gpt-5.4"] != "gpt-5.3-codex-spark" {
		t.Fatalf("upstream aliases not loaded: %#v", cfg.UpstreamModelAliases)
	}
	if _, ok := cfg.UpstreamModelAliases["gpt-5.5"]; ok {
		t.Fatalf("gpt-5.5 must not get an implicit Spark alias: %#v", cfg.UpstreamModelAliases)
	}
}

func TestManagementRegisterExposesOnlyAdminMonitorResource(t *testing.T) {
	raw, err := managementRegister()
	if err != nil {
		t.Fatal(err)
	}
	var reg managementRegistrationResponse
	unwrapEnvelope(t, raw, &reg)
	foundAdmin := false
	menuCount := 0
	for _, resource := range reg.Resources {
		if resource.Path == "/admin" && resource.Menu == "CodexCont Executor" {
			foundAdmin = true
		}
		if resource.Menu == "CodexCont Executor" {
			menuCount++
		}
		if strings.HasPrefix(resource.Path, "/user") || strings.Contains(resource.Path, "/keys") || strings.Contains(resource.Path, "/quota") {
			t.Fatalf("executor resource overlaps user/key control plane: %#v", resource)
		}
	}
	if !foundAdmin {
		t.Fatalf("executor monitor admin resource missing: %#v", reg.Resources)
	}
	for _, route := range reg.Routes {
		if strings.TrimSpace(route.Menu) != "" {
			t.Fatalf("management route should stay hidden from CPAMP sidebar: %#v", route)
		}
		if strings.Contains(route.Path, "/user") || strings.Contains(route.Path, "/keys") || strings.Contains(route.Path, "/quota") {
			t.Fatalf("executor route overlaps user/key control plane: %#v", route)
		}
	}
	if menuCount != 1 {
		t.Fatalf("executor should expose exactly one CPAMP menu entry, got %d in %#v", menuCount, reg)
	}
}

func TestRouteSwitch(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","stream":true}`)
	configureTestState(t, false)
	raw, err := routeModel(mustJSON(t, modelRouteRequest{SourceFormat: "openai", Stream: true, Body: body}))
	if err != nil {
		t.Fatal(err)
	}
	var resp modelRouteResponse
	unwrapEnvelope(t, raw, &resp)
	if resp.Handled {
		t.Fatalf("disabled route handled request: %#v", resp)
	}
	configureTestState(t, true)
	raw, err = routeModel(mustJSON(t, modelRouteRequest{SourceFormat: "openai-response", Stream: true, Body: body}))
	if err != nil {
		t.Fatal(err)
	}
	unwrapEnvelope(t, raw, &resp)
	if !resp.Handled || resp.TargetKind != routeTargetSelf {
		t.Fatalf("enabled route did not handle streaming response: %#v", resp)
	}
	raw, err = routeModel(mustJSON(t, modelRouteRequest{SourceFormat: "openai", Stream: true, Body: []byte(`{"model":"gpt-5.5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)}))
	if err != nil {
		t.Fatal(err)
	}
	unwrapEnvelope(t, raw, &resp)
	if resp.Handled {
		t.Fatalf("chat-completions request should not be handled: %#v", resp)
	}
	raw, err = routeModel(mustJSON(t, modelRouteRequest{SourceFormat: "openai", Stream: false, Body: []byte(`{"model":"gpt-5.5"}`)}))
	if err != nil {
		t.Fatal(err)
	}
	unwrapEnvelope(t, raw, &resp)
	if resp.Handled {
		t.Fatalf("non-stream request should not be handled: %#v", resp)
	}
}

func TestManagementStatusNoStore(t *testing.T) {
	configureTestState(t, true)
	raw, err := managementHandle(mustJSON(t, managementRequest{Method: http.MethodGet, Path: "/plugins/cpa-codexcont-executor/status"}))
	if err != nil {
		t.Fatal(err)
	}
	var resp managementResponse
	unwrapEnvelope(t, raw, &resp)
	if resp.StatusCode != http.StatusOK || resp.Headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("management response = %#v", resp)
	}
}

func TestManagementAdminMonitorHTML(t *testing.T) {
	configureTestState(t, false)
	raw, err := managementHandle(mustJSON(t, managementRequest{Method: http.MethodGet, Path: "/v0/resource/plugins/cpa-codexcont-executor/admin"}))
	if err != nil {
		t.Fatal(err)
	}
	var resp managementResponse
	unwrapEnvelope(t, raw, &resp)
	html := string(resp.Body)
	if resp.StatusCode != http.StatusOK || resp.Headers.Get("Cache-Control") != "no-store" || !strings.Contains(resp.Headers.Get("Content-Type"), "text/html") {
		t.Fatalf("admin monitor response = %#v", resp)
	}
	for _, want := range []string{"实时滚动监控", "/v0/resource/plugins/cpa-codexcont-executor/admin/api", "summaries?limit=120"} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin monitor html missing %q", want)
		}
	}
	for _, want := range []string{"keyDisplay", "shortID", "调用者 / 请求", "未知 Key"} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin monitor html missing readable request marker %q", want)
		}
	}
	for _, forbidden := range []string{"/user/api", "keys/create", "quota"} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("admin monitor html should not expose %q", forbidden)
		}
	}
}

func TestUsageHandleIsObservabilityOnlyNoop(t *testing.T) {
	raw, err := handleMethod(methodUsageHandle, []byte(`{"api_key":"key-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	unwrapEnvelope(t, raw, &body)
	if body["handled"] != false || body["observability_only"] != true {
		t.Fatalf("usage handle should be no-op observability shim: %#v", body)
	}
}

func TestFrontendAuthIsObservabilityOnlyNoop(t *testing.T) {
	raw, err := handleMethod(methodFrontendAuthAuthenticate, []byte(`{"Path":"/v1/responses"}`))
	if err != nil {
		t.Fatal(err)
	}
	var resp frontendAuthResponse
	unwrapEnvelope(t, raw, &resp)
	if resp.Authenticated || resp.Metadata["mode"] != "observability_only" {
		t.Fatalf("frontend auth should not authenticate requests: %#v", resp)
	}
}

func TestSummariesIncludeProcessingMonitor(t *testing.T) {
	configureTestState(t, true)
	id := monitor.Start(executorRequest{
		AuthID: "key-1",
		Model:  "gpt-5.5",
		AuthMetadata: map[string]any{
			"key_name":  "kuma的官key",
			"key_alias": "kuma专用",
			"preview":   "abcd1234...ef5678",
			"source":    "native_cpa",
		},
	}, map[string]any{"model": "gpt-5.5"})
	if id == "" {
		t.Fatal("processing monitor id is empty")
	}
	raw, err := managementHandle(mustJSON(t, managementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/cpa-codexcont-executor/admin/api/summaries",
		Query:  map[string][]string{"limit": {"10"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var resp managementResponse
	unwrapEnvelope(t, raw, &resp)
	var body struct {
		OK        bool             `json:"ok"`
		Summaries []map[string]any `json:"summaries"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Summaries) != 1 || body.Summaries[0]["protection"] != "processing" {
		t.Fatalf("processing summary missing: %#v", body.Summaries)
	}
	if body.Summaries[0]["request_id"] != id {
		t.Fatalf("request id = %#v, want %s", body.Summaries[0]["request_id"], id)
	}
	identity, _ := body.Summaries[0]["key_identity"].(map[string]any)
	if identity["id"] != "key-1" || identity["name"] != "kuma的官key" || identity["alias"] != "kuma专用" || identity["preview"] != "abcd1234...ef5678" {
		t.Fatalf("processing summary should include safe key identity: %#v", identity)
	}
}

func TestKeyIdentityFromRequestUsesSafeMetadata(t *testing.T) {
	req := executorRequest{
		AuthID: "codex-upstream-auth",
		AuthMetadata: map[string]any{
			"key_id":    "native_abc_123",
			"key_name":  "QQ的官key",
			"key_alias": "QQ专用",
			"preview":   "abc123...def456",
			"source":    "native_cpa",
			"ignored":   "sk-should-not-leak",
		},
		AuthAttributes: map[string]string{
			"preview": "later-preview",
		},
		Metadata: map[string]any{
			"key_alias": "later-alias",
		},
	}
	identity := keyIdentityFromRequest(req)
	if identity["id"] != "native_abc_123" || identity["name"] != "QQ的官key" || identity["alias"] != "QQ专用" || identity["preview"] != "abc123...def456" || identity["source"] != "native_cpa" {
		t.Fatalf("unexpected identity: %#v", identity)
	}
	raw, _ := json.Marshal(identity)
	if strings.Contains(string(raw), "sk-") || strings.Contains(string(raw), "sha256:") {
		t.Fatalf("identity leaked unsafe value: %s", raw)
	}
}

func TestKeyIdentityFromAuthorizationUsesNativePreviewAndAlias(t *testing.T) {
	rawKey := "sk-test-native-identity"
	aliasPath := filepath.Join(t.TempDir(), "cpamp.sqlite")
	db, err := sql.Open("sqlite", aliasPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table api_key_aliases(api_key_hash text primary key, alias text)`); err != nil {
		t.Fatal(err)
	}
	hash := sha256Hex(rawKey)
	if _, err := db.Exec(`insert into api_key_aliases(api_key_hash, alias) values(?, ?)`, "SHA256:"+hash, "kuma的官key"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	configureTestStateWithConfig(t, func(cfg *executor.Config) {
		cfg.RouteEnabled = true
		cfg.CPAMPAliasDBPath = aliasPath
	})
	identity := keyIdentityFromRequest(executorRequest{
		AuthID:  "codex-upstream-auth",
		Headers: http.Header{"Authorization": []string{"Bearer " + rawKey}},
	})
	if identity["id"] != "native_"+sanitizeIDPart(hashPreview(hash)) || identity["name"] != "kuma的官key" || identity["alias"] != "kuma的官key" || identity["preview"] != hashPreview(hash) || identity["source"] != "native_cpa" {
		t.Fatalf("unexpected authorization identity: %#v", identity)
	}
	encoded, _ := json.Marshal(identity)
	if strings.Contains(string(encoded), rawKey) || strings.Contains(string(encoded), hash) {
		t.Fatalf("authorization identity leaked raw key/hash: %s", encoded)
	}
}

func TestSaveFoldSummaryPersistsSafeKeyIdentity(t *testing.T) {
	configureTestState(t, true)
	result := &executor.FoldResult{
		RequestID:  "resp-identity",
		Protection: "protected_clean",
		Summary: map[string]any{
			"request_id": "resp-identity",
			"protection": "protected_clean",
		},
	}
	summary := saveFoldSummary(executorRequest{
		AuthID: "native_key",
		Model:  "gpt-5.5",
		AuthMetadata: map[string]any{
			"key_name":  "阿伟的官key",
			"key_alias": "阿伟专用",
			"preview":   "12345678...abcdef",
			"source":    "native_cpa",
		},
	}, result, nil)
	identity, _ := summary["key_identity"].(map[string]any)
	if identity["id"] != "native_key" || identity["name"] != "阿伟的官key" {
		t.Fatalf("summary identity = %#v", identity)
	}
	items, err := state.store.RecentCodexSummaries(context.Background(), "native_key", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].KeyID != "native_key" {
		t.Fatalf("persisted summaries = %#v", items)
	}
}

func TestExecuteStreamAcceptsOfficialFlattenedRPCPayload(t *testing.T) {
	configureTestState(t, true)
	originalHostCall := hostCall
	t.Cleanup(func() { hostCall = originalHostCall })

	requestBody := []byte(`{"model":"gpt-5.5","stream":true,"input":[{"role":"user","content":"hi"}],"tools":[{"type":"image_generation"},{"type":"function","name":"lookup"}],"tool_choice":"image_generation"}`)
	upstreamChunks := [][]byte{
		executor.SerializeEvent(map[string]any{"type": "response.created", "response": map[string]any{"id": "resp-flat", "status": "in_progress"}}),
		executor.SerializeEvent(map[string]any{"type": "response.completed", "response": map[string]any{
			"id":     "resp-flat",
			"status": "completed",
			"usage": map[string]any{
				"input_tokens":  10,
				"output_tokens": 20,
				"total_tokens":  30,
				"output_tokens_details": map[string]any{
					"reasoning_tokens": 12,
				},
			},
		}}),
	}
	var mu sync.Mutex
	var openedBody []byte
	var openedModel string
	var openedProtocol string
	var openedHostCallbackID string
	var emitted strings.Builder
	readIndex := 0
	done := make(chan struct{})
	closeOnce := sync.Once{}
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		mu.Lock()
		defer mu.Unlock()
		switch method {
		case methodHostModelExecuteStream:
			req := payload.(hostModelExecutionRequest)
			openedModel = req.Model
			openedBody = append([]byte(nil), req.Body...)
			openedProtocol = req.EntryProtocol + "->" + req.ExitProtocol
			openedHostCallbackID = req.HostCallbackID
			return rawJSON(t, hostModelStreamResponse{
				StatusCode: http.StatusOK,
				Headers:    http.Header{"Content-Type": []string{"text/event-stream"}},
				StreamID:   "upstream-flat",
			}), nil
		case methodHostModelStreamRead:
			if readIndex >= len(upstreamChunks) {
				return rawJSON(t, hostModelStreamReadResponse{Done: true}), nil
			}
			chunk := upstreamChunks[readIndex]
			readIndex++
			return rawJSON(t, hostModelStreamReadResponse{Payload: chunk, Done: readIndex >= len(upstreamChunks)}), nil
		case methodHostModelStreamClose:
			return rawJSON(t, map[string]any{}), nil
		case methodHostStreamEmit:
			req := payload.(hostStreamEmitRequest)
			emitted.Write(req.Payload)
			return rawJSON(t, map[string]any{}), nil
		case methodHostStreamClose:
			closeOnce.Do(func() { close(done) })
			return rawJSON(t, map[string]any{}), nil
		default:
			t.Fatalf("unexpected host call %s", method)
			return nil, nil
		}
	}

	raw, err := executorExecuteStream(mustJSON(t, map[string]any{
		"AuthID":           "key-1",
		"Model":            "gpt-5.5",
		"Format":           "openai-response",
		"Stream":           true,
		"SourceFormat":     "openai-response",
		"Payload":          requestBody,
		"OriginalRequest":  requestBody,
		"stream_id":        "client-flat",
		"host_callback_id": "callback-flat",
	}))
	if err != nil {
		t.Fatal(err)
	}
	var resp executorStreamResponse
	unwrapEnvelope(t, raw, &resp)
	if resp.Headers.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream headers = %#v", resp.Headers)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("executor stream did not finish")
	}
	mu.Lock()
	defer mu.Unlock()
	var opened map[string]any
	if err := json.Unmarshal(openedBody, &opened); err != nil {
		t.Fatalf("opened body is not JSON: %v body=%q", err, openedBody)
	}
	if openedModel != "gpt-5.5" || opened["model"] != "gpt-5.5" || len(openedBody) == 0 || strings.Contains(string(openedBody), "reasoning.encrypted_content") {
		t.Fatalf("opened upstream request should pass through client model by default: request=%q body=%s", openedModel, string(openedBody))
	}
	if !strings.Contains(string(openedBody), "image_generation") || !strings.Contains(string(openedBody), `"type":"function"`) {
		t.Fatalf("default pass-through should not filter tools: %s", openedBody)
	}
	if openedProtocol != "openai-response->openai-response" {
		t.Fatalf("opened protocol = %q, want openai-response->openai-response", openedProtocol)
	}
	if openedHostCallbackID != "callback-flat" {
		t.Fatalf("host model stream callback id = %q, want callback-flat", openedHostCallbackID)
	}
	if !strings.Contains(emitted.String(), "response.completed") {
		t.Fatalf("emitted stream missing terminal event:\n%s", emitted.String())
	}
	summaryRaw, err := managementHandle(mustJSON(t, managementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/cpa-codexcont-executor/admin/api/summaries",
		Query:  map[string][]string{"limit": {"5"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var summaryResp managementResponse
	unwrapEnvelope(t, summaryRaw, &summaryResp)
	var summaryBody struct {
		Summaries []map[string]any `json:"summaries"`
	}
	if err := json.Unmarshal(summaryResp.Body, &summaryBody); err != nil {
		t.Fatal(err)
	}
	if len(summaryBody.Summaries) == 0 {
		t.Fatal("expected saved executor summary")
	}
	gotSummary := summaryBody.Summaries[0]
	if gotSummary["model"] != "gpt-5.5" {
		t.Fatalf("summary model = %#v, want gpt-5.5", gotSummary["model"])
	}
	diagnostics, _ := gotSummary["diagnostics"].(map[string]any)
	diagRounds, _ := diagnostics["rounds"].([]any)
	if len(diagRounds) == 0 {
		t.Fatalf("summary diagnostics missing: %#v", gotSummary)
	}
	firstDiag, _ := diagRounds[0].(map[string]any)
	if firstDiag["model"] != "gpt-5.5" || firstDiag["requested_model"] != "gpt-5.5" || firstDiag["body_model"] != "gpt-5.5" {
		t.Fatalf("diagnostics should record default model pass-through: %#v", firstDiag)
	}
	filtered, _ := firstDiag["filtered_tool_types"].([]any)
	if len(filtered) != 0 {
		t.Fatalf("default pass-through should not filter tool types: %#v", firstDiag)
	}
	if _, leaked := firstDiag["body"]; leaked {
		t.Fatalf("diagnostics must not include request body: %#v", firstDiag)
	}
	if firstDiag["host_callback"] != true || firstDiag["stream_id_present"] != true {
		t.Fatalf("unsafe or incomplete diagnostics: %#v", firstDiag)
	}
	if firstDiag["first_read_payload_bytes"].(float64) <= 0 {
		t.Fatalf("first read diagnostics did not record payload length: %#v", firstDiag)
	}
}

func TestExecuteStreamUsesConfiguredUpstreamModelAlias(t *testing.T) {
	configureTestStateWithConfig(t, func(cfg *executor.Config) {
		cfg.RouteEnabled = true
		cfg.UpstreamModelAliases = map[string]string{"gpt-5.4": "gpt-5.3-codex-spark"}
	})
	originalHostCall := hostCall
	t.Cleanup(func() { hostCall = originalHostCall })

	requestBody := []byte(`{"model":"gpt-5.4","stream":true,"input":[{"role":"user","content":"hi"}]}`)
	upstreamChunks := [][]byte{
		executor.SerializeEvent(map[string]any{"type": "response.created", "response": map[string]any{"id": "resp-alias", "model": "gpt-5.3-codex-spark", "status": "in_progress"}}),
		executor.SerializeEvent(map[string]any{"type": "response.completed", "response": map[string]any{
			"id":     "resp-alias",
			"model":  "gpt-5.3-codex-spark",
			"status": "completed",
			"usage": map[string]any{
				"input_tokens":  10,
				"output_tokens": 20,
				"total_tokens":  30,
				"output_tokens_details": map[string]any{
					"reasoning_tokens": 12,
				},
			},
		}}),
	}
	var mu sync.Mutex
	var openedModel string
	var openedBodyModel string
	var emitted strings.Builder
	readIndex := 0
	done := make(chan struct{})
	closeOnce := sync.Once{}
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		mu.Lock()
		defer mu.Unlock()
		switch method {
		case methodHostModelExecuteStream:
			req := payload.(hostModelExecutionRequest)
			openedModel = req.Model
			openedBodyModel = modelFromBody(req.Body)
			return rawJSON(t, hostModelStreamResponse{
				StatusCode: http.StatusOK,
				Headers:    http.Header{"Content-Type": []string{"text/event-stream"}},
				StreamID:   "upstream-alias",
			}), nil
		case methodHostModelStreamRead:
			if readIndex >= len(upstreamChunks) {
				return rawJSON(t, hostModelStreamReadResponse{Done: true}), nil
			}
			chunk := upstreamChunks[readIndex]
			readIndex++
			return rawJSON(t, hostModelStreamReadResponse{Payload: chunk, Done: readIndex >= len(upstreamChunks)}), nil
		case methodHostModelStreamClose:
			return rawJSON(t, map[string]any{}), nil
		case methodHostStreamEmit:
			req := payload.(hostStreamEmitRequest)
			emitted.Write(req.Payload)
			return rawJSON(t, map[string]any{}), nil
		case methodHostStreamClose:
			closeOnce.Do(func() { close(done) })
			return rawJSON(t, map[string]any{}), nil
		default:
			t.Fatalf("unexpected host call %s", method)
			return nil, nil
		}
	}

	raw, err := executorExecuteStream(mustJSON(t, map[string]any{
		"AuthID":           "key-1",
		"Model":            "gpt-5.4",
		"Format":           "openai-response",
		"Stream":           true,
		"SourceFormat":     "openai-response",
		"Payload":          requestBody,
		"OriginalRequest":  requestBody,
		"stream_id":        "client-alias",
		"host_callback_id": "callback-alias",
	}))
	if err != nil {
		t.Fatal(err)
	}
	var resp executorStreamResponse
	unwrapEnvelope(t, raw, &resp)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("executor stream did not finish")
	}
	mu.Lock()
	defer mu.Unlock()
	if openedModel != "gpt-5.3-codex-spark" || openedBodyModel != "gpt-5.3-codex-spark" {
		t.Fatalf("upstream model mismatch: request=%q body=%q", openedModel, openedBodyModel)
	}
	if !strings.Contains(emitted.String(), `"model":"gpt-5.4"`) {
		t.Fatalf("downstream stream should keep client-visible model:\n%s", emitted.String())
	}
	if strings.Contains(emitted.String(), `"model":"gpt-5.3-codex-spark"`) {
		t.Fatalf("downstream stream leaked upstream model:\n%s", emitted.String())
	}
	summaryRaw, err := managementHandle(mustJSON(t, managementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/cpa-codexcont-executor/admin/api/summaries",
		Query:  map[string][]string{"limit": {"5"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var summaryResp managementResponse
	unwrapEnvelope(t, summaryRaw, &summaryResp)
	var summaryBody struct {
		Summaries []map[string]any `json:"summaries"`
	}
	if err := json.Unmarshal(summaryResp.Body, &summaryBody); err != nil {
		t.Fatal(err)
	}
	if len(summaryBody.Summaries) == 0 {
		t.Fatal("expected saved executor summary")
	}
	gotSummary := summaryBody.Summaries[0]
	if gotSummary["model"] != "gpt-5.4" {
		t.Fatalf("summary model = %#v, want client model", gotSummary["model"])
	}
	diagnostics, _ := gotSummary["diagnostics"].(map[string]any)
	diagRounds, _ := diagnostics["rounds"].([]any)
	firstDiag, _ := diagRounds[0].(map[string]any)
	if firstDiag["model"] != "gpt-5.3-codex-spark" || firstDiag["requested_model"] != "gpt-5.4" || firstDiag["body_model"] != "gpt-5.3-codex-spark" {
		t.Fatalf("diagnostics should show safe alias routing evidence: %#v", firstDiag)
	}
}

func TestExecuteStreamConfiguredSparkAliasFiltersUnsupportedTools(t *testing.T) {
	configureTestStateWithConfig(t, func(cfg *executor.Config) {
		cfg.RouteEnabled = true
		cfg.UpstreamModelAliases = map[string]string{"gpt-5.5": "gpt-5.3-codex-spark"}
	})
	originalHostCall := hostCall
	t.Cleanup(func() { hostCall = originalHostCall })

	requestBody := []byte(`{"model":"gpt-5.5","stream":true,"input":[{"role":"user","content":"hi"}],"tools":[{"type":"image_generation"},{"type":"function","name":"lookup"}],"tool_choice":"image_generation"}`)
	upstreamChunks := [][]byte{
		executor.SerializeEvent(map[string]any{"type": "response.created", "response": map[string]any{"id": "resp-spark", "model": "gpt-5.3-codex-spark", "status": "in_progress"}}),
		executor.SerializeEvent(map[string]any{"type": "response.completed", "response": map[string]any{
			"id":     "resp-spark",
			"model":  "gpt-5.3-codex-spark",
			"status": "completed",
			"usage": map[string]any{
				"input_tokens":  10,
				"output_tokens": 20,
				"total_tokens":  30,
			},
		}}),
	}
	var openedBody []byte
	var openedModel string
	readIndex := 0
	done := make(chan struct{})
	closeOnce := sync.Once{}
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		switch method {
		case methodHostModelExecuteStream:
			req := payload.(hostModelExecutionRequest)
			openedModel = req.Model
			openedBody = append([]byte(nil), req.Body...)
			return rawJSON(t, hostModelStreamResponse{StatusCode: http.StatusOK, StreamID: "upstream-spark"}), nil
		case methodHostModelStreamRead:
			if readIndex >= len(upstreamChunks) {
				return rawJSON(t, hostModelStreamReadResponse{Done: true}), nil
			}
			chunk := upstreamChunks[readIndex]
			readIndex++
			return rawJSON(t, hostModelStreamReadResponse{Payload: chunk, Done: readIndex >= len(upstreamChunks)}), nil
		case methodHostModelStreamClose:
			return rawJSON(t, map[string]any{}), nil
		case methodHostStreamEmit:
			return rawJSON(t, map[string]any{}), nil
		case methodHostStreamClose:
			closeOnce.Do(func() { close(done) })
			return rawJSON(t, map[string]any{}), nil
		default:
			t.Fatalf("unexpected host call %s", method)
			return nil, nil
		}
	}

	if _, err := executorExecuteStream(mustJSON(t, map[string]any{
		"AuthID":       "key-1",
		"Model":        "gpt-5.5",
		"Format":       "openai-response",
		"Stream":       true,
		"SourceFormat": "openai-response",
		"Payload":      requestBody,
		"stream_id":    "client-spark",
	})); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("executor stream did not finish")
	}
	if openedModel != "gpt-5.3-codex-spark" || !strings.Contains(string(openedBody), `"model":"gpt-5.3-codex-spark"`) {
		t.Fatalf("explicit Spark alias not applied: model=%q body=%s", openedModel, openedBody)
	}
	if strings.Contains(string(openedBody), "image_generation") || !strings.Contains(string(openedBody), `"type":"function"`) {
		t.Fatalf("Spark alias should filter only unsupported built-ins: %s", openedBody)
	}
}

func TestRewritePayloadModelLeavesInvalidJSONUntouched(t *testing.T) {
	raw := []byte(`not-json`)
	got := rewritePayloadModel(raw, "gpt-5.3-codex-spark")
	if string(got) != string(raw) {
		t.Fatalf("invalid JSON should be copied unchanged: %q", got)
	}
}

func TestRewriteUpstreamPayloadFiltersUnsupportedSparkBuiltins(t *testing.T) {
	raw := []byte(`{"model":"gpt-5.5","tools":[{"type":"image_generation"},{"type":"function","name":"lookup"}],"tool_choice":{"type":"image_generation"}}`)
	got, filtered := rewriteUpstreamPayload(raw, "gpt-5.3-codex-spark")
	if strings.Join(filtered, ",") != "image_generation" {
		t.Fatalf("filtered tools = %#v", filtered)
	}
	var body map[string]any
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "gpt-5.3-codex-spark" {
		t.Fatalf("model was not rewritten: %s", got)
	}
	if _, ok := body["tool_choice"]; ok {
		t.Fatalf("image_generation tool_choice should be removed: %s", got)
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected only custom/function tool to remain: %#v body=%s", tools, got)
	}
	tool, _ := tools[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != "lookup" {
		t.Fatalf("function tool should be preserved: %#v", tool)
	}
}

func TestRewriteUpstreamPayloadRemovesEmptyTools(t *testing.T) {
	raw := []byte(`{"model":"gpt-5.5","tools":[{"type":"image_generation"}]}`)
	got, filtered := rewriteUpstreamPayload(raw, "gpt-5.3-codex-spark")
	if strings.Join(filtered, ",") != "image_generation" {
		t.Fatalf("filtered tools = %#v", filtered)
	}
	var body map[string]any
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["tools"]; ok {
		t.Fatalf("empty tools array should be omitted: %s", got)
	}
}

func TestExecutorProtocolDefaultsToOpenAIResponse(t *testing.T) {
	for _, value := range []string{"", "responses", "openai-responses", "openai_responses"} {
		if got := executorProtocol(value); got != "openai-response" {
			t.Fatalf("executorProtocol(%q) = %q", value, got)
		}
	}
	if got := executorProtocol("openai-response"); got != "openai-response" {
		t.Fatalf("executorProtocol(openai-response) = %q", got)
	}
}

func TestExecutorRequestNormalizationKeepsLegacyNestedPayload(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","stream":true}`)
	req := normalizedExecutorRequest(executorCallRequest{
		NestedExecutorRequest: executorRequest{Model: "gpt-5.5", Payload: body},
	})
	if req.Model != "gpt-5.5" || string(req.Payload) != string(body) {
		t.Fatalf("normalized nested request = %#v", req)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	return rawJSON(t, value)
}

func rawJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
