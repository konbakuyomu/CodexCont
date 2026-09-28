package policyplus

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestBudgetCycleEndStepsWholeWeeksFromTheAnchor(t *testing.T) {
	anchor := int64(1_790_000_000)
	week := int64(BudgetCycleSeconds)
	cases := []struct{ now, want int64 }{
		{anchor - 1, anchor},
		{anchor, anchor + week},
		{anchor + 3*week + 5, anchor + 4*week},
		{anchor - week, anchor},
		{anchor - week - 1, anchor - week},
	}
	for _, tc := range cases {
		if got := BudgetCycleEnd(anchor, tc.now); got != tc.want {
			t.Fatalf("BudgetCycleEnd(anchor, %d) = %d, want %d", tc.now-anchor, got-anchor, tc.want-anchor)
		}
	}
	if BudgetCycleEnd(0, anchor) != 0 {
		t.Fatal("no anchor means no cycle")
	}
	loc := time.FixedZone("Asia/Shanghai", 8*3600)
	sunday := time.Date(2026, 9, 27, 23, 0, 0, 0, loc)
	if got := time.Unix(DefaultBudgetResetAt(sunday), 0).In(loc); got.Weekday() != time.Monday || got.Hour() != 0 || got.Day() != 28 {
		t.Fatalf("default reset must be the next Monday 00:00 Beijing, got %v", got)
	}
	monday := time.Date(2026, 9, 28, 0, 0, 0, 0, loc)
	if got := time.Unix(DefaultBudgetResetAt(monday), 0).In(loc); got.Day() != 5 || got.Month() != time.October {
		t.Fatalf("at Monday 00:00 the default reset is a week later, got %v", got)
	}
}

func TestBudgetObservationsMeterEachKeyAgainstTheBudget(t *testing.T) {
	end := int64(1_790_000_000)
	startMS := (end - BudgetCycleSeconds) * 1000
	events := []CostEvent{
		{KeyID: "before", AtMS: startMS - 5, Cost: 50},
		{KeyID: "qq", AtMS: startMS + 1000, Cost: 30},
		{KeyID: "aw", AtMS: startMS + 1000, Cost: 10}, // same instant as qq
		{KeyID: "qq", AtMS: startMS + 2000, Cost: 20},
		{KeyID: "aw", AtMS: startMS + 3000, Cost: 60},
		{KeyID: "after", AtMS: end*1000 + 1, Cost: 99},
	}
	obs := BudgetObservations(200, end, events)
	if len(obs) != 4 || obs[0].UsedPercent != 0 || obs[0].ObservedAtMS != startMS || !near(obs[3].UsedPercent, 60) {
		t.Fatalf("observations must start at 0%% and climb by cost/budget, got %+v", obs)
	}
	ledger := BuildCycleLedger(obs, events, LedgerParams{FloorUSD: 200, RefUSD: 200})
	if !near(ledger.LastUsed, 60) || !near(ledger.Unattributed, 0) {
		t.Fatalf("used %.4f unattributed %.4f, want 60 and 0", ledger.LastUsed, ledger.Unattributed)
	}
	if !near(ledger.Attributed["qq"], 25) || !near(ledger.Attributed["aw"], 35) {
		t.Fatalf("each key pays cost/budget: %+v", ledger.Attributed)
	}
	if ledger.Attributed["before"] != 0 || ledger.Attributed["after"] != 0 {
		t.Fatalf("costs outside the cycle must not count: %+v", ledger.Attributed)
	}
	if !ledger.Capacity.Known() || !near(ledger.Capacity.USD, 200) {
		t.Fatalf("capacity of a budget cycle is the budget, got %+v", ledger.Capacity)
	}
	if got := BudgetObservations(0, end, events); got != nil {
		t.Fatal("no budget means no observations")
	}
}

