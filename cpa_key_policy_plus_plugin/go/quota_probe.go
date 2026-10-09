package main

// Active quota probes.
//
// Quota headers only arrive with requests, so an account nobody can reach
// (members over their share, the account itself full) never shows that it
// was reset, for instance with a reset credit. Pool accounts are therefore
// asked for their usage directly, at the endpoint the official Codex client
// reads its status from, through CPA's HTTP client and proxy: every 10
// minutes while requests bring no fresh numbers, every 2 minutes while
// someone is held back, and on demand. Answers take the same path as response
// headers, so a reset found this way restarts the members' ledgers at once.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"codexcont/cpa-key-policy-plus-plugin/internal/policyplus"
)

const (
	methodHostAuthGet = "host.auth.get"
	methodHostHTTPDo  = "host.http.do"

	probeIdleEvery  = 10 * time.Minute
	probeStuckEvery = 2 * time.Minute
	probeMaxBackoff = 30 * time.Minute
	probeLoopEvery  = 30 * time.Second
	probeManualGap  = 15 * time.Second

	codexUsageURL   = "https://chatgpt.com/backend-api/wham/usage"
	codexProbeAgent = "codex_cli_rs/0.76.0"

	settingQuotaProbe = "quota_probe"
)

var errProbeBusy = errors.New("正在查询，请稍后再试")

type probeStatus struct {
	AttemptAt time.Time
	OKAt      time.Time
	Error     string
	Failures  int
	running   bool
}

var probes = struct {
	sync.Mutex
	accounts map[string]*probeStatus
	stop     chan struct{}
}{accounts: map[string]*probeStatus{}}

// startQuotaProbes runs the probe loop once CPA has wired its callbacks.
func startQuotaProbes() {
	probes.Lock()
	defer probes.Unlock()
	if probes.stop != nil {
		return
	}
	stop := make(chan struct{})
	probes.stop = stop
	go func() {
		ticker := time.NewTicker(probeLoopEvery)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-ticker.C:
				runDueProbes(now)
			}
		}
	}()
}

func stopQuotaProbes() {
	probes.Lock()
	defer probes.Unlock()
	if probes.stop != nil {
		close(probes.stop)
		probes.stop = nil
	}
}

// quotaProbeEnabled is on unless the admin switched probes off.
func quotaProbeEnabled(ctx context.Context, store *policyplus.Store) bool {
	if store == nil {
		return false
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		return true
	}
	return settings[settingQuotaProbe] != "off"
}

// probeSupported lists the providers whose usage endpoint is known to answer
// in the shape parsed here.
func probeSupported(provider string) bool {
	return provider == "codex"
}

type probeTarget struct {
	pool      policyplus.Pool
	authIndex string
	provider  string
}

// probeTargets are the accounts of quota pools, once each, skipping accounts
// switched off in CPA.
func probeTargets(ctx context.Context) []probeTarget {
	hosts := map[string]hostAuthAccount{}
	for _, host := range hostAuthAccounts() {
		hosts[host.AuthIndex] = host
	}
	seen := map[string]bool{}
	var out []probeTarget
	for _, pool := range loadedPools(ctx) {
		authIndex := strings.TrimSpace(pool.AuthIndex)
		if authIndex == "" || pool.Budgeted() || seen[authIndex] {
			continue
		}
		seen[authIndex] = true
		provider := pool.ProviderName()
		if host, ok := hosts[authIndex]; ok {
			if host.Disabled {
				continue
			}
			provider = firstNonEmpty(host.Provider, provider)
		}
		if probeSupported(provider) {
			out = append(out, probeTarget{pool: pool, authIndex: authIndex, provider: provider})
		}
	}
	return out
}

func runDueProbes(now time.Time) {
	// The loop runs inside CPA's process; a panic here would stop CPA.
	defer func() { _ = recover() }()
	store := loadedStore()
	ctx := context.Background()
	if !quotaProbeEnabled(ctx, store) {
		return
	}
	for _, target := range probeTargets(ctx) {
		if probeDue(ctx, store, target, now) {
			_ = probeAccount(ctx, store, target.authIndex, target.provider)
		}
	}
}

