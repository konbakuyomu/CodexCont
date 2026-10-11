package policyplus

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

const perMillion = 1_000_000.0

const (
	CostSourceKeyPolicyPlusPriceBook = "key_policy_plus_price_book"
	CostSourceCPAMPPriceBook         = "cpamp_price_book"
	CostSourceCPAMPCachedPriceBook   = "cpamp_cached_price_book"
	CostSourceCPAMPPriceUnavailable  = "cpamp_price_unavailable"
	CostSourceCPAMPBillingPending    = "cpamp_billing_pending"
)

type TokenUsage struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
}

type CostBreakdown struct {
	Source                 string             `json:"source"`
	Model                  string             `json:"model"`
	ServiceTier            string             `json:"service_tier,omitempty"`
	ServiceTierMultiplier  float64            `json:"service_tier_multiplier,omitempty"`
	ContextThresholdTokens int64              `json:"context_threshold_tokens,omitempty"`
	ServiceTierRule        string             `json:"service_tier_rule,omitempty"`
	PriceMissing           bool               `json:"price_missing,omitempty"`
	BillingPending         bool               `json:"billing_pending,omitempty"`
	BillingPendingReason   string             `json:"billing_pending_reason,omitempty"`
	BillingPriceModel      string             `json:"billing_price_model,omitempty"`
	Prices                 ModelPrice         `json:"prices"`
	Tokens                 map[string]int64   `json:"tokens"`
	Costs                  map[string]float64 `json:"costs"`
}

type PriceBook struct {
	Source      string                `json:"source"`
	Path        string                `json:"path,omitempty"`
	LoadedAt    time.Time             `json:"loaded_at,omitempty"`
	UpdatedAtMS int64                 `json:"updated_at_ms,omitempty"`
	SyncedAtMS  int64                 `json:"synced_at_ms,omitempty"`
	Prices      map[string]ModelPrice `json:"prices"`
	Warnings    []string              `json:"warnings,omitempty"`
}

func (b PriceBook) NormalizedSource() string {
	source := strings.TrimSpace(b.Source)
	if source == "" {
		return CostSourceCPAMPPriceUnavailable
	}
	return source
}

func (b PriceBook) PriceFor(model string) (ModelPrice, bool) {
	return PriceForModel(b.Prices, model)
}

func (b PriceBook) PricedModelIDs() []string {
	ids := make([]string, 0, len(b.Prices))
	for name, price := range b.Prices {
		model := strings.TrimSpace(price.Model)
		if model == "" {
			model = strings.TrimSpace(name)
		}
		if model == "" || !PriceAllowsAdmission(price, "") {
			continue
		}
		ids = append(ids, model)
	}
	return cleanCaseInsensitive(ids)
}

func (b PriceBook) Fingerprint() string {
	type row struct {
		Model string     `json:"model"`
		Price ModelPrice `json:"price"`
	}
	rows := make([]row, 0, len(b.Prices))
	for name, price := range b.Prices {
		model := strings.TrimSpace(price.Model)
		if model == "" {
			model = strings.TrimSpace(name)
		}
		if model == "" {
			continue
		}
		price.Model = model
		rows = append(rows, row{Model: strings.ToLower(model), Price: price})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Model < rows[j].Model })
	raw, _ := json.Marshal(struct {
		UpdatedAtMS int64 `json:"updated_at_ms"`
		SyncedAtMS  int64 `json:"synced_at_ms"`
		Rows        []row `json:"rows"`
	}{UpdatedAtMS: b.UpdatedAtMS, SyncedAtMS: b.SyncedAtMS, Rows: rows})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func PriceForModel(prices map[string]ModelPrice, model string) (ModelPrice, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return ModelPrice{}, false
	}
	if price, ok := prices[model]; ok {
		return price, true
	}
	lower := strings.ToLower(model)
	for name, price := range prices {
		if strings.ToLower(name) == lower {
			return price, true
		}
	}
	return ModelPrice{}, false
}

func CostForUsage(price ModelPrice, usage TokenUsage, model string) CostBreakdown {
	return costForUsage(price, usage, model, "", CostSourceKeyPolicyPlusPriceBook)
}

func CostForUsageFromPriceBook(book PriceBook, usage TokenUsage, model, serviceTier string) (CostBreakdown, bool) {
	source := book.NormalizedSource()
	price, ok := book.PriceFor(model)
	if !ok {
		breakdown := costForUsage(ModelPrice{Model: model}, usage, model, serviceTier, source)
		breakdown.PriceMissing = true
		return breakdown, false
	}
	effective, _, _ := effectiveModelPrice(price, max64(usage.InputTokens, 0), serviceTier)
	if !priceAllowsRecordedUsage(price, max64(usage.InputTokens, 0), serviceTier) || !hasReliableTokenPrice(effective) {
		breakdown := costForUsage(ModelPrice{Model: model}, usage, model, serviceTier, source)
		breakdown.PriceMissing = true
		return breakdown, false
	}
	return costForUsage(price, usage, model, serviceTier, source), true
}

