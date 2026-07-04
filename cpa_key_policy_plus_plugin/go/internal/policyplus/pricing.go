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
	Source                string             `json:"source"`
	Model                 string             `json:"model"`
	ServiceTier           string             `json:"service_tier,omitempty"`
	ServiceTierMultiplier float64            `json:"service_tier_multiplier,omitempty"`
	PriceMissing          bool               `json:"price_missing,omitempty"`
	Prices                ModelPrice         `json:"prices"`
	Tokens                map[string]int64   `json:"tokens"`
	Costs                 map[string]float64 `json:"costs"`
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
		if model == "" || !HasBillablePrice(price) {
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
	if !ok || !HasBillablePrice(price) {
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
	billableInput := max64(input-cached, 0)
	cachePrice := price.CachePerMillion
	cacheReadPrice := fallbackPrice(price.CacheReadPerMillion, cachePrice)
	cacheCreationPrice := price.CacheCreationPerMillion
	if cacheCreationPrice <= 0 {
		cacheCreationPrice = price.InputPerMillion
	}
	inputCost := float64(billableInput) * price.InputPerMillion / perMillion
	cachedCost := float64(cached) * cachePrice / perMillion
	cacheReadCost := float64(cacheRead) * cacheReadPrice / perMillion
	cacheCreationCost := float64(cacheCreation) * cacheCreationPrice / perMillion
	outputCost := float64(output) * price.OutputPerMillion / perMillion
	multiplier := ServiceTierMultiplier(model, serviceTier)
	inputCost *= multiplier
	cachedCost *= multiplier
	cacheReadCost *= multiplier
	cacheCreationCost *= multiplier
	outputCost *= multiplier
	total := inputCost + cachedCost + cacheReadCost + cacheCreationCost + outputCost
	totalTokens := usage.TotalTokens
	if totalTokens <= 0 {
		totalTokens = input + output
	}
	if strings.TrimSpace(model) == "" {
		model = price.Model
	}
	return CostBreakdown{
		Source:                source,
		Model:                 model,
		ServiceTier:           strings.TrimSpace(serviceTier),
		ServiceTierMultiplier: multiplier,
		Prices:                price,
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
	return price.InputPerMillion > 0 || price.OutputPerMillion > 0 || price.CachePerMillion > 0 ||
		price.CacheReadPerMillion > 0 || price.CacheCreationPerMillion > 0
}

func ServiceTierMultiplier(modelName string, serviceTier string) float64 {
	tier := strings.ToLower(strings.TrimSpace(serviceTier))
	if tier != "priority" && tier != "fast" {
		return 1
	}
	modelName = strings.ToLower(strings.TrimSpace(modelName))
	switch {
	case isModelFamily(modelName, "gpt-5.5"):
		return 2.5
	case isModelFamily(modelName, "gpt-5.4-mini"):
		return 2
	case isModelFamily(modelName, "gpt-5.4"):
		return 2
	case isModelFamily(modelName, "gpt-5.3-codex"):
		return 2
	default:
		return 1
	}
}

func isModelFamily(modelName string, family string) bool {
	return modelName == family || strings.HasPrefix(modelName, family+"-")
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
