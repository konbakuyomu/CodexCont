package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"codexcont/cpa-key-policy-plus-plugin/internal/policyplus"
)

// Carpool pools: each pool pins a group of keys to one upstream subscription
// account, meters each member's share of its weekly quota and blocks members
// whose share is used up. See internal/policyplus/pool.go for the ledger.

const (
	routeTargetProvider = "provider"

	accountStateTTL  = 15 * time.Second
	poolConfigTTL    = 10 * time.Second
	modelCatalogTTL  = 60 * time.Second
	hostAccountsTTL  = 30 * time.Second
	staleObservation = 6 * time.Hour
	ledgerHistory    = 38 * 24 * time.Hour
)

type accountState struct {
	AuthIndex  string
	Plan       string
	Provider   string
	Latest     *policyplus.QuotaAccount
	Current    policyplus.CycleLedger
	Ledgers    []policyplus.CycleLedger
	Capacity   policyplus.CapacityEstimate
	Status     string
	computedAt time.Time
}

// InCycle reports whether Current describes the cycle running now.
func (a accountState) InCycle(now time.Time) bool {
	return a.Current.HasData() && now.Unix() < a.Current.CycleResetAt
}

type poolRuntime struct {
	mu          sync.Mutex
	pools       []policyplus.Pool
	poolsAt     time.Time
	accounts    map[string]accountState
	catalog     map[string]string // lower-case model id -> owned_by
	catalogOK   bool
	catalogAt   time.Time
	hostAuths   []hostAuthAccount
	hostAuthsAt time.Time
	limitHits   map[string]limitHit // auth_index -> last usage_limit_reached
}

// limitHit is an upstream "usage limit reached" answer: the account refuses
// requests until Until (unix seconds).
type limitHit struct {
	Until int64
	At    time.Time
}

var poolState = &poolRuntime{accounts: map[string]accountState{}, limitHits: map[string]limitHit{}}

func resetPoolCaches() {
	poolState.mu.Lock()
	defer poolState.mu.Unlock()
	poolState.pools = nil
	poolState.poolsAt = time.Time{}
	poolState.accounts = map[string]accountState{}
	poolState.catalog = nil
	poolState.catalogAt = time.Time{}
	poolState.hostAuths = nil
	poolState.hostAuthsAt = time.Time{}
	poolState.limitHits = map[string]limitHit{}
}

func invalidatePoolConfig() {
	poolState.mu.Lock()
	poolState.poolsAt = time.Time{}
	poolState.mu.Unlock()
}

func invalidateAccountState(authIndex string) {
	poolState.mu.Lock()
	delete(poolState.accounts, authIndex)
	poolState.mu.Unlock()
}

func loadedPools(ctx context.Context) []policyplus.Pool {
	poolState.mu.Lock()
	if !poolState.poolsAt.IsZero() && time.Since(poolState.poolsAt) < poolConfigTTL {
		pools := poolState.pools
		poolState.mu.Unlock()
		return pools
	}
	poolState.mu.Unlock()
	store := loadedStore()
	if store == nil {
		return nil
	}
	pools, err := store.ListPools(ctx)
	if err != nil {
		return nil
	}
	poolState.mu.Lock()
	poolState.pools = pools
	poolState.poolsAt = time.Now()
	poolState.mu.Unlock()
	return pools
}

func poolMembership(ctx context.Context, keyID string) (policyplus.Pool, policyplus.PoolMember, bool) {
	if strings.TrimSpace(keyID) == "" {
		return policyplus.Pool{}, policyplus.PoolMember{}, false
	}
	for _, pool := range loadedPools(ctx) {
		if member, ok := pool.Member(keyID); ok {
			return pool, member, true
		}
	}
	return policyplus.Pool{}, policyplus.PoolMember{}, false
}

// ingestQuotaSignal records the upstream quota headers carried by a usage
// record. Observation time is when the response headers arrived, which is
// before the request's own cost was committed.
func ingestQuotaSignal(ctx context.Context, store *policyplus.Store, rec usageRecord) {
	authIndex := strings.TrimSpace(rec.AuthIndex)
	if store == nil || authIndex == "" || len(rec.ResponseHeaders) == 0 {
		return
	}
	observedAt := rec.RequestedAt
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	switch {
	case rec.TTFT > 0:
		observedAt = observedAt.Add(rec.TTFT)
	case rec.Latency > time.Millisecond:
		observedAt = observedAt.Add(rec.Latency - time.Millisecond)
	}
	sig, ok := policyplus.ParseCodexQuotaHeaders(rec.ResponseHeaders, observedAt)
	if !ok {
		return
	}
	sig.AuthIndex = authIndex
	sig.AuthID = rec.AuthID
	sig.Provider = rec.Provider
	if tick, err := store.RecordQuotaSignal(ctx, sig); err == nil && tick {
		invalidateAccountState(authIndex)
	}
}

// noteUpstreamLimit remembers an account answering "usage limit reached" and
// forgets it once the account serves a request made after that answer. CPA
// cools such a credential down; this lets pools react before CPA's credential
// list is read again.
func noteUpstreamLimit(rec usageRecord) {
	authIndex := strings.TrimSpace(rec.AuthIndex)
	if authIndex == "" {
		return
	}
	requested := rec.RequestedAt
	if requested.IsZero() {
		requested = time.Now()
	}
	poolState.mu.Lock()
	defer poolState.mu.Unlock()
	if !rec.Failed {
		if hit, ok := poolState.limitHits[authIndex]; ok && requested.After(hit.At) {
			delete(poolState.limitHits, authIndex)
		}
		return
	}
	if until, ok := usageLimitUntil(rec.Failure.Body, requested); ok {
		poolState.limitHits[authIndex] = limitHit{Until: until, At: requested}
		poolState.hostAuthsAt = time.Time{}
	}
}

// usageLimitUntil reads a Codex usage_limit_reached error body and returns when
// the account takes requests again. Without reset details the account is
// treated as full for a few minutes, until CPA's own cooldown shows up.
func usageLimitUntil(body string, requested time.Time) (int64, bool) {
	if !strings.Contains(body, "usage_limit_reached") {
		return 0, false
	}
	type limit struct {
		Type            string  `json:"type"`
		ResetsAt        float64 `json:"resets_at"`
		ResetsInSeconds float64 `json:"resets_in_seconds"`
	}
	var parsed struct {
		limit
		Error *limit `json:"error"`
	}
	_ = json.Unmarshal([]byte(body), &parsed)
	for _, candidate := range []*limit{parsed.Error, &parsed.limit} {
		if candidate == nil || candidate.Type != "usage_limit_reached" {
			continue
		}
		if candidate.ResetsAt > 0 {
			return int64(candidate.ResetsAt), true
		}
		if candidate.ResetsInSeconds > 0 {
			return requested.Unix() + int64(math.Round(candidate.ResetsInSeconds)), true
		}
	}
	return requested.Add(5 * time.Minute).Unix(), true
}

