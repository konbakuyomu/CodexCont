package policyplus

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestParseCodexQuotaHeadersPicksWeeklyWindowByLength(t *testing.T) {
	observed := time.Unix(1_790_000_000, 0)
	pro := http.Header{}
	pro.Set("X-Codex-Plan-Type", "pro")
	pro.Set("X-Codex-Primary-Used-Percent", "5")
	pro.Set("X-Codex-Primary-Window-Minutes", "10080")
	pro.Set("X-Codex-Primary-Reset-At", "1791046703")
	sig, ok := ParseCodexQuotaHeaders(pro, observed)
	if !ok || sig.Weekly == nil || sig.Weekly.Used != 5 || sig.Weekly.ResetAt != 1791046703 || sig.Short != nil {
		t.Fatalf("pro weekly window must come from the primary slot, got %+v", sig)
	}
	if sig.PlanType != "pro" || sig.ObservedAtMS != observed.UnixMilli() {
		t.Fatalf("plan and observation time not kept: %+v", sig)
	}

	// Plus: 5-hour primary, weekly secondary; lower-case keys as HTTP/2 hands
	// them over, and only reset-after for the weekly slot.
	plus := http.Header{
		"x-codex-primary-used-percent":          {"100"},
		"x-codex-primary-window-minutes":        {"300"},
		"x-codex-primary-reset-after-seconds":   {"1200"},
		"x-codex-secondary-used-percent":        {"16"},
		"x-codex-secondary-window-minutes":      {"10080"},
		"x-codex-secondary-reset-after-seconds": {"86400"},
		"x-codex-limit-reached":                 {"true"},
	}
	sig, ok = ParseCodexQuotaHeaders(plus, observed)
	if !ok || sig.Weekly == nil || sig.Weekly.Used != 16 || sig.Weekly.ResetAt != observed.Unix()+86400 {
		t.Fatalf("plus weekly window must come from the secondary slot, got %+v", sig.Weekly)
	}
	if sig.Short == nil || sig.Short.Minutes != 300 || sig.Short.Used != 100 || !sig.LimitReached {
		t.Fatalf("plus 5-hour window not reported as short window: %+v", sig)
	}

	if _, ok := ParseCodexQuotaHeaders(http.Header{"Content-Type": {"text/event-stream"}}, observed); ok {
		t.Fatal("responses without x-codex headers must not produce a signal")
	}
}

func openPoolTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "pool.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func weeklySignal(auth string, used float64, resetAt int64, atMS int64) QuotaSignal {
	return QuotaSignal{AuthIndex: auth, Provider: "codex", PlanType: "pro", ObservedAtMS: atMS,
		Weekly: &QuotaWindow{Minutes: WeeklyWindowMinutes, Used: used, ResetAt: resetAt}}
}

func TestRecordQuotaSignalKeepsFirstSightingOfEachValue(t *testing.T) {
	ctx := context.Background()
	store := openPoolTestStore(t)
	reset := int64(1_791_000_000)
	steps := []struct {
		sig  QuotaSignal
		tick bool
	}{
		{weeklySignal("acc", 5, reset, 1_000), true},
		{weeklySignal("acc", 5, reset+2, 2_000), false},      // same value, jittered reset
		{weeklySignal("acc", 6, reset-1, 5_000), true},       // rise
		{weeklySignal("acc", 6, reset, 4_000), true},         // slower request saw 6 earlier
		{weeklySignal("acc", 6, reset, 4_500), false},        // later sighting changes nothing
		{weeklySignal("acc", 0, reset+7*86400, 9_000), true}, // new cycle
	}
	for i, step := range steps {
		tick, err := store.RecordQuotaSignal(ctx, step.sig)
		if err != nil {
			t.Fatal(err)
		}
		if tick != step.tick {
			t.Fatalf("step %d: tick = %v, want %v", i, tick, step.tick)
		}
	}
	obs, err := store.QuotaObservations(ctx, "acc", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 3 {
		t.Fatalf("want 3 observations (5, 6, new cycle 0), got %+v", obs)
	}
	if obs[0].CycleResetAt != reset || obs[1].CycleResetAt != reset || obs[1].ObservedAtMS != 4_000 {
		t.Fatalf("jittered resets must share one cycle and keep the earliest sighting: %+v", obs)
	}
	if obs[2].CycleResetAt != reset+7*86400 {
		t.Fatalf("a reset a week later is a new cycle: %+v", obs[2])
	}
	accounts, err := store.QuotaAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].Weekly == nil || accounts[0].Weekly.Used != 0 || accounts[0].ObservedAtMS != 9_000 {
		t.Fatalf("latest snapshot should be the newest observation: %+v", accounts)
	}
	// An older snapshot arriving late must not replace the latest one.
	if _, err := store.RecordQuotaSignal(ctx, weeklySignal("acc", 6, reset, 8_000)); err != nil {
		t.Fatal(err)
	}
	accounts, _ = store.QuotaAccounts(ctx)
	if accounts[0].Weekly.Used != 0 {
		t.Fatalf("late old snapshot replaced the latest one: %+v", accounts[0].Weekly)
	}
}

