package governor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ptr(v float64) *float64 { return &v }

func writePolicyState(t *testing.T, dir string, rawKey string) string {
	t.Helper()
	path := filepath.Join(dir, "key-policy.json")
	body := map[string]any{
		"keys": []map[string]any{{
			"id":       "alice-key",
			"name":     "Alice",
			"key_hash": "sha256:" + SHA256Hex(rawKey),
			"enabled":  true,
			"rpm":      12,
			"models": []map[string]any{{
				"alias":                        "gpt-5.5",
				"target_model":                 "gpt-5.5",
				"input_price_per_million":      5,
				"output_price_per_million":     30,
				"cache_read_price_per_million": 0.5,
			}},
			"daily_limit_usd":  5,
			"weekly_limit_usd": 30,
		}},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestKeyPolicyStateParsesSafeRecords(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyState(t, dir, "cpa_live")
	state, err := LoadKeyPolicyState(path)
	if err != nil {
		t.Fatal(err)
	}
	key, ok := state.FindByRawKey(" cpa_live ")
	if !ok {
		t.Fatal("raw key did not match policy state")
	}
	if key.ID != "alice-key" || key.Name != "Alice" || !key.Enabled {
		t.Fatalf("unexpected key: %#v", key)
	}
	if len(key.Models) != 1 || key.Models[0] != "gpt-5.5" {
		t.Fatalf("models = %#v", key.Models)
	}
	price, ok := PriceForModel(key.Prices, "GPT-5.5")
	if !ok || price.InputPerMillion != 5 || price.OutputPerMillion != 30 || price.CacheReadPerMillion != 0.5 {
		t.Fatalf("price = %#v ok=%v", price, ok)
	}
	safe := key.Safe()
	encoded, _ := json.Marshal(safe)
	if strings.Contains(string(encoded), SHA256Hex("cpa_live")) || strings.Contains(string(encoded), "cpa_live") {
		t.Fatalf("safe projection leaked key material: %s", encoded)
	}
}

func TestPricingBreakdownUsesPerMillionAndCachedInput(t *testing.T) {
	price := ModelPrice{
		Model:               "gpt-5.5",
		InputPerMillion:     5,
		OutputPerMillion:    30,
		CacheReadPerMillion: 0.5,
	}
	breakdown := CostForUsage(price, TokenUsage{
		InputTokens:     100,
		CachedTokens:    20,
		OutputTokens:    50,
		ReasoningTokens: 30,
		TotalTokens:     150,
	}, "gpt-5.5")
	if got := breakdown.Tokens["billable_uncached_input"]; got != 80 {
		t.Fatalf("billable input = %d", got)
	}
	if got := breakdown.Tokens["visible_output_estimate"]; got != 20 {
		t.Fatalf("visible output estimate = %d", got)
	}
	if breakdown.Costs["total"] <= 0 {
		t.Fatalf("total cost should be positive: %#v", breakdown.Costs)
	}
	if breakdown.Costs["cached_input"] <= 0 {
		t.Fatalf("cached input should be charged with cache read price: %#v", breakdown.Costs)
	}
}

func TestStoreUsageWindowsAndSoftReset(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "governor.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := KeyRecord{
		ID:              "alice-key",
		Name:            "Alice",
		KeyHash:         "sha256:" + SHA256Hex("cpa_live"),
		Enabled:         true,
		Preview:         HashPreview(SHA256Hex("cpa_live")),
		FiveHourUSD:     ptr(1),
		MonthlyLimitUSD: ptr(10),
	}
	if err := store.UpsertKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.InsertUsage(ctx, UsageEvent{
		RequestID:   "req-old",
		KeyID:       "alice-key",
		RequestedAt: now.Add(-2 * time.Hour),
		Cost:        0.5,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertUsage(ctx, UsageEvent{
		RequestID:   "req-new",
		KeyID:       "alice-key",
		RequestedAt: now.Add(-30 * time.Minute),
		Cost:        0.25,
	}); err != nil {
		t.Fatal(err)
	}
	used, err := store.UsageSum(ctx, "alice-key", WindowFor(Range5H, now))
	if err != nil {
		t.Fatal(err)
	}
	if used != 0.75 {
		t.Fatalf("used before reset = %v", used)
	}
	if err := store.Reset(ctx, "alice-key", Range5H, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	used, err = store.UsageSum(ctx, "alice-key", WindowFor(Range5H, now))
	if err != nil {
		t.Fatal(err)
	}
	if used != 0.25 {
		t.Fatalf("used after reset = %v", used)
	}
}

func TestSecurityAndRedaction(t *testing.T) {
	raw := " cpa_live "
	hash := SHA256Hex(raw)
	if hash != SHA256Hex(strings.TrimSpace(raw)) {
		t.Fatal("SHA256Hex should trim raw keys")
	}
	if hash != SHA256Hex("Bearer cpa_live") || hash != SHA256Hex("Authorization: Bearer cpa_live") {
		t.Fatal("SHA256Hex should normalize pasted bearer prefixes")
	}
	token, err := SignSession(SessionPayload{KeyID: "alice", KeyHash: "sha256:" + hash, ExpiresAt: time.Now().Add(time.Hour).Unix()}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := VerifySession(token, "secret", time.Now())
	if !ok || payload.KeyID != "alice" {
		t.Fatalf("session verify failed: %#v ok=%v", payload, ok)
	}
	if _, ok := VerifySession(token, "wrong", time.Now()); ok {
		t.Fatal("session verified with wrong secret")
	}
	brief := Brief("Authorization: Bearer secret and api_key=abc", 200)
	if strings.Contains(brief, "secret") || strings.Contains(brief, "abc") {
		t.Fatalf("secret leaked in brief: %s", brief)
	}
}

func TestSubmittedKeyHints(t *testing.T) {
	cases := []struct {
		name string
		in   string
		code string
	}{
		{name: "missing", in: " ", code: "missing_api_key"},
		{name: "native", in: "sk-abc", code: "native_cpa_key_not_supported"},
		{name: "preview", in: "cpa_abcd...efgh", code: "key_preview_not_usable"},
		{name: "unsupported", in: "abc", code: "unsupported_key_format"},
		{name: "short cpa", in: "Bearer cpa_live", code: "key_preview_not_usable"},
		{name: "full cpa", in: "Bearer cpa_abcdefghijklmnopqrstuvwxyz0123456789", code: "invalid_api_key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hint := ExplainUnmatchedSubmittedKey(tc.in)
			if hint.Error != tc.code || hint.Message == "" {
				t.Fatalf("hint = %#v", hint)
			}
		})
	}
	if got := NormalizeSubmittedKey("Authorization: Bearer Bearer cpa_live "); got != "cpa_live" {
		t.Fatalf("normalized key = %q", got)
	}
	if got := NormalizeSubmittedKey("\ufeff“Bearer cpa_live\u200b”"); got != "cpa_live" {
		t.Fatalf("normalized decorated key = %q", got)
	}
}
