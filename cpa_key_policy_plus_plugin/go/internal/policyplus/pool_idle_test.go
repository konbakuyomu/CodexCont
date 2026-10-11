package policyplus

import (
	"context"
	"testing"
	"time"
)

const week = 7 * 86400

func cycleKeys(t *testing.T, store *Store, auth string) map[int64][]QuotaObservation {
	t.Helper()
	obs, err := store.QuotaObservations(context.Background(), auth, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int64][]QuotaObservation{}
	for _, item := range obs {
		out[item.CycleResetAt] = append(out[item.CycleResetAt], item)
	}
	return out
}

func TestIdleCycleFollowsTheMovingResetTime(t *testing.T) {
	ctx := context.Background()
	store := openPoolTestStore(t)
	if err := store.SavePools(ctx, []Pool{{ID: "plus", Name: "Plus", AuthIndex: "acc", Members: []PoolMember{{KeyID: "a", SharePercent: share50()}}}}); err != nil {
		t.Fatal(err)
	}
	start := int64(1_790_000_000)
	record := func(used float64, resetAt, at int64) QuotaRecord {
		t.Helper()
		rec, err := store.RecordQuota(ctx, weeklySignal("acc", used, resetAt, at*1000))
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	// Unused, the account reports 0% with the reset a week from now.
	if rec := record(0, start+week, start); rec.Cycle != start+week || rec.NewCycle {
		t.Fatalf("first sighting = %+v", rec)
	}
	if err := store.StartMemberBurst(ctx, "plus", "a", BurstModeCarry, start+week, time.Unix(start+60, 0)); err != nil {
		t.Fatal(err)
	}
	// Probes every ten minutes for two hours: one cycle, following the time.
	var at int64
	for i := int64(1); i <= 12; i++ {
		at = start + 600*i
		rec := record(0, at+week, at)
		if rec.Cycle != at+week || rec.NewCycle || !rec.Tick || !rec.Moved {
			t.Fatalf("probe %d = %+v", i, rec)
		}
	}
	cycles := cycleKeys(t, store, "acc")
	if len(cycles) != 1 || len(cycles[at+week]) != 1 || cycles[at+week][0].ObservedAtMS != start*1000 {
		t.Fatalf("an idle account keeps one cycle, tracked since its first sighting: %+v", cycles)
	}
	listed, _ := store.ListPools(ctx)
	if len(listed[0].Bursts) != 1 || listed[0].Bursts[0].CycleResetAt != at+week {
		t.Fatalf("the burst moves with its cycle: %+v", listed[0].Bursts)
	}
	// First use starts the window; the cycle takes its real reset time.
	used := at + 250
	if err := insertUsage(ctx, store.db, UsageEvent{KeyID: "a", RequestedAt: time.Unix(used, 0), AuthIndex: "acc", ConsumedAtMS: used * 1000, Usage: TokenUsage{TotalTokens: 1000}, Cost: 0.5}); err != nil {
		t.Fatal(err)
	}
	real := used + week
	if rec := record(2, real, at+300); rec.Cycle != real || rec.NewCycle || !rec.Tick {
		t.Fatalf("first rise = %+v", rec)
	}
	// From then on the reset time is fixed, and jitter stays in the cycle.
	if rec := record(2, real+30, at+900); rec.Cycle != real || rec.NewCycle {
		t.Fatalf("a started cycle keeps its reset time: %+v", rec)
	}
	if rec := record(5, real, at+1500); rec.Cycle != real || rec.NewCycle || !rec.Tick {
		t.Fatalf("later rise = %+v", rec)
	}
	cycles = cycleKeys(t, store, "acc")
	if len(cycles) != 1 || len(cycles[real]) != 3 {
		t.Fatalf("one cycle from first sighting to now: %+v", cycles)
	}
	ledger := BuildCycleLedger(cycles[real], nil, LedgerParams{})
	if ledger.TrackingSinceMS != start*1000 || ledger.StartUsed != 0 || ledger.LastUsed != 5 {
		t.Fatalf("ledger = %+v", ledger)
	}
}

func TestIdleCycleIgnoresJitterAndEndsOnTime(t *testing.T) {
	ctx := context.Background()
	store := openPoolTestStore(t)
	start := int64(1_790_000_000)
	record := func(used float64, resetAt, at int64) QuotaRecord {
		t.Helper()
		rec, err := store.RecordQuota(ctx, weeklySignal("acc", used, resetAt, at*1000))
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	record(0, start+week, start)
	if rec := record(0, start+week+20, start+600); rec.Cycle != start+week || rec.Tick {
		t.Fatalf("a reset time that only jitters keeps the cycle: %+v", rec)
	}
	if rec := record(0, start+week-30, start+1200); rec.Cycle != start+week || rec.Tick {
		t.Fatalf("an idle cycle never moves back: %+v", rec)
	}
	if rec := record(0, start+2*week, start+week+60); rec.Cycle != start+2*week || !rec.NewCycle {
		t.Fatalf("a cycle that ended is followed by a new one: %+v", rec)
	}
}

func TestCollapseIdleCyclesFoldsTheHourlyCyclesOfAnIdleAccount(t *testing.T) {
	ctx := context.Background()
	store := openPoolTestStore(t)
	if err := store.SavePools(ctx, []Pool{{ID: "plus", Name: "Plus", AuthIndex: "acc", Members: []PoolMember{{KeyID: "a", SharePercent: share50()}}}}); err != nil {
		t.Fatal(err)
	}
	insert := func(cycle int64, used float64, at int64) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, `insert into quota_observations(auth_index, cycle_reset_at, used_percent, observed_at_ms, window_minutes)
			values ('acc', ?, ?, ?, ?)`, cycle, used, at*1000, WeeklyWindowMinutes); err != nil {
			t.Fatal(err)
		}
	}
	real := int64(1_790_000_000)
	// A week with nothing on it, which ended before the next cycle began.
	insert(real-week-100, 0, real-2*week)
	// A used cycle, then the account sat idle after its reset and a new
	// cycle opened every 63 minutes.
	insert(real, 10, real-week+100)
	insert(real, 33, real-week+5000)
	var idle []int64
	for i := int64(0); i < 6; i++ {
		at := real + 60 + 3780*i
		idle = append(idle, at+week)
		insert(at+week, 0, at)
	}
	if err := store.StartMemberBurst(ctx, "plus", "a", BurstModeCarry, idle[2], time.Unix(real+8000, 0)); err != nil {
		t.Fatal(err)
	}
	folded, err := store.CollapseIdleCycles(ctx)
	if err != nil || folded != 5 {
		t.Fatalf("folded = %d err = %v", folded, err)
	}
	cycles := cycleKeys(t, store, "acc")
	last := idle[len(idle)-1]
	if len(cycles) != 3 || len(cycles[real]) != 2 || len(cycles[real-week-100]) != 1 || len(cycles[last]) != 1 || cycles[last][0].ObservedAtMS != (real+60)*1000 {
		t.Fatalf("cycles = %+v", cycles)
	}
	listed, _ := store.ListPools(ctx)
	if len(listed[0].Bursts) != 1 || listed[0].Bursts[0].CycleResetAt != last {
		t.Fatalf("bursts follow the fold: %+v", listed[0].Bursts)
	}
	if again, err := store.CollapseIdleCycles(ctx); err != nil || again != 0 {
		t.Fatalf("collapsing again = %d err = %v", again, err)
	}
}

func TestResetAfterUseBelowOnePercentOpensANewCycle(t *testing.T) {
	ctx := context.Background()
	store := openPoolTestStore(t)
	start := int64(1_790_000_000)
	record := func(used float64, resetAt, at int64) QuotaRecord {
		t.Helper()
		rec, err := store.RecordQuota(ctx, weeklySignal("acc", used, resetAt, at*1000))
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	// The window started and the account served a little, still under 1%.
	record(0, start+week, start)
	if err := insertUsage(ctx, store.db, UsageEvent{KeyID: "a", RequestedAt: time.Unix(start+600, 0), AuthIndex: "acc", ConsumedAtMS: (start + 600) * 1000, Usage: TokenUsage{TotalTokens: 5000}, Cost: 5}); err != nil {
		t.Fatal(err)
	}
	// A reset credit two hours later starts a new window: a new cycle, not the
	// same one moving.
	rec := record(0, start+7200+week, start+7200)
	if rec.Cycle != start+7200+week || !rec.NewCycle || rec.Moved {
		t.Fatalf("a reset after use is a new cycle: %+v", rec)
	}
	if cycles := cycleKeys(t, store, "acc"); len(cycles) != 2 {
		t.Fatalf("both cycles kept: %+v", cycles)
	}
}