// accountBlocked reports whether requests pinned to authIndex are refused right
// now, because CPA has the credential switched off or cooling down, or the
// account's weekly or 5-hour quota is full, and when it is expected back
// (0 when unknown).
func accountBlocked(ctx context.Context, store *policyplus.Store, authIndex string, now time.Time) (bool, int64) {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return false, 0
	}
	var until []int64 // recovery times; 0 = unknown
	for _, host := range hostAuthAccounts() {
		if host.AuthIndex != authIndex {
			continue
		}
		switch {
		case host.Disabled:
			until = append(until, 0)
		case host.Unavailable && host.NextRetryAfter.IsZero():
			until = append(until, 0)
		case host.Unavailable && host.NextRetryAfter.After(now):
			until = append(until, host.NextRetryAfter.Unix())
		}
	}
	if store != nil {
		if latest := accountStateFor(ctx, store, authIndex, now).Latest; latest != nil {
			for _, window := range []*policyplus.QuotaWindow{latest.Weekly, latest.Short} {
				if window != nil && window.Used >= 100 && window.ResetAt > now.Unix() {
					until = append(until, window.ResetAt)
				}
			}
		}
	}
	poolState.mu.Lock()
	hit, ok := poolState.limitHits[authIndex]
	poolState.mu.Unlock()
	if ok && hit.Until > now.Unix() {
		until = append(until, hit.Until)
	}
	if len(until) == 0 {
		return false, 0
	}
	var latest int64
	for _, at := range until {
		if at == 0 {
			return true, 0
		}
		if at > latest {
			latest = at
		}
	}
	return true, latest
}

// poolBorrow is a bursting member riding another pool's account.
type poolBorrow struct {
	Lender policyplus.Pool
	Reason string // "account_full"
	Until  int64  // when the member's own account takes them back; 0 = unknown
}

// borrowFor decides whether a pool member rides another pool's account right
// now: they are bursting, their own account is full or cooling down, and some
// other pool lends its account and still has room. The lender with the most
// room left this week is picked.
func borrowFor(ctx context.Context, store *policyplus.Store, pool policyplus.Pool, member policyplus.PoolMember, now time.Time) (poolBorrow, bool) {
	if _, bursting := pool.BurstOf(member.KeyID, now); !bursting {
		return poolBorrow{}, false
	}
	blocked, until := accountBlocked(ctx, store, pool.AuthIndex, now)
	if !blocked {
		return poolBorrow{}, false
	}
	lender, ok := pickLender(ctx, store, pool, now)
	if !ok {
		return poolBorrow{}, false
	}
	return poolBorrow{Lender: lender, Reason: "account_full", Until: until}, true
}

// pickLender finds the pool that would take this pool's bursting members if
// they had to borrow now: another pool that lends its account, with room left.
func pickLender(ctx context.Context, store *policyplus.Store, pool policyplus.Pool, now time.Time) (policyplus.Pool, bool) {
	if store == nil || strings.TrimSpace(pool.AuthIndex) == "" {
		return policyplus.Pool{}, false
	}
	var picked policyplus.Pool
	best := -1.0
	for _, candidate := range loadedPools(ctx) {
		if candidate.ID == pool.ID || candidate.ModelPrefix == "" || !candidate.Lending() {
			continue
		}
		if blocked, _ := accountBlocked(ctx, store, candidate.AuthIndex, now); blocked {
			continue
		}
		used := 0.0
		if account := accountStateFor(ctx, store, candidate.AuthIndex, now); account.InCycle(now) {
			used = account.Current.LastUsed
		}
		if best < 0 || used < best {
			best, picked = used, candidate
		}
	}
	return picked, best >= 0
}

// shareUsedUp reports whether a member's share of the pool account is used up
// this cycle, which is when the share check blocks them, and when the cycle
// resets. A bursting member is never used up.
func shareUsedUp(ctx context.Context, store *policyplus.Store, pool policyplus.Pool, member policyplus.PoolMember, now time.Time) (int64, bool) {
	if !member.Shared() || pool.AuthIndex == "" {
		return 0, false
	}
	if _, bursting := pool.BurstOf(member.KeyID, now); bursting {
		return 0, false
	}
	ledger := computePoolLedger(ctx, store, pool, now)
	if !ledger.InCycle {
		return 0, false
	}
	row, ok := ledger.row(member.KeyID)
	if !ok || row.AvailablePP == nil || row.RemainingPP == nil || *row.RemainingPP > 1e-9 {
		return 0, false
	}
	return ledger.Account.Current.CycleResetAt, true
}

// poolShareOf returns the pool whose share governs a key: a member with a share
// in a pool bound to an account. Such keys are not held to their USD windows.
func poolShareOf(keyID string) (policyplus.Pool, policyplus.PoolMember, bool) {
	pool, member, ok := poolMembership(context.Background(), keyID)
	if !ok || !member.Shared() || strings.TrimSpace(pool.AuthIndex) == "" {
		return policyplus.Pool{}, policyplus.PoolMember{}, false
	}
	return pool, member, true
}

// poolKeyView describes a key's pool role for key lists and the member page.
func poolKeyView(keyID string) map[string]any {
	pool, member, ok := poolMembership(context.Background(), keyID)
	if !ok {
		return nil
	}
	role := "member"
	switch {
	case member.Owner():
		role = "owner"
	case member.Pinned():
		role = "pinned"
	}
	view := map[string]any{"pool": pool.Name, "role": role}
	if member.Shared() {
		view["share_percent"] = *member.SharePercent
	}
	// Only share members have their USD windows replaced by the share.
	view["usd_windows_off"] = member.Shared() && strings.TrimSpace(pool.AuthIndex) != ""
	return view
}

// lenderServes reports whether CPA lists the lender's prefixed twin of model,
// which is what a borrowed request is sent as.
func lenderServes(lender policyplus.Pool, model string) bool {
	base, _ := splitModelSuffix(model)
	if base == "" || strings.Contains(base, "/") || lender.ModelPrefix == "" {
		return false
	}
	catalog, ok := modelCatalog()
	if !ok {
		return false
	}
	_, listed := catalog[strings.ToLower(lender.ModelPrefix+"/"+base)]
	return listed
}

func accountStateFor(ctx context.Context, store *policyplus.Store, authIndex string, now time.Time) accountState {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" || store == nil {
		return accountState{AuthIndex: authIndex, Status: "unassigned"}
	}
	poolState.mu.Lock()
	cached, ok := poolState.accounts[authIndex]
	poolState.mu.Unlock()
	if ok && now.Sub(cached.computedAt) < accountStateTTL && now.Sub(cached.computedAt) >= 0 {
		return cached
	}
	next := loadAccountState(ctx, store, authIndex, now)
	poolState.mu.Lock()
	poolState.accounts[authIndex] = next
	poolState.mu.Unlock()
	return next
}