func TestBuildCycleLedgerSplitsEachRiseByCostSinceThePreviousRise(t *testing.T) {
	obs := []QuotaObservation{
		{CycleResetAt: 9_999, UsedPercent: 10, ObservedAtMS: 0, WindowMinutes: WeeklyWindowMinutes},
		{CycleResetAt: 9_999, UsedPercent: 11, ObservedAtMS: 100},
		{CycleResetAt: 9_999, UsedPercent: 13, ObservedAtMS: 200},
	}
	events := []CostEvent{
		{KeyID: "early", AtMS: -5, Cost: 50}, // before tracking: part of the starting 10%
		{KeyID: "a", AtMS: 50, Cost: 5},
		{KeyID: "b", AtMS: 80, Cost: 15},
		{KeyID: "a", AtMS: 150, Cost: 10},
		{KeyID: "b", AtMS: 200, Cost: 10}, // exactly at the rise: counts toward it
		{KeyID: "a", AtMS: 250, Cost: 3},  // after the last rise: pending
	}
	ledger := BuildCycleLedger(obs, events, LedgerParams{})
	if !near(ledger.Attributed["a"], 0.25+1) || !near(ledger.Attributed["b"], 0.75+1) {
		t.Fatalf("attribution = %+v", ledger.Attributed)
	}
	if ledger.Attributed["early"] != 0 || ledger.CycleCost["early"] != 0 {
		t.Fatalf("cost before tracking must not be attributed: %+v", ledger.Attributed)
	}
	if !near(ledger.PendingCost["a"], 3) || ledger.PendingCost["b"] != 0 {
		t.Fatalf("pending = %+v", ledger.PendingCost)
	}
	total := ledger.Unattributed
	for _, pp := range ledger.Attributed {
		total += pp
	}
	if !near(total, ledger.LastUsed-ledger.StartUsed) || ledger.StartUsed != 10 || ledger.LastUsed != 13 {
		t.Fatalf("ledger must add up to the observed rise: total %v, %+v", total, ledger)
	}
	// Capacity is measured rise to rise, skipping the first rise: $20 for 2pp.
	if !near(ledger.Capacity.USD, 1000) || !near(ledger.Capacity.Low, 2000.0/3*1) || !near(ledger.Capacity.High, 2000) {
		t.Fatalf("capacity = %+v", ledger.Capacity)
	}
	if len(ledger.WeekLine) != 1 || !near(ledger.WeekLine[0].USD, 1000) || ledger.WeekLine[0].Used != 13 {
		t.Fatalf("week line = %+v", ledger.WeekLine)
	}
}

func TestBuildCycleLedgerKeepsRisesWithoutRecordedCostUnattributed(t *testing.T) {
	obs := []QuotaObservation{
		{CycleResetAt: 1, UsedPercent: 0, ObservedAtMS: 0},
		{CycleResetAt: 1, UsedPercent: 2, ObservedAtMS: 100}, // nobody in CPA spent anything
		{CycleResetAt: 1, UsedPercent: 7, ObservedAtMS: 200}, // $0.40 cannot explain 5pp
	}
	events := []CostEvent{{KeyID: "a", AtMS: 150, Cost: 0.4}}
	ledger := BuildCycleLedger(obs, events, LedgerParams{FloorUSD: 40}) // floor: 1pp = $0.40 at most
	if !near(ledger.Attributed["a"], 2) {                               // 100*0.4/40 + 1pp display carry
		t.Fatalf("attributed = %v, want 2", ledger.Attributed["a"])
	}
	if !near(ledger.Unattributed, 2+3) {
		t.Fatalf("unattributed = %v, want 5", ledger.Unattributed)
	}
}

