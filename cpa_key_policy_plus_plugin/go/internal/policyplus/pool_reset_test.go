package policyplus

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestParseCodexUsageFindsTheWeeklyWindowByLength(t *testing.T) {
	at := time.Unix(1_791_000_000, 0)
	pro := []byte(`{"plan_type":"pro","rate_limit":{"allowed":true,"limit_reached":false,
		"primary_window":{"used_percent":4,"limit_window_seconds":604800,"reset_after_seconds":500000,"reset_at":1791400000},
		"secondary_window":null},"credits":{"has_credits":false}}`)
	sig, ok := ParseCodexUsage(pro, at)
	if !ok || sig.PlanType != "pro" || sig.Weekly == nil || sig.Weekly.Used != 4 || sig.Weekly.ResetAt != 1_791_400_000 || sig.Weekly.Minutes != WeeklyWindowMinutes || sig.Short != nil {
		t.Fatalf("pro usage = %+v ok %v", sig, ok)
	}
	plus := []byte(`{"plan_type":"plus","rate_limit":{"limit_reached":true,
		"primary_window":{"used_percent":60,"limit_window_seconds":18000,"reset_after_seconds":3600},
		"secondary_window":{"used_percent":31,"limit_window_seconds":604800,"reset_after_seconds":86400}}}`)
	sig, ok = ParseCodexUsage(plus, at)
	if !ok || !sig.LimitReached || sig.Weekly == nil || sig.Weekly.Used != 31 || sig.Weekly.ResetAt != at.Unix()+86400 ||
		sig.Short == nil || sig.Short.Used != 60 || sig.Short.Minutes != 300 {
		t.Fatalf("plus usage = %+v ok %v", sig, ok)
	}
	for _, body := range []string{`{}`, `{"rate_limit":null}`, `not json`, `{"rate_limit":{"primary_window":{"limit_window_seconds":604800}}}`} {
		if _, ok := ParseCodexUsage([]byte(body), at); ok {
			t.Fatalf("%s must not parse", body)
		}
	}
}

func TestParseClaudeQuotaHeadersReadsUtilizationAsPercent(t *testing.T) {
	at := time.Unix(1_791_000_000, 0)
	headers := http.Header{}
	headers.Set("Anthropic-Ratelimit-Unified-Status", "rejected")
	headers.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.69")
	headers.Set("Anthropic-Ratelimit-Unified-7d-Reset", "1791400000")
	headers.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "1.02")
	headers.Set("Anthropic-Ratelimit-Unified-5h-Reset", fmt.Sprint(at.Add(time.Hour).Unix()))
	sig, ok := ParseClaudeQuotaHeaders(headers, at)
	if !ok || !sig.LimitReached || sig.Weekly == nil || sig.Weekly.Used != 69 || sig.Weekly.ResetAt != 1_791_400_000 ||
		sig.Short == nil || sig.Short.Used != 102 || sig.PlanType != "claude" {
		t.Fatalf("claude headers = %+v ok %v", sig, ok)
	}
	if _, ok := ParseClaudeQuotaHeaders(http.Header{"Anthropic-Ratelimit-Unified-7d-Utilization": {"0.5"}}, at); ok {
		t.Fatal("a window without a reset time is not usable")
	}
	if FloorCapacityUSD("claude") <= FloorCapacityUSD("") || PriorCapacityUSD("claude") <= PriorCapacityUSD("") {
		t.Fatal("claude accounts get subscription-sized priors")
	}
}