func loadAccountState(ctx context.Context, store *policyplus.Store, authIndex string, now time.Time) accountState {
	out := accountState{AuthIndex: authIndex, Status: "no_data", computedAt: now}
	if accounts, err := store.QuotaAccounts(ctx); err == nil {
		for i := range accounts {
			if accounts[i].AuthIndex == authIndex {
				account := accounts[i]
				out.Latest = &account
				out.Plan = account.PlanType
				out.Provider = account.Provider
			}
		}
	}
	obs, err := store.QuotaObservations(ctx, authIndex, now.Add(-ledgerHistory).Unix())
	if err != nil || len(obs) == 0 {
		out.Capacity = priorCapacity(out.Plan)
		return out
	}
	fromMS := obs[0].ObservedAtMS
	for _, item := range obs {
		if item.ObservedAtMS < fromMS {
			fromMS = item.ObservedAtMS
		}
	}
	events, _ := store.AuthCostEvents(ctx, authIndex, fromMS-1, now.UnixMilli())
	params := policyplus.LedgerParams{FloorUSD: policyplus.FloorCapacityUSD(out.Plan)}
	for start := 0; start < len(obs); {
		end := start
		for end < len(obs) && obs[end].CycleResetAt == obs[start].CycleResetAt {
			end++
		}
		limit := obs[start].CycleResetAt * 1000
		cut := sort.Search(len(events), func(i int) bool { return events[i].AtMS > limit })
		ledger := policyplus.BuildCycleLedger(obs[start:end], events[:cut], params)
		out.Ledgers = append(out.Ledgers, ledger)
		if ledger.RefUSD > 0 {
			params.RefUSD = ledger.RefUSD
		}
		start = end
	}
	out.Current = out.Ledgers[len(out.Ledgers)-1]
	lastSeen := out.Current.LastSeenMS
	if out.Latest != nil && out.Latest.ObservedAtMS > lastSeen {
		lastSeen = out.Latest.ObservedAtMS
	}
	switch {
	case now.Unix() >= out.Current.CycleResetAt:
		out.Status = "reset_pending"
	case now.Sub(time.UnixMilli(lastSeen)) > staleObservation:
		out.Status = "stale"
	default:
		out.Status = "live"
	}
	out.Capacity = effectiveCapacity(out)
	return out
}

func priorCapacity(plan string) policyplus.CapacityEstimate {
	return policyplus.CapacityEstimate{USD: policyplus.PriorCapacityUSD(plan), Basis: "prior"}
}

// effectiveCapacity picks the estimate used to price pending cost: this
// cycle once it has a few rises, otherwise the previous cycle, otherwise a
// plan prior.
func effectiveCapacity(state accountState) policyplus.CapacityEstimate {
	current := state.Current.Capacity
	if current.Known() && current.DeltaPP >= 3 {
		return current
	}
	for i := len(state.Ledgers) - 2; i >= 0; i-- {
		if previous := state.Ledgers[i].Capacity; previous.Known() {
			previous.Basis = "previous"
			return previous
		}
	}
	if current.Known() {
		return current
	}
	return priorCapacity(state.Plan)
}

// ---------------------------------------------------------------------------
// Pool ledger rows

type poolRow struct {
	KeyID        string   `json:"key_id,omitempty"`
	Label        string   `json:"label"`
	Preview      string   `json:"preview,omitempty"`
	Role         string   `json:"role"`
	Self         bool     `json:"self,omitempty"`
	SharePercent *float64 `json:"share_percent"`
	CarryPP      float64  `json:"carry_pp"`
	AvailablePP  *float64 `json:"available_pp"`
	AttributedPP float64  `json:"attributed_pp"`
	PendingPP    float64  `json:"pending_pp"`
	UsedPP       float64  `json:"used_pp"`
	RemainingPP  *float64 `json:"remaining_pp"`
	RemainingUSD *float64 `json:"remaining_usd"`
	CostUSD      float64  `json:"cost_usd"`
	State        string   `json:"state"`
	FromPool     string   `json:"from_pool,omitempty"`
	// Burst is the member's burst in the current cycle, running or stopped.
	Burst *policyplus.MemberBurst `json:"burst,omitempty"`
	Heat  *poolHeat               `json:"heat,omitempty"`
}

type poolHeat struct {
	Hours []float64 `json:"hours"`
	Days  []float64 `json:"days"`
}

type poolLedger struct {
	Pool        policyplus.Pool
	Account     accountState
	InCycle     bool
	Rows        []poolRow
	UsedPercent float64
	PreTracking float64
	Unassigned  float64
	OutsidePP   float64
	LentPP      float64
}

func (l poolLedger) row(keyID string) (poolRow, bool) {
	for _, row := range l.Rows {
		if row.KeyID == keyID {
			return row, true
		}
	}
	return poolRow{}, false
}

func computePoolLedger(ctx context.Context, store *policyplus.Store, pool policyplus.Pool, now time.Time) poolLedger {
	account := accountStateFor(ctx, store, pool.AuthIndex, now)
	if settled := maybeSettleBursts(ctx, store, pool, account, now); settled {
		invalidatePoolConfig()
		for _, fresh := range loadedPools(ctx) {
			if fresh.ID == pool.ID {
				pool = fresh
			}
		}
	}
	ledger := poolLedger{Pool: pool, Account: account, InCycle: account.InCycle(now)}
	keys := keysByID(ctx, store)
	carry := map[string]float64{}
	if ledger.InCycle {
		carry, _ = store.PoolCarry(ctx, pool.ID, account.Current.CycleResetAt)
		ledger.UsedPercent = account.Current.LastUsed
		ledger.PreTracking = account.Current.StartUsed
		ledger.Unassigned = account.Current.Unattributed
	}
	capacity := account.Capacity.USD
	member := map[string]bool{}
	build := func(keyID string, share *float64, role string) poolRow {
		row := poolRow{KeyID: keyID, Role: role, SharePercent: share, State: role}
		if key, ok := keys[keyID]; ok {
			row.Label = safeKeyDisplayName(key)
			row.Preview = key.Preview
		} else {
			row.Label = firstNonEmpty(keyID, "未知 Key")
		}
		if ledger.InCycle {
			row.AttributedPP = account.Current.Attributed[keyID]
			if capacity > 0 {
				row.PendingPP = 100 * account.Current.PendingCost[keyID] / capacity
			}
			row.CostUSD = account.Current.CycleCost[keyID]
		}
		row.UsedPP = row.AttributedPP + row.PendingPP
		if share != nil {
			row.CarryPP = carry[keyID]
			available := *share + row.CarryPP
			remaining := available - row.UsedPP
			row.AvailablePP = &available
			row.RemainingPP = &remaining
			for i := range pool.Bursts {
				burst := pool.Bursts[i]
				if burst.KeyID == keyID && now.Unix() < burst.CycleResetAt {
					row.Burst = &burst
				}
			}
			if capacity > 0 {
				usd := math.Max(remaining, 0) * capacity / 100
				row.RemainingUSD = &usd
			}
			switch {
			case remaining <= 1e-9:
				row.State = "over"
			case remaining < math.Max(available*0.1, 1):
				row.State = "near"
			default:
				row.State = "ok"
			}
		}
		return row
	}
	for _, m := range pool.Members {
		member[m.KeyID] = true
		role := "member"
		switch {
		case m.Owner():
			role = "owner"
		case m.Pinned():
			role = "pinned"
		}
		ledger.Rows = append(ledger.Rows, build(m.KeyID, m.SharePercent, role))
	}
	if ledger.InCycle {
		outside := map[string]bool{}
		for _, costs := range []map[string]float64{account.Current.Attributed, account.Current.PendingCost, account.Current.CycleCost} {
			for keyID := range costs {
				if !member[keyID] {
					outside[keyID] = true
				}
			}
		}
		ids := make([]string, 0, len(outside))
		for keyID := range outside {
			ids = append(ids, keyID)
		}
		sort.Strings(ids)
		// Members of other pools who rode this account while it was lent out
		// are borrowers, not strangers.
		var borrowers map[string]bool
		home := map[string]string{}
		if len(ids) > 0 {
			borrowers, _ = store.LendWindowKeys(ctx, pool.AuthIndex, account.Current.TrackingSinceMS/1000)
			for _, other := range loadedPools(ctx) {
				if other.ID == pool.ID {
					continue
				}
				for _, m := range other.Members {
					home[m.KeyID] = other.Name
				}
			}
		}
		for _, keyID := range ids {
			row := build(keyID, nil, "outside")
			if from, ok := home[keyID]; ok && borrowers[keyID] {
				row.Role, row.State, row.FromPool = "borrowed", "borrowed", from
				ledger.LentPP += row.AttributedPP
			} else {
				ledger.OutsidePP += row.AttributedPP
			}
			ledger.Rows = append(ledger.Rows, row)
		}
	}
	return ledger
}