func costForUsage(price ModelPrice, usage TokenUsage, model, serviceTier, source string) CostBreakdown {
	input := max64(usage.InputTokens, 0)
	output := max64(usage.OutputTokens, 0)
	cached := max64(usage.CachedTokens, 0)
	cacheRead := max64(usage.CacheReadTokens, 0)
	cacheCreation := max64(usage.CacheCreationTokens, 0)
	reasoning := max64(usage.ReasoningTokens, 0)
	price, contextThreshold, serviceTierRule := effectiveModelPrice(price, input, serviceTier)
	billableInput := max64(input-cached-cacheRead-cacheCreation, 0)
	cachePrice := price.CachePerMillion
	cacheReadPrice := price.CacheReadPerMillion
	if !price.CacheReadConfigured && cacheReadPrice <= 0 {
		cacheReadPrice = fallbackPrice(cachePrice, price.InputPerMillion*0.1)
	}
	cacheCreationPrice := price.CacheCreationPerMillion
	if !price.CacheCreationConfigured && cacheCreationPrice <= 0 {
		cacheCreationPrice = price.InputPerMillion
	}
	inputCost := float64(billableInput) * price.InputPerMillion / perMillion
	cachedCost := float64(cached) * cachePrice / perMillion
	cacheReadCost := float64(cacheRead) * cacheReadPrice / perMillion
	cacheCreationCost := float64(cacheCreation) * cacheCreationPrice / perMillion
	outputCost := float64(output) * price.OutputPerMillion / perMillion
	total := inputCost + cachedCost + cacheReadCost + cacheCreationCost + outputCost
	totalTokens := usage.TotalTokens
	if totalTokens <= 0 {
		totalTokens = input + output
	}
	if strings.TrimSpace(model) == "" {
		model = price.Model
	}
	return CostBreakdown{
		Source:                 source,
		Model:                  model,
		ServiceTier:            strings.TrimSpace(serviceTier),
		ServiceTierMultiplier:  1,
		ContextThresholdTokens: contextThreshold,
		ServiceTierRule:        serviceTierRule,
		Prices:                 price,
		Tokens: map[string]int64{
			"input":                   input,
			"billable_uncached_input": billableInput,
			"cached_input":            cached,
			"cache_read":              cacheRead,
			"cache_creation":          cacheCreation,
			"output":                  output,
			"reasoning":               reasoning,
			"visible_output_estimate": max64(output-reasoning, 0),
			"total":                   totalTokens,
		},
		Costs: map[string]float64{
			"input":          inputCost,
			"cached_input":   cachedCost,
			"cache_read":     cacheReadCost,
			"cache_creation": cacheCreationCost,
			"output":         outputCost,
			"total":          total,
		},
	}
}

func HasBillablePrice(price ModelPrice) bool {
	if hasBaseBillablePrice(price) {
		return true
	}
	for _, tier := range price.ContextTiers {
		if hasBaseBillablePrice(ModelPrice{
			InputPerMillion:         tier.InputPerMillion,
			OutputPerMillion:        tier.OutputPerMillion,
			CachePerMillion:         tier.CachePerMillion,
			CacheReadPerMillion:     tier.CacheReadPerMillion,
			CacheCreationPerMillion: tier.CacheCreationPerMillion,
		}) {
			return true
		}
	}
	for _, tier := range price.ServiceTiers {
		if hasBaseBillablePrice(ModelPrice{
			InputPerMillion:         tier.InputPerMillion,
			OutputPerMillion:        tier.OutputPerMillion,
			CachePerMillion:         tier.CachePerMillion,
			CacheReadPerMillion:     tier.CacheReadPerMillion,
			CacheCreationPerMillion: tier.CacheCreationPerMillion,
		}) {
			return true
		}
	}
	return false
}

