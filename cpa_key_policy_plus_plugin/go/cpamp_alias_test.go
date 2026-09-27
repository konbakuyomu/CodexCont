package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"codexcont/cpa-key-policy-plus-plugin/internal/policyplus"
)

type fakeCPAMP struct {
	sync.Mutex
	items []cpampAliasItem
	puts  int
}

func (f *fakeCPAMP) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/api-key-aliases" || r.Header.Get("Authorization") != "Bearer admin-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.Lock()
		defer f.Unlock()
		if r.Method == http.MethodPut {
			var body struct {
				Items []cpampAliasItem `json:"items"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode put: %v", err)
			}
			seen := map[string]bool{}
			for _, item := range body.Items {
				if seen[item.Alias] {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"code":"api_key_alias_duplicate"}`))
					return
				}
				seen[item.Alias] = true
			}
			f.items = body.Items
			f.puts++
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": f.items})
	}
}

func TestCPAMPAliasesNameKeysByPrincipalAndKeepRawAliases(t *testing.T) {
	key := setupTestState(t)
	twin := key
	twin.ID = "alice-twin"
	keys := []policyplus.KeyRecord{key, twin}
	rawHash := policyplus.SHA256Hex("sk-alice-secret")
	fake := &fakeCPAMP{items: []cpampAliasItem{{APIKeyHash: rawHash, Alias: "Alice"}}}
	server := httptest.NewServer(fake.handler(t))
	defer server.Close()

	want := cpampAliasTargets(keys)
	if got := want[cpampPrincipalHash(key.ID)]; got != "Alice·KP+" {
		t.Fatalf("alias = %q", got)
	}
	if got := want[cpampPrincipalHash(twin.ID)]; got != "Alice e-twin·KP+" {
		t.Fatalf("twin alias = %q, want a distinct name", got)
	}
	if err := pushCPAMPAliases(context.Background(), server.URL, "admin-secret", want); err != nil {
		t.Fatal(err)
	}
	if fake.puts != 1 || len(fake.items) != 3 || fake.items[0] != (cpampAliasItem{APIKeyHash: rawHash, Alias: "Alice"}) {
		t.Fatalf("items after push = %+v (puts %d)", fake.items, fake.puts)
	}
	if err := pushCPAMPAliases(context.Background(), server.URL, "admin-secret", want); err != nil || fake.puts != 1 {
		t.Fatalf("unchanged aliases must not be re-put: err %v, puts %d", err, fake.puts)
	}
	if err := pushCPAMPAliases(context.Background(), server.URL, "wrong", want); err == nil || err.Error() != "CPAMP 管理员密钥无效" {
		t.Fatalf("bad key error = %v", err)
	}
}

func TestCPAMPAliasSyncNeedsTheAdminKeyFile(t *testing.T) {
	key := setupTestState(t)
	fake := &fakeCPAMP{}
	server := httptest.NewServer(fake.handler(t))
	defer server.Close()
	state.mu.Lock()
	state.cfg.CPAMPURL = server.URL
	cfg := state.cfg
	state.mu.Unlock()
	cpampAliasState.Lock()
	cpampAliasState.pushed, cpampAliasState.at, cpampAliasState.err = nil, time.Time{}, ""
	cpampAliasState.Unlock()

	maybeSyncCPAMPAliases([]policyplus.KeyRecord{key})
	time.Sleep(50 * time.Millisecond)
	if fake.puts != 0 || cpampAliasStatus()["configured"] != false {
		t.Fatal("no admin key file: nothing may be pushed")
	}

	if err := os.WriteFile(filepath.Join(filepath.Dir(cfg.StateDBPath), "cpamp-admin-key"), []byte("admin-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	maybeSyncCPAMPAliases([]policyplus.KeyRecord{key})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if status := cpampAliasStatus(); status["synced"] == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	status := cpampAliasStatus()
	if status["configured"] != true || status["synced"] != 1 || status["error"] != nil {
		t.Fatalf("status = %+v", status)
	}
	maybeSyncCPAMPAliases([]policyplus.KeyRecord{key})
	time.Sleep(50 * time.Millisecond)
	fake.Lock()
	defer fake.Unlock()
	if fake.puts != 1 {
		t.Fatalf("fresh sync must not repeat, puts = %d", fake.puts)
	}
}
