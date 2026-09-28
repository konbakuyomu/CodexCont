package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"codexcont/cpa-key-policy-plus-plugin/internal/policyplus"
)

// Funding sources: who pays for a key's use of a model. See
// internal/policyplus/funding.go for the rule.
const (
	fundingSeat      = "seat"
	fundingWindows   = "windows"
	fundingUnlimited = "unlimited"
	fundingNone      = "none"
)

type fundingSource struct {
	Kind     string   `json:"kind"`
	Provider string   `json:"provider"`
	Label    string   `json:"label"`
	Pool     string   `json:"pool,omitempty"`
	PoolID   string   `json:"pool_id,omitempty"`
	Role     string   `json:"role,omitempty"`
	Share    *float64 `json:"share_percent,omitempty"`
	Budgeted bool     `json:"budgeted,omitempty"`
}

// providerForOwner maps a model's owned_by to the CPA provider that serves it.
func providerForOwner(owner string) string {
	owner = strings.ToLower(strings.TrimSpace(owner))
	switch owner {
	case "", "openai", "codex":
		return "codex"
	case "xai", "x-ai", "grok":
		return "xai"
	}
	return owner
}

func providerLabel(provider string) string {
	switch provider {
	case "codex":
		return "GPT"
	case "xai":
		return "Grok"
	case "":
		return "其他模型"
	}
	return provider
}

// modelProvider classifies a model by the provider that serves it: the CPA
// model list's owner when listed, else by name. "" means unknown.
func modelProvider(model string) string {
	base, _ := splitModelSuffix(model)
	lower := strings.ToLower(strings.TrimSpace(base))
	if lower == "" {
		return ""
	}
	if i := strings.Index(lower, "/"); i > 0 {
		for _, pool := range loadedPools(context.Background()) {
			if pool.ModelPrefix != "" && strings.EqualFold(pool.ModelPrefix, lower[:i]) {
				return pool.ProviderName()
			}
		}
	}
	if catalog, _ := modelCatalog(); catalog != nil {
		if owner, known := catalog[lower]; known {
			return providerForOwner(owner)
		}
	}
	if i := strings.Index(lower, "/"); i > 0 {
		lower = lower[i+1:]
	}
	switch {
	case codexLikeModel(lower):
		return "codex"
	case strings.HasPrefix(lower, "grok"):
		return "xai"
	}
	return ""
}

func seatSource(pool policyplus.Pool, member policyplus.PoolMember, provider string) fundingSource {
	src := fundingSource{Kind: fundingSeat, Provider: provider, Label: providerLabel(provider), Pool: pool.Name, PoolID: pool.ID, Role: "owner", Budgeted: pool.Budgeted()}
	if member.Shared() {
		src.Role = "member"
		share := *member.SharePercent
		src.Share = &share
	}
	return src
}

// fundsBySeat reports whether a pool seat pays for its pool's models: a share
// or the owner, on a pool bound to an account. Pinned members pay with their
// own USD windows.
func fundsBySeat(pool policyplus.Pool, member policyplus.PoolMember) bool {
	return (member.Shared() || member.Owner()) && strings.TrimSpace(pool.AuthIndex) != ""
}

// fundingFor finds who pays for key's request of model.
func fundingFor(ctx context.Context, key policyplus.KeyRecord, model string) fundingSource {
	provider := modelProvider(model)
	if pool, member, ok := poolMembershipFor(ctx, key.ID, model); ok && fundsBySeat(pool, member) {
		return seatSource(pool, member, pool.ProviderName())
	}
	return fundingFallback(key, provider)
}

// fundingForProvider is fundingFor for a whole provider, for key views.
func fundingForProvider(ctx context.Context, key policyplus.KeyRecord, provider string) fundingSource {
	for _, seat := range poolSeats(ctx, key.ID) {
		if seat.Pool.ProviderName() == provider && fundsBySeat(seat.Pool, seat.Member) {
			return seatSource(seat.Pool, seat.Member, provider)
		}
	}
	return fundingFallback(key, provider)
}

func fundingFallback(key policyplus.KeyRecord, provider string) fundingSource {
	src := fundingSource{Kind: fundingNone, Provider: provider, Label: providerLabel(provider)}
	switch {
	case hasCostQuota(key):
		src.Kind = fundingWindows
	case key.QuotaUnlimited:
		src.Kind = fundingUnlimited
	}
	return src
}