// PriceAllowsAdmission answers the pre-request question rather than the
// post-request accounting question. Context length is not final at admission
// time, so every configured context override must still yield a billable
// price. A service-tier-only price is safe only when the caller has named its
// matching tier; otherwise the base price must be billable.
func PriceAllowsAdmission(price ModelPrice, serviceTier string) bool {
	for _, tier := range price.ContextTiers {
		if !hasReliableTokenPrice(applyContextTier(price, tier)) {
			return false
		}
	}
	effective, _, serviceTierRule := effectiveModelPrice(price, 0, serviceTier)
	if isNonDefaultServiceTier(serviceTier) {
		if serviceTierRule == "" || !hasReliableTokenPrice(effective) {
			return false
		}
		tier, _ := serviceTierFor(price, serviceTier)
		for _, contextTier := range tier.ContextTiers {
			if !hasReliableTokenPrice(applyContextTier(effective, contextTier)) {
				return false
			}
		}
		return true
	}
	return hasReliableTokenPrice(price)
}

// EstimatedPriorityMultiplier prices priority (Codex "fast") requests on a
// model CPAMP lists no priority rule for: providers charge about twice the
// standard rate for it.
const EstimatedPriorityMultiplier = 2.0

// EstimatedPriorityCost prices a priority request on a model without a
// priority rule at the standard price times EstimatedPriorityMultiplier. It
// is for accounting that would otherwise record the request as free (pool
// shares); keys with USD windows stay strict and wait for a real price.
func EstimatedPriorityCost(book PriceBook, usage TokenUsage, model, serviceTier string) (CostBreakdown, bool) {
	price, ok := book.PriceFor(model)
	if !ok {
		return CostBreakdown{}, false
	}
	estimated, ok := estimatedPriorityPrice(price, serviceTier)
	if !ok || !priceAllowsRecordedUsage(estimated, max64(usage.InputTokens, 0), "") {
		return CostBreakdown{}, false
	}
	breakdown := costForUsage(estimated, usage, model, "", book.NormalizedSource())
	breakdown.ServiceTier = strings.TrimSpace(serviceTier)
	breakdown.ServiceTierMultiplier = EstimatedPriorityMultiplier
	breakdown.ServiceTierRule = "estimated"
	return breakdown, true
}

// PriorityTier reports a tier that buys faster processing at a premium.
func PriorityTier(serviceTier string) bool {
	switch strings.ToLower(strings.TrimSpace(serviceTier)) {
	case "priority", "fast":
		return true
	}
	return false
}

// estimatedPriorityPrice is the standard price scaled to the priority premium,
// for priority requests on a model without its own priority rule.
func estimatedPriorityPrice(price ModelPrice, serviceTier string) (ModelPrice, bool) {
	if !PriorityTier(serviceTier) {
		return ModelPrice{}, false
	}
	if _, ok := serviceTierFor(price, serviceTier); ok {
		return ModelPrice{}, false
	}
	m := EstimatedPriorityMultiplier
	scaled := price
	scaled.ServiceTiers = nil
	scaled.InputPerMillion *= m
	scaled.OutputPerMillion *= m
	scaled.CachePerMillion *= m
	scaled.CacheReadPerMillion *= m
	scaled.CacheCreationPerMillion *= m
	scaled.ContextTiers = make([]ModelPriceContextTier, len(price.ContextTiers))
	for i, tier := range price.ContextTiers {
		tier.InputPerMillion *= m
		tier.OutputPerMillion *= m
		tier.CachePerMillion *= m
		tier.CacheReadPerMillion *= m
		tier.CacheCreationPerMillion *= m
		scaled.ContextTiers[i] = tier
	}
	return scaled, true
}

func priceAllowsRecordedUsage(price ModelPrice, inputTokens int64, serviceTier string) bool {
	if isNonDefaultServiceTier(serviceTier) {
		// A premium tier the model has no rule for is unpriced at any context
		// length; the standard long-context price would undercharge it.
		if _, ok := serviceTierFor(price, serviceTier); !ok {
			return false
		}
	}
	effective, contextThreshold, serviceTierRule := effectiveModelPrice(price, inputTokens, serviceTier)
	if contextThreshold > 0 {
		return hasReliableTokenPrice(effective)
	}
	if isNonDefaultServiceTier(serviceTier) {
		return serviceTierRule != "" && hasReliableTokenPrice(effective)
	}
	return hasReliableTokenPrice(effective)
}

func isNonDefaultServiceTier(serviceTier string) bool {
	switch strings.ToLower(strings.TrimSpace(serviceTier)) {
	case "", "auto", "default", "standard":
		return false
	default:
		return true
	}
}

func hasBaseBillablePrice(price ModelPrice) bool {
	return price.InputPerMillion > 0 || price.OutputPerMillion > 0 || price.CachePerMillion > 0 ||
		price.CacheReadPerMillion > 0 || price.CacheCreationPerMillion > 0
}