func keysByID(ctx context.Context, store *policyplus.Store) map[string]policyplus.KeyRecord {
	out := map[string]policyplus.KeyRecord{}
	if store == nil {
		return out
	}
	keys, err := store.ListKeys(ctx)
	if err != nil {
		return out
	}
	for _, key := range keys {
		out[key.ID] = key
	}
	return out
}

// maybeSettleBursts settles the member bursts of every cycle the account has
// left behind. Members who burst in carry mode pay what they used past their
// share back from their next cycle to the members who left share unused.
func maybeSettleBursts(ctx context.Context, store *policyplus.Store, pool policyplus.Pool, account accountState, now time.Time) bool {
	if len(pool.Bursts) == 0 || store == nil || !account.Current.HasData() {
		return false
	}
	cycles := map[int64][]policyplus.MemberBurst{}
	for _, burst := range pool.Bursts {
		if account.Current.CycleResetAt > burst.CycleResetAt+3600 {
			cycles[burst.CycleResetAt] = append(cycles[burst.CycleResetAt], burst)
		}
	}
	settled := false
	for cycleResetAt, bursts := range cycles {
		var cycleLedger policyplus.CycleLedger
		for _, ledger := range account.Ledgers {
			if diff := ledger.CycleResetAt - cycleResetAt; diff >= -3600 && diff <= 3600 {
				cycleLedger = ledger
			}
		}
		billed := map[string]bool{}
		for _, burst := range bursts {
			billed[burst.KeyID] = burst.Mode == policyplus.BurstModeCarry
		}
		carry, _ := store.PoolCarry(ctx, pool.ID, cycleResetAt)
		inputs := []policyplus.SettlementInput{}
		for _, member := range pool.Members {
			if !member.Shared() {
				continue
			}
			inputs = append(inputs, policyplus.SettlementInput{
				KeyID:   member.KeyID,
				SharePP: *member.SharePercent,
				CarryPP: carry[member.KeyID],
				UsedPP:  cycleLedger.Attributed[member.KeyID],
				Billed:  billed[member.KeyID],
			})
		}
		settlement := policyplus.SettleCarry(inputs)
		settlement.Bursts = bursts
		settlement.EvidenceMS = cycleLedger.LastSeenMS
		settlement.Used = cycleLedger.LastUsed
		// What other pools borrowed is recorded but never settled between members.
		if borrowers, err := store.LendWindowKeys(ctx, pool.AuthIndex, cycleLedger.TrackingSinceMS/1000); err == nil {
			own := map[string]bool{}
			for _, member := range pool.Members {
				own[member.KeyID] = true
			}
			for keyID := range borrowers {
				if !own[keyID] {
					settlement.LentPP += cycleLedger.Attributed[keyID]
				}
			}
			settlement.LentPP = math.Round(settlement.LentPP*100000) / 100000
		}
		if err := store.FinishMemberBursts(ctx, pool.ID, cycleResetAt, account.Current.CycleResetAt, settlement, now); err == nil {
			settled = true
		}
	}
	if settled {
		invalidatePoolConfig()
	}
	return settled
}

// ---------------------------------------------------------------------------
// Enforcement and routing

type poolShareDecision struct {
	Allowed     bool
	PoolName    string
	UsedPP      float64
	AvailablePP float64
	ResetAt     int64
}

// checkPoolShare blocks a member whose share of the pool account's weekly
// quota is used up. The owner, bursting members, models the pool account does
// not serve and accounts without a current observation are never blocked here.
func checkPoolShare(key policyplus.KeyRecord, model string) poolShareDecision {
	allow := poolShareDecision{Allowed: true}
	store := loadedStore()
	if store == nil || key.ID == "" {
		return allow
	}
	ctx := context.Background()
	pool, member, ok := poolMembership(ctx, key.ID)
	if !ok || !member.Shared() || pool.AuthIndex == "" || !poolServesModel(pool, model) {
		return allow
	}
	now := time.Now()
	if _, bursting := pool.BurstOf(member.KeyID, now); bursting {
		return allow
	}
	ledger := computePoolLedger(ctx, store, pool, now)
	if !ledger.InCycle {
		return allow
	}
	row, ok := ledger.row(key.ID)
	if !ok || row.AvailablePP == nil || row.RemainingPP == nil || *row.RemainingPP > 1e-9 {
		return allow
	}
	return poolShareDecision{
		Allowed:     false,
		PoolName:    pool.Name,
		UsedPP:      row.UsedPP,
		AvailablePP: *row.AvailablePP,
		ResetAt:     ledger.Account.Current.CycleResetAt,
	}
}

func beijingTimeLabel(unix int64) string {
	if unix <= 0 {
		return "下个周期"
	}
	loc := time.FixedZone("Asia/Shanghai", 8*3600)
	return time.Unix(unix, 0).In(loc).Format("1月2日 15:04") + "（北京时间）"
}

// recoveryText says when a full account takes requests again, if known.
func recoveryText(until int64) string {
	if until <= 0 {
		return ""
	}
	loc := time.FixedZone("Asia/Shanghai", 8*3600)
	return "（预计北京时间 " + time.Unix(until, 0).In(loc).Format("1月2日 15:04") + " 恢复）"
}

func usdText(value float64) string { return fmt.Sprintf("$%.2f", value) }

func splitModelSuffix(model string) (string, string) {
	model = strings.TrimSpace(model)
	if i := strings.Index(model, "("); i > 0 && strings.HasSuffix(model, ")") {
		return model[:i], model[i:]
	}
	return model, ""
}

