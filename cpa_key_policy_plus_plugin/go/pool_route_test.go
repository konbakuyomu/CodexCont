package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"codexcont/cpa-key-policy-plus-plugin/internal/policyplus"
)

// failModelList makes the host's model registry callback fail while the
// credential list keeps working, as seen in production under load.
func failModelList(t *testing.T) {
	t.Helper()
	working := hostCall
	t.Cleanup(func() { hostCall = working })
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		if method == methodHostModelsList {
			return nil, fmt.Errorf("host callback timed out")
		}
		return working(method, payload)
	}
}

// countLocalModelCalls points the native CPA config at a fake CPA and counts
// calls to its /v1/models, the fallback that used to stall routing.
func countLocalModelCalls(t *testing.T) *int64 {
	t.Helper()
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("port: "+port+"\napi-keys:\n  - sk-native-one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	old := state.cfg.NativeKeysConfigPath
	state.cfg.NativeKeysConfigPath = path
	state.mu.Unlock()
	t.Cleanup(func() {
		state.mu.Lock()
		state.cfg.NativeKeysConfigPath = old
		state.mu.Unlock()
	})
	return &hits
}

func expireModelCatalog() {
	poolState.mu.Lock()
	poolState.catalogAt = time.Time{}
	poolState.mu.Unlock()
}

func TestMembersStayPinnedWhenTheModelListCannotBeRead(t *testing.T) {
	f := setupPoolFixture(t)
	stubPoolHost(t, true)
	savePool(t, policyplus.PoolVisibilityAnonymous, "pro",
		policyplus.PoolMember{KeyID: f.owner.ID},
		policyplus.PoolMember{KeyID: f.alice.ID, SharePercent: share(30)},
	)
	if got := routeFor(t, "sk-alice-secret", "gpt-5.5"); got.TargetModel != "pro/gpt-5.5" {
		t.Fatalf("member pinned while the model list is readable: %+v", got)
	}
	hits := countLocalModelCalls(t)
	failModelList(t)

	// The list was read before: a failed refresh keeps it.
	expireModelCatalog()
	if got := routeFor(t, "sk-alice-secret", "gpt-5.5"); got.TargetModel != "pro/gpt-5.5" {
		t.Fatalf("a failed refresh must not unpin members: %+v", got)
	}
	// Never read at all: members are still pinned rather than left to CPA's
	// rotation, where they land on other pools' accounts.
	resetPoolCaches()
	if got := routeFor(t, "sk-alice-secret", "gpt-5.5(high)"); got.TargetModel != "pro/gpt-5.5(high)" {
		t.Fatalf("an unreadable model list must not unpin members: %+v", got)
	}
	if got := routeFor(t, "sk-carol-secret", "gpt-5.5"); got.Handled {
		t.Fatalf("keys outside the pool are not routed: %+v", got)
	}
	if n := atomic.LoadInt64(hits); n != 0 {
		t.Fatalf("routing must not call back into CPA's /v1/models (%d calls)", n)
	}
	poolState.mu.Lock()
	retryIn := modelCatalogTTL - time.Since(poolState.catalogAt)
	poolState.mu.Unlock()
	if retryIn > catalogRetryTTL+time.Second {
		t.Fatalf("a failed read is retried within %v, not cached for %v", catalogRetryTTL, retryIn)
	}
}

func TestRouteModelUsesTheInjectedIdentityWhenAuthorizationIsReplaced(t *testing.T) {
	f := setupPoolFixture(t)
	stubPoolHost(t, true)
	savePool(t, policyplus.PoolVisibilityAnonymous, "pro", policyplus.PoolMember{KeyID: f.alice.ID, SharePercent: share(30)})
	route := func(meta map[string]any) modelRouteResponse {
		t.Helper()
		raw, err := routeModel(mustJSON(t, modelRouteRequest{
			RequestedModel: "gpt-5.5",
			Headers:        http.Header{"Authorization": {"Bearer upstream-access-token"}},
			Metadata:       meta,
		}))
		if err != nil {
			t.Fatal(err)
		}
		var resp modelRouteResponse
		unwrapPlusEnvelope(t, raw, &resp)
		return resp
	}
	if got := route(map[string]any{"provider": pluginID, "key_id": f.alice.ID}); got.TargetModel != "pro/gpt-5.5" {
		t.Fatalf("the identity this plugin injected at auth must pin the member: %+v", got)
	}
	if got := route(map[string]any{"provider": "someone-else", "key_id": f.alice.ID}); got.Handled {
		t.Fatalf("metadata from other sources is not trusted: %+v", got)
	}
}
