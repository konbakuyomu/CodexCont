package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"codexcont/cpa-governor-plugin/internal/governor"
)

func configureTestState(t *testing.T) governor.KeyRecord {
	t.Helper()
	store, err := governor.OpenStore(filepath.Join(t.TempDir(), "governor.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	key := governor.KeyRecord{
		ID:          "alice-key",
		Name:        "Alice",
		KeyHash:     "sha256:" + governor.SHA256Hex("cpa_live"),
		Enabled:     true,
		Preview:     governor.HashPreview(governor.SHA256Hex("cpa_live")),
		RPM:         60,
		Concurrency: 2,
		Models:      []string{"gpt-5.5"},
		Prices: map[string]governor.ModelPrice{
			"gpt-5.5": {
				Model:               "gpt-5.5",
				InputPerMillion:     5,
				OutputPerMillion:    30,
				CacheReadPerMillion: 0.5,
			},
		},
	}
	if err := store.UpsertKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.cfg = governor.DefaultConfig()
	state.cfg.SessionSecret = "secret"
	state.store = store
	state.keyState = governor.KeyPolicyState{}
	state.rpmBuckets = map[string][]time.Time{}
	state.concurrency = map[string]int{}
	state.mu.Unlock()
	t.Cleanup(func() {
		state.mu.Lock()
		if state.store != nil {
			_ = state.store.Close()
			state.store = nil
		}
		state.mu.Unlock()
	})
	return key
}

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

func TestPluginRegistrationUsesLocalABI(t *testing.T) {
	raw, err := handleMethod(methodPluginRegister, nil)
	if err != nil {
		t.Fatal(err)
	}
	var reg registration
	unwrapEnvelope(t, raw, &reg)
	if reg.SchemaVersion != schemaVersion || !reg.Capabilities.ManagementAPI || !reg.Capabilities.UsagePlugin {
		t.Fatalf("registration = %#v", reg)
	}
	if len(reg.Metadata.ConfigFields) == 0 {
		t.Fatal("config fields should be exposed")
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(env.Result, &payload); err != nil {
		t.Fatal(err)
	}
	caps, _ := payload["capabilities"].(map[string]any)
	for _, key := range []string{"frontend_auth_provider", "model_router", "executor", "usage_plugin", "management_api"} {
		if caps[key] != true {
			t.Fatalf("capability %s missing or false in RPC registration: %s", key, string(env.Result))
		}
	}
	if _, ok := caps["FrontendAuthProvider"]; ok {
		t.Fatalf("unexpected Go-style capability field in RPC registration: %s", string(env.Result))
	}
}

func TestManagementRegisterUsesCPARPCSchema(t *testing.T) {
	raw, err := managementRegister()
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("envelope error: %#v", env.Error)
	}
	var payload map[string]any
	if err := json.Unmarshal(env.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["routes"]; !ok {
		t.Fatalf("missing lowercase routes field: %s", string(env.Result))
	}
	if _, ok := payload["resources"]; !ok {
		t.Fatalf("missing lowercase resources field: %s", string(env.Result))
	}
	if _, ok := payload["Routes"]; ok {
		t.Fatalf("unexpected uppercase Routes field: %s", string(env.Result))
	}
	if _, ok := payload["Resources"]; ok {
		t.Fatalf("unexpected uppercase Resources field: %s", string(env.Result))
	}
}

func TestFrontendAuthAcceptsManagedKeyAndRejectsDisallowedModel(t *testing.T) {
	configureTestState(t)
	authBody, _ := json.Marshal(frontendAuthRequest{
		Headers: http.Header{"Authorization": []string{"Bearer cpa_live"}},
		Body:    []byte(`{"model":"gpt-5.5","stream":true}`),
	})
	raw, err := frontendAuth(authBody)
	if err != nil {
		t.Fatal(err)
	}
	var resp frontendAuthResponse
	unwrapEnvelope(t, raw, &resp)
	if !resp.Authenticated || resp.Principal != "alice-key" {
		t.Fatalf("auth response = %#v", resp)
	}
	releaseConcurrency("alice-key")

	disallowed, _ := json.Marshal(frontendAuthRequest{
		Headers: http.Header{"Authorization": []string{"Bearer cpa_live"}},
		Body:    []byte(`{"model":"other-model","stream":true}`),
	})
	raw, err = frontendAuth(disallowed)
	if err != nil {
		t.Fatal(err)
	}
	unwrapEnvelope(t, raw, &resp)
	if resp.Authenticated {
		t.Fatalf("disallowed model authenticated: %#v", resp)
	}
}

func TestUsageHandleStoresCostAndReleasesConcurrency(t *testing.T) {
	key := configureTestState(t)
	if !acquireConcurrency(key) {
		t.Fatal("expected concurrency acquire")
	}
	rec := usageRecord{
		Model:       "gpt-5.5",
		Alias:       "gpt-5.5",
		APIKey:      "alice-key",
		RequestedAt: time.Now(),
		Latency:     1500 * time.Millisecond,
		Detail: usageDetail{
			InputTokens:     100,
			CachedTokens:    20,
			OutputTokens:    50,
			ReasoningTokens: 30,
			TotalTokens:     150,
		},
	}
	rawRec, _ := json.Marshal(rec)
	if _, err := usageHandle(rawRec); err != nil {
		t.Fatal(err)
	}
	if state.concurrency["alice-key"] != 0 {
		t.Fatalf("concurrency not released: %d", state.concurrency["alice-key"])
	}
	events, err := loadedStore().RecentEvents(context.Background(), "alice-key", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Cost <= 0 || events[0].Usage.ReasoningTokens != 30 {
		t.Fatalf("events = %#v", events)
	}
}

func TestRefreshKeyPolicyStateImportsNewKeys(t *testing.T) {
	dir := t.TempDir()
	store, err := governor.OpenStore(filepath.Join(dir, "governor.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "policy.json")
	writePolicy := func(id, rawKey string) {
		t.Helper()
		body := map[string]any{"keys": []map[string]any{{
			"id":       id,
			"name":     id,
			"key_hash": "sha256:" + governor.SHA256Hex(rawKey),
			"enabled":  true,
		}}}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writePolicy("alice", "cpa_alice")

	state.mu.Lock()
	state.cfg = governor.DefaultConfig()
	state.store = store
	state.keyState = governor.KeyPolicyState{}
	state.keyStatePath = path
	state.keyStateModTime = time.Time{}
	state.keyStateLastCheck = time.Time{}
	state.rpmBuckets = map[string][]time.Time{}
	state.concurrency = map[string]int{}
	state.mu.Unlock()
	t.Cleanup(func() {
		state.mu.Lock()
		if state.store != nil {
			_ = state.store.Close()
			state.store = nil
		}
		state.mu.Unlock()
	})

	if err := refreshKeyPolicyState(true); err != nil {
		t.Fatal(err)
	}
	if key, ok := findKeyByRaw("cpa_alice"); !ok || key.ID != "alice" {
		t.Fatalf("alice not imported: %#v ok=%v", key, ok)
	}

	writePolicy("bob", "cpa_bob")
	if err := refreshKeyPolicyState(true); err != nil {
		t.Fatal(err)
	}
	if key, ok := findKeyByRaw("cpa_bob"); !ok || key.ID != "bob" {
		t.Fatalf("bob not imported after refresh: %#v ok=%v", key, ok)
	}
}