func poolServesModel(pool policyplus.Pool, model string) bool {
	base, _ := splitModelSuffix(model)
	if base == "" {
		return true
	}
	if i := strings.Index(base, "/"); i > 0 {
		return pool.ModelPrefix != "" && strings.EqualFold(base[:i], pool.ModelPrefix)
	}
	catalog, ok := modelCatalog()
	if ok {
		if pool.ModelPrefix != "" {
			if _, pinned := catalog[strings.ToLower(pool.ModelPrefix+"/"+base)]; pinned {
				return true
			}
		}
		if owner, known := catalog[strings.ToLower(base)]; known {
			owner = strings.ToLower(owner)
			return owner == "" || owner == "openai" || owner == "codex"
		}
	}
	return codexLikeModel(base)
}

func codexLikeModel(model string) bool {
	lower := strings.ToLower(strings.TrimSpace(model))
	if strings.HasPrefix(lower, "gpt-") || strings.HasPrefix(lower, "codex") {
		return true
	}
	return len(lower) >= 2 && lower[0] == 'o' && lower[1] >= '1' && lower[1] <= '9'
}

// poolRoutingConfigured is the cheap gate in front of every model.route call.
func poolRoutingConfigured(ctx context.Context) bool {
	for _, pool := range loadedPools(ctx) {
		if pool.ModelPrefix != "" && (len(pool.Members) > 0 || pool.LendDuringBurst) {
			return true
		}
	}
	return false
}

// poolRouteTarget pins a pool member's request to the pool account by asking
// CPA for the prefixed model (pro/gpt-5.5), which only the credential carrying
// that prefix serves. A member who borrows (see borrowFor) is sent to the
// lender's prefix instead. It only routes when CPA really lists the prefixed
// model, so an unconfigured prefix leaves requests on normal rotation.
func poolRouteTarget(keyID, model string) (modelRouteResponse, bool) {
	ctx := context.Background()
	pool, member, ok := poolMembership(ctx, keyID)
	if !ok {
		return modelRouteResponse{}, false
	}
	base, _ := splitModelSuffix(model)
	if base == "" || strings.Contains(base, "/") {
		return modelRouteResponse{}, false
	}
	catalog, ok := modelCatalog()
	if !ok {
		return modelRouteResponse{}, false
	}
	store := loadedStore()
	now := time.Now()
	if borrow, ok := borrowFor(ctx, store, pool, member, now); ok {
		if _, listed := catalog[strings.ToLower(borrow.Lender.ModelPrefix+"/"+base)]; listed {
			return prefixRoute(ctx, store, borrow.Lender, model, now, "cpa_key_policy_plus_pool_borrow"), true
		}
	}
	if pool.ModelPrefix == "" {
		return modelRouteResponse{}, false
	}
	if _, listed := catalog[strings.ToLower(pool.ModelPrefix+"/"+base)]; !listed {
		return modelRouteResponse{}, false
	}
	return prefixRoute(ctx, store, pool, model, now, "cpa_key_policy_plus_pool_route"), true
}

func prefixRoute(ctx context.Context, store *policyplus.Store, pool policyplus.Pool, model string, now time.Time, reason string) modelRouteResponse {
	provider := "codex"
	if store != nil {
		if account := accountStateFor(ctx, store, pool.AuthIndex, now); account.Provider != "" {
			provider = account.Provider
		}
	}
	return modelRouteResponse{
		Handled:     true,
		TargetKind:  routeTargetProvider,
		Target:      provider,
		TargetModel: pool.ModelPrefix + "/" + strings.TrimSpace(model),
		Reason:      reason,
	}
}

// modelCatalog maps lower-case model ids CPA serves to their owner.
func modelCatalog() (map[string]string, bool) {
	poolState.mu.Lock()
	if !poolState.catalogAt.IsZero() && time.Since(poolState.catalogAt) < modelCatalogTTL {
		catalog, ok := poolState.catalog, poolState.catalogOK
		poolState.mu.Unlock()
		return catalog, ok
	}
	poolState.mu.Unlock()
	models, warnings := hostRegistryModelCatalog()
	catalog := map[string]string{}
	for _, model := range models {
		catalog[strings.ToLower(model.ID)] = model.OwnedBy
	}
	ok := len(catalog) > 0 && len(warnings) == 0
	poolState.mu.Lock()
	poolState.catalog, poolState.catalogOK, poolState.catalogAt = catalog, ok, time.Now()
	poolState.mu.Unlock()
	return catalog, ok
}

// ---------------------------------------------------------------------------
// Upstream accounts known to CPA

type hostAuthAccount struct {
	AuthIndex      string    `json:"auth_index"`
	Label          string    `json:"label"`
	Provider       string    `json:"provider"`
	AccountType    string    `json:"account_type,omitempty"`
	Status         string    `json:"status,omitempty"`
	Disabled       bool      `json:"disabled,omitempty"`
	Unavailable    bool      `json:"unavailable,omitempty"`
	NextRetryAfter time.Time `json:"-"`
}

func hostAuthAccounts() []hostAuthAccount {
	poolState.mu.Lock()
	if !poolState.hostAuthsAt.IsZero() && time.Since(poolState.hostAuthsAt) < hostAccountsTTL {
		out := poolState.hostAuths
		poolState.mu.Unlock()
		return out
	}
	poolState.mu.Unlock()
	var out []hostAuthAccount
	if result, err := callHost(methodHostAuthList, map[string]any{}); err == nil {
		var body struct {
			Files []struct {
				AuthIndex      string `json:"auth_index"`
				Name           string `json:"name"`
				Provider       string `json:"provider"`
				Type           string `json:"type"`
				Label          string `json:"label"`
				Email          string `json:"email"`
				AccountType    string `json:"account_type"`
				Status         string `json:"status"`
				Disabled       bool   `json:"disabled"`
				Unavailable    bool   `json:"unavailable"`
				NextRetryAfter string `json:"next_retry_after"`
			} `json:"files"`
		}
		if json.Unmarshal(result, &body) == nil {
			for _, file := range body.Files {
				if strings.TrimSpace(file.AuthIndex) == "" {
					continue
				}
				account := hostAuthAccount{
					AuthIndex:   strings.TrimSpace(file.AuthIndex),
					Label:       accountLabel(file.Label, file.Email, file.Name),
					Provider:    strings.ToLower(firstNonEmpty(file.Provider, file.Type)),
					AccountType: file.AccountType,
					Status:      file.Status,
					Disabled:    file.Disabled,
					Unavailable: file.Unavailable,
				}
				if at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(file.NextRetryAfter)); err == nil {
					account.NextRetryAfter = at
				}
				out = append(out, account)
			}
		}
	}
	poolState.mu.Lock()
	poolState.hostAuths, poolState.hostAuthsAt = out, time.Now()
	poolState.mu.Unlock()
	return out
}

// accountLabel never shows a full e-mail address; plugin pages get
// screenshotted and shared.
func accountLabel(label, email, name string) string {
	for _, value := range []string{label, email, name} {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		return maskEmail(value)
	}
	return ""
}

func maskEmail(value string) string {
	at := strings.Index(value, "@")
	if at <= 0 {
		return value
	}
	local := []rune(value[:at])
	keep := 2
	if len(local) <= 2 {
		keep = 1
	}
	return string(local[:keep]) + "***" + value[at:]
}

