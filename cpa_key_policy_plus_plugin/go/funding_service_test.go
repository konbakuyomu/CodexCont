package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"codexcont/cpa-key-policy-plus-plugin/internal/policyplus"
)

func setFundingMode(t *testing.T, mode string) {
	t.Helper()
	if err := loadedStore().SetFundingMode(context.Background(), mode); err != nil {
		t.Fatal(err)
	}
	invalidateFundingMode()
}

func saveKey(t *testing.T, key policyplus.KeyRecord) policyplus.KeyRecord {
	t.Helper()
	if err := loadedStore().SaveKeySettings(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestUnfundedModelsAreRecordedInObserveModeAndRefusedWhenEnforced(t *testing.T) {
	f := setupPoolFixture(t)
	stubPoolHost(t, false)
	priceGrok(t)
	ctx := context.Background()
	// bob: no pool, no USD window, not unlimited.
	if d := evaluatePolicy(f.bob, "grok-4", true); !d.Allowed {
		t.Fatalf("observe mode lets the request through: %+v", d)
	}
	misses, _ := loadedStore().FundingMisses(ctx, 10)
	if len(misses) != 1 || misses[0].KeyID != f.bob.ID || misses[0].Model != "grok-4" || misses[0].Provider != "xai" || misses[0].Reason != policyplus.FundingMissNoSource {
		t.Fatalf("observe mode records what it would refuse: %+v", misses)
	}
	evaluatePolicy(f.bob, "grok-4", false)
	if misses, _ := loadedStore().FundingMisses(ctx, 10); misses[0].Count != 1 {
		t.Fatalf("checks that do not admit a request are not counted: %+v", misses)
	}

	setFundingMode(t, policyplus.FundingModeEnforce)
	d := evaluatePolicy(f.bob, "grok-4", true)
	if d.Allowed || d.Code != "no_funding_source" || !strings.Contains(d.Message, "Grok") {
		t.Fatalf("enforce mode refuses an unfunded model: %+v", d)
	}
	if d := evaluatePolicy(f.bob, "", true); !d.Allowed {
		t.Fatalf("requests without a model are not checked: %+v", d)
	}

	bob := f.bob
	bob.QuotaUnlimited = true
	saveKey(t, bob)
	if d := evaluatePolicy(bob, "grok-4", true); !d.Allowed {
		t.Fatalf("a quota-unlimited key is funded: %+v", d)
	}
	carol := f.carol
	carol.WeeklyLimitUSD = floatPtr(50)
	saveKey(t, carol)
	if d := evaluatePolicy(carol, "grok-4", true); !d.Allowed {
		t.Fatalf("a key with a USD window is funded: %+v", d)
	}
}

func TestPoolSeatsFundOnlyTheirOwnProvider(t *testing.T) {
	f := setupPoolFixture(t)
	stubPoolHost(t, false)
	priceGrok(t)
	setFundingMode(t, policyplus.FundingModeEnforce)
	// Grok pool: bob and carol hold shares. Codex pool: owner owns, carol is
	// only pinned.
	budget := 100.0
	pools := []policyplus.Pool{
		{ID: "pro", Name: "Pro 20x", AuthIndex: "acc-pro", Members: []policyplus.PoolMember{
			{KeyID: f.owner.ID}, {KeyID: f.carol.ID, Role: policyplus.PoolRolePinned},
		}},
		{ID: "grok", Name: "Grok", AuthIndex: "acc-xai", Provider: "xai", BudgetUSD: &budget, BudgetResetAt: f.now.Unix() + 3600, Members: []policyplus.PoolMember{
			{KeyID: f.bob.ID, SharePercent: share(50)}, {KeyID: f.carol.ID, SharePercent: share(50)},
		}},
	}
	if err := loadedStore().SavePools(context.Background(), pools); err != nil {
		t.Fatal(err)
	}
	resetPoolCaches()
	cases := []struct {
		key   policyplus.KeyRecord
		model string
		ok    bool
		why   string
	}{
		{f.bob, "grok-4", true, "a Grok share funds Grok"},
		{f.bob, "gpt-5.5", false, "a Grok share does not fund GPT"},
		{f.owner, "gpt-5.5", true, "the codex owner is funded for GPT"},
		{f.owner, "grok-4", false, "owning the codex pool does not fund Grok"},
		{f.carol, "gpt-5.5", false, "a pinned seat pays with the key's own USD windows, which carol lacks"},
		{f.carol, "grok-4", true, "carol's Grok share funds Grok"},
	}
	for _, tc := range cases {
		d := evaluatePolicy(tc.key, tc.model, true)
		if d.Allowed != tc.ok || (!tc.ok && d.Code != "no_funding_source") {
			t.Fatalf("%s: %+v", tc.why, d)
		}
	}
}

func TestUnpricedModelsNeedAPriceUnlessTheKeyIsUnlimited(t *testing.T) {
	f := setupPoolFixture(t)
	stubPoolHost(t, false)
	ctx := context.Background()
	budget := 100.0
	if err := loadedStore().SavePools(ctx, []policyplus.Pool{{ID: "grok", Name: "Grok", AuthIndex: "acc-xai", Provider: "xai", BudgetUSD: &budget, BudgetResetAt: f.now.Unix() + 3600,
		Members: []policyplus.PoolMember{{KeyID: f.bob.ID, SharePercent: share(50)}}}}); err != nil {
		t.Fatal(err)
	}
	resetPoolCaches()
	// grok-9 has no CPAMP price.
	if d := evaluatePolicy(f.bob, "grok-9", true); !d.Allowed {
		t.Fatalf("observe mode only records the missing price: %+v", d)
	}
	misses, _ := loadedStore().FundingMisses(ctx, 10)
	if len(misses) != 1 || misses[0].Reason != policyplus.FundingMissUnpriced {
		t.Fatalf("the unpriced model is recorded: %+v", misses)
	}
	setFundingMode(t, policyplus.FundingModeEnforce)
	if d := evaluatePolicy(f.bob, "grok-9", true); d.Allowed || d.Code != "model_price_unavailable" {
		t.Fatalf("a pool member cannot use an unpriced model: %+v", d)
	}
	owner := f.owner
	owner.QuotaUnlimited = true
	saveKey(t, owner)
	if d := evaluatePolicy(owner, "grok-9", true); !d.Allowed {
		t.Fatalf("a quota-unlimited key may use unpriced models: %+v", d)
	}
}

func TestAdminFundingAPIAndKeyRows(t *testing.T) {
	f := setupPoolFixture(t)
	stubPoolHost(t, false)
	priceGrok(t)
	saveProAndGrokPools(t, f, 100, f.now.Unix()+3600)
	carol := f.carol
	carol.QuotaUnlimited = true
	saveKey(t, carol)
	evaluatePolicy(f.alice, "grok-4", true)

	path := "/v0/management/plugins/cpa-key-policy-plus/funding"
	resp := portalGet(t, path, "", nil)
	var body struct {
		Mode   string           `json:"mode"`
		Total  int64            `json:"total"`
		Misses []map[string]any `json:"misses"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("funding = %d %s", resp.StatusCode, resp.Body)
	}
	if body.Mode != policyplus.FundingModeObserve || body.Total != 1 || body.Misses[0]["label"] != "Grok" {
		t.Fatalf("funding status: %+v", body)
	}
	if resp := adminPost(t, path, map[string]any{"mode": "enforce"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("switching mode needs confirm: %d %s", resp.StatusCode, resp.Body)
	}
	if resp := adminPost(t, path, map[string]any{"mode": "enforce", "clear": true, "confirm": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("switch = %d %s", resp.StatusCode, resp.Body)
	}
	if currentFundingMode() != policyplus.FundingModeEnforce {
		t.Fatal("mode not switched")
	}
	if misses, _ := loadedStore().FundingMisses(context.Background(), 10); len(misses) != 0 {
		t.Fatalf("misses not cleared: %+v", misses)
	}

	resp = portalGet(t, "/v0/management/plugins/cpa-key-policy-plus/keys", "", nil)
	var keys struct {
		Keys        []map[string]any `json:"keys"`
		FundingMode string           `json:"funding_mode"`
	}
	if err := json.Unmarshal(resp.Body, &keys); err != nil {
		t.Fatal(err)
	}
	if keys.FundingMode != policyplus.FundingModeEnforce {
		t.Fatalf("key list reports the mode: %q", keys.FundingMode)
	}
	want := map[string]string{f.bob.ID: "seat/seat", f.owner.ID: "seat/seat", f.carol.ID: "seat/seat", f.alice.ID: "none/none"}
	for _, row := range keys.Keys {
		expected, ok := want[row["id"].(string)]
		if !ok {
			continue
		}
		funding, _ := row["funding"].([]any)
		var kinds []string
		for _, item := range funding {
			kinds = append(kinds, item.(map[string]any)["kind"].(string))
		}
		if strings.Join(kinds, "/") != expected {
			t.Fatalf("%v funding = %v, want %s", row["name"], funding, expected)
		}
		if row["id"] == f.carol.ID && row["quota_unlimited"] != true {
			t.Fatalf("carol's row reports quota_unlimited: %+v", row)
		}
	}
	resp = adminPost(t, "/v0/management/plugins/cpa-key-policy-plus/keys/save", map[string]any{"keys": []map[string]any{{"id": f.alice.ID, "enabled": true, "quota_unlimited": true}}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save = %d %s", resp.StatusCode, resp.Body)
	}
	for _, key := range keysByID(context.Background(), loadedStore()) {
		if key.ID == f.alice.ID && !key.QuotaUnlimited {
			t.Fatal("quota_unlimited not saved")
		}
	}
}

func TestFundingRouteIsRegistered(t *testing.T) {
	raw, err := managementRegister()
	if err != nil {
		t.Fatal(err)
	}
	var reg managementRegistrationResponse
	unwrapPlusEnvelope(t, raw, &reg)
	routes := map[string]bool{}
	for _, route := range reg.Routes {
		routes[route.Method+" "+route.Path] = true
	}
	if !routes["GET /plugins/cpa-key-policy-plus/funding"] || !routes["PUT /plugins/cpa-key-policy-plus/funding"] {
		t.Fatalf("funding routes missing: %#v", reg.Routes)
	}
}