func TestSettleCarryFollowsTheBorrowingRules(t *testing.T) {
	adjust := func(s BurstSettlement) []float64 {
		out := []float64{}
		for _, row := range s.Rows {
			out = append(out, row.AdjustmentPP)
		}
		return out
	}
	// Whole account used: B and C borrowed 8 and 9 points from A.
	s := SettleCarry([]SettlementInput{{"a", 50, 0, 33, true}, {"b", 25, 0, 33, true}, {"c", 25, 0, 34, true}})
	if got := adjust(s); !near(got[0], 17) || !near(got[1], -8) || !near(got[2], -9) || !near(s.BorrowedPP, 17) {
		t.Fatalf("example 1 = %v borrowed %v", got, s.BorrowedPP)
	}
	// B over by 5; A and C lent in proportion 30:20.
	s = SettleCarry([]SettlementInput{{"a", 50, 0, 20, true}, {"b", 25, 0, 30, true}, {"c", 25, 0, 5, true}})
	if got := adjust(s); !near(got[0], 3) || !near(got[1], -5) || !near(got[2], 2) {
		t.Fatalf("example 2 = %v", got)
	}
	// Nobody exceeded their share: nothing carries, idle share expires.
	s = SettleCarry([]SettlementInput{{"a", 50, 0, 50, true}, {"b", 25, 0, 15, true}, {"c", 25, 0, 10, true}})
	if got := adjust(s); got[0] != 0 || got[1] != 0 || got[2] != 0 {
		t.Fatalf("example 3 = %v", got)
	}
	// Prior carry counts toward what is available this cycle.
	s = SettleCarry([]SettlementInput{{"a", 50, 17, 60, true}, {"b", 25, -8, 20, true}, {"c", 25, -9, 20, true}})
	if got := adjust(s); !near(got[0], 7) || !near(got[1], -3) || !near(got[2], -4) {
		t.Fatalf("second cycle = %v", got)
	}
	// Thirds: the ledger still balances exactly at 5 decimals.
	s = SettleCarry([]SettlementInput{{"a", 30, 0, 29, true}, {"b", 30, 0, 29, true}, {"c", 30, 0, 29, true}, {"d", 10, 0, 11, true}})
	var sum float64
	for _, v := range adjust(s) {
		sum += v
		if math.Abs(v*settlementUnits-math.Round(v*settlementUnits)) > 1e-6 {
			t.Fatalf("adjustment %v has more than 5 decimals", v)
		}
	}
	if math.Abs(sum) > 1e-9 || !near(s.Rows[3].AdjustmentPP, -1) {
		t.Fatalf("thirds must cancel exactly: %v sum %v", adjust(s), sum)
	}
	// Only members who burst in carry mode pay back: c overshot without being
	// billed (a no-carry burst, or estimate lag), so only b's 5 points move.
	s = SettleCarry([]SettlementInput{{"a", 50, 0, 20, false}, {"b", 25, 0, 30, true}, {"c", 25, 0, 35, false}})
	if got := adjust(s); !near(got[0], 5) || !near(got[1], -5) || got[2] != 0 || !near(s.BorrowedPP, 5) {
		t.Fatalf("billed only = %v borrowed %v", got, s.BorrowedPP)
	}
}

