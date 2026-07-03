package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"codexcont/cpa-codexcont-executor-plugin/internal/executor"
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
	path := filepath.Join(t.TempDir(), "executor.sqlite")
	store, err := executor.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	if state.store != nil {
		_ = state.store.Close()
	}
	state.cfg = executor.DefaultConfig()
	state.cfg.RouteEnabled = routeEnabled
	state.cfg.StateDBPath = path
	state.store = store
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
	if reg.Capabilities.FrontendAuthProvider || reg.Capabilities.UsagePlugin {
		t.Fatalf("executor plugin must not own frontend auth or usage portal: %#v", reg.Capabilities)
	}
	if !reg.Capabilities.ModelRouter || !reg.Capabilities.Executor || !reg.Capabilities.ManagementAPI {
		t.Fatalf("executor capabilities missing: %#v", reg.Capabilities)
	}
}

func TestManagementRegisterDoesNotExposeUserResource(t *testing.T) {
	raw, err := managementRegister()
	if err != nil {
		t.Fatal(err)
	}
	var reg managementRegistrationResponse
	unwrapEnvelope(t, raw, &reg)
	if len(reg.Resources) != 0 {
		t.Fatalf("executor plugin must not expose user resources: %#v", reg.Resources)
	}
	for _, route := range reg.Routes {
		if route.Path == "/user" || route.Path == "/user/api/session" {
			t.Fatalf("executor route overlaps Plus user portal: %#v", route)
		}
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
	raw, err = routeModel(mustJSON(t, modelRouteRequest{SourceFormat: "openai", Stream: true, Body: body}))
	if err != nil {
		t.Fatal(err)
	}
	unwrapEnvelope(t, raw, &resp)
	if !resp.Handled || resp.TargetKind != routeTargetSelf {
		t.Fatalf("enabled route did not handle streaming response: %#v", resp)
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

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