func hasReliableTokenPrice(price ModelPrice) bool {
	inputKnown := price.InputConfigured || price.InputPerMillion > 0
	outputKnown := price.OutputConfigured || price.OutputPerMillion > 0
	return inputKnown && outputKnown && hasBaseBillablePrice(price)
}

// effectiveModelPrice picks the price a request pays: its service tier's
// rule when the model has one, with that tier's own long-context prices past
// their threshold (a tier without its own yields to the model's long-context
// price); otherwise the model's long-context or base price.
func effectiveModelPrice(price ModelPrice, inputTokens int64, serviceTier string) (ModelPrice, int64, string) {
	if tier, ok := serviceTierFor(price, serviceTier); ok {
		rule := strings.TrimSpace(tier.Mode) + "/" + strings.TrimSpace(tier.ServiceTier)
		if len(tier.ContextTiers) == 0 {
			if contextTier := contextTierFor(price.ContextTiers, inputTokens); contextTier != nil {
				return applyContextTier(price, *contextTier), contextTier.ThresholdTokens, ""
			}
			return applyServiceTier(price, tier), 0, rule
		}
		effective := applyServiceTier(price, tier)
		if contextTier := contextTierFor(tier.ContextTiers, inputTokens); contextTier != nil {
			return applyContextTier(effective, *contextTier), contextTier.ThresholdTokens, rule
		}
		return effective, 0, rule
	}
	if contextTier := contextTierFor(price.ContextTiers, inputTokens); contextTier != nil {
		return applyContextTier(price, *contextTier), contextTier.ThresholdTokens, ""
	}
	return price, 0, ""
}

// serviceTierFor is the model's rule for the requested service tier.
func serviceTierFor(price ModelPrice, serviceTier string) (ModelPriceServiceTier, bool) {
	name := strings.ToLower(strings.TrimSpace(serviceTier))
	if name == "" {
		return ModelPriceServiceTier{}, false
	}
	for _, tier := range price.ServiceTiers {
		if name == strings.ToLower(strings.TrimSpace(tier.Mode)) || name == strings.ToLower(strings.TrimSpace(tier.ServiceTier)) {
			return tier, true
		}
	}
	return ModelPriceServiceTier{}, false
}

// contextTierFor is the highest tier whose threshold the input crosses.
func contextTierFor(tiers []ModelPriceContextTier, inputTokens int64) *ModelPriceContextTier {
	var picked *ModelPriceContextTier
	for index := range tiers {
		tier := &tiers[index]
		if tier.ThresholdTokens > 0 && inputTokens > tier.ThresholdTokens && (picked == nil || tier.ThresholdTokens > picked.ThresholdTokens) {
			picked = tier
		}
	}
	return picked
}

func applyContextTier(price ModelPrice, tier ModelPriceContextTier) ModelPrice {
	effective := price
	effective.ContextTiers = nil
	effective.ServiceTiers = nil
	if tier.InputConfigured {
		effective.InputPerMillion = tier.InputPerMillion
		effective.InputConfigured = true
	}
	if tier.OutputConfigured {
		effective.OutputPerMillion = tier.OutputPerMillion
		effective.OutputConfigured = true
	}
	if tier.CacheConfigured {
		effective.CachePerMillion = tier.CachePerMillion
	}
	if tier.CacheReadConfigured {
		effective.CacheReadPerMillion = tier.CacheReadPerMillion
		effective.CacheReadConfigured = true
	}
	if tier.CacheCreationConfigured {
		effective.CacheCreationPerMillion = tier.CacheCreationPerMillion
		effective.CacheCreationConfigured = true
	}
	return effective
}

func applyServiceTier(price ModelPrice, tier ModelPriceServiceTier) ModelPrice {
	effective := price
	effective.ContextTiers = nil
	effective.ServiceTiers = nil
	if tier.InputConfigured {
		effective.InputPerMillion = tier.InputPerMillion
		effective.InputConfigured = true
	}
	if tier.OutputConfigured {
		effective.OutputPerMillion = tier.OutputPerMillion
		effective.OutputConfigured = true
	}
	if tier.CacheConfigured {
		effective.CachePerMillion = tier.CachePerMillion
	}
	if tier.CacheReadConfigured {
		effective.CacheReadPerMillion = tier.CacheReadPerMillion
		effective.CacheReadConfigured = true
	}
	if tier.CacheCreationConfigured {
		effective.CacheCreationPerMillion = tier.CacheCreationPerMillion
		effective.CacheCreationConfigured = true
	}
	return effective
}

func fallbackPrice(value float64, fallback float64) float64 {
	if value > 0 {
		return value
	}
	return fallback
}

func cleanCaseInsensitive(items []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		key := strings.ToLower(item)
		if item == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