func TestKeyMayRideOnePoolPerProvider(t *testing.T) {
	share := func(v float64) *float64 { return &v }
	budget := share(300)
	both := []Pool{
		{ID: "pro", Name: "Pro", AuthIndex: "a", Members: []PoolMember{{KeyID: "qq", SharePercent: share(30)}}},
		{ID: "grok", Name: "Grok", AuthIndex: "g", Provider: "xai", BudgetUSD: budget, Members: []PoolMember{{KeyID: "qq", SharePercent: share(50)}}},
	}
	if err := ValidatePools(both); err != nil {
		t.Fatalf("a key may ride a codex pool and an xai pool: %v", err)
	}
	two := []Pool{
		{ID: "pro", Name: "Pro", Members: []PoolMember{{KeyID: "qq", SharePercent: share(30)}}},
		{ID: "plus", Name: "Plus", Provider: "codex", Members: []PoolMember{{KeyID: "qq", SharePercent: share(30)}}},
	}
	if err := ValidatePools(two); err == nil {
		t.Fatal("two codex pools for one key must be rejected")
	}
	noBudget := []Pool{{ID: "grok", Name: "Grok", AuthIndex: "g", Provider: "xai"}}
	if err := ValidatePools(noBudget); err == nil {
		t.Fatal("an xai pool with an account needs a weekly budget")
	}
	zero := 0.0
	bad := []Pool{{ID: "grok", Name: "Grok", AuthIndex: "g", Provider: "xai", BudgetUSD: &zero}}
	if err := ValidatePools(bad); err == nil {
		t.Fatal("a zero budget must be rejected")
	}
}

func TestSavePoolsKeepsBudgetAndLetsAKeyJoinTwoProviders(t *testing.T) {
	store := openPoolTestStore(t)
	ctx := context.Background()
	share := func(v float64) *float64 { return &v }
	pools := []Pool{
		{ID: "pro", Name: "Pro", AuthIndex: "a", Members: []PoolMember{{KeyID: "qq", SharePercent: share(30)}}},
		{ID: "grok", Name: "Grok", AuthIndex: "g", Provider: "XAI", BudgetUSD: share(300), Members: []PoolMember{{KeyID: "qq", SharePercent: share(50)}}},
	}
	if err := store.SavePools(ctx, pools); err != nil {
		t.Fatal(err)
	}
	got, err := store.ListPools(ctx)
	if err != nil || len(got) != 2 {
		t.Fatalf("pools not listed: %v %+v", err, got)
	}
	grok := got[1]
	if grok.Provider != "xai" || !grok.Budgeted() || *grok.BudgetUSD != 300 || grok.BudgetResetAt <= time.Now().Unix() {
		t.Fatalf("budget pool not kept: %+v", grok)
	}
	if got[0].ProviderName() != "codex" || got[0].Budgeted() || len(got[0].Members) != 1 || len(grok.Members) != 1 {
		t.Fatalf("members or codex pool lost: %+v", got)
	}
}

func TestPoolMembersTableIsRekeyedByPoolAndKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`create table pool_members (key_id text primary key, pool_id text not null, share_percent real,
			role text not null default '', joined_at integer not null default 0, created_at integer not null)`,
		`insert into pool_members(key_id, pool_id, share_percent, role, joined_at, created_at) values ('qq', 'pro', 30, '', 111, 100)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `insert into pools(id, name, created_at, updated_at) values ('pro', 'Pro', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	pools, err := store.ListPools(ctx)
	if err != nil || len(pools) != 1 || len(pools[0].Members) != 1 || pools[0].Members[0].JoinedAt != 111 {
		t.Fatalf("old member row lost in the migration: %v %+v", err, pools)
	}
	if _, err := store.db.ExecContext(ctx, `insert into pool_members(key_id, pool_id, share_percent, created_at) values ('qq', 'grok', 50, 1)`); err != nil {
		t.Fatalf("a key must be able to sit in a second pool after the migration: %v", err)
	}
}
