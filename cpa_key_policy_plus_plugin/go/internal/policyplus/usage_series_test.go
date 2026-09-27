package policyplus

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func openSeriesStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "policyplus.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func insertSeriesUsage(t *testing.T, store *Store, keyID, requestID string, at time.Time, cost float64) {
	t.Helper()
	if err := store.InsertUsage(context.Background(), UsageEvent{RequestID: requestID, KeyID: keyID, Model: "gpt-5.5", RequestedAt: at, Cost: cost}); err != nil {
		t.Fatal(err)
	}
}

func TestUsageSeriesAlignsDaysToViewerClock(t *testing.T) {
	ctx := context.Background()
	store := openSeriesStore(t)
	beijing := int64(8 * 60 * 60)
	window := Window{Name: Range7D, From: time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC), To: time.Date(2026, 9, 27, 3, 0, 0, 0, time.UTC)}
	// 15:59:59Z is still 9/20 in Beijing; 16:00:00Z is Beijing midnight of 9/21.
	insertSeriesUsage(t, store, "k", "req-late", time.Date(2026, 9, 20, 15, 59, 59, 0, time.UTC), 0.2)
	insertSeriesUsage(t, store, "k", "req-midnight", time.Date(2026, 9, 20, 16, 0, 0, 0, time.UTC), 0.3)
	insertSeriesUsage(t, store, "k", "req-before-window", time.Date(2026, 9, 20, 2, 0, 0, 0, time.UTC), 9)

	series, err := store.UsageSeries(ctx, "k", window, 24*time.Hour, beijing)
	if err != nil {
		t.Fatal(err)
	}
	if len(series.Buckets) != 8 {
		t.Fatalf("7d series from 11:00 Beijing should have 8 day buckets, got %d", len(series.Buckets))
	}
	first, second := series.Buckets[0], series.Buckets[1]
	if first.Start != window.From.Unix() || first.End != time.Date(2026, 9, 20, 16, 0, 0, 0, time.UTC).Unix() || first.Calls != 1 {
		t.Fatalf("first bucket should be the rest of 9/20 Beijing: %#v", first)
	}
	if second.Start != first.End || second.Calls != 1 || math.Abs(second.Cost-0.3) > 1e-9 {
		t.Fatalf("second bucket should start at Beijing midnight: %#v", second)
	}
	if last := series.Buckets[len(series.Buckets)-1]; last.End != window.To.Unix() {
		t.Fatalf("last bucket should end at the window end: %#v", last)
	}
	if math.Abs(series.TotalCost-0.5) > 1e-9 {
		t.Fatalf("rows before the window must not count: %.4f", series.TotalCost)
	}

	resetAt := time.Date(2026, 9, 20, 16, 0, 0, 0, time.UTC)
	if err := store.Reset(ctx, "k", Range7D, resetAt); err != nil {
		t.Fatal(err)
	}
	afterReset, err := store.UsageSeries(ctx, "k", window, 24*time.Hour, beijing)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := store.UsageSum(ctx, "k", window)
	if err != nil {
		t.Fatal(err)
	}
	if afterReset.ResetAt != resetAt.Unix() || math.Abs(afterReset.TotalCost-sum) > 1e-9 || afterReset.Buckets[0].Calls != 0 {
		t.Fatalf("series must follow the soft-reset watermark like UsageSum: %#v sum=%.4f", afterReset, sum)
	}
}

func TestWindowRecoveryAtMatchesUsageSum(t *testing.T) {
	ctx := context.Background()
	store := openSeriesStore(t)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	insertSeriesUsage(t, store, "k", "req-oldest", now.Add(-4*time.Hour), 0.6)
	insertSeriesUsage(t, store, "k", "req-newer", now.Add(-time.Hour), 0.5)

	at, ok, err := store.WindowRecoveryAt(ctx, "k", WindowFor(Range5H, now), 1)
	if err != nil || !ok {
		t.Fatalf("exhausted 5h window should report recovery: ok=%v err=%v", ok, err)
	}
	if want := now.Add(time.Hour + time.Second); !at.Equal(want) {
		t.Fatalf("recovery = %s, want %s", at, want)
	}
	stillBlocked, _ := store.UsageSum(ctx, "k", WindowFor(Range5H, at.Add(-time.Second)))
	recovered, _ := store.UsageSum(ctx, "k", WindowFor(Range5H, at))
	if CheckLimit(stillBlocked, ptr(1)).Allowed || !CheckLimit(recovered, ptr(1)).Allowed {
		t.Fatalf("recovery time must be the first second the quota check allows: before=%.2f at=%.2f", stillBlocked, recovered)
	}

	for name, tc := range map[string]struct {
		window Window
		limit  float64
	}{
		"under limit": {WindowFor(Range5H, now), 2},
		"zero limit":  {WindowFor(Range5H, now), 0},
		"month":       {WindowFor(RangeMonth, now), 0.5},
	} {
		if _, ok, err := store.WindowRecoveryAt(ctx, "k", tc.window, tc.limit); ok || err != nil {
			t.Fatalf("%s should not report aging recovery: ok=%v err=%v", name, ok, err)
		}
	}
}

func TestMonthResetAtIsNextBeijingMonth(t *testing.T) {
	// 2026-12-31 20:00Z is already 2027-01-01 04:00 in Beijing.
	got := MonthResetAt(time.Date(2026, 12, 31, 20, 0, 0, 0, time.UTC))
	if want := time.Date(2027, 1, 31, 16, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("month reset = %s, want %s", got.UTC(), want)
	}
}

func TestRequestRateCountsTheBusiestSixtySeconds(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "rate.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().Truncate(time.Second)
	at := func(ago time.Duration, key string) {
		if err := store.InsertUsage(context.Background(), UsageEvent{RequestID: key + ago.String(), KeyID: key, RequestedAt: now.Add(-ago)}); err != nil {
			t.Fatal(err)
		}
	}
	// A burst of 4 inside one minute an hour ago, split across a clock minute.
	for _, s := range []int{3630, 3610, 3590, 3575} {
		at(time.Duration(s)*time.Second, "k")
	}
	at(30*time.Second, "k")
	at(10*time.Second, "k")
	at(5*time.Second, "other")
	rate, err := store.RequestRate(context.Background(), "k", now.Add(-24*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if rate.Peak != 4 || rate.LastMinute != 2 {
		t.Fatalf("rate = %+v, want peak 4 and 2 in the last minute", rate)
	}
}