// probeDue spaces probes out: none while requests keep the numbers fresh,
// sooner while the account or a member is held back, and backing off after
// failures.
func probeDue(ctx context.Context, store *policyplus.Store, target probeTarget, now time.Time) bool {
	status := probeStatusOf(target.authIndex)
	if status.running {
		return false
	}
	every := probeIdleEvery
	if probeUrgent(ctx, store, target.pool, now) {
		every = probeStuckEvery
	}
	if status.Failures > 0 {
		every = min(every<<min(status.Failures-1, 5), probeMaxBackoff)
	}
	last := status.AttemptAt
	if status.Failures == 0 {
		if latest := accountStateFor(ctx, store, target.authIndex, now).Latest; latest != nil {
			if seen := time.UnixMilli(latest.ObservedAtMS); seen.After(last) {
				last = seen
			}
		}
	}
	return now.Sub(last) >= every
}

// probeUrgent: the account is full or past its reset time, or a member is
// held back by their share. Each is a state that requests cannot clear.
func probeUrgent(ctx context.Context, store *policyplus.Store, pool policyplus.Pool, now time.Time) bool {
	account := accountStateFor(ctx, store, pool.AuthIndex, now)
	if account.Status == "reset_pending" {
		return true
	}
	if latest := account.Latest; latest != nil {
		if latest.LimitReached || (latest.Weekly != nil && latest.Weekly.Used >= 100) {
			return true
		}
	}
	if blocked, _ := accountBlocked(ctx, store, pool.AuthIndex, now); blocked {
		return true
	}
	ledger := computePoolLedger(ctx, store, pool, now)
	for _, row := range ledger.Rows {
		if row.State != "over" {
			continue
		}
		if _, bursting := currentBurst(ledger.Pool, ledger.Account, row.KeyID, now); !bursting {
			return true
		}
	}
	return false
}

// resetProbeStatus forgets probe history, with the other pool caches. A probe
// still running keeps its own record and finishes on it.
func resetProbeStatus() {
	probes.Lock()
	defer probes.Unlock()
	probes.accounts = map[string]*probeStatus{}
}

func probeStatusOf(authIndex string) probeStatus {
	probes.Lock()
	defer probes.Unlock()
	if status, ok := probes.accounts[authIndex]; ok {
		return *status
	}
	return probeStatus{}
}

// refreshAccountQuota probes one pool account now, for the admin's refresh
// button and before a burst starts. Probes closer together than
// probeManualGap reuse the last answer.
func refreshAccountQuota(ctx context.Context, store *policyplus.Store, pool policyplus.Pool) error {
	authIndex := strings.TrimSpace(pool.AuthIndex)
	if store == nil || authIndex == "" || pool.Budgeted() {
		return nil
	}
	provider := pool.ProviderName()
	for _, host := range hostAuthAccounts() {
		if host.AuthIndex == authIndex {
			provider = firstNonEmpty(host.Provider, provider)
		}
	}
	if !probeSupported(provider) {
		return nil
	}
	if !quotaProbeEnabled(ctx, store) {
		return nil
	}
	status := probeStatusOf(authIndex)
	if time.Since(status.AttemptAt) < probeManualGap {
		if status.Error != "" {
			return errors.New(status.Error)
		}
		return nil
	}
	return probeAccount(ctx, store, authIndex, provider)
}

func probeAccount(ctx context.Context, store *policyplus.Store, authIndex, provider string) error {
	probes.Lock()
	status, ok := probes.accounts[authIndex]
	if !ok {
		status = &probeStatus{}
		probes.accounts[authIndex] = status
	}
	if status.running {
		probes.Unlock()
		return errProbeBusy
	}
	status.running = true
	status.AttemptAt = time.Now()
	probes.Unlock()

	sig, err := fetchAccountUsage(ctx, authIndex, provider)
	if err == nil {
		sig.AuthIndex, sig.Provider = authIndex, provider
		var record policyplus.QuotaRecord
		if record, err = store.RecordQuota(ctx, sig); err == nil {
			afterQuotaRecorded(authIndex, sig, record, true)
		}
	}

	probes.Lock()
	status.running = false
	if err != nil {
		status.Failures++
		status.Error = err.Error()
	} else {
		status.Failures = 0
		status.Error = ""
		status.OKAt = time.Now()
	}
	probes.Unlock()
	return err
}