func TestMonthLineClosesEachDayWithTheRunningEstimate(t *testing.T) {
	cst := time.FixedZone("CST", 8*3600)
	day1 := time.Date(2026, 9, 1, 0, 0, 0, 0, cst)
	at := func(day int, hour int) int64 {
		return day1.AddDate(0, 0, day).Add(time.Duration(hour) * time.Hour).UnixMilli()
	}
	obs := []QuotaObservation{
		{CycleResetAt: day1.AddDate(0, 0, 6).Unix(), UsedPercent: 0, ObservedAtMS: at(0, 1)},
		{CycleResetAt: day1.AddDate(0, 0, 6).Unix(), UsedPercent: 1, ObservedAtMS: at(0, 2)},
		{CycleResetAt: day1.AddDate(0, 0, 6).Unix(), UsedPercent: 3, ObservedAtMS: at(0, 10)},
		{CycleResetAt: day1.AddDate(0, 0, 6).Unix(), UsedPercent: 4, ObservedAtMS: at(1, 9)},
	}
	events := []CostEvent{
		{KeyID: "a", AtMS: at(0, 1) + 1, Cost: 10},
		{KeyID: "a", AtMS: at(0, 5), Cost: 40}, // 2pp for $40 → $2000/100%
		{KeyID: "a", AtMS: at(1, 8), Cost: 10}, // 1pp for $10 → running $50/3pp
	}
	ledger := BuildCycleLedger(obs, events, LedgerParams{})
	line := MonthLine([]CycleLedger{ledger}, day1.AddDate(0, 0, 1).Add(20*time.Hour), 3, 8*3600)
	if len(line) != 3 || line[1].Day != "2026-09-01" || line[2].Day != "2026-09-02" {
		t.Fatalf("days = %+v", line)
	}
	if line[0].CloseUSD != 0 || line[0].HasUsed {
		t.Fatalf("a day before tracking has no close: %+v", line[0])
	}
	if !near(line[1].CloseUSD, 2000) || line[1].Used != 3 || !near(line[1].DayUSD, 2000) {
		t.Fatalf("day 1 close = %+v", line[1])
	}
	if !near(line[2].CloseUSD, 100*50.0/3) || line[2].Used != 4 || line[2].DayUSD != 0 {
		t.Fatalf("day 2 close = %+v (one rise alone gives no same-day rate)", line[2])
	}
}

func TestValidatePoolsRejectsOverbookedAndDuplicateMembers(t *testing.T) {
	share := func(v float64) *float64 { return &v }
	cases := []struct {
		name  string
		pools []Pool
	}{
		{"overbooked", []Pool{{ID: "pro", Name: "Pro", Members: []PoolMember{{KeyID: "a", SharePercent: share(60)}, {KeyID: "b", SharePercent: share(41)}}}}},
		{"key twice", []Pool{
			{ID: "pro", Name: "Pro", Members: []PoolMember{{KeyID: "a", SharePercent: share(10)}}},
			{ID: "plus", Name: "Plus", Members: []PoolMember{{KeyID: "a", SharePercent: share(10)}}},
		}},
		{"account twice", []Pool{{ID: "pro", Name: "Pro", AuthIndex: "x"}, {ID: "plus", Name: "Plus", AuthIndex: "x"}}},
		{"prefix slash", []Pool{{ID: "pro", Name: "Pro", ModelPrefix: "pro/"}}},
		{"bad id", []Pool{{ID: "Pro Pool", Name: "Pro"}}},
	}
	for _, tc := range cases {
		if err := ValidatePools(tc.pools); err == nil {
			t.Fatalf("%s: expected an error", tc.name)
		}
	}
	ok := []Pool{{ID: "pro", Name: "Pro 20x", AuthIndex: "a31d", ModelPrefix: "pro", Members: []PoolMember{
		{KeyID: "owner"}, {KeyID: "lz", SharePercent: share(20)}, {KeyID: "qq", SharePercent: share(40)}, {KeyID: "aw", SharePercent: share(40)},
	}}}
	if err := ValidatePools(ok); err != nil {
		t.Fatalf("valid pool rejected: %v", err)
	}
}