func TestRecordQuotaOpensASegmentWhenAResetKeepsTheResetTime(t *testing.T) {
	ctx := context.Background()
	store := openPoolTestStore(t)
	reset := int64(1_791_000_000)
	record := func(used float64, resetAt, atMS int64) QuotaRecord {
		t.Helper()
		rec, err := store.RecordQuota(ctx, weeklySignal("acc", used, resetAt, atMS))
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	if rec := record(10, reset, 1_000); rec.Cycle != reset || rec.NewCycle || !rec.Tick {
		t.Fatalf("first sighting = %+v", rec)
	}
	record(30, reset, 2_000)
	record(45, reset, 3_000)
	if rec := record(44, reset, 3_500); rec.Cycle != reset || rec.NewCycle {
		t.Fatalf("a one-point dip is noise, not a reset: %+v", rec)
	}
	rec := record(0, reset+5, 4_000)
	if rec.Cycle != reset+1 || !rec.NewCycle || !rec.Tick {
		t.Fatalf("a drop to zero with the same reset time is a reset: %+v", rec)
	}
	if rec := record(3, reset, 5_000); rec.Cycle != reset+1 || rec.NewCycle {
		t.Fatalf("later sightings stay in the new segment: %+v", rec)
	}
	// A slow response from before the reset still belongs to the old segment.
	if rec := record(40, reset, 3_800); rec.Cycle != reset || rec.NewCycle {
		t.Fatalf("a late old snapshot is filed by when it was taken: %+v", rec)
	}
	// A reset credit that starts a new window is a new cycle as before.
	if rec := record(0, reset+7*86400, 9_000); rec.Cycle != reset+7*86400 || !rec.NewCycle {
		t.Fatalf("a new reset time is a new cycle: %+v", rec)
	}
	obs, err := store.QuotaObservations(ctx, "acc", 0)
	if err != nil {
		t.Fatal(err)
	}
	segments := map[int64][]float64{}
	for _, item := range obs {
		segments[item.CycleResetAt] = append(segments[item.CycleResetAt], item.UsedPercent)
	}
	if len(segments[reset]) != 5 || len(segments[reset+1]) != 2 || len(segments[reset+7*86400]) != 1 {
		t.Fatalf("segments = %+v", segments)
	}
	ledger := BuildCycleLedger(obsOf(obs, reset+1), nil, LedgerParams{})
	if ledger.StartUsed != 0 || ledger.LastUsed != 3 {
		t.Fatalf("the new segment starts from zero: %+v", ledger)
	}
}

func obsOf(all []QuotaObservation, cycle int64) []QuotaObservation {
	var out []QuotaObservation
	for _, item := range all {
		if item.CycleResetAt == cycle {
			out = append(out, item)
		}
	}
	return out
}

func TestFinishMemberBurstsSettlesOnlyItsOwnSegment(t *testing.T) {
	ctx := context.Background()
	store := openPoolTestStore(t)
	pools := []Pool{{ID: "pro", Name: "Pro", AuthIndex: "acc", Members: []PoolMember{
		{KeyID: "a", SharePercent: share50()}, {KeyID: "b", SharePercent: share50()},
	}}}
	if err := store.SavePools(ctx, pools); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_790_000_000, 0)
	old := now.Unix() + 86400
	if err := store.StartMemberBurst(ctx, "pro", "a", BurstModeCarry, old, now); err != nil {
		t.Fatal(err)
	}
	// The account resets keeping its reset time: the next segment is a second
	// later, and b bursts in it before the first segment is settled.
	if err := store.StartMemberBurst(ctx, "pro", "b", BurstModeCarry, old+1, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	settlement := SettleCarry([]SettlementInput{{"a", 50, 0, 60, true}, {"b", 50, 0, 40, false}})
	if err := store.FinishMemberBursts(ctx, "pro", old, old+1, settlement, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	listed, _ := store.ListPools(ctx)
	if len(listed[0].Bursts) != 1 || listed[0].Bursts[0].KeyID != "b" || listed[0].Bursts[0].CycleResetAt != old+1 {
		t.Fatalf("only the settled segment's bursts are cleared: %+v", listed[0].Bursts)
	}
	if carry, _ := store.PoolCarry(ctx, "pro", old+1); !near(carry["a"], -10) || !near(carry["b"], 10) {
		t.Fatalf("carry lands on the next segment: %+v", carry)
	}
	if carry, _ := store.PoolCarry(ctx, "pro", old); len(carry) != 0 {
		t.Fatalf("nothing is carried into the settled segment: %+v", carry)
	}
}

func share50() *float64 { v := 50.0; return &v }