// fetchAccountUsage asks the upstream for an account's usage with the
// credential CPA holds for it. The token stays inside this call.
func fetchAccountUsage(ctx context.Context, authIndex, provider string) (policyplus.QuotaSignal, error) {
	_ = ctx
	if provider != "codex" {
		return policyplus.QuotaSignal{}, fmt.Errorf("暂不支持查询 %s 账号", provider)
	}
	raw, err := callHost(methodHostAuthGet, map[string]any{"auth_index": authIndex})
	if err != nil {
		return policyplus.QuotaSignal{}, errors.New("读不到这个账号的凭证")
	}
	var file struct {
		JSON json.RawMessage `json:"json"`
	}
	var cred struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	}
	if json.Unmarshal(raw, &file) != nil || json.Unmarshal(file.JSON, &cred) != nil || strings.TrimSpace(cred.AccessToken) == "" {
		return policyplus.QuotaSignal{}, errors.New("凭证里没有可用的 access_token")
	}
	headers := map[string][]string{
		"Authorization": {"Bearer " + strings.TrimSpace(cred.AccessToken)},
		"Accept":        {"application/json"},
		"User-Agent":    {codexProbeAgent},
		"Originator":    {"codex_cli_rs"},
	}
	if id := strings.TrimSpace(cred.AccountID); id != "" {
		headers["Chatgpt-Account-Id"] = []string{id}
	}
	raw, err = callHost(methodHostHTTPDo, map[string]any{"method": http.MethodGet, "url": codexUsageURL, "headers": headers})
	if err != nil {
		return policyplus.QuotaSignal{}, errors.New("连不上用量接口")
	}
	var resp struct {
		StatusCode int
		Body       []byte
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return policyplus.QuotaSignal{}, errors.New("用量接口的回复读不懂")
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return policyplus.QuotaSignal{}, fmt.Errorf("凭证无效或已过期（HTTP %d）", resp.StatusCode)
	case resp.StatusCode == http.StatusTooManyRequests:
		return policyplus.QuotaSignal{}, errors.New("查询太频繁，被上游限流（HTTP 429）")
	case resp.StatusCode != http.StatusOK:
		return policyplus.QuotaSignal{}, fmt.Errorf("用量接口返回 HTTP %d", resp.StatusCode)
	}
	sig, ok := policyplus.ParseCodexUsage(resp.Body, time.Now())
	if !ok {
		return policyplus.QuotaSignal{}, errors.New("用量接口的回复里没有周限数据")
	}
	return sig, nil
}

// afterQuotaRecorded refreshes what depends on an account's quota. A new
// cycle means the account was reset, so its remembered "usage limit reached"
// is stale; so is it when a probe finds the account taking requests again.
func afterQuotaRecorded(authIndex string, sig policyplus.QuotaSignal, record policyplus.QuotaRecord, probed bool) {
	if record.Tick || record.NewCycle || probed {
		invalidateAccountState(authIndex)
	}
	open := !sig.LimitReached && (sig.Weekly == nil || sig.Weekly.Used < 100)
	if record.NewCycle || (probed && open) {
		poolState.mu.Lock()
		delete(poolState.limitHits, authIndex)
		if record.NewCycle {
			poolState.hostAuthsAt = time.Time{}
		}
		poolState.mu.Unlock()
	}
}

// probeView describes an account's probes for the admin page.
func probeView(ctx context.Context, store *policyplus.Store, pool policyplus.Pool) map[string]any {
	provider := pool.ProviderName()
	view := map[string]any{"enabled": quotaProbeEnabled(ctx, store), "supported": probeSupported(provider) && !pool.Budgeted()}
	status := probeStatusOf(strings.TrimSpace(pool.AuthIndex))
	if !status.AttemptAt.IsZero() {
		view["attempt_ms"] = status.AttemptAt.UnixMilli()
	}
	if !status.OKAt.IsZero() {
		view["ok_ms"] = status.OKAt.UnixMilli()
	}
	if status.Error != "" {
		view["error"] = status.Error
	}
	return view
}