// knownProviders are the providers CPA has accounts for, plus those of
// pools, codex first.
func knownProviders(ctx context.Context) []string {
	seen := map[string]bool{}
	var out []string
	add := func(provider string) {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider == "" || seen[provider] {
			return
		}
		seen[provider] = true
		out = append(out, provider)
	}
	for _, host := range hostAuthAccounts() {
		add(firstNonEmpty(host.Provider, "codex"))
	}
	for _, pool := range loadedPools(ctx) {
		add(pool.ProviderName())
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i] == "codex" && out[j] != "codex" })
	return out
}

// keyFunding lists, per provider the key may use, who pays for it. A model
// whitelist narrows the list to providers it names.
func keyFunding(ctx context.Context, key policyplus.KeyRecord, providers []string) []fundingSource {
	allowed := map[string]bool{}
	all := len(key.Models) == 0
	for _, model := range key.Models {
		provider := modelProvider(model)
		if provider == "" || strings.Contains(model, "*") {
			all = true
		}
		allowed[provider] = true
	}
	out := []fundingSource{}
	for _, provider := range providers {
		if all || allowed[provider] {
			out = append(out, fundingForProvider(ctx, key, provider))
		}
	}
	return out
}

func fundingMissText(key policyplus.KeyRecord, model string, src fundingSource) string {
	return fmt.Sprintf("CPA Key Policy+ 已拦截：%s 使用 %s 没有额度来源（没有加入%s拼车池，也没有设置费用上限）。请联系管理员。",
		safeKeyDisplayName(key), model, providerLabel(src.Provider))
}

// ---------------------------------------------------------------------------
// Mode

var fundingModeState struct {
	sync.Mutex
	mode string
	at   time.Time
}

func currentFundingMode() string {
	fundingModeState.Lock()
	if !fundingModeState.at.IsZero() && time.Since(fundingModeState.at) < poolConfigTTL {
		mode := fundingModeState.mode
		fundingModeState.Unlock()
		return mode
	}
	fundingModeState.Unlock()
	mode := policyplus.FundingModeObserve
	if store := loadedStore(); store != nil {
		mode = store.FundingMode(context.Background())
	}
	fundingModeState.Lock()
	fundingModeState.mode, fundingModeState.at = mode, time.Now()
	fundingModeState.Unlock()
	return mode
}

func invalidateFundingMode() {
	fundingModeState.Lock()
	fundingModeState.at = time.Time{}
	fundingModeState.Unlock()
}

func recordFundingMiss(key policyplus.KeyRecord, model, provider, reason string) {
	if store := loadedStore(); store != nil {
		_ = store.RecordFundingMiss(context.Background(), key.ID, model, provider, reason, time.Now())
	}
}

// ---------------------------------------------------------------------------
// Admin API

func adminFunding(req managementRequest) ([]byte, error) {
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	ctx := context.Background()
	if req.Method == http.MethodPut || req.Method == http.MethodPost {
		var body struct {
			Mode    string `json:"mode"`
			Clear   bool   `json:"clear"`
			Confirm bool   `json:"confirm"`
		}
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_json"})
		}
		if !body.Confirm {
			return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "confirm_required", "message": "需要确认后才能修改额度来源设置。"})
		}
		if body.Clear {
			if err := store.ClearFundingMisses(ctx); err != nil {
				return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			}
		}
		if strings.TrimSpace(body.Mode) != "" {
			if err := store.SetFundingMode(ctx, body.Mode); err != nil {
				return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			}
			invalidateFundingMode()
		}
	}
	misses, err := store.FundingMisses(ctx, 200)
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
	}
	keys := keysByID(ctx, store)
	rows := make([]map[string]any, 0, len(misses))
	var total int64
	for _, miss := range misses {
		name := miss.KeyID
		if key, ok := keys[miss.KeyID]; ok {
			name = safeKeyDisplayName(key)
		}
		total += miss.Count
		rows = append(rows, map[string]any{
			"key_id": miss.KeyID, "name": name, "model": miss.Model, "provider": miss.Provider,
			"label": providerLabel(miss.Provider), "reason": miss.Reason, "count": miss.Count,
			"first_at": miss.FirstAt, "last_at": miss.LastAt,
		})
	}
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "mode": currentFundingMode(), "misses": rows, "total": total})
}
