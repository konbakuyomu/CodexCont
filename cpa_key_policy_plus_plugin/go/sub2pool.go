package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Optional read-only Sub2Pool integration: when sub2pool_url and
// sub2pool_api_key are configured and a pool names its Sub2Pool account id,
// the admin page shows Sub2Pool's capacity estimate beside the pool's own.
// Only the documented read API is called; nothing is written back.

const sub2poolTTL = time.Minute

type sub2poolView struct {
	USD        float64 `json:"usd,omitempty"`
	Low        float64 `json:"low,omitempty"`
	High       float64 `json:"high,omitempty"`
	Used       float64 `json:"used_percent,omitempty"`
	ObservedAt string  `json:"observed_at,omitempty"`
	Error      string  `json:"error,omitempty"`
}

type sub2poolCacheEntry struct {
	view sub2poolView
	at   time.Time
}

var sub2poolCache = struct {
	sync.Mutex
	entries map[string]sub2poolCacheEntry
}{entries: map[string]sub2poolCacheEntry{}}

var sub2poolHTTPClient = &http.Client{Timeout: 4 * time.Second, Transport: &http.Transport{Proxy: nil}}

func sub2poolEstimate(accountID int64) (sub2poolView, bool) {
	cfg := loadedConfig()
	if accountID <= 0 || cfg.Sub2PoolURL == "" || cfg.Sub2PoolAPIKey == "" {
		return sub2poolView{}, false
	}
	cacheKey := cfg.Sub2PoolURL + "#" + strconv.FormatInt(accountID, 10)
	sub2poolCache.Lock()
	if entry, ok := sub2poolCache.entries[cacheKey]; ok && time.Since(entry.at) < sub2poolTTL {
		sub2poolCache.Unlock()
		return entry.view, true
	}
	sub2poolCache.Unlock()
	view := fetchSub2PoolEstimate(cfg.Sub2PoolURL, cfg.Sub2PoolAPIKey, accountID)
	sub2poolCache.Lock()
	sub2poolCache.entries[cacheKey] = sub2poolCacheEntry{view: view, at: time.Now()}
	sub2poolCache.Unlock()
	return view, true
}

func fetchSub2PoolEstimate(baseURL, apiKey string, accountID int64) sub2poolView {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		fmt.Sprintf("%s/api/v1/dashboard?account_id=%d", baseURL, accountID), nil)
	if err != nil {
		return sub2poolView{Error: "Sub2Pool 地址无效"}
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := sub2poolHTTPClient.Do(req)
	if err != nil {
		return sub2poolView{Error: "连不上 Sub2Pool"}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return sub2poolView{Error: "Sub2Pool API Key 无效"}
	}
	if resp.StatusCode != http.StatusOK {
		return sub2poolView{Error: fmt.Sprintf("Sub2Pool 返回 HTTP %d", resp.StatusCode)}
	}
	var body struct {
		OK   bool `json:"ok"`
		Data struct {
			Cycle *struct {
				ObservedAt       string   `json:"observed_at"`
				UsedPercent      float64  `json:"upstream_used_percent"`
				USDPerPercent    *float64 `json:"effective_usd_per_percent"`
				CapacityLowerUSD *float64 `json:"capacity_lower_usd"`
				CapacityUpperUSD *float64 `json:"capacity_upper_usd"`
			} `json:"cycle"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil || !body.OK {
		return sub2poolView{Error: "Sub2Pool 返回格式无法识别"}
	}
	cycle := body.Data.Cycle
	if cycle == nil || cycle.USDPerPercent == nil || *cycle.USDPerPercent <= 0 {
		return sub2poolView{Error: "Sub2Pool 暂无这个账号的折算"}
	}
	view := sub2poolView{USD: *cycle.USDPerPercent * 100, Used: cycle.UsedPercent, ObservedAt: cycle.ObservedAt}
	if cycle.CapacityLowerUSD != nil {
		view.Low = *cycle.CapacityLowerUSD
	}
	if cycle.CapacityUpperUSD != nil {
		view.High = *cycle.CapacityUpperUSD
	}
	return view
}