// ---------------------------------------------------------------------------
// Admin API

type poolMemberInput struct {
	KeyID        string   `json:"key_id"`
	SharePercent *float64 `json:"share_percent"`
	Role         string   `json:"role"`
}

type poolInput struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	AuthIndex       string            `json:"auth_index"`
	ModelPrefix     string            `json:"model_prefix"`
	Visibility      string            `json:"visibility"`
	Members         []poolMemberInput `json:"members"`
	Sub2PoolID      int64             `json:"sub2pool_account_id"`
	LendDuringBurst bool              `json:"lend_during_burst"`
	HideAccount     bool              `json:"hide_account"`
}

func adminPools(req managementRequest) ([]byte, error) {
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	_ = syncNativeKeysFromLoadedConfig()
	ctx := context.Background()
	now := time.Now()
	offset := viewerOffsetSeconds(req.Query.Get("tz_offset"))
	pools := loadedPools(ctx)
	quotaAccounts, _ := store.QuotaAccounts(ctx)
	accounts := mergeAccounts(hostAuthAccounts(), quotaAccounts, pools)
	keyRows := []map[string]any{}
	keys, _ := store.ListKeys(ctx)
	for _, key := range currentAdminKeyRows(keys, false) {
		keyRows = append(keyRows, map[string]any{"id": key.ID, "name": safeKeyDisplayName(key), "preview": key.Preview, "enabled": key.Enabled})
	}
	views := make([]map[string]any, 0, len(pools))
	for _, pool := range pools {
		views = append(views, adminPoolView(ctx, store, pool, now, offset))
	}
	_, catalogOK := modelCatalog()
	return jsonResponse(http.StatusOK, map[string]any{
		"ok":          true,
		"now":         now.Unix(),
		"pools":       views,
		"accounts":    accounts,
		"keys":        keyRows,
		"catalog_ok":  catalogOK,
		"tz_offset_s": offset,
	})
}

func mergeAccounts(hostAccounts []hostAuthAccount, quotaAccounts []policyplus.QuotaAccount, pools []policyplus.Pool) []map[string]any {
	byIndex := map[string]map[string]any{}
	order := []string{}
	add := func(authIndex string) map[string]any {
		if row, ok := byIndex[authIndex]; ok {
			return row
		}
		row := map[string]any{"auth_index": authIndex}
		byIndex[authIndex] = row
		order = append(order, authIndex)
		return row
	}
	for _, host := range hostAccounts {
		if host.Provider != "" && host.Provider != "codex" {
			continue
		}
		row := add(host.AuthIndex)
		row["label"] = host.Label
		row["provider"] = host.Provider
		row["account_type"] = host.AccountType
		row["status"] = host.Status
		row["disabled"] = host.Disabled
		row["unavailable"] = host.Unavailable
	}
	for _, account := range quotaAccounts {
		row := add(account.AuthIndex)
		if _, ok := row["provider"]; !ok {
			row["provider"] = account.Provider
		}
		row["plan_type"] = account.PlanType
		row["weekly"] = account.Weekly
		row["short"] = account.Short
		row["limit_reached"] = account.LimitReached
		row["observed_at_ms"] = account.ObservedAtMS
	}
	for _, pool := range pools {
		if row, ok := byIndex[pool.AuthIndex]; ok {
			row["pool_id"] = pool.ID
		}
	}
	out := make([]map[string]any, 0, len(order))
	for _, authIndex := range order {
		out = append(out, byIndex[authIndex])
	}
	return out
}