func TestMemberBurstLifecycleWritesCarryForItsCycle(t *testing.T) {
	ctx := context.Background()
	store := openPoolTestStore(t)
	share := func(v float64) *float64 { return &v }
	pools := []Pool{{ID: "pro", Name: "Pro 20x", AuthIndex: "acc", ModelPrefix: "pro", Visibility: "whatever", Members: []PoolMember{
		{KeyID: "owner"}, {KeyID: "a", SharePercent: share(50)}, {KeyID: "b", SharePercent: share(25)},
	}}}
	if err := store.SavePools(ctx, pools); err != nil {
		t.Fatal(err)
	}
	listed, err := store.ListPools(ctx)
	if err != nil || len(listed) != 1 || len(listed[0].Members) != 3 || listed[0].Visibility != PoolVisibilityAnonymous {
		t.Fatalf("listed = %+v err %v", listed, err)
	}
	if !listed[0].Members[0].Owner() || *listed[0].Members[1].SharePercent != 50 {
		t.Fatalf("members = %+v", listed[0].Members)
	}
	now := time.Unix(1_790_000_000, 0)
	cycle := now.Unix() + 3*86400
	if err := store.StartMemberBurst(ctx, "pro", "b", BurstModeCarry, cycle, now); err != nil {
		t.Fatal(err)
	}
	if err := store.StartMemberBurst(ctx, "pro", "b", BurstModeCarry, cycle, now); err == nil {
		t.Fatal("a member cannot burst twice at once")
	}
	listed, _ = store.ListPools(ctx)
	if burst, on := listed[0].BurstOf("b", now); !on || burst.Mode != BurstModeCarry || burst.CycleResetAt != cycle {
		t.Fatalf("b's burst = %+v on %v", listed[0].Bursts, on)
	}
	if _, on := listed[0].BurstOf("a", now); on {
		t.Fatal("only b bursts")
	}
	if _, on := listed[0].BurstOf("b", time.Unix(cycle, 0)); on {
		t.Fatal("a burst ends with its cycle")
	}
	if err := store.StopMemberBurst(ctx, "pro", "b", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.StopMemberBurst(ctx, "pro", "b", now.Add(time.Hour)); err == nil {
		t.Fatal("stopping a stopped burst is an error")
	}
	listed, _ = store.ListPools(ctx)
	if _, on := listed[0].BurstOf("b", now.Add(2*time.Hour)); on || len(listed[0].Bursts) != 1 || listed[0].Bursts[0].StoppedAt == 0 {
		t.Fatalf("a stopped burst stays on record until its cycle settles: %+v", listed[0].Bursts)
	}
	// Turn it back on in the same cycle: the start time is kept.
	if err := store.StartMemberBurst(ctx, "pro", "b", BurstModeCarry, cycle, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	listed, _ = store.ListPools(ctx)
	if burst, on := listed[0].BurstOf("b", now.Add(3*time.Hour)); !on || burst.StartedAt != now.Unix() {
		t.Fatalf("reopened burst = %+v", listed[0].Bursts)
	}
	settlement := SettleCarry([]SettlementInput{{"a", 50, 0, 20, false}, {"b", 25, 0, 30, true}})
	next := cycle + 7*86400
	if err := store.FinishMemberBursts(ctx, "pro", cycle, next, settlement, now.Add(4*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishMemberBursts(ctx, "pro", cycle, next, settlement, now.Add(4*24*time.Hour)); err != nil {
		t.Fatal(err) // idempotent
	}
	carry, err := store.PoolCarry(ctx, "pro", next+30)
	if err != nil || !near(carry["a"], 5) || !near(carry["b"], -5) {
		t.Fatalf("carry = %+v err %v", carry, err)
	}
	history, err := store.PoolSettlements(ctx, "pro", 10)
	if err != nil || len(history) != 1 || history[0].Status != "settled" || history[0].Mode != "member" {
		t.Fatalf("history = %+v err %v", history, err)
	}
	listed, _ = store.ListPools(ctx)
	if len(listed[0].Bursts) != 0 {
		t.Fatalf("settled bursts are cleared: %+v", listed[0].Bursts)
	}
	// Saving the configuration again keeps carry; a member who leaves takes
	// their burst with them.
	if err := store.StartMemberBurst(ctx, "pro", "b", BurstModeNoCarry, next, now.Add(5*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.SavePools(ctx, pools); err != nil {
		t.Fatal(err)
	}
	if carry, _ := store.PoolCarry(ctx, "pro", next); !near(carry["a"], 5) {
		t.Fatalf("carry lost on save: %+v", carry)
	}
	if listed, _ = store.ListPools(ctx); len(listed[0].Bursts) != 1 {
		t.Fatalf("saving keeps a staying member's burst: %+v", listed[0].Bursts)
	}
	pools[0].Members = pools[0].Members[:2]
	if err := store.SavePools(ctx, pools); err != nil {
		t.Fatal(err)
	}
	if listed, _ = store.ListPools(ctx); len(listed[0].Bursts) != 0 {
		t.Fatalf("a member who left keeps no burst: %+v", listed[0].Bursts)
	}
}

func TestKeyHeatmapBucketsByViewerHour(t *testing.T) {
	ctx := context.Background()
	store := openPoolTestStore(t)
	cst := time.FixedZone("CST", 8*3600)
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, cst)
	for _, event := range []UsageEvent{
		{KeyID: "a", RequestedAt: from.Add(30 * time.Minute), Cost: 1},
		{KeyID: "a", RequestedAt: from.Add(50 * time.Minute), Cost: 2},
		{KeyID: "a", RequestedAt: from.Add(25 * time.Hour), Cost: 4},
		{KeyID: "b", RequestedAt: from.Add(2 * time.Hour), Cost: 8},
		{KeyID: "a", RequestedAt: from.Add(3 * time.Hour), Cost: 16, BillingPending: false},
	} {
		if err := insertUsage(ctx, store.db, event); err != nil {
			t.Fatal(err)
		}
	}
	heat, err := store.KeyHeatmap(ctx, []string{"a", "b"}, from.Add(10*time.Minute), 48, time.Hour, 8*3600)
	if err != nil {
		t.Fatal(err)
	}
	if heat["a"][0] != 3 || heat["a"][3] != 16 || heat["a"][25] != 4 || heat["b"][2] != 8 {
		t.Fatalf("heat = a %v b %v", heat["a"][:4], heat["b"][:4])
	}
}

func TestBuildCycleLedgerCapsRisesByTheAccountsTypicalCapacity(t *testing.T) {
	// Six clean rises at $20 per point ($2000 per 100%), then a rise of three
	// points that CPA only recorded $20 for: someone used the account outside.
	obs := []QuotaObservation{{CycleResetAt: 1, UsedPercent: 0, ObservedAtMS: 0}}
	events := []CostEvent{}
	for i := 1; i <= 7; i++ {
		obs = append(obs, QuotaObservation{CycleResetAt: 1, UsedPercent: float64(i), ObservedAtMS: int64(i * 100)})
		events = append(events, CostEvent{KeyID: "a", AtMS: int64(i*100 - 50), Cost: 20})
	}
	obs = append(obs, QuotaObservation{CycleResetAt: 1, UsedPercent: 10, ObservedAtMS: 800})
	events = append(events, CostEvent{KeyID: "b", AtMS: 750, Cost: 20})
	ledger := BuildCycleLedger(obs, events, LedgerParams{FloorUSD: 300})
	if !near(ledger.RefUSD, 2000) {
		t.Fatalf("reference capacity = %v, want the median rise $2000", ledger.RefUSD)
	}
	wantB := 100*20/(0.75*2000) + 0.5
	if !near(ledger.Attributed["b"], wantB) || !near(ledger.Unattributed, 3-wantB) {
		t.Fatalf("b = %v unattributed = %v, want %v and %v", ledger.Attributed["b"], ledger.Unattributed, wantB, 3-wantB)
	}
	if !near(ledger.Attributed["a"], 7) {
		t.Fatalf("clean rises stay fully attributed: a = %v", ledger.Attributed["a"])
	}
	// Before a cycle has five rises of its own, the previous cycle's
	// reference applies.
	short := BuildCycleLedger(obs[:3], events[:2], LedgerParams{FloorUSD: 300, RefUSD: 2000})
	if short.RefUSD != 2000 {
		t.Fatalf("carried reference = %v", short.RefUSD)
	}
}

func TestLendWindowsTellBorrowsFromLeaks(t *testing.T) {
	ctx := context.Background()
	store := openPoolTestStore(t)
	share := func(v float64) *float64 { return &v }
	now := time.Unix(1_790_000_000, 0)
	pools := []Pool{
		{ID: "plus", Name: "Plus", AuthIndex: "acc-plus", ModelPrefix: "plus", Members: []PoolMember{{KeyID: "a", SharePercent: share(50)}}},
		{ID: "pro", Name: "Pro", AuthIndex: "acc-pro", ModelPrefix: "pro", Members: []PoolMember{{KeyID: "owner"}}},
	}
	if err := store.SavePools(ctx, pools); err != nil {
		t.Fatal(err)
	}
	listed, err := store.ListPools(ctx)
	if err != nil || len(listed) != 2 || listed[0].LendDuringBurst || listed[1].LendDuringBurst {
		t.Fatalf("listed = %+v err %v", listed, err)
	}
	use := func(auth string, at time.Time) {
		t.Helper()
		if err := store.InsertUsage(ctx, UsageEvent{RequestID: fmt.Sprintf("a-%s-%d", auth, at.Unix()), KeyID: "a", Model: "gpt-5.5",
			Provider: "codex", RequestedAt: at, LatencyMS: 1000, Cost: 1, AuthIndex: auth}); err != nil {
			t.Fatal(err)
		}
	}
	sync := func(at time.Time) {
		t.Helper()
		pools, err := store.ListPools(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SyncLendWindows(ctx, pools, at); err != nil {
			t.Fatal(err)
		}
	}
	setLend := func(on bool) {
		t.Helper()
		pools, _ := store.ListPools(ctx)
		pools[1].LendDuringBurst = on
		if err := store.SavePools(ctx, pools); err != nil {
			t.Fatal(err)
		}
	}

	use("acc-pro", now.Add(-time.Hour)) // not lending yet: a leak
	sync(now)
	start := now.Add(time.Minute)
	setLend(true)
	sync(start)
	sync(start.Add(time.Minute))              // nothing changes while lending
	use("acc-pro", start.Add(10*time.Minute)) // borrowed
	off := start.Add(time.Hour)
	setLend(false)
	sync(off)
	use("acc-pro", off.Add(10*time.Minute))  // not lending: a leak
	use("acc-plus", off.Add(15*time.Minute)) // its own account: neither
	back := off.Add(20 * time.Minute)
	setLend(true)
	sync(back)
	use("acc-pro", back.Add(10*24*time.Hour)) // a lend window has no end date: borrowed

	away, err := store.MemberAwayUsage(ctx, map[string]int64{"a": now.Add(-2 * time.Hour).Unix()}, "acc-plus")
	if err != nil || away.LeakRequests != 2 || away.BorrowRequests != 2 || !near(away.LeakCost, 2) || !near(away.BorrowCost, 2) {
		t.Fatalf("away usage = %+v err %v", away, err)
	}
	keys, err := store.LendWindowKeys(ctx, "acc-pro", now.Add(-2*time.Hour).Unix())
	if err != nil || len(keys) != 1 || !keys["a"] {
		t.Fatalf("borrowers = %+v err %v", keys, err)
	}
	if keys, _ := store.LendWindowKeys(ctx, "acc-plus", 0); len(keys) != 0 {
		t.Fatalf("an account that was never lent has no borrowers: %+v", keys)
	}
	setLend(false)
	sync(back.Add(11 * 24 * time.Hour))
	if keys, _ := store.LendWindowKeys(ctx, "acc-pro", back.Add(12*24*time.Hour).Unix()); len(keys) != 0 {
		t.Fatalf("no borrow after the window closed: %+v", keys)
	}
}

func TestSavePoolsKeepsJoinTimesAndTracksRouting(t *testing.T) {
	ctx := context.Background()
	store := openPoolTestStore(t)
	share := func(v float64) *float64 { return &v }
	pools := []Pool{{ID: "pro", Name: "Pro", AuthIndex: "acc", ModelPrefix: "pro", Members: []PoolMember{
		{KeyID: "a", SharePercent: share(50)}, {KeyID: "p", Role: PoolRolePinned, SharePercent: share(10)},
	}}}
	if err := store.SavePools(ctx, pools); err != nil {
		t.Fatal(err)
	}
	first, _ := store.ListPools(ctx)
	if first[0].RoutedSince == 0 || first[0].Members[0].JoinedAt == 0 || !first[0].Members[1].Pinned() || first[0].Members[1].SharePercent != nil {
		t.Fatalf("first save = %+v", first)
	}
	if _, err := store.db.ExecContext(ctx, `update pool_members set joined_at = 1000; update pools set routed_since = 2000`); err != nil {
		t.Fatal(err)
	}
	pools[0].Members = append(pools[0].Members, PoolMember{KeyID: "b", SharePercent: share(20)})
	pools[0].Name = "Pro 20x"
	if err := store.SavePools(ctx, pools); err != nil {
		t.Fatal(err)
	}
	second, _ := store.ListPools(ctx)
	if second[0].RoutedSince != 2000 || second[0].Members[0].JoinedAt != 1000 || second[0].Members[2].JoinedAt <= 1000 {
		t.Fatalf("renaming keeps routing and join times, a new member joins now: %+v", second)
	}
	pools[0].ModelPrefix = "pro2"
	if err := store.SavePools(ctx, pools); err != nil {
		t.Fatal(err)
	}
	third, _ := store.ListPools(ctx)
	if third[0].RoutedSince <= 2000 || third[0].Members[0].JoinedAt != 1000 {
		t.Fatalf("a new prefix restarts routing, members keep their join time: %+v", third)
	}
}
