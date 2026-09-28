package policyplus

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// Funding: every model a key uses needs someone paying for it. A request is
// funded by a pool seat for the model's provider (share or owner), by the
// key's own USD windows (at least one limit set), or by the key being marked
// quota-unlimited. In observe mode unfunded requests pass and are recorded;
// in enforce mode they are refused.
const (
	FundingModeObserve = "observe"
	FundingModeEnforce = "enforce"

	fundingModeSetting      = "funding_mode"
	fundingMigrationSetting = "funding_migration_v1"

	FundingMissNoSource = "no_source"
	FundingMissUnpriced = "unpriced"
)

// NormalizeFundingMode returns observe for anything but enforce.
func NormalizeFundingMode(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), FundingModeEnforce) {
		return FundingModeEnforce
	}
	return FundingModeObserve
}

func (s *Store) FundingMode(ctx context.Context) string {
	var value string
	if err := s.db.QueryRowContext(ctx, `select value from settings where key=?`, fundingModeSetting).Scan(&value); err != nil {
		return FundingModeObserve
	}
	return NormalizeFundingMode(value)
}

func (s *Store) SetFundingMode(ctx context.Context, mode string) error {
	mode = NormalizeFundingMode(mode)
	if _, err := s.db.ExecContext(ctx, `insert into settings(key, value, updated_at) values(?, ?, ?)
		on conflict(key) do update set value=excluded.value, updated_at=excluded.updated_at`,
		fundingModeSetting, mode, time.Now().Unix()); err != nil {
		return err
	}
	return s.Audit(ctx, "admin", "set_funding_mode", "policyplus", map[string]any{"mode": mode})
}

// migrateQuotaUnlimited runs once: keys with no USD window at all were
// unlimited before funding existed, so they keep that explicitly. An admin
// who later clears the flag is not overridden.
func (s *Store) migrateQuotaUnlimited(ctx context.Context) error {
	var done string
	err := s.db.QueryRowContext(ctx, `select value from settings where key=?`, fundingMigrationSetting).Scan(&done)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `select id from keys where five_hour_limit_usd is null and daily_limit_usd is null
		and weekly_limit_usd is null and monthly_limit_usd is null and coalesce(quota_unlimited, 0) = 0`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	now := time.Now().Unix()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `update keys set quota_unlimited=1, updated_at=? where id=?`, now, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `insert into settings(key, value, updated_at) values(?, ?, ?)`, fundingMigrationSetting, "done", now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.Audit(ctx, "system", "funding_migration", "keys", map[string]any{"marked_unlimited": ids})
}

// FundingMiss is a key/model pair that had (or would have had) a request
// refused for lack of funding or price.
type FundingMiss struct {
	KeyID    string `json:"key_id"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Reason   string `json:"reason"`
	Count    int64  `json:"count"`
	FirstAt  int64  `json:"first_at"`
	LastAt   int64  `json:"last_at"`
}

func (s *Store) RecordFundingMiss(ctx context.Context, keyID, model, provider, reason string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `insert into funding_misses(key_id, model, provider, reason, count, first_at, last_at)
		values(?, ?, ?, ?, 1, ?, ?)
		on conflict(key_id, model) do update set count=count+1, last_at=excluded.last_at,
		provider=excluded.provider, reason=excluded.reason`,
		strings.TrimSpace(keyID), strings.TrimSpace(model), provider, reason, at.Unix(), at.Unix())
	return err
}

func (s *Store) FundingMisses(ctx context.Context, limit int) ([]FundingMiss, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `select key_id, model, provider, reason, count, first_at, last_at
		from funding_misses order by last_at desc, key_id asc limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FundingMiss{}
	for rows.Next() {
		var miss FundingMiss
		if err := rows.Scan(&miss.KeyID, &miss.Model, &miss.Provider, &miss.Reason, &miss.Count, &miss.FirstAt, &miss.LastAt); err != nil {
			return nil, err
		}
		out = append(out, miss)
	}
	return out, rows.Err()
}

func (s *Store) ClearFundingMisses(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `delete from funding_misses`); err != nil {
		return err
	}
	return s.Audit(ctx, "admin", "clear_funding_misses", "policyplus", nil)
}
