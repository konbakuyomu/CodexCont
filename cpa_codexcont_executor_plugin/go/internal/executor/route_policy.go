package executor

import "strings"

const (
	RoutePolicyModeProtectAll      = "protect_all"
	RoutePolicyModeProtectSelected = "protect_selected"
	RoutePolicyModeOff             = "off"
)

type RoutePolicy struct {
	Mode                    string   `yaml:"mode"`
	ProtectedModels         []string `yaml:"protected_models"`
	BypassModels            []string `yaml:"bypass_models"`
	ProtectedKeyAliases     []string `yaml:"protected_key_aliases"`
	BypassKeyAliases        []string `yaml:"bypass_key_aliases"`
	MaxRequestBodyBytes     int64    `yaml:"max_request_body_bytes"`
	LargeContextMaxContinue int      `yaml:"large_context_max_continue"`
}

type RouteIdentity struct {
	ID      string
	Alias   string
	Name    string
	Preview string
	Source  string
}

type RouteDecision struct {
	Protected            bool
	Mode                 string
	Reason               string
	Model                string
	KeyScope             string
	BodyBytes            int64
	MaxContinue          int
	MaxTotalOutputTokens int
	FailMode             string
}

func (p RoutePolicy) Normalize() RoutePolicy {
	p.Mode = strings.ToLower(strings.TrimSpace(p.Mode))
	switch p.Mode {
	case "", RoutePolicyModeProtectAll, RoutePolicyModeProtectSelected, RoutePolicyModeOff:
	default:
		p.Mode = RoutePolicyModeProtectAll
	}
	p.ProtectedModels = cleanStringList(p.ProtectedModels)
	p.BypassModels = cleanStringList(p.BypassModels)
	p.ProtectedKeyAliases = cleanStringList(p.ProtectedKeyAliases)
	p.BypassKeyAliases = cleanStringList(p.BypassKeyAliases)
	if p.MaxRequestBodyBytes < 0 {
		p.MaxRequestBodyBytes = 0
	}
	if p.LargeContextMaxContinue < 0 {
		p.LargeContextMaxContinue = 0
	}
	return p
}

func DecideRoute(cfg Config, model string, identity RouteIdentity, bodyBytes int64) RouteDecision {
	cfg = cfg.Normalize()
	model = strings.TrimSpace(model)
	if bodyBytes < 0 {
		bodyBytes = 0
	}
	policy := cfg.RoutePolicy.Normalize()
	mode := policy.Mode
	if mode == "" {
		mode = RoutePolicyModeProtectAll
	}
	decision := RouteDecision{
		Protected:            true,
		Mode:                 mode,
		Reason:               "protect_all",
		Model:                model,
		KeyScope:             identity.Scope(),
		BodyBytes:            bodyBytes,
		MaxContinue:          cfg.MaxContinue,
		MaxTotalOutputTokens: cfg.MaxTotalOutputTokens,
		FailMode:             cfg.FailMode,
	}
	switch {
	case !cfg.Enabled:
		return decision.bypass("executor_disabled")
	case !cfg.RouteEnabled:
		return decision.bypass("route_enabled_false")
	case mode == RoutePolicyModeOff:
		return decision.bypass("policy_off")
	}
	if matchesAnyModel(model, policy.BypassModels) {
		return decision.bypass("model_bypass_selected")
	}
	if identity.MatchesAny(policy.BypassKeyAliases) {
		return decision.bypass("key_bypass_selected")
	}
	if policy.MaxRequestBodyBytes > 0 && bodyBytes > policy.MaxRequestBodyBytes {
		if policy.LargeContextMaxContinue > 0 {
			decision.Reason = "large_context_budget_limited"
			decision.MaxContinue = policy.LargeContextMaxContinue
			return decision
		}
		return decision.bypass("body_too_large")
	}
	if mode == RoutePolicyModeProtectSelected {
		if matchesAnyModel(model, policy.ProtectedModels) {
			decision.Reason = "model_protect_selected"
			return decision
		}
		if identity.MatchesAny(policy.ProtectedKeyAliases) {
			decision.Reason = "key_protect_selected"
			return decision
		}
		return decision.bypass("not_selected")
	}
	return decision
}

func (d RouteDecision) EffectiveConfig(cfg Config) Config {
	cfg = cfg.Normalize()
	if d.MaxContinue > 0 {
		cfg.MaxContinue = d.MaxContinue
	}
	if d.MaxTotalOutputTokens > 0 {
		cfg.MaxTotalOutputTokens = d.MaxTotalOutputTokens
	}
	return cfg
}

func (d RouteDecision) SafeSummary() map[string]any {
	out := map[string]any{
		"protected":               d.Protected,
		"mode":                    strings.TrimSpace(d.Mode),
		"reason":                  strings.TrimSpace(d.Reason),
		"body_bytes":              d.BodyBytes,
		"max_continue":            d.MaxContinue,
		"max_total_output_tokens": d.MaxTotalOutputTokens,
		"fail_mode":               strings.TrimSpace(d.FailMode),
	}
	if model := strings.TrimSpace(d.Model); model != "" {
		out["model"] = model
	}
	if scope := strings.TrimSpace(d.KeyScope); scope != "" {
		out["key_scope"] = scope
	}
	return out
}

func (d RouteDecision) bypass(reason string) RouteDecision {
	d.Protected = false
	d.Reason = reason
	return d
}

func (i RouteIdentity) Scope() string {
	for _, value := range []string{i.Alias, i.Name, i.ID, i.Preview, i.Source} {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (i RouteIdentity) MatchesAny(values []string) bool {
	scopes := []string{i.ID, i.Alias, i.Name, i.Preview, i.Source}
	for _, scope := range scopes {
		if matchesAny(scope, values) {
			return true
		}
	}
	return false
}

func cleanStringList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func matchesAny(value string, candidates []string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, candidate := range candidates {
		if matchSelector(value, candidate, false) {
			return true
		}
	}
	return false
}

// matchesAnyModel matches model selectors with exact, trailing-wildcard, and
// family-prefix rules. Family prefix means "gpt-5.5" also matches
// "gpt-5.5-pro" / "gpt-5.5-2026-04-23", and "gpt-5.6" matches "gpt-5.6-sol".
// Key alias matching stays on matchesAny (exact + optional trailing "*") so a
// short key alias does not accidentally swallow longer aliases.
func matchesAnyModel(value string, candidates []string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, candidate := range candidates {
		if matchSelector(value, candidate, true) {
			return true
		}
	}
	return false
}

func matchSelector(value, candidate string, allowFamilyPrefix bool) bool {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return false
	}
	if strings.HasSuffix(candidate, "*") {
		prefix := strings.TrimSpace(strings.TrimSuffix(candidate, "*"))
		if prefix == "" {
			return true
		}
		return strings.HasPrefix(strings.ToLower(value), strings.ToLower(prefix))
	}
	if strings.EqualFold(value, candidate) {
		return true
	}
	if !allowFamilyPrefix {
		return false
	}
	// Family prefix: candidate is a full model family token and value continues
	// with '-' (gpt-5.5 -> gpt-5.5-pro). A bare "gpt-5" must not match "gpt-5.5".
	if len(value) <= len(candidate) {
		return false
	}
	if !strings.EqualFold(value[:len(candidate)], candidate) {
		return false
	}
	return value[len(candidate)] == '-'
}
