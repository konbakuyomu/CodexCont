package policyplus

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Carpool accounting.
//
// A pool is a group of keys sharing one upstream subscription account (one CPA
// credential, identified by auth_index). The account exposes a weekly quota
// as an integer "used percent" in the x-codex-* response headers. Members hold
// a contract share of that weekly quota in percentage points (pp); the owner
// holds no share and is never limited.
//
// Consumption is attributed hop by hop: each time the observed weekly percent
// rises, the rise is split among keys in proportion to the cost they finished
// since the previous rise. Cost after the latest rise is pending and is
// converted to pp with the cycle's capacity estimate (USD per 100%). Nothing is
// persisted except observations, raw usage, member bursts and their
// settlements, so the
// ledger is always recomputed from facts.

const (
	WeeklyWindowMinutes = 10080

	PoolVisibilityNamed     = "named"
	PoolVisibilityAnonymous = "anonymous"
	PoolVisibilitySelf      = "self"

	BurstModeCarry   = "carry"
	BurstModeNoCarry = "no_carry"

	// cycleMatchSeconds groups observations whose reset time differs by
	// less than this into one cycle (reset-after-seconds jitters).
	cycleMatchSeconds = 3600
)

type QuotaWindow struct {
	Minutes int64   `json:"window_minutes"`
	Used    float64 `json:"used_percent"`
	ResetAt int64   `json:"reset_at"`
}

// QuotaSignal is one passive quota snapshot taken from an upstream response.
type QuotaSignal struct {
	AuthIndex    string
	AuthID       string
	Provider     string
	PlanType     string
	ObservedAtMS int64
	LimitReached bool
	Weekly       *QuotaWindow
	Short        *QuotaWindow
}

// ParseCodexQuotaHeaders reads the Codex rate-limit headers. The weekly window
// is chosen by length, never by slot: Pro reports it as primary, Plus as
// secondary next to a 5-hour primary window.
func ParseCodexQuotaHeaders(headers http.Header, observedAt time.Time) (QuotaSignal, bool) {
	if len(headers) == 0 {
		return QuotaSignal{}, false
	}
	values := make(map[string]string, len(headers))
	for key, list := range headers {
		lower := strings.ToLower(strings.TrimSpace(key))
		if !strings.HasPrefix(lower, "x-codex-") || len(list) == 0 {
			continue
		}
		values[lower] = strings.TrimSpace(list[len(list)-1])
	}
	if len(values) == 0 {
		return QuotaSignal{}, false
	}
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	sig := QuotaSignal{
		ObservedAtMS: observedAt.UnixMilli(),
		PlanType:     strings.ToLower(boundedHeaderValue(values["x-codex-plan-type"], 32)),
		LimitReached: truthyHeader(values["x-codex-limit-reached"]),
	}
	var windows []QuotaWindow
	for _, slot := range []string{"primary", "secondary"} {
		prefix := "x-codex-" + slot + "-"
		used, ok := parseHeaderFloat(values[prefix+"used-percent"])
		if !ok || used < 0 || used > 1000 {
			continue
		}
		minutes, _ := parseHeaderFloat(values[prefix+"window-minutes"])
		resetAt := parseResetAt(values[prefix+"reset-at"])
		if resetAt == 0 {
			if after, ok := parseHeaderFloat(values[prefix+"reset-after-seconds"]); ok && after > 0 {
				resetAt = observedAt.Unix() + int64(math.Round(after))
			}
		}
		windows = append(windows, QuotaWindow{Minutes: int64(math.Round(minutes)), Used: used, ResetAt: resetAt})
	}
	weeklyIndex := -1
	for i, window := range windows {
		distance := absInt64(window.Minutes - WeeklyWindowMinutes)
		if distance > 1440 {
			continue
		}
		if weeklyIndex < 0 || distance < absInt64(windows[weeklyIndex].Minutes-WeeklyWindowMinutes) {
			weeklyIndex = i
		}
	}
	for i := range windows {
		window := windows[i]
		if i == weeklyIndex {
			sig.Weekly = &window
		} else if window.Minutes > 0 && sig.Short == nil {
			sig.Short = &window
		}
	}
	if sig.Weekly == nil && sig.Short == nil && sig.PlanType == "" {
		return QuotaSignal{}, false
	}
	return sig, true
}

func boundedHeaderValue(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		return value[:limit]
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return ""
		}
	}
	return value
}

func truthyHeader(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

func parseHeaderFloat(value string) (float64, bool) {
	value = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "%"))
	if value == "" {
		return 0, false
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	return number, true
}

