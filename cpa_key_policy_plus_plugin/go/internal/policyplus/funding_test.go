package policyplus

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func fundingKey(id string, weekly *float64) KeyRecord {
	return KeyRecord{ID: id, Name: id, KeyHash: "sha256:" + SHA256Hex(id), Enabled: true, Preview: id, WeeklyLimitUSD: weekly}
}

func TestQuotaUnlimitedMigrationMarksOnlyUncappedKeysOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cap := 100.0
	for _, key := range []KeyRecord{fundingKey("admin", nil), fundingKey("lz", &cap)} {
		if err := store.UpsertKey(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate a database from before funding: no migration marker yet.
	if _, err := store.db.ExecContext(ctx, `delete from settings where key=?`, fundingMigrationSetting); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := store.ListKeys(ctx)
	flags := map[string]bool{}
	for _, key := range keys {
		flags[key.ID] = key.QuotaUnlimited
	}
	if !flags["admin"] || flags["lz"] {
		t.Fatalf("only the key without any USD window is marked unlimited: %+v", flags)
	}
	// The admin clears the flag; reopening must not mark it again.
	admin := keys[0]
	for _, key := range keys {
		if key.ID == "admin" {
			admin = key
		}
	}
	admin.QuotaUnlimited = false
	if err := store.SaveKeySettings(ctx, admin); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	keys, _ = store.ListKeys(ctx)
	for _, key := range keys {
		if key.QuotaUnlimited {
			t.Fatalf("the migration ran again and re-marked %s", key.ID)
		}
	}
}

func TestQuotaUnlimitedSurvivesUpsertAndInheritance(t *testing.T) {
	store := openPoolTestStore(t)
	ctx := context.Background()
	key := fundingKey("admin", nil)
	key.QuotaUnlimited = true
	if err := store.UpsertKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	// A background sync upserts the key without the flag; it must stay.
	plain := fundingKey("admin", nil)
	if err := store.UpsertKey(ctx, plain); err != nil {
		t.Fatal(err)
	}
	keys, _ := store.ListKeys(ctx)
	if len(keys) != 1 || !keys[0].QuotaUnlimited {
		t.Fatalf("a sync upsert must not clear the flag: %+v", keys)
	}
	if next := CopyPolicyFields(fundingKey("new", nil), keys[0]); !next.QuotaUnlimited {
		t.Fatal("a key inheriting another's policy inherits quota_unlimited")
	}
	if safe := keys[0].Safe(); safe["quota_unlimited"] != true {
		t.Fatalf("Safe() must report the flag: %+v", safe)
	}
}

func TestFundingModeAndMisses(t *testing.T) {
	store := openPoolTestStore(t)
	ctx := context.Background()
	if store.FundingMode(ctx) != FundingModeObserve {
		t.Fatal("funding starts in observe mode")
	}
	if err := store.SetFundingMode(ctx, "ENFORCE"); err != nil || store.FundingMode(ctx) != FundingModeEnforce {
		t.Fatalf("mode not switched: %v", err)
	}
	if err := store.SetFundingMode(ctx, "whatever"); err != nil || store.FundingMode(ctx) != FundingModeObserve {
		t.Fatal("unknown modes fall back to observe")
	}
	at := time.Unix(1_790_000_000, 0)
	for i := 0; i < 3; i++ {
		if err := store.RecordFundingMiss(ctx, "qq", "grok-4.6", "xai", FundingMissNoSource, at.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	_ = store.RecordFundingMiss(ctx, "aw", "grok-9", "xai", FundingMissUnpriced, at.Add(time.Hour))
	misses, err := store.FundingMisses(ctx, 10)
	if err != nil || len(misses) != 2 || misses[0].KeyID != "aw" || misses[1].Count != 3 || misses[1].FirstAt != at.Unix() || misses[1].LastAt != at.Add(2*time.Minute).Unix() {
		t.Fatalf("misses = %+v, %v", misses, err)
	}
	if err := store.ClearFundingMisses(ctx); err != nil {
		t.Fatal(err)
	}
	if misses, _ := store.FundingMisses(ctx, 10); len(misses) != 0 {
		t.Fatalf("misses not cleared: %+v", misses)
	}
	var n int
	if err := store.db.QueryRowContext(ctx, `select count(*) from settings where key=?`, fundingMigrationSetting).Scan(&n); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("a fresh store records the funding migration as done")
	}
}