func adminPoolView(ctx context.Context, store *policyplus.Store, pool policyplus.Pool, now time.Time, offset int64) map[string]any {
	ledger := computePoolLedger(ctx, store, pool, now)
	pool = ledger.Pool
	account := ledger.Account
	warnings := []string{}
	keyIDs := make([]string, 0, len(ledger.Rows))
	for _, row := range ledger.Rows {
		keyIDs = append(keyIDs, row.KeyID)
	}
	loc := time.FixedZone("viewer", int(offset))
	local := now.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	// Seven whole viewer days (today last, its future hours empty) so the
	// page can draw a days x hours grid, and 30 days for the expanded strip.
	hourStart := today.AddDate(0, 0, -6)
	dayStart := today.AddDate(0, 0, -29)
	hours, _ := store.KeyHeatmap(ctx, keyIDs, hourStart, 168, time.Hour, offset)
	days, _ := store.KeyHeatmap(ctx, keyIDs, dayStart, 30, 24*time.Hour, offset)
	for i := range ledger.Rows {
		ledger.Rows[i].Heat = &poolHeat{Hours: roundSeries(hours[ledger.Rows[i].KeyID]), Days: roundSeries(days[ledger.Rows[i].KeyID])}
	}
	cycle := map[string]any{"status": account.Status}
	var weekLine []policyplus.LinePoint
	if account.Current.HasData() {
		current := account.Current
		cycle["reset_at"] = current.CycleResetAt
		cycle["window_minutes"] = current.WindowMinutes
		cycle["tracking_since_ms"] = current.TrackingSinceMS
		cycle["last_seen_ms"] = current.LastSeenMS
		cycle["ticks"] = len(current.Ticks)
		cycle["resolution"] = current.Resolution
		if ledger.InCycle {
			cycle["used_percent"] = ledger.UsedPercent
			cycle["pre_tracking_pp"] = ledger.PreTracking
			cycle["unattributed_pp"] = ledger.Unassigned
			cycle["outside_pp"] = ledger.OutsidePP
			cycle["lent_pp"] = ledger.LentPP
			weekLine = current.WeekLine
		}
		dayStartMS := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).UnixMilli()
		if today := policyplus.DayCapacity(account.Ledgers, dayStartMS, now.UnixMilli()+1); today.Known() {
			cycle["today"] = today
		}
	}
	cycle["capacity"] = account.Capacity
	if account.Latest != nil && account.Latest.Short != nil {
		cycle["short"] = account.Latest.Short
	}
	if account.Latest != nil {
		cycle["plan_type"] = account.Latest.PlanType
		cycle["limit_reached"] = account.Latest.LimitReached
	}
	if estimate, ok := sub2poolEstimate(pool.Sub2PoolAccountID); ok {
		cycle["sub2pool"] = estimate
	}
	var shareTotal float64
	for _, member := range pool.Members {
		if member.Shared() {
			shareTotal += *member.SharePercent
		}
	}
	notes := []string{}
	leaks := policyplus.AwayUsage{}
	if account.Current.HasData() && pool.AuthIndex != "" {
		// A member's request can only leak once the pool routes and they are in it.
		since := map[string]int64{}
		for _, member := range pool.Members {
			since[member.KeyID] = max(account.Current.TrackingSinceMS/1000, pool.RoutedSince, member.JoinedAt)
		}
		if away, err := store.MemberAwayUsage(ctx, since, pool.AuthIndex); err == nil {
			leaks = away
			if away.LeakRequests > 0 {
				warnings = append(warnings, fmt.Sprintf("本周期有 %d 次成员请求走了其他账号，路由没有固定到这个池。", away.LeakRequests))
			}
			if away.BorrowRequests > 0 {
				notes = append(notes, fmt.Sprintf("本周期有 %d 次爽蹬成员的请求借用了其他车，花费 %s，不占这个号的额度。", away.BorrowRequests, usdText(away.BorrowCost)))
			}
		}
	}
	if pool.LendDuringBurst && pool.ModelPrefix == "" {
		warnings = append(warnings, "打开了「借给其他车爽蹬的人」，但这个池没有设置模型前缀，借用的请求路由不过来。")
	}
	if pool.ModelPrefix != "" {
		if catalog, ok := modelCatalog(); !ok {
			warnings = append(warnings, "读不到 CPA 模型列表，暂时无法确认 "+pool.ModelPrefix+"/ 前缀模型，成员请求按原来的轮换走。")
		} else if !catalogHasPrefix(catalog, pool.ModelPrefix) {
			warnings = append(warnings, "CPA 里还没有 "+pool.ModelPrefix+"/ 开头的模型：请先给这个账号的凭证设置前缀 "+pool.ModelPrefix+"，之后成员请求才会固定走这个号。")
		}
	}
	// While another pool lends its account, bursting members of a full or
	// unusable account borrow it instead.
	lenderName := ""
	lending := false
	if lender, ok := pickLender(ctx, store, pool, now); ok {
		lenderName = lender.Name
		if blocked, until := accountBlocked(ctx, store, pool.AuthIndex, now); blocked {
			bursting := 0
			for _, member := range pool.Members {
				if _, on := pool.BurstOf(member.KeyID, now); on {
					bursting++
				}
			}
			if bursting > 0 {
				lending = true
				notes = append(notes, fmt.Sprintf("这个号现在用不了%s：%d 位爽蹬中的成员会借用「%s」，其他成员暂时用不了。", recoveryText(until), bursting, lender.Name))
			}
		}
	}
	for _, host := range hostAuthAccounts() {
		if host.AuthIndex != pool.AuthIndex || lending {
			continue
		}
		if host.Disabled || host.Unavailable || strings.EqualFold(host.Status, "error") {
			if pool.ModelPrefix != "" {
				warnings = append(warnings, "上游账号在 CPA 里当前不可用：固定路由的成员请求不会改走其他账号，会直接失败。")
			} else {
				warnings = append(warnings, "上游账号在 CPA 里当前不可用。")
			}
		}
	}
	if pool.AuthIndex == "" {
		warnings = append(warnings, "还没有选择上游账号。")
	} else if account.Status == "no_data" {
		warnings = append(warnings, "还没有观测到这个账号的周限百分比：等成员用它发出一次请求后开始记账。")
	}
	if shareTotal > 100+1e-9 {
		warnings = append(warnings, "份额合计超过 100%。")
	}
	settlements, _ := store.PoolSettlements(ctx, pool.ID, 6)
	return map[string]any{
		"id":                  pool.ID,
		"name":                pool.Name,
		"auth_index":          pool.AuthIndex,
		"model_prefix":        pool.ModelPrefix,
		"visibility":          pool.Visibility,
		"members":             pool.Members,
		"sub2pool_account_id": pool.Sub2PoolAccountID,
		"lend_during_burst":   pool.LendDuringBurst,
		"hide_account":        pool.HideAccount,
		"share_total":         shareTotal,
		"cycle":               cycle,
		"rows":                ledger.Rows,
		"heat":                map[string]any{"hours_start_ms": hourStart.UnixMilli(), "days_start_ms": dayStart.UnixMilli()},
		"week_line":           weekLine,
		"month_line":          policyplus.MonthLine(account.Ledgers, now, 30, offset),
		"leaks":               leaks,
		"settlements":         settlements,
		"warnings":            warnings,
		"notes":               notes,
		"lender":              lenderName,
	}
}

func catalogHasPrefix(catalog map[string]string, prefix string) bool {
	needle := strings.ToLower(prefix) + "/"
	for id := range catalog {
		if strings.HasPrefix(id, needle) {
			return true
		}
	}
	return false
}

func roundSeries(values []float64) []float64 {
	out := make([]float64, len(values))
	for i, value := range values {
		out[i] = math.Round(value*10000) / 10000
	}
	return out
}

func adminSavePools(req managementRequest) ([]byte, error) {
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	var body struct {
		Pools []poolInput `json:"pools"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_json"})
	}
	ctx := context.Background()
	known := keysByID(ctx, store)
	pools := make([]policyplus.Pool, 0, len(body.Pools))
	for _, input := range body.Pools {
		pool := policyplus.Pool{
			ID:                strings.ToLower(strings.TrimSpace(input.ID)),
			Name:              input.Name,
			AuthIndex:         input.AuthIndex,
			ModelPrefix:       input.ModelPrefix,
			Visibility:        input.Visibility,
			Sub2PoolAccountID: input.Sub2PoolID,
			LendDuringBurst:   input.LendDuringBurst,
			HideAccount:       input.HideAccount,
		}
		for _, member := range input.Members {
			keyID := strings.TrimSpace(member.KeyID)
			if _, ok := known[keyID]; !ok {
				return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "unknown_key", "message": "成员里有不存在的 Key：" + keyID})
			}
			pool.Members = append(pool.Members, policyplus.PoolMember{KeyID: keyID, SharePercent: member.SharePercent, Role: strings.TrimSpace(member.Role)})
		}
		pools = append(pools, pool)
	}
	if err := store.SavePools(ctx, pools); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_pools", "message": err.Error()})
	}
	invalidatePoolConfig()
	_ = store.SyncLendWindows(ctx, loadedPools(ctx), time.Now())
	return adminPools(req)
}

// adminPoolBurst turns one member's burst on or off.
func adminPoolBurst(req managementRequest) ([]byte, error) {
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	var body struct {
		PoolID   string `json:"pool_id"`
		KeyID    string `json:"key_id"`
		Action   string `json:"action"`
		Mode     string `json:"mode"`
		Notified bool   `json:"notified"`
		Confirm  bool   `json:"confirm"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_json"})
	}
	if !body.Confirm {
		return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "confirm_required", "message": "需要确认后才能操作爽蹬。"})
	}
	ctx := context.Background()
	invalidatePoolConfig()
	var pool policyplus.Pool
	found := false
	for _, candidate := range loadedPools(ctx) {
		if candidate.ID == strings.TrimSpace(body.PoolID) {
			pool, found = candidate, true
		}
	}
	if !found {
		return jsonResponse(http.StatusNotFound, map[string]any{"ok": false, "error": "pool_not_found"})
	}
	member, ok := pool.Member(strings.TrimSpace(body.KeyID))
	if !ok {
		return jsonResponse(http.StatusNotFound, map[string]any{"ok": false, "error": "member_not_found", "message": "这把 Key 不在这个拼车池里。"})
	}
	now := time.Now()
	var err error
	switch body.Action {
	case "start":
		if !member.Shared() {
			return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "not_a_share_member", "message": "只有按份额的成员才需要爽蹬：车主本来就不限，只固定路由的成员按自己的金额窗口。"})
		}
		if body.Mode == policyplus.BurstModeNoCarry && !body.Notified {
			return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "riders_not_notified", "message": "不结转爽蹬会占用其他车友的额度，需要先告知其他车友。"})
		}
		account := accountStateFor(ctx, store, pool.AuthIndex, now)
		if !account.InCycle(now) {
			return jsonResponse(http.StatusConflict, map[string]any{"ok": false, "error": "cycle_unknown", "message": "还没有观测到这个账号的当前周期，等下一次请求后再开启。"})
		}
		// Settle what earlier cycles left first.
		maybeSettleBursts(ctx, store, pool, account, now)
		err = store.StartMemberBurst(ctx, pool.ID, member.KeyID, body.Mode, account.Current.CycleResetAt, now)
	case "stop":
		err = store.StopMemberBurst(ctx, pool.ID, member.KeyID, now)
	default:
		return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_action"})
	}
	if err != nil {
		return jsonResponse(http.StatusConflict, map[string]any{"ok": false, "error": "burst_rejected", "message": err.Error()})
	}
	invalidatePoolConfig()
	return adminPools(req)
}