func parseResetAt(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if number, ok := parseHeaderFloat(value); ok && number > 0 {
		if number > 1e12 {
			number /= 1000
		}
		return int64(number)
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.Unix()
	}
	return 0
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

// QuotaAccount is the latest snapshot of one upstream credential.
type QuotaAccount struct {
	AuthIndex    string       `json:"auth_index"`
	AuthID       string       `json:"-"`
	Provider     string       `json:"provider,omitempty"`
	PlanType     string       `json:"plan_type,omitempty"`
	Weekly       *QuotaWindow `json:"weekly,omitempty"`
	Short        *QuotaWindow `json:"short,omitempty"`
	LimitReached bool         `json:"limit_reached,omitempty"`
	ObservedAtMS int64        `json:"observed_at_ms"`
}

type QuotaObservation struct {
	CycleResetAt  int64
	UsedPercent   float64
	ObservedAtMS  int64
	WindowMinutes int64
}

// RecordQuotaSignal stores the latest snapshot and the first time each weekly
// percent value was seen in its cycle. It reports whether a value appeared for
// the first time, which is when cached ledgers need recomputing.
func (s *Store) RecordQuotaSignal(ctx context.Context, sig QuotaSignal) (bool, error) {
	authIndex := strings.TrimSpace(sig.AuthIndex)
	if s == nil || s.db == nil || authIndex == "" || sig.ObservedAtMS <= 0 {
		return false, nil
	}
	now := time.Now().Unix()
	weeklyMinutes, weeklyReset, shortMinutes, shortReset := int64(0), int64(0), int64(0), int64(0)
	var weeklyUsed, shortUsed any
	if sig.Weekly != nil {
		weeklyMinutes, weeklyReset, weeklyUsed = sig.Weekly.Minutes, sig.Weekly.ResetAt, sig.Weekly.Used
	}
	if sig.Short != nil {
		shortMinutes, shortReset, shortUsed = sig.Short.Minutes, sig.Short.ResetAt, sig.Short.Used
	}
	if _, err := s.db.ExecContext(ctx, `insert into quota_accounts(
			auth_index, auth_id, provider, plan_type, weekly_minutes, weekly_used, weekly_reset_at,
			short_minutes, short_used, short_reset_at, limit_reached, observed_at_ms, updated_at
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		on conflict(auth_index) do update set
			auth_id=case when excluded.auth_id != '' then excluded.auth_id else quota_accounts.auth_id end,
			provider=case when excluded.provider != '' then excluded.provider else quota_accounts.provider end,
			plan_type=case when excluded.plan_type != '' then excluded.plan_type else quota_accounts.plan_type end,
			weekly_minutes=case when excluded.weekly_used is not null then excluded.weekly_minutes else quota_accounts.weekly_minutes end,
			weekly_used=coalesce(excluded.weekly_used, quota_accounts.weekly_used),
			weekly_reset_at=case when excluded.weekly_used is not null then excluded.weekly_reset_at else quota_accounts.weekly_reset_at end,
			short_minutes=case when excluded.short_used is not null then excluded.short_minutes else quota_accounts.short_minutes end,
			short_used=coalesce(excluded.short_used, quota_accounts.short_used),
			short_reset_at=case when excluded.short_used is not null then excluded.short_reset_at else quota_accounts.short_reset_at end,
			limit_reached=excluded.limit_reached,
			observed_at_ms=excluded.observed_at_ms,
			updated_at=excluded.updated_at
		where excluded.observed_at_ms >= quota_accounts.observed_at_ms`,
		authIndex, strings.TrimSpace(sig.AuthID), strings.ToLower(strings.TrimSpace(sig.Provider)), sig.PlanType,
		weeklyMinutes, weeklyUsed, weeklyReset, shortMinutes, shortUsed, shortReset,
		boolInt(sig.LimitReached), sig.ObservedAtMS, now,
	); err != nil {
		return false, err
	}
	if sig.Weekly == nil || sig.Weekly.ResetAt <= 0 {
		return false, nil
	}
	cycle, err := s.resolveCycle(ctx, authIndex, sig.Weekly.ResetAt)
	if err != nil {
		return false, err
	}
	var existing sql.NullInt64
	err = s.db.QueryRowContext(ctx, `select observed_at_ms from quota_observations
		where auth_index=? and cycle_reset_at=? and used_percent=?`, authIndex, cycle, sig.Weekly.Used).Scan(&existing)
	switch {
	case err == sql.ErrNoRows:
		_, err = s.db.ExecContext(ctx, `insert into quota_observations(auth_index, cycle_reset_at, used_percent, observed_at_ms, window_minutes)
			values (?, ?, ?, ?, ?)`, authIndex, cycle, sig.Weekly.Used, sig.ObservedAtMS, sig.Weekly.Minutes)
		return err == nil, err
	case err != nil:
		return false, err
	case sig.ObservedAtMS < existing.Int64:
		// A slower request can report an older snapshot after a newer one; the
		// value was really first seen at the earlier moment.
		_, err = s.db.ExecContext(ctx, `update quota_observations set observed_at_ms=?
			where auth_index=? and cycle_reset_at=? and used_percent=?`, sig.ObservedAtMS, authIndex, cycle, sig.Weekly.Used)
		return err == nil, err
	}
	return false, nil
}

func (s *Store) resolveCycle(ctx context.Context, authIndex string, resetAt int64) (int64, error) {
	var cycle int64
	err := s.db.QueryRowContext(ctx, `select cycle_reset_at from quota_observations
		where auth_index=? and cycle_reset_at between ? and ?
		order by abs(cycle_reset_at - ?) asc limit 1`,
		authIndex, resetAt-cycleMatchSeconds, resetAt+cycleMatchSeconds, resetAt).Scan(&cycle)
	if err == sql.ErrNoRows {
		return resetAt, nil
	}
	return cycle, err
}

func (s *Store) QuotaAccounts(ctx context.Context) ([]QuotaAccount, error) {
	rows, err := s.db.QueryContext(ctx, `select auth_index, auth_id, provider, plan_type,
		weekly_minutes, weekly_used, weekly_reset_at, short_minutes, short_used, short_reset_at,
		limit_reached, observed_at_ms from quota_accounts order by auth_index`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QuotaAccount
	for rows.Next() {
		var account QuotaAccount
		var weeklyMinutes, weeklyReset, shortMinutes, shortReset int64
		var weeklyUsed, shortUsed sql.NullFloat64
		var limitReached int
		if err := rows.Scan(&account.AuthIndex, &account.AuthID, &account.Provider, &account.PlanType,
			&weeklyMinutes, &weeklyUsed, &weeklyReset, &shortMinutes, &shortUsed, &shortReset,
			&limitReached, &account.ObservedAtMS); err != nil {
			return nil, err
		}
		if weeklyUsed.Valid {
			account.Weekly = &QuotaWindow{Minutes: weeklyMinutes, Used: weeklyUsed.Float64, ResetAt: weeklyReset}
		}
		if shortUsed.Valid {
			account.Short = &QuotaWindow{Minutes: shortMinutes, Used: shortUsed.Float64, ResetAt: shortReset}
		}
		account.LimitReached = limitReached != 0
		out = append(out, account)
	}
	return out, rows.Err()
}

// QuotaObservations returns the observations of every cycle that was still
// running at or after sinceUnix, oldest first.
func (s *Store) QuotaObservations(ctx context.Context, authIndex string, sinceUnix int64) ([]QuotaObservation, error) {
	rows, err := s.db.QueryContext(ctx, `select cycle_reset_at, used_percent, observed_at_ms, window_minutes
		from quota_observations where auth_index=? and cycle_reset_at >= ?
		order by cycle_reset_at asc, observed_at_ms asc, used_percent asc`, strings.TrimSpace(authIndex), sinceUnix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QuotaObservation
	for rows.Next() {
		var item QuotaObservation
		if err := rows.Scan(&item.CycleResetAt, &item.UsedPercent, &item.ObservedAtMS, &item.WindowMinutes); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// CostEvent is one settled request cost on an upstream account.
type CostEvent struct {
	KeyID string
	AtMS  int64
	Cost  float64
}

// AuthCostEvents returns settled costs finished on authIndex in (fromMS, toMS].
func (s *Store) AuthCostEvents(ctx context.Context, authIndex string, fromMS, toMS int64) ([]CostEvent, error) {
	rows, err := s.db.QueryContext(ctx, `select coalesce(key_id, ''), consumed_at_ms, coalesce(cost, 0)
		from usage_events where auth_index=? and consumed_at_ms > ? and consumed_at_ms <= ?
		and coalesce(billing_pending, 0) = 0 and coalesce(cost, 0) > 0
		order by consumed_at_ms asc, id asc`, strings.TrimSpace(authIndex), fromMS, toMS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CostEvent
	for rows.Next() {
		var item CostEvent
		if err := rows.Scan(&item.KeyID, &item.AtMS, &item.Cost); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// AwayUsage counts a pool's member requests that another account served.
type AwayUsage struct {
	LeakRequests   int64   `json:"requests"`
	LeakCost       float64 `json:"cost"`
	BorrowRequests int64   `json:"borrow_requests"`
	BorrowCost     float64 `json:"borrow_cost"`
}

// lendWindowEnd is when a lend window closes; an open window with no end
// date (until_at 0) runs until lending is turned off.
const lendWindowEnd = `case when w.ended_at > 0 then w.ended_at when w.until_at > 0 then w.until_at else 9223372036854775807 end`

// inLendWindow matches usage_events rows served while their account was lent
// out to bursting members of other pools.
const inLendWindow = `exists (select 1 from pool_lend_windows w where w.auth_index = usage_events.auth_index
	and usage_events.requested_at >= w.started_at
	and usage_events.requested_at < ` + lendWindowEnd + `)`

// MemberAwayUsage counts Codex requests by the given keys, each since its own
// unix time, that an account other than authIndex served. Requests served by
// an account while it was lent out are borrows; the rest mean routing is
// leaking, usually because the CPA credential prefix is not set.
func (s *Store) MemberAwayUsage(ctx context.Context, since map[string]int64, authIndex string) (AwayUsage, error) {
	var out AwayUsage
	if len(since) == 0 || strings.TrimSpace(authIndex) == "" {
		return out, nil
	}
	keyIDs := make([]string, 0, len(since))
	for id := range since {
		keyIDs = append(keyIDs, id)
	}
	sort.Strings(keyIDs)
	args := []any{strings.TrimSpace(authIndex)}
	marks := make([]string, 0, len(keyIDs))
	for _, id := range keyIDs {
		marks = append(marks, "(key_id = ? and requested_at >= ?)")
		args = append(args, id, since[id])
	}
	err := s.db.QueryRowContext(ctx, `select
			coalesce(sum(case when `+inLendWindow+` then 0 else 1 end), 0),
			coalesce(sum(case when not `+inLendWindow+` and coalesce(billing_pending, 0) = 0 then cost else 0 end), 0),
			coalesce(sum(case when `+inLendWindow+` then 1 else 0 end), 0),
			coalesce(sum(case when `+inLendWindow+` and coalesce(billing_pending, 0) = 0 then cost else 0 end), 0)
		from usage_events where auth_index != '' and auth_index != ?
		and lower(coalesce(provider, '')) = 'codex' and (`+strings.Join(marks, " or ")+`)`, args...).
		Scan(&out.LeakRequests, &out.LeakCost, &out.BorrowRequests, &out.BorrowCost)
	return out, err
}

// LendWindowKeys lists the keys whose requests authIndex served since fromUnix
// while it was lent out.
func (s *Store) LendWindowKeys(ctx context.Context, authIndex string, fromUnix int64) (map[string]bool, error) {
	out := map[string]bool{}
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return out, nil
	}
	// Most accounts are never lent out; skip the usage scan for them.
	var windows int
	if err := s.db.QueryRowContext(ctx, `select count(*) from pool_lend_windows w where w.auth_index = ?
		and `+lendWindowEnd+` > ?`, authIndex, fromUnix).Scan(&windows); err != nil || windows == 0 {
		return out, err
	}
	// A request finishes after it starts, so consumed_at_ms bounds the scan
	// through the (auth_index, consumed_at_ms) index.
	rows, err := s.db.QueryContext(ctx, `select distinct key_id from usage_events
		where auth_index = ? and consumed_at_ms >= ? and requested_at >= ? and coalesce(key_id, '') != '' and `+inLendWindow,
		authIndex, fromUnix*1000, fromUnix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var keyID string
		if err := rows.Scan(&keyID); err != nil {
			return nil, err
		}
		out[keyID] = true
	}
	return out, rows.Err()
}

// Lending reports whether the pool lends its account to bursting members of
// other pools.
func (p Pool) Lending() bool {
	return p.LendDuringBurst && strings.TrimSpace(p.AuthIndex) != ""
}

// SyncLendWindows records when each pool's account is lent out: a window opens
// when lending is turned on and closes when it is turned off or the pool moves
// to another account.
func (s *Store) SyncLendWindows(ctx context.Context, pools []Pool, now time.Time) error {
	lending := map[string]Pool{}
	for _, pool := range pools {
		if pool.Lending() {
			lending[pool.ID] = pool
		}
	}
	type window struct {
		poolID, authIndex string
		startedAt, until  int64
	}
	rows, err := s.db.QueryContext(ctx, `select pool_id, auth_index, started_at, until_at from pool_lend_windows where ended_at = 0`)
	if err != nil {
		return err
	}
	var open []window
	for rows.Next() {
		var w window
		if err := rows.Scan(&w.poolID, &w.authIndex, &w.startedAt, &w.until); err != nil {
			rows.Close()
			return err
		}
		open = append(open, w)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, w := range open {
		if pool, ok := lending[w.poolID]; ok && strings.TrimSpace(pool.AuthIndex) == w.authIndex && w.until == 0 {
			delete(lending, w.poolID)
			continue
		}
		end := now.Unix()
		if w.until > 0 && w.until < end {
			end = w.until
		}
		if _, err := s.db.ExecContext(ctx, `update pool_lend_windows set ended_at=? where pool_id=? and started_at=?`, end, w.poolID, w.startedAt); err != nil {
			return err
		}
	}
	for _, pool := range lending {
		if _, err := s.db.ExecContext(ctx, `insert or replace into pool_lend_windows(pool_id, auth_index, started_at, until_at, ended_at)
			values (?, ?, ?, 0, 0)`, pool.ID, strings.TrimSpace(pool.AuthIndex), now.Unix()); err != nil {
			return err
		}
	}
	return nil
}

// KeyHeatmap sums each key's settled cost into fixed buckets aligned to the
// viewer clock (offsetSeconds east of UTC). Reset watermarks are ignored: it
// describes activity, not quota.
func (s *Store) KeyHeatmap(ctx context.Context, keyIDs []string, from time.Time, buckets int, bucket time.Duration, offsetSeconds int64) (map[string][]float64, error) {
	out := make(map[string][]float64, len(keyIDs))
	size := int64(bucket / time.Second)
	if len(keyIDs) == 0 || buckets <= 0 || size <= 0 {
		return out, nil
	}
	start := from.Unix()
	start = start + offsetSeconds - (((start+offsetSeconds)%size)+size)%size - offsetSeconds
	end := start + size*int64(buckets)
	args := []any{start, end}
	marks := make([]string, 0, len(keyIDs))
	for _, id := range keyIDs {
		out[id] = make([]float64, buckets)
		marks = append(marks, "?")
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `select key_id, requested_at, coalesce(cost, 0) from usage_events
		where requested_at >= ? and requested_at < ? and coalesce(billing_pending, 0) = 0
		and key_id in (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var keyID string
		var ts int64
		var cost float64
		if err := rows.Scan(&keyID, &ts, &cost); err != nil {
			return nil, err
		}
		index := int((ts - start) / size)
		if series, ok := out[keyID]; ok && index >= 0 && index < buckets {
			series[index] += cost
		}
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Pools

// PoolRolePinned marks a member who is only routed to the pool account: no
// share, not an owner, still bound by the key's own USD windows.
const PoolRolePinned = "pinned"

type PoolMember struct {
	KeyID        string   `json:"key_id"`
	SharePercent *float64 `json:"share_percent"`
	Role         string   `json:"role,omitempty"`
	// JoinedAt is when the key joined this pool (unix seconds).
	JoinedAt int64 `json:"joined_at,omitempty"`
}

// Owner reports an owner: no share and never limited.
func (m PoolMember) Owner() bool { return m.SharePercent == nil && m.Role != PoolRolePinned }

// Pinned reports a member who only rides the pool account.
func (m PoolMember) Pinned() bool { return m.Role == PoolRolePinned }

// Shared reports a member governed by a share of the pool account.
func (m PoolMember) Shared() bool { return m.SharePercent != nil && m.Role != PoolRolePinned }

type Pool struct {
	ID                string       `json:"id"`
	Name              string       `json:"name"`
	AuthIndex         string       `json:"auth_index"`
	ModelPrefix       string       `json:"model_prefix"`
	Visibility        string       `json:"visibility"`
	Members           []PoolMember `json:"members"`
	Sub2PoolAccountID int64        `json:"sub2pool_account_id,omitempty"`
	// LendDuringBurst lends this account to bursting members of other pools
	// while their own account is full or cooling down.
	LendDuringBurst bool `json:"lend_during_burst,omitempty"`
	// HideAccount shows members only their own share, not the account's
	// overall usage or anyone else's.
	HideAccount bool `json:"hide_account,omitempty"`
	// RoutedSince is when the pool last got its account or model prefix; member
	// requests before it could not have been pinned.
	RoutedSince int64 `json:"routed_since,omitempty"`
	// Provider is the CPA provider of the pool account ("" means codex). A key
	// may ride one pool per provider, since each serves different models.
	Provider string `json:"provider,omitempty"`
	// BudgetUSD makes the pool meter a weekly dollar budget instead of the
	// account's own quota signal, for accounts that report none (xai). Usage
	// is the account's settled cost against the budget; cycles are 7 days
	// long and end at BudgetResetAt plus a whole number of weeks.
	BudgetUSD     *float64 `json:"budget_usd,omitempty"`
	BudgetResetAt int64    `json:"budget_reset_at,omitempty"`
	UpdatedAt     int64    `json:"updated_at"`
	// Bursts are the members' bursts not yet settled, current cycle or past.
	Bursts []MemberBurst `json:"-"`
}

// ProviderName is the pool account's provider, codex when unset.
func (p Pool) ProviderName() string {
	if provider := strings.ToLower(strings.TrimSpace(p.Provider)); provider != "" {
		return provider
	}
	return "codex"
}

// Budgeted reports a pool metered against a weekly dollar budget.
func (p Pool) Budgeted() bool { return p.BudgetUSD != nil && *p.BudgetUSD > 0 }

// BudgetCycleSeconds is the length of a budget pool's cycle.
const BudgetCycleSeconds = 7 * 24 * 3600

// BudgetCycleEnd is when the budget cycle running at now ends.
func BudgetCycleEnd(anchor, now int64) int64 {
	if anchor <= 0 {
		return 0
	}
	d := now - anchor
	n := d / BudgetCycleSeconds
	if d%BudgetCycleSeconds != 0 && d < 0 {
		n--
	}
	return anchor + (n+1)*BudgetCycleSeconds
}

// DefaultBudgetResetAt is next Monday 00:00 Beijing time.
func DefaultBudgetResetAt(now time.Time) int64 {
	loc := time.FixedZone("Asia/Shanghai", 8*3600)
	local := now.In(loc)
	days := (8 - int(local.Weekday())) % 7
	if days == 0 {
		days = 7
	}
	next := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, days)
	return next.Unix()
}

// BudgetObservations turns one budget cycle's settled costs into the quota
// observations an account with a real signal would have produced: the cycle
// starts at 0% and every cost moves it by 100*cost/budget points. Costs are
// merged per timestamp so each rise carries its own cost.
func BudgetObservations(budget float64, cycleEnd int64, events []CostEvent) []QuotaObservation {
	if budget <= 0 || cycleEnd <= 0 {
		return nil
	}
	startMS := (cycleEnd - BudgetCycleSeconds) * 1000
	endMS := cycleEnd * 1000
	minutes := int64(BudgetCycleSeconds / 60)
	obs := []QuotaObservation{{CycleResetAt: cycleEnd, UsedPercent: 0, ObservedAtMS: startMS, WindowMinutes: minutes}}
	sorted := append([]CostEvent(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].AtMS < sorted[j].AtMS })
	used := 0.0
	for i := 0; i < len(sorted); {
		at := sorted[i].AtMS
		cost := 0.0
		for i < len(sorted) && sorted[i].AtMS == at {
			cost += sorted[i].Cost
			i++
		}
		if at <= startMS || at > endMS || cost <= 0 {
			continue
		}
		used += 100 * cost / budget
		obs = append(obs, QuotaObservation{CycleResetAt: cycleEnd, UsedPercent: used, ObservedAtMS: at, WindowMinutes: minutes})
	}
	return obs
}

// BurstOf returns the member's running burst.
func (p Pool) BurstOf(keyID string, now time.Time) (MemberBurst, bool) {
	for _, burst := range p.Bursts {
		if burst.KeyID == keyID && burst.Active(now) {
			return burst, true
		}
	}
	return MemberBurst{}, false
}

// MemberBurst lets one share member use the pool account past their share
// until the account's weekly cycle resets. In carry mode what they use beyond
// their share is paid back from their next cycle to the members who left
// share unused; in no-carry mode it is not.
type MemberBurst struct {
	KeyID        string `json:"key_id"`
	Mode         string `json:"mode"`
	StartedAt    int64  `json:"started_at"`
	CycleResetAt int64  `json:"cycle_reset_at"`
	// StoppedAt is set when the burst was turned off before the cycle ended.
	StoppedAt int64 `json:"stopped_at,omitempty"`
}

func (b MemberBurst) Active(now time.Time) bool {
	return b.StoppedAt == 0 && now.Unix() < b.CycleResetAt
}

func (p Pool) Member(keyID string) (PoolMember, bool) {
	for _, member := range p.Members {
		if member.KeyID == keyID {
			return member, true
		}
	}
	return PoolMember{}, false
}

var (
	poolIDPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	poolPrefixPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)
)

func NormalizePoolVisibility(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case PoolVisibilityNamed:
		return PoolVisibilityNamed
	case PoolVisibilitySelf:
		return PoolVisibilitySelf
	default:
		return PoolVisibilityAnonymous
	}
}

// ValidatePools checks a complete pool configuration.
func ValidatePools(pools []Pool) error {
	seenPool := map[string]bool{}
	seenKey := map[string]string{}
	seenAuth := map[string]string{}
	for _, pool := range pools {
		if !poolIDPattern.MatchString(pool.ID) {
			return fmt.Errorf("拼车池 ID 只能使用小写字母、数字、- 和 _：%q", pool.ID)
		}
		if seenPool[pool.ID] {
			return fmt.Errorf("拼车池 ID 重复：%s", pool.ID)
		}
		seenPool[pool.ID] = true
		name := strings.TrimSpace(pool.Name)
		if name == "" || len([]rune(name)) > 40 {
			return fmt.Errorf("拼车池名称需要 1 到 40 个字：%s", pool.ID)
		}
		if prefix := strings.TrimSpace(pool.ModelPrefix); prefix != "" && !poolPrefixPattern.MatchString(prefix) {
			return fmt.Errorf("模型前缀只能使用字母、数字、.、- 和 _，不能包含 /：%q", prefix)
		}
		if auth := strings.TrimSpace(pool.AuthIndex); auth != "" {
			if other, ok := seenAuth[auth]; ok {
				return fmt.Errorf("同一个上游账号不能同时属于 %s 和 %s", other, pool.ID)
			}
			seenAuth[auth] = pool.ID
		}
		if pool.BudgetUSD != nil {
			budget := *pool.BudgetUSD
			if math.IsNaN(budget) || math.IsInf(budget, 0) || budget <= 0 || budget > 1e6 {
				return fmt.Errorf("拼车池 %s 的每周预算需要大于 0", pool.ID)
			}
		}
		if strings.TrimSpace(pool.AuthIndex) != "" && pool.ProviderName() != "codex" && !pool.Budgeted() {
			return fmt.Errorf("拼车池 %s 的账号没有额度信号，需要填写每周预算", pool.ID)
		}
		total := 0.0
		for _, member := range pool.Members {
			keyID := strings.TrimSpace(member.KeyID)
			if keyID == "" {
				return fmt.Errorf("拼车池 %s 有空的成员 Key", pool.ID)
			}
			slot := pool.ProviderName() + "\x00" + keyID
			if other, ok := seenKey[slot]; ok {
				return fmt.Errorf("一把 Key 在同一类账号里只能加入一个拼车池（已在 %s）", other)
			}
			seenKey[slot] = pool.ID
			if member.SharePercent == nil {
				continue
			}
			share := *member.SharePercent
			if math.IsNaN(share) || share < 0 || share > 100 {
				return fmt.Errorf("份额需要在 0 到 100 之间")
			}
			total += share
		}
		if total > 100+1e-9 {
			return fmt.Errorf("拼车池 %s 的份额合计 %.2f%%，超过 100%%", pool.ID, total)
		}
	}
	return nil
}

func (s *Store) ListPools(ctx context.Context) ([]Pool, error) {
	rows, err := s.db.QueryContext(ctx, `select id, name, auth_index, model_prefix, visibility, coalesce(sub2pool_account_id, 0),
		coalesce(lend_during_burst, 0), coalesce(hide_account, 0), case when coalesce(routed_since, 0) > 0 then routed_since else created_at end, updated_at,
		coalesce(provider, ''), budget_usd, coalesce(budget_reset_at, 0)
		from pools order by created_at asc, id asc`)
	if err != nil {
		return nil, err
	}
	var pools []Pool
	index := map[string]int{}
	for rows.Next() {
		var pool Pool
		var lend, hide int
		var budget sql.NullFloat64
		if err := rows.Scan(&pool.ID, &pool.Name, &pool.AuthIndex, &pool.ModelPrefix, &pool.Visibility,
			&pool.Sub2PoolAccountID, &lend, &hide, &pool.RoutedSince, &pool.UpdatedAt,
			&pool.Provider, &budget, &pool.BudgetResetAt); err != nil {
			rows.Close()
			return nil, err
		}
		pool.LendDuringBurst = lend != 0
		pool.HideAccount = hide != 0
		pool.BudgetUSD = nullFloatPtr(budget)
		pool.Visibility = NormalizePoolVisibility(pool.Visibility)
		index[pool.ID] = len(pools)
		pools = append(pools, pool)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	memberRows, err := s.db.QueryContext(ctx, `select pool_id, key_id, share_percent, coalesce(role, ''),
		case when coalesce(joined_at, 0) > 0 then joined_at else created_at end
		from pool_members order by created_at asc, key_id asc`)
	if err != nil {
		return nil, err
	}
	defer memberRows.Close()
	for memberRows.Next() {
		var poolID string
		var member PoolMember
		var share sql.NullFloat64
		if err := memberRows.Scan(&poolID, &member.KeyID, &share, &member.Role, &member.JoinedAt); err != nil {
			return nil, err
		}
		member.SharePercent = nullFloatPtr(share)
		if member.Pinned() {
			member.SharePercent = nil
		}
		if i, ok := index[poolID]; ok {
			pools[i].Members = append(pools[i].Members, member)
		}
	}
	if err := memberRows.Err(); err != nil {
		return nil, err
	}
	burstRows, err := s.db.QueryContext(ctx, `select pool_id, key_id, mode, started_at, cycle_reset_at, stopped_at
		from member_bursts order by started_at asc, key_id asc`)
	if err != nil {
		return nil, err
	}
	defer burstRows.Close()
	for burstRows.Next() {
		var poolID string
		var burst MemberBurst
		if err := burstRows.Scan(&poolID, &burst.KeyID, &burst.Mode, &burst.StartedAt, &burst.CycleResetAt, &burst.StoppedAt); err != nil {
			return nil, err
		}
		if i, ok := index[poolID]; ok {
			pools[i].Bursts = append(pools[i].Bursts, burst)
		}
	}
	return pools, burstRows.Err()
}

// SavePools replaces the whole pool configuration. Burst state and settlement
// history of pools that still exist are kept.
func (s *Store) SavePools(ctx context.Context, pools []Pool) error {
	for i := range pools {
		pools[i].ID = strings.TrimSpace(pools[i].ID)
		pools[i].Name = strings.TrimSpace(pools[i].Name)
		pools[i].AuthIndex = strings.TrimSpace(pools[i].AuthIndex)
		pools[i].ModelPrefix = strings.TrimSpace(pools[i].ModelPrefix)
		pools[i].Visibility = NormalizePoolVisibility(pools[i].Visibility)
		pools[i].Provider = strings.ToLower(strings.TrimSpace(pools[i].Provider))
		if pools[i].Provider == "codex" {
			pools[i].Provider = ""
		}
		if pools[i].Budgeted() && pools[i].BudgetResetAt <= 0 {
			pools[i].BudgetResetAt = DefaultBudgetResetAt(time.Now())
		}
		for j := range pools[i].Members {
			member := &pools[i].Members[j]
			member.KeyID = strings.TrimSpace(member.KeyID)
			if member.Role != PoolRolePinned {
				member.Role = ""
			}
			if member.Pinned() {
				member.SharePercent = nil
			}
		}
	}
	if err := ValidatePools(pools); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	// Keep each member's join time while they stay in the same pool.
	joined := map[string]int64{}
	if rows, err := tx.QueryContext(ctx, `select pool_id, key_id, case when coalesce(joined_at, 0) > 0 then joined_at else created_at end from pool_members`); err == nil {
		for rows.Next() {
			var poolID, keyID string
			var at int64
			if rows.Scan(&poolID, &keyID, &at) == nil {
				joined[poolID+"\x00"+keyID] = at
			}
		}
		rows.Close()
	}
	keep := make([]any, 0, len(pools))
	marks := make([]string, 0, len(pools))
	for i, pool := range pools {
		keep = append(keep, pool.ID)
		marks = append(marks, "?")
		if pool.Sub2PoolAccountID < 0 {
			pool.Sub2PoolAccountID = 0
		}
		if _, err := tx.ExecContext(ctx, `insert into pools(id, name, auth_index, model_prefix, visibility, sub2pool_account_id, lend_during_burst, hide_account, routed_since, provider, budget_usd, budget_reset_at, created_at, updated_at)
			values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			on conflict(id) do update set name=excluded.name,
			routed_since=case when pools.auth_index != excluded.auth_index or pools.model_prefix != excluded.model_prefix
				then excluded.routed_since else pools.routed_since end,
			auth_index=excluded.auth_index, model_prefix=excluded.model_prefix, visibility=excluded.visibility,
			sub2pool_account_id=excluded.sub2pool_account_id, lend_during_burst=excluded.lend_during_burst,
			hide_account=excluded.hide_account, provider=excluded.provider, budget_usd=excluded.budget_usd,
			budget_reset_at=excluded.budget_reset_at, updated_at=excluded.updated_at`,
			pool.ID, pool.Name, pool.AuthIndex, pool.ModelPrefix, pool.Visibility, pool.Sub2PoolAccountID, boolInt(pool.LendDuringBurst), boolInt(pool.HideAccount), now,
			pool.Provider, floatPtrValue(pool.BudgetUSD), pool.BudgetResetAt, now+int64(i), now); err != nil {
			return err
		}
	}
	deletePools := `delete from pools`
	deleteMembers := `delete from pool_members`
	if len(keep) > 0 {
		deletePools += ` where id not in (` + strings.Join(marks, ",") + `)`
	}
	if _, err := tx.ExecContext(ctx, deletePools, keep...); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, deleteMembers); err != nil {
		return err
	}
	for _, pool := range pools {
		for i, member := range pool.Members {
			joinedAt, ok := joined[pool.ID+"\x00"+member.KeyID]
			if !ok {
				joinedAt = now
			}
			if _, err := tx.ExecContext(ctx, `insert into pool_members(key_id, pool_id, share_percent, role, joined_at, created_at) values (?, ?, ?, ?, ?, ?)`,
				member.KeyID, pool.ID, floatPtrValue(member.SharePercent), member.Role, joinedAt, now+int64(i)); err != nil {
				return err
			}
		}
	}
	// A burst belongs to a share member of the pool; it goes when they do.
	if _, err := tx.ExecContext(ctx, `delete from member_bursts where not exists (select 1 from pool_members m
		where m.pool_id = member_bursts.pool_id and m.key_id = member_bursts.key_id
		and m.share_percent is not null and m.role != ?)`, PoolRolePinned); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_ = s.Audit(ctx, "admin", "pools_saved", "", map[string]any{"pools": len(pools)})
	return nil
}

func floatPtrValue(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

// StartMemberBurst lets a share member use the pool account past their share
// until cycleResetAt. Turning a stopped burst back on in the same cycle keeps
// its start time.
func (s *Store) StartMemberBurst(ctx context.Context, poolID, keyID, mode string, cycleResetAt int64, now time.Time) error {
	if mode != BurstModeCarry && mode != BurstModeNoCarry {
		return fmt.Errorf("未知的爽蹬模式：%s", mode)
	}
	if cycleResetAt <= now.Unix() {
		return fmt.Errorf("还没有观测到当前周期，无法开启爽蹬")
	}
	var running int
	if err := s.db.QueryRowContext(ctx, `select count(*) from member_bursts where pool_id=? and key_id=?
		and stopped_at=0 and cycle_reset_at > ?`, poolID, keyID, now.Unix()).Scan(&running); err != nil {
		return err
	}
	if running > 0 {
		return fmt.Errorf("这位成员已经在爽蹬了")
	}
	if _, err := s.db.ExecContext(ctx, `insert into member_bursts(pool_id, key_id, cycle_reset_at, mode, started_at, stopped_at)
		values (?, ?, ?, ?, ?, 0)
		on conflict(pool_id, key_id, cycle_reset_at) do update set mode=excluded.mode, stopped_at=0`,
		poolID, keyID, cycleResetAt, mode, now.Unix()); err != nil {
		return err
	}
	_ = s.Audit(ctx, "admin", "member_burst_started", keyID, map[string]any{"pool": poolID, "mode": mode, "cycle_reset_at": cycleResetAt})
	return nil
}

// StopMemberBurst puts a member back under their share. The burst stays on
// record, so in carry mode what they used past their share is still settled
// when the cycle ends.
func (s *Store) StopMemberBurst(ctx context.Context, poolID, keyID string, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `update member_bursts set stopped_at=? where pool_id=? and key_id=?
		and stopped_at=0 and cycle_reset_at > ?`, now.Unix(), poolID, keyID, now.Unix())
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return fmt.Errorf("这位成员当前没有在爽蹬")
	}
	_ = s.Audit(ctx, "admin", "member_burst_stopped", keyID, map[string]any{"pool": poolID})
	return nil
}

// FinishMemberBursts settles the bursts of one ended cycle. Carry rows apply
// to nextCycleResetAt. It is idempotent per cycle.
func (s *Store) FinishMemberBursts(ctx context.Context, poolID string, cycleResetAt, nextCycleResetAt int64, settlement BurstSettlement, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var startedAt int64
	_ = tx.QueryRowContext(ctx, `select coalesce(min(started_at), 0) from member_bursts where pool_id=?
		and cycle_reset_at between ? and ?`, poolID, cycleResetAt-cycleMatchSeconds, cycleResetAt+cycleMatchSeconds).Scan(&startedAt)
	res, err := tx.ExecContext(ctx, `delete from member_bursts where pool_id=? and cycle_reset_at between ? and ?`,
		poolID, cycleResetAt-cycleMatchSeconds, cycleResetAt+cycleMatchSeconds)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return nil
	}
	settlement.NextCycleResetAt = nextCycleResetAt
	detail, _ := json.Marshal(settlement)
	if _, err := tx.ExecContext(ctx, `insert or replace into pool_settlements(pool_id, cycle_reset_at, started_at, mode, status, detail_json, created_at)
		values (?, ?, ?, 'member', 'settled', ?, ?)`, poolID, cycleResetAt, startedAt, string(detail), now.Unix()); err != nil {
		return err
	}
	if nextCycleResetAt > 0 {
		for _, row := range settlement.Rows {
			if row.AdjustmentPP == 0 {
				continue
			}
			if _, err := tx.ExecContext(ctx, `insert into pool_carry(pool_id, key_id, cycle_reset_at, pp, source_cycle_reset_at, created_at)
				values (?, ?, ?, ?, ?, ?)
				on conflict(pool_id, key_id, cycle_reset_at) do update set pp=pool_carry.pp + excluded.pp`,
				poolID, row.KeyID, nextCycleResetAt, row.AdjustmentPP, cycleResetAt, now.Unix()); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_ = s.Audit(ctx, "system", "member_bursts_settled", poolID, map[string]any{"cycle_reset_at": cycleResetAt, "borrowed_pp": settlement.BorrowedPP})
	return nil
}

// PoolCarry returns carry adjustments (pp) that apply to the given cycle.
func (s *Store) PoolCarry(ctx context.Context, poolID string, cycleResetAt int64) (map[string]float64, error) {
	out := map[string]float64{}
	if cycleResetAt <= 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `select key_id, pp from pool_carry where pool_id=?
		and cycle_reset_at between ? and ?`, poolID, cycleResetAt-cycleMatchSeconds, cycleResetAt+cycleMatchSeconds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var keyID string
		var pp float64
		if err := rows.Scan(&keyID, &pp); err != nil {
			return nil, err
		}
		out[keyID] += pp
	}
	return out, rows.Err()
}

type PoolSettlementRecord struct {
	CycleResetAt int64           `json:"cycle_reset_at"`
	StartedAt    int64           `json:"started_at"`
	Mode         string          `json:"mode"`
	Status       string          `json:"status"`
	Detail       json.RawMessage `json:"detail,omitempty"`
	CreatedAt    int64           `json:"created_at"`
}

func (s *Store) PoolSettlements(ctx context.Context, poolID string, limit int) ([]PoolSettlementRecord, error) {
	if limit <= 0 || limit > 50 {
		limit = 6
	}
	rows, err := s.db.QueryContext(ctx, `select cycle_reset_at, started_at, mode, status, detail_json, created_at
		from pool_settlements where pool_id=? order by created_at desc limit ?`, poolID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PoolSettlementRecord
	for rows.Next() {
		var item PoolSettlementRecord
		var detail string
		if err := rows.Scan(&item.CycleResetAt, &item.StartedAt, &item.Mode, &item.Status, &detail, &item.CreatedAt); err != nil {
			return nil, err
		}
		if json.Valid([]byte(detail)) {
			item.Detail = json.RawMessage(detail)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Ledger engine (pure functions over observations and costs)

type LedgerTick struct {
	AtMS       int64   `json:"t"`
	Used       float64 `json:"used"`
	Delta      float64 `json:"delta"`
	Cost       float64 `json:"cost"`
	Attributed float64 `json:"attributed"`
}

// CapacityEstimate is the account's weekly capacity in USD per 100%.
// High == 0 means the upper bound is not yet known.
type CapacityEstimate struct {
	USD     float64 `json:"usd"`
	Low     float64 `json:"low"`
	High    float64 `json:"high"`
	DeltaPP float64 `json:"delta_pp"`
	Cost    float64 `json:"cost"`
	Basis   string  `json:"basis"`
}

func (c CapacityEstimate) Known() bool { return c.USD > 0 }

type LinePoint struct {
	AtMS int64   `json:"t"`
	USD  float64 `json:"usd"`
	Low  float64 `json:"low"`
	High float64 `json:"high"`
	Used float64 `json:"used"`
}

type CycleLedger struct {
	CycleResetAt    int64              `json:"reset_at"`
	WindowMinutes   int64              `json:"window_minutes"`
	TrackingSinceMS int64              `json:"tracking_since_ms"`
	StartUsed       float64            `json:"start_used"`
	LastUsed        float64            `json:"used"`
	LastTickMS      int64              `json:"last_tick_ms"`
	LastSeenMS      int64              `json:"last_seen_ms"`
	Resolution      float64            `json:"resolution"`
	Ticks           []LedgerTick       `json:"-"`
	Attributed      map[string]float64 `json:"-"`
	PendingCost     map[string]float64 `json:"-"`
	CycleCost       map[string]float64 `json:"-"`
	Unattributed    float64            `json:"unattributed_pp"`
	RefUSD          float64            `json:"ref_usd"`
	Capacity        CapacityEstimate   `json:"capacity"`
	WeekLine        []LinePoint        `json:"-"`
}

func (l CycleLedger) HasData() bool { return l.TrackingSinceMS > 0 }

// FloorCapacityUSD is a deliberately low bound of an account's weekly capacity,
// used to cap attribution before the account has a measured reference.
func FloorCapacityUSD(plan string) float64 {
	switch strings.ToLower(strings.TrimSpace(plan)) {
	case "pro", "pro20x", "pro_20x", "pro5x", "pro_5x":
		return 300
	default:
		return 40
	}
}

// PriorCapacityUSD is used for pending estimates before a cycle has enough
// rises to measure itself.
func PriorCapacityUSD(plan string) float64 {
	switch strings.ToLower(strings.TrimSpace(plan)) {
	case "pro", "pro20x", "pro_20x":
		return 2000
	case "pro5x", "pro_5x":
		return 1000
	default:
		return 150
	}
}

// LedgerParams tunes one cycle's attribution.
type LedgerParams struct {
	// FloorUSD is the plan's loose capacity floor.
	FloorUSD float64
	// RefUSD is the previous cycle's typical cost per 100%, used until this
	// cycle has enough rises of its own.
	RefUSD float64
}

const (
	refMinRises = 5
	// A rise is blamed on recorded cost only as far as that cost explains it
	// at 3/4 of the account's typical capacity (plus half a display step for
	// commit timing); the rest came from use outside CPA and stays
	// unattributed, so members never pay for it.
	refCapFactor = 0.75
	refSlack     = 0.5
)

// BuildCycleLedger attributes one cycle. Observations must belong to a single
// cycle; events may extend past it and are filtered by time.
//
// Every rise is measured from the moment one value was first seen to the
// moment the next was, so the cost finished in between is what moved the
// account by that many points. Each rise is split among keys in proportion to
// that cost, capped by what the cost can explain at the account's typical
// capacity; the remainder is unattributed (someone used the account outside
// CPA).
func BuildCycleLedger(obs []QuotaObservation, events []CostEvent, params LedgerParams) CycleLedger {
	ledger := CycleLedger{
		Attributed:  map[string]float64{},
		PendingCost: map[string]float64{},
		CycleCost:   map[string]float64{},
		Resolution:  1,
	}
	if len(obs) == 0 {
		return ledger
	}
	sorted := append([]QuotaObservation(nil), obs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].ObservedAtMS != sorted[j].ObservedAtMS {
			return sorted[i].ObservedAtMS < sorted[j].ObservedAtMS
		}
		return sorted[i].UsedPercent < sorted[j].UsedPercent
	})
	ledger.CycleResetAt = sorted[0].CycleResetAt
	ledger.WindowMinutes = sorted[0].WindowMinutes
	ledger.TrackingSinceMS = sorted[0].ObservedAtMS
	ledger.StartUsed = sorted[0].UsedPercent
	ledger.LastUsed = sorted[0].UsedPercent
	ledger.LastTickMS = sorted[0].ObservedAtMS
	for _, item := range sorted {
		if item.UsedPercent != math.Trunc(item.UsedPercent) {
			ledger.Resolution = 0.01
		}
		if item.ObservedAtMS > ledger.LastSeenMS {
			ledger.LastSeenMS = item.ObservedAtMS
		}
		if item.UsedPercent > ledger.LastUsed+1e-9 {
			ledger.Ticks = append(ledger.Ticks, LedgerTick{AtMS: item.ObservedAtMS, Used: item.UsedPercent, Delta: item.UsedPercent - ledger.LastUsed})
			ledger.LastUsed = item.UsedPercent
			ledger.LastTickMS = item.ObservedAtMS
		}
	}
	costs := append([]CostEvent(nil), events...)
	sort.SliceStable(costs, func(i, j int) bool { return costs[i].AtMS < costs[j].AtMS })
	cursor := 0
	for cursor < len(costs) && costs[cursor].AtMS <= ledger.TrackingSinceMS {
		cursor++
	}
	for _, item := range costs[cursor:] {
		ledger.CycleCost[item.KeyID] += item.Cost
	}
	intervals := make([]map[string]float64, len(ledger.Ticks))
	var ratios []float64
	for k := range ledger.Ticks {
		tick := &ledger.Ticks[k]
		intervals[k] = map[string]float64{}
		for cursor < len(costs) && costs[cursor].AtMS <= tick.AtMS {
			intervals[k][costs[cursor].KeyID] += costs[cursor].Cost
			tick.Cost += costs[cursor].Cost
			cursor++
		}
		if k > 0 && tick.Cost > 0 {
			ratios = append(ratios, 100*tick.Cost/tick.Delta)
		}
	}
	for _, item := range costs[cursor:] {
		ledger.PendingCost[item.KeyID] += item.Cost
	}
	ledger.RefUSD = params.RefUSD
	if len(ratios) >= refMinRises {
		ledger.RefUSD = median(ratios)
	}
	capUSD, slack := params.FloorUSD, ledger.Resolution
	if ledger.RefUSD > 0 && refCapFactor*ledger.RefUSD > capUSD {
		capUSD, slack = refCapFactor*ledger.RefUSD, refSlack*ledger.Resolution
	}
	var weekCost, weekPP float64
	for k := range ledger.Ticks {
		tick := &ledger.Ticks[k]
		if tick.Cost <= 0 {
			ledger.Unattributed += tick.Delta
			continue
		}
		tick.Attributed = tick.Delta
		if capUSD > 0 {
			tick.Attributed = math.Min(tick.Delta, 100*tick.Cost/capUSD+slack)
		}
		ledger.Unattributed += tick.Delta - tick.Attributed
		for keyID, cost := range intervals[k] {
			ledger.Attributed[keyID] += tick.Attributed * cost / tick.Cost
		}
		// The first rise is measured from an arbitrary starting observation,
		// so capacity is measured from the first rise onward, rise to rise.
		if k == 0 {
			continue
		}
		weekCost += tick.Cost
		weekPP += tick.Attributed
		if estimate := capacityFrom(weekCost, weekPP, ledger.Resolution); estimate.Known() {
			ledger.WeekLine = append(ledger.WeekLine, LinePoint{AtMS: tick.AtMS, USD: estimate.USD, Low: estimate.Low, High: estimate.High, Used: tick.Used})
		}
	}
	ledger.Capacity = capacityFrom(weekCost, weekPP, ledger.Resolution)
	if ledger.Capacity.Known() {
		ledger.Capacity.Basis = "cycle"
	}
	return ledger
}

func median(values []float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func capacityFrom(cost, pp, resolution float64) CapacityEstimate {
	if cost <= 0 || pp < resolution-1e-9 {
		return CapacityEstimate{}
	}
	estimate := CapacityEstimate{USD: 100 * cost / pp, DeltaPP: pp, Cost: cost, Low: 100 * cost / (pp + resolution)}
	if pp > resolution+1e-9 {
		estimate.High = 100 * cost / (pp - resolution)
	}
	return estimate
}

// DayCapacity measures capacity from rises inside [fromMS, toMS) only, rise to
// rise, the way a same-day conversion reads.
func DayCapacity(ledgers []CycleLedger, fromMS, toMS int64) CapacityEstimate {
	var cost, pp, resolution float64 = 0, 0, 1
	for _, ledger := range ledgers {
		first := true
		for _, tick := range ledger.Ticks {
			if tick.AtMS < fromMS || tick.AtMS >= toMS || tick.Cost <= 0 {
				continue
			}
			if first {
				first = false
				continue
			}
			cost += tick.Cost
			pp += tick.Attributed
		}
		if ledger.Resolution < resolution {
			resolution = ledger.Resolution
		}
	}
	estimate := capacityFrom(cost, pp, resolution)
	if estimate.Known() {
		estimate.Basis = "day"
	}
	return estimate
}

type DayClose struct {
	Day      string  `json:"day"`
	StartMS  int64   `json:"t"`
	CloseUSD float64 `json:"close_usd"`
	CloseLow float64 `json:"close_low"`
	CloseHi  float64 `json:"close_high"`
	DayUSD   float64 `json:"day_usd"`
	Used     float64 `json:"used"`
	HasUsed  bool    `json:"has_used"`
}

// MonthLine lists the daily close of the running cycle estimate for the last
// `days` days on the viewer clock (offsetSeconds east of UTC).
func MonthLine(ledgers []CycleLedger, now time.Time, days int, offsetSeconds int64) []DayClose {
	if days <= 0 {
		return nil
	}
	loc := time.FixedZone("viewer", int(offsetSeconds))
	local := now.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	out := make([]DayClose, 0, days)
	for i := days - 1; i >= 0; i-- {
		start := today.AddDate(0, 0, -i)
		end := start.AddDate(0, 0, 1)
		point := DayClose{Day: start.Format("2006-01-02"), StartMS: start.UnixMilli()}
		endMS := end.UnixMilli()
		if endMS > now.UnixMilli() {
			endMS = now.UnixMilli() + 1
		}
		var bestAt int64
		for _, ledger := range ledgers {
			if ledger.CycleResetAt*1000 <= start.UnixMilli() || ledger.TrackingSinceMS >= endMS {
				continue
			}
			for _, p := range ledger.WeekLine {
				if p.AtMS < endMS && p.AtMS >= bestAt {
					bestAt = p.AtMS
					point.CloseUSD, point.CloseLow, point.CloseHi = p.USD, p.Low, p.High
				}
			}
			used, ok := usedAt(ledger, endMS)
			if ok {
				point.Used, point.HasUsed = used, true
			}
		}
		if day := DayCapacity(ledgers, start.UnixMilli(), end.UnixMilli()); day.Known() {
			point.DayUSD = day.USD
		}
		out = append(out, point)
	}
	return out
}

func usedAt(ledger CycleLedger, atMS int64) (float64, bool) {
	if !ledger.HasData() || ledger.TrackingSinceMS >= atMS {
		return 0, false
	}
	used := ledger.StartUsed
	for _, tick := range ledger.Ticks {
		if tick.AtMS >= atMS {
			break
		}
		used = tick.Used
	}
	return used, true
}

// ---------------------------------------------------------------------------
// Burst settlement

type SettlementInput struct {
	KeyID   string
	SharePP float64
	CarryPP float64
	UsedPP  float64
	// Billed marks a member whose use past their share is paid back: one who
	// burst in carry mode. Anyone else's overshoot is not charged.
	Billed bool
}

type SettlementRow struct {
	KeyID        string  `json:"key_id"`
	AvailablePP  float64 `json:"available_pp"`
	UsedPP       float64 `json:"used_pp"`
	UnusedPP     float64 `json:"unused_pp"`
	OverPP       float64 `json:"over_pp"`
	AdjustmentPP float64 `json:"adjustment_pp"`
}

type BurstSettlement struct {
	BorrowedPP float64 `json:"borrowed_pp"`
	// Bursts are the member bursts this settlement closes.
	Bursts []MemberBurst `json:"bursts,omitempty"`
	// LentPP is what members of other pools used while this account was lent
	// out. It is never settled between the pool's own members.
	LentPP           float64         `json:"lent_pp,omitempty"`
	Rows             []SettlementRow `json:"rows"`
	NextCycleResetAt int64           `json:"next_cycle_reset_at,omitempty"`
	EvidenceMS       int64           `json:"evidence_ms,omitempty"`
	Used             float64         `json:"used,omitempty"`
}

const settlementUnits = 100000 // 5 decimals of a percentage point

// SettleCarry settles a cycle's bursts in percentage points:
//
//	available = contract + prior carry
//	unused    = max(available - used, 0)
//	over      = max(used - available, 0)
//	borrowed  = min(sum unused, sum over of billed members)
//
// Lenders are compensated in proportion to what they left unused, billed
// borrowers debited in proportion to their overuse. Credits and debits cancel
// exactly at 5 decimals; idle share nobody borrowed expires.
func SettleCarry(inputs []SettlementInput) BurstSettlement {
	rows := make([]SettlementRow, len(inputs))
	var totalUnused, totalOver float64
	for i, input := range inputs {
		available := input.SharePP + input.CarryPP
		rows[i] = SettlementRow{
			KeyID:       input.KeyID,
			AvailablePP: roundUnits(available),
			UsedPP:      roundUnits(input.UsedPP),
			UnusedPP:    roundUnits(math.Max(available-input.UsedPP, 0)),
			OverPP:      roundUnits(math.Max(input.UsedPP-available, 0)),
		}
		totalUnused += rows[i].UnusedPP
		if input.Billed {
			totalOver += rows[i].OverPP
		}
	}
	borrowedUnits := int64(math.Round(math.Min(totalUnused, totalOver) * settlementUnits))
	settlement := BurstSettlement{BorrowedPP: float64(borrowedUnits) / settlementUnits, Rows: rows}
	if borrowedUnits <= 0 {
		return settlement
	}
	credits := apportionUnits(rows, borrowedUnits, func(row SettlementRow) float64 { return row.UnusedPP })
	billed := map[string]bool{}
	for _, input := range inputs {
		billed[input.KeyID] = input.Billed
	}
	debits := apportionUnits(rows, borrowedUnits, func(row SettlementRow) float64 {
		if !billed[row.KeyID] {
			return 0
		}
		return row.OverPP
	})
	for i := range rows {
		rows[i].AdjustmentPP = float64(credits[i]-debits[i]) / settlementUnits
	}
	return settlement
}

func roundUnits(value float64) float64 {
	return math.Round(value*settlementUnits) / settlementUnits
}

// apportionUnits splits total integer units by weight with the largest
// remainder method; ties go to the lower key id, so the result is stable.
func apportionUnits(rows []SettlementRow, total int64, weight func(SettlementRow) float64) []int64 {
	out := make([]int64, len(rows))
	var sum float64
	for _, row := range rows {
		sum += weight(row)
	}
	if sum <= 0 {
		return out
	}
	type remainder struct {
		index int
		frac  float64
	}
	var assigned int64
	rest := make([]remainder, 0, len(rows))
	for i, row := range rows {
		exact := float64(total) * weight(row) / sum
		out[i] = int64(math.Floor(exact))
		assigned += out[i]
		if weight(row) > 0 {
			rest = append(rest, remainder{index: i, frac: exact - math.Floor(exact)})
		}
	}
	sort.SliceStable(rest, func(i, j int) bool {
		if rest[i].frac != rest[j].frac {
			return rest[i].frac > rest[j].frac
		}
		return rows[rest[i].index].KeyID < rows[rest[j].index].KeyID
	})
	for i := 0; assigned < total && len(rest) > 0; i++ {
		out[rest[i%len(rest)].index]++
		assigned++
	}
	return out
}
