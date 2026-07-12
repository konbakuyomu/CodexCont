package executor

import "testing"

func TestRoutePolicyDefaultKeepsExistingProtectAllBehavior(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.RouteEnabled = true
	decision := DecideRoute(cfg, "gpt-5.5", RouteIdentity{}, 512)
	if !decision.Protected || decision.Mode != RoutePolicyModeProtectAll || decision.Reason != "protect_all" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestRoutePolicyOffAlignsWithRouteDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.RouteEnabled = true
	cfg.RoutePolicy.Mode = RoutePolicyModeOff
	decision := DecideRoute(cfg, "gpt-5.5", RouteIdentity{}, 512)
	if decision.Protected || decision.Reason != "policy_off" {
		t.Fatalf("decision = %#v", decision)
	}

	cfg.RoutePolicy.Mode = ""
	cfg.RouteEnabled = false
	decision = DecideRoute(cfg, "gpt-5.5", RouteIdentity{}, 512)
	if decision.Protected || decision.Reason != "route_enabled_false" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestRoutePolicyProtectSelectedAndBypassSelectors(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.RouteEnabled = true
	cfg.RoutePolicy.Mode = RoutePolicyModeProtectSelected
	cfg.RoutePolicy.ProtectedModels = []string{"gpt-5.5"}
	cfg.RoutePolicy.BypassKeyAliases = []string{"baseline-cpa-only"}

	decision := DecideRoute(cfg, "gpt-5.5", RouteIdentity{Alias: "baseline-cpa-only"}, 512)
	if decision.Protected || decision.Reason != "key_bypass_selected" {
		t.Fatalf("bypass decision = %#v", decision)
	}

	decision = DecideRoute(cfg, "gpt-5.5", RouteIdentity{Alias: "regular"}, 512)
	if !decision.Protected || decision.Reason != "model_protect_selected" {
		t.Fatalf("protected decision = %#v", decision)
	}

	decision = DecideRoute(cfg, "gpt-5.4", RouteIdentity{Alias: "regular"}, 512)
	if decision.Protected || decision.Reason != "not_selected" {
		t.Fatalf("unselected decision = %#v", decision)
	}
}

func TestRoutePolicyProtectSelectedModelFamiliesAndWildcards(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.RouteEnabled = true
	cfg.RoutePolicy.Mode = RoutePolicyModeProtectSelected
	cfg.RoutePolicy.ProtectedModels = []string{"gpt-5.5", "gpt-5.6*"}

	cases := []struct {
		model     string
		protected bool
		reason    string
	}{
		{model: "gpt-5.5", protected: true, reason: "model_protect_selected"},
		{model: "gpt-5.5-pro", protected: true, reason: "model_protect_selected"},
		{model: "gpt-5.5-2026-04-23", protected: true, reason: "model_protect_selected"},
		{model: "gpt-5.6", protected: true, reason: "model_protect_selected"},
		{model: "gpt-5.6-sol", protected: true, reason: "model_protect_selected"},
		{model: "gpt-5.6-terra", protected: true, reason: "model_protect_selected"},
		{model: "gpt-5.6-luna", protected: true, reason: "model_protect_selected"},
		{model: "GPT-5.6-Sol", protected: true, reason: "model_protect_selected"},
		{model: "gpt-5.4", protected: false, reason: "not_selected"},
		{model: "gpt-5.4-mini", protected: false, reason: "not_selected"},
		{model: "grok-4.5", protected: false, reason: "not_selected"},
		{model: "gpt-5.3-codex-spark", protected: false, reason: "not_selected"},
	}
	for _, tc := range cases {
		decision := DecideRoute(cfg, tc.model, RouteIdentity{Alias: "regular"}, 512)
		if decision.Protected != tc.protected || decision.Reason != tc.reason {
			t.Fatalf("model %q decision = %#v want protected=%v reason=%q", tc.model, decision, tc.protected, tc.reason)
		}
	}
}

func TestRoutePolicyModelBypassUsesFamilyMatching(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.RouteEnabled = true
	cfg.RoutePolicy.Mode = RoutePolicyModeProtectAll
	cfg.RoutePolicy.BypassModels = []string{"gpt-5.4"}

	decision := DecideRoute(cfg, "gpt-5.4-mini", RouteIdentity{}, 512)
	if decision.Protected || decision.Reason != "model_bypass_selected" {
		t.Fatalf("family bypass decision = %#v", decision)
	}

	decision = DecideRoute(cfg, "gpt-5.5", RouteIdentity{}, 512)
	if !decision.Protected || decision.Reason != "protect_all" {
		t.Fatalf("non-bypassed decision = %#v", decision)
	}
}

func TestRoutePolicyKeyAliasDoesNotUseModelFamilyPrefix(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.RouteEnabled = true
	cfg.RoutePolicy.Mode = RoutePolicyModeProtectSelected
	cfg.RoutePolicy.ProtectedModels = []string{"gpt-5.5"}
	cfg.RoutePolicy.BypassKeyAliases = []string{"base"}

	// Key matching is exact/wildcard only: "base" must not match "baseline".
	decision := DecideRoute(cfg, "gpt-5.5", RouteIdentity{Alias: "baseline"}, 512)
	if !decision.Protected || decision.Reason != "model_protect_selected" {
		t.Fatalf("key family overmatch decision = %#v", decision)
	}

	cfg.RoutePolicy.BypassKeyAliases = []string{"base*"}
	decision = DecideRoute(cfg, "gpt-5.5", RouteIdentity{Alias: "baseline"}, 512)
	if decision.Protected || decision.Reason != "key_bypass_selected" {
		t.Fatalf("key wildcard decision = %#v", decision)
	}
}

func TestRoutePolicyBypassLargeContextOrLimitBudget(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.RouteEnabled = true
	cfg.MaxContinue = 8
	cfg.RoutePolicy.MaxRequestBodyBytes = 1024

	decision := DecideRoute(cfg, "gpt-5.5", RouteIdentity{}, 2048)
	if decision.Protected || decision.Reason != "body_too_large" {
		t.Fatalf("large context bypass decision = %#v", decision)
	}

	cfg.RoutePolicy.LargeContextMaxContinue = 2
	decision = DecideRoute(cfg, "gpt-5.5", RouteIdentity{}, 2048)
	if !decision.Protected || decision.Reason != "large_context_budget_limited" || decision.MaxContinue != 2 {
		t.Fatalf("large context budget decision = %#v", decision)
	}
	if got := decision.EffectiveConfig(cfg).MaxContinue; got != 2 {
		t.Fatalf("effective MaxContinue = %d", got)
	}
}