// ---------------------------------------------------------------------------
// Member API

func userPool(req managementRequest) ([]byte, error) {
	key, ok := keyFromSession(req)
	if !ok {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": "not_authenticated", "category": "auth", "message": "会话已过期，请重新登录。"})
	}
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	ctx := context.Background()
	pool, member, isMember := poolMembership(ctx, key.ID)
	if !isMember {
		return jsonResponse(http.StatusOK, map[string]any{"ok": true, "pool": nil})
	}
	now := time.Now()
	ledger := computePoolLedger(ctx, store, pool, now)
	pool = ledger.Pool
	account := ledger.Account
	accountView := map[string]any{"status": account.Status}
	if account.Current.HasData() {
		accountView["reset_at"] = account.Current.CycleResetAt
		accountView["last_seen_ms"] = account.Current.LastSeenMS
		if ledger.InCycle {
			accountView["used_percent"] = ledger.UsedPercent
		}
	}
	if account.Latest != nil {
		accountView["plan_type"] = account.Latest.PlanType
		accountView["limit_reached"] = account.Latest.LimitReached
		if account.Latest.Short != nil {
			accountView["short"] = account.Latest.Short
		}
	}
	// A pool that hides the account shows members their own share only; the
	// owner still sees everything.
	hidden := pool.HideAccount && !member.Owner()
	if hidden {
		delete(accountView, "used_percent")
		delete(accountView, "short")
		delete(accountView, "limit_reached")
	}
	if full, until := accountBlocked(ctx, store, pool.AuthIndex, now); full {
		accountView["full"] = true
		if until > 0 {
			accountView["full_until"] = until
		}
	}
	capacity := map[string]any{"basis": account.Capacity.Basis}
	if account.Capacity.Known() {
		capacity["usd"] = account.Capacity.USD
		capacity["per_pp"] = account.Capacity.USD / 100
		capacity["low"] = account.Capacity.Low
		capacity["high"] = account.Capacity.High
	}
	var me map[string]any
	team := []map[string]any{}
	// Usage not listed by name: other people (hidden members and keys outside
	// the pool), bursting members of other pools borrowing the account, and
	// usage no key is charged for (before tracking began, or outside CPA).
	others, lent, untracked := 0.0, 0.0, 0.0
	letter := 0
	for _, row := range memberRowsInStableOrder(ledger.Rows) {
		switch row.Role {
		case "outside":
			others += row.AttributedPP
			continue
		case "borrowed":
			lent += row.AttributedPP
			continue
		}
		self := row.KeyID == key.ID
		view := memberRowView(row)
		switch {
		case self:
			view["label"] = "你"
			view["self"] = true
			me = view
		case row.Role == "owner":
			view["label"] = "车主"
		case row.Role == "pinned" && pool.Visibility != policyplus.PoolVisibilityNamed:
			view["label"] = "固定路由"
		case pool.Visibility == policyplus.PoolVisibilityNamed:
			view["label"] = row.Label
		default:
			view["label"] = "车友 " + string(rune('A'+letter%26))
			letter++
		}
		if hidden && !self {
			continue
		}
		if pool.Visibility == policyplus.PoolVisibilitySelf && !self {
			others += row.UsedPP
			continue
		}
		team = append(team, view)
	}
	if ledger.InCycle {
		untracked = ledger.PreTracking + ledger.Unassigned
	}
	if hidden {
		others, lent, untracked = 0, 0, 0
	}
	var borrowView map[string]any
	if borrow, ok := borrowFor(ctx, store, pool, member, now); ok {
		borrowView = map[string]any{
			"active": true,
			"reason": borrow.Reason,
			"lender": borrow.Lender.Name,
			"until":  borrow.Until,
		}
		if row, ok := computePoolLedger(ctx, store, borrow.Lender, now).row(key.ID); ok {
			borrowView["used_pp"] = row.UsedPP
			borrowView["cost_usd"] = row.CostUSD
		}
	}
	return jsonResponse(http.StatusOK, map[string]any{
		"ok": true,
		"pool": map[string]any{
			"name":           pool.Name,
			"visibility":     pool.Visibility,
			"account":        accountView,
			"capacity":       capacity,
			"me":             me,
			"team":           team,
			"others_pp":      others,
			"lent_pp":        lent,
			"untracked_pp":   untracked,
			"borrow":         borrowView,
			"in_cycle":       ledger.InCycle,
			"account_hidden": hidden,
		},
	})
}

func memberRowsInStableOrder(rows []poolRow) []poolRow {
	out := append([]poolRow(nil), rows...)
	rank := func(row poolRow) int {
		switch row.Role {
		case "owner":
			return 0
		case "member":
			return 1
		}
		return 2
	}
	sort.SliceStable(out, func(i, j int) bool {
		if rank(out[i]) != rank(out[j]) {
			return rank(out[i]) < rank(out[j])
		}
		return policyplus.SHA256Hex(out[i].KeyID) < policyplus.SHA256Hex(out[j].KeyID)
	})
	return out
}

func memberRowView(row poolRow) map[string]any {
	return map[string]any{
		"role":          row.Role,
		"share_percent": row.SharePercent,
		"carry_pp":      row.CarryPP,
		"available_pp":  row.AvailablePP,
		"used_pp":       row.UsedPP,
		"attributed_pp": row.AttributedPP,
		"pending_pp":    row.PendingPP,
		"remaining_pp":  row.RemainingPP,
		"remaining_usd": row.RemainingUSD,
		"state":         row.State,
		"burst":         memberBurstView(row.Burst),
	}
}

// memberBurstView describes a burst for the member page, without the key id.
func memberBurstView(burst *policyplus.MemberBurst) map[string]any {
	if burst == nil {
		return nil
	}
	return map[string]any{
		"active":         burst.Active(time.Now()),
		"mode":           burst.Mode,
		"started_at":     burst.StartedAt,
		"cycle_reset_at": burst.CycleResetAt,
		"stopped_at":     burst.StoppedAt,
	}
}
